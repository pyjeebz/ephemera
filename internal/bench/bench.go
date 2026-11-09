// Package bench measures a host: how fast sandboxes start, how many fit,
// how fast setup snapshots save and load, and how fast output arrives. It
// drives the real Firecracker backend, host agent, central server and CLI
// client in one process, and reports results as JSON so runs can be
// compared over time.
package bench

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/record"
	"github.com/pyjeebz/ephemera/internal/sandbox/firecracker"
)

// Config says what to measure and where.
type Config struct {
	ImageDir string // guest image from deploy/guest/build-image.sh
	// WorkDir holds everything the run creates, and is removed after. It
	// must be on the same filesystem as ImageDir when Jailed.
	WorkDir  string
	Jailed   bool
	Runs     int // samples per timing; default 10
	Density  int // sandboxes started at once; 0 skips the density test
	MemoryMB int // guest memory per sandbox; default 1024
	CPUs     int // vCPUs per sandbox; default 2
}

// Report is one benchmark run.
type Report struct {
	Version int       `json:"version"`
	Commit  string    `json:"commit"`
	Date    time.Time `json:"date"`
	Machine Machine   `json:"machine"`
	Config  struct {
		Jailed   bool `json:"jailed"`
		Runs     int  `json:"runs"`
		Density  int  `json:"density"`
		MemoryMB int  `json:"memory_mb"`
		CPUs     int  `json:"cpus"`
	} `json:"config"`
	Results []Result `json:"results"`
}

// Machine describes the host the run was made on.
type Machine struct {
	Hostname    string `json:"hostname"`
	CPU         string `json:"cpu"`
	CPUs        int    `json:"cpus"`
	MemoryMB    int    `json:"memory_mb"`
	Kernel      string `json:"kernel"`
	Virtualized bool   `json:"virtualized"` // true inside a VM: nested virtualization
}

// Result is one measurement. Timings have P50, P90 and Max over N
// samples; a single figure has only Value.
type Result struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"` // the median, or the single figure
	P90   float64 `json:"p90,omitempty"`
	Max   float64 `json:"max,omitempty"`
	N     int     `json:"n,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// The measurements, by name, in the order they run.
const (
	ColdBoot        = "cold_boot"
	TemplateBuild   = "template_build"
	Restore         = "restore"
	RunEndToEnd     = "run_end_to_end"
	FirstByte       = "first_output_byte"
	LineDelay       = "line_delay_p99"
	SetupFirstRun   = "setup_first_run"
	SetupSnapshot   = "setup_from_snapshot"
	DensityStartAll = "density_start_all"
	DensityMemory   = "density_memory_per_sandbox"
	DensityPSS      = "density_pss_per_sandbox"
)

// Run measures the host. progress, if set, gets a line as each
// measurement finishes.
func Run(ctx context.Context, cfg Config, progress func(Result)) (*Report, error) {
	if cfg.Runs <= 0 {
		cfg.Runs = 10
	}
	if cfg.MemoryMB <= 0 {
		cfg.MemoryMB = 1024
	}
	if cfg.CPUs <= 0 {
		cfg.CPUs = 2
	}
	for _, f := range []string{"vmlinux", "rootfs.ext4"} {
		if _, err := os.Stat(filepath.Join(cfg.ImageDir, f)); err != nil {
			return nil, fmt.Errorf("guest image: %w", err)
		}
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(cfg.WorkDir)

	r := &Report{Version: 1, Date: time.Now().UTC(), Machine: machine()}
	r.Config.Jailed, r.Config.Runs, r.Config.Density = cfg.Jailed, cfg.Runs, cfg.Density
	r.Config.MemoryMB, r.Config.CPUs = cfg.MemoryMB, cfg.CPUs
	add := func(res Result) {
		r.Results = append(r.Results, res)
		if progress != nil {
			progress(res)
		}
	}

	// Cold boot: no snapshot, so each sandbox boots its kernel.
	cold := cfg.backend("cold", false)
	d, err := timeCreates(ctx, cold, "cold", cfg.Runs)
	if err != nil {
		return r, fmt.Errorf("cold boot: %w", err)
	}
	add(timing(ColdBoot, d, ""))

	// The template snapshot, then sandboxes restored from it.
	be := cfg.backend("snap", true)
	start := time.Now()
	if err := be.Prepare(ctx); err != nil {
		return r, fmt.Errorf("template snapshot: %w", err)
	}
	add(Result{Name: TemplateBuild, Unit: "ms", Value: ms(time.Since(start)), Note: "once, when the host starts"})
	if d, err = timeCreates(ctx, be, "restore", cfg.Runs); err != nil {
		return r, fmt.Errorf("restore: %w", err)
	}
	add(timing(Restore, d, ""))

	// The rest goes through the host agent, central server and client.
	st, err := newStack(ctx, be, cfg.WorkDir)
	if err != nil {
		return r, err
	}
	defer st.close()
	for _, m := range []func(context.Context, *stack, Config, func(Result)) error{measureRun, measureOutput, measureSetup} {
		if err := m(ctx, st, cfg, add); err != nil {
			return r, err
		}
	}
	if cfg.Density > 0 {
		if err := measureDensity(ctx, be, cfg.Density, add); err != nil {
			return r, fmt.Errorf("density: %w", err)
		}
	}
	return r, nil
}

func (cfg Config) backend(name string, snapshots bool) *firecracker.Backend {
	dir := filepath.Join(cfg.WorkDir, name)
	be := &firecracker.Backend{ImageDir: cfg.ImageDir, StateDir: filepath.Join(dir, "vms"),
		DefaultCPUs: cfg.CPUs, DefaultMemoryMB: cfg.MemoryMB,
		// Its own user IDs, so a host agent running alongside is unaffected.
		Jailed: cfg.Jailed, ChrootBase: filepath.Join(dir, "jail"), UIDBase: 400000,
		Snapshots: snapshots, TemplateDir: filepath.Join(dir, "template"),
		ProjectSnapshotDir: filepath.Join(dir, "projects")}
	os.MkdirAll(be.StateDir, 0o700)
	return be
}

// timeCreates starts and destroys n sandboxes one after another, timing
// each start until the guest answers a command.
func timeCreates(ctx context.Context, be *firecracker.Backend, prefix string, n int) ([]time.Duration, error) {
	var d []time.Duration
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sbx_bench_%s_%02d", prefix, i)
		start := time.Now()
		err := be.Create(ctx, id, api.Spec{})
		if err == nil {
			_, err = be.Exec(ctx, id, api.ExecRequest{Argv: []string{"true"}}, io.Discard, io.Discard)
		}
		took := time.Since(start)
		be.Destroy(ctx, id)
		if err != nil {
			return nil, err
		}
		d = append(d, took)
	}
	return d, nil
}

// stack is a host agent and central server on loopback, with a client.
type stack struct {
	host   *host.Server
	client *client.Client
	stop   func()
}

func newStack(ctx context.Context, be *firecracker.Backend, workDir string) (*stack, error) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	secret, token := randHex(16), randHex(16)
	key, err := record.LoadOrCreateKey(filepath.Join(workDir, "host.key"))
	if err != nil {
		return nil, err
	}
	cs := &control.Server{Tokens: control.StaticToken{Name: "bench", Token: token}, HostSecret: secret, Log: quiet}
	hs := &host.Server{Backend: be, Secret: secret, Log: quiet, DefaultImage: "default", Name: "bench", BackendName: "firecracker", Key: key}
	cURL, cStop, err := serve(cs.Handler())
	if err != nil {
		return nil, err
	}
	hURL, hStop, err := serve(hs.Handler())
	if err != nil {
		cStop()
		return nil, err
	}
	rctx, cancel := context.WithCancel(ctx)
	go hs.Register(rctx, http.DefaultClient, cURL, secret, api.RegisterHostRequest{Name: "bench", URL: hURL, Capacity: 1000}, time.Second)
	st := &stack{host: hs, client: &client.Client{Server: cURL, Token: token}, stop: func() {
		cancel()
		dctx, c := context.WithTimeout(context.Background(), time.Minute)
		defer c()
		hs.DestroyAll(dctx)
		hStop()
		cStop()
	}}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if hosts, err := st.client.Hosts(ctx); err == nil && len(hosts) > 0 {
			return st, nil
		}
		if time.Now().After(deadline) {
			st.close()
			return nil, fmt.Errorf("the host agent did not register")
		}
	}
}

func (s *stack) close() { s.stop() }

func serve(h http.Handler) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }, nil
}

// measureRun times `ephemera run -- true` from the client: create, sync,
// run, destroy, with the signed record.
func measureRun(ctx context.Context, s *stack, cfg Config, add func(Result)) error {
	dir, err := os.MkdirTemp(cfg.WorkDir, "project-")
	if err != nil {
		return err
	}
	os.WriteFile(filepath.Join(dir, "index.js"), []byte("console.log(1)\n"), 0o644)
	var d []time.Duration
	for i := 0; i < cfg.Runs; i++ {
		start := time.Now()
		res, err := s.client.Run(ctx, client.RunOptions{Dir: dir, Exec: api.ExecRequest{Argv: []string{"true"}},
			Stdout: io.Discard, Stderr: io.Discard})
		if err != nil || res.ExitCode != 0 {
			return fmt.Errorf("run: exit %d, %v", res.ExitCode, err)
		}
		d = append(d, time.Since(start))
	}
	add(timing(RunEndToEnd, d, "create, sync, run true, destroy, signed record"))
	return nil
}

// measureOutput times how fast output reaches the client: the first byte
// of a command, and how much each streamed line is delayed.
func measureOutput(ctx context.Context, s *stack, cfg Config, add func(Result)) error {
	sb, err := s.client.Create(ctx, api.Spec{})
	if err != nil {
		return err
	}
	defer s.client.Destroy(context.WithoutCancel(ctx), sb.ID)

	var first []time.Duration
	for i := 0; i < cfg.Runs*3; i++ {
		pr, pw := io.Pipe()
		start := time.Now()
		got := make(chan time.Duration, 1)
		go func() {
			bufio.NewReader(pr).ReadByte()
			got <- time.Since(start)
			io.Copy(io.Discard, pr)
		}()
		_, err := s.client.Exec(ctx, sb, api.ExecRequest{Argv: []string{"echo", "hi"}}, pw, io.Discard)
		pw.Close()
		if err != nil {
			return fmt.Errorf("exec: %w", err)
		}
		first = append(first, <-got)
	}
	add(timing(FirstByte, first, "from asking to run echo to its first byte"))

	// Each line carries the guest's clock when it was printed. Delay is
	// measured beyond the fastest line, so the offset between the guest's
	// and host's clocks cancels out.
	lt := &lineTimer{}
	script := "import time,sys\nfor _ in range(200):\n    sys.stdout.write('%d\\n' % time.time_ns()); sys.stdout.flush(); time.sleep(0.01)\n"
	if _, err := s.client.Exec(ctx, sb, api.ExecRequest{Argv: []string{"python3", "-c", script}}, lt, io.Discard); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if len(lt.delay) == 0 {
		return fmt.Errorf("no timestamped lines came back")
	}
	min := pct(lt.delay, 0)
	var extra []time.Duration
	for _, d := range lt.delay {
		extra = append(extra, d-min)
	}
	add(Result{Name: LineDelay, Unit: "ms", Value: ms(pct(extra, 0.99)), Max: ms(pct(extra, 1)), N: len(extra),
		Note: "a line every 10 ms; delay beyond the fastest line"})
	return nil
}

// measureSetup times --setup: the first run runs setup and saves an
// encrypted snapshot; the second starts from it.
func measureSetup(ctx context.Context, s *stack, cfg Config, add func(Result)) error {
	dir, err := os.MkdirTemp(cfg.WorkDir, "setup-")
	if err != nil {
		return err
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644)
	key := make([]byte, 32)
	rand.Read(key)
	// A setup that takes 3 s and leaves 200 MB of dependencies behind.
	setup := &client.Setup{SnapshotID: randHex(16), Key: key, Argv: []string{"sh", "-c",
		"sleep 3; mkdir -p node_modules/dep && head -c 200000000 /dev/urandom > node_modules/dep/blob"}}
	runOnce := func() (time.Duration, bool, error) {
		loaded := false
		start := time.Now()
		res, err := s.client.Run(ctx, client.RunOptions{Dir: dir, Setup: setup,
			Exec:   api.ExecRequest{Argv: []string{"test", "-s", "node_modules/dep/blob"}},
			Stdout: io.Discard, Stderr: io.Discard,
			Events: func(ev string, f map[string]any) {
				if ev == "snapshot" && f["loaded"] == true {
					loaded = true
				}
			}})
		if err == nil && res.ExitCode != 0 {
			err = fmt.Errorf("the setup's files are missing (exit %d)", res.ExitCode)
		}
		return time.Since(start), loaded, err
	}
	d1, _, err := runOnce()
	if err != nil {
		return fmt.Errorf("setup run: %w", err)
	}
	add(Result{Name: SetupFirstRun, Unit: "ms", Value: ms(d1), Note: "3 s setup writing 200 MB, then save the encrypted snapshot"})
	var d []time.Duration
	for i := 0; i < 3; i++ {
		took, loaded, err := runOnce()
		if err != nil {
			return fmt.Errorf("run from snapshot: %w", err)
		}
		if !loaded {
			return fmt.Errorf("the project snapshot was not used")
		}
		d = append(d, took)
	}
	add(timing(SetupSnapshot, d, "the same run, starting from the snapshot"))
	return nil
}

// measureDensity starts n sandboxes at once, gives each a little work, and
// measures the host memory they hold while idle.
func measureDensity(ctx context.Context, be *firecracker.Backend, n int, add func(Result)) error {
	before := memAvailableMB()
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range ids {
		ids[i] = fmt.Sprintf("sbx_bench_dense_%03d", i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = be.Create(ctx, ids[i], api.Spec{})
		}(i)
	}
	wg.Wait()
	all := time.Since(start)
	defer func() {
		for _, id := range ids {
			be.Destroy(context.WithoutCancel(ctx), id)
		}
	}()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("sandbox %d of %d: %w", i+1, n, err)
		}
	}
	for _, id := range ids {
		if _, err := be.Exec(ctx, id, api.ExecRequest{Argv: []string{"sh", "-c", "echo hi > x && node -e 1"}}, io.Discard, io.Discard); err != nil {
			return err
		}
	}
	time.Sleep(2 * time.Second)
	used := before - memAvailableMB()
	var pss float64
	for _, id := range ids {
		if pid, ok := be.PID(id); ok {
			pss += pssMB(pid)
		}
	}
	add(Result{Name: DensityStartAll, Unit: "ms", Value: ms(all), N: n, Note: fmt.Sprintf("%d sandboxes started at once", n)})
	add(Result{Name: DensityMemory, Unit: "MB", Value: round(float64(used) / float64(n)), N: n, Note: "host memory used, per idle sandbox"})
	add(Result{Name: DensityPSS, Unit: "MB", Value: round(pss / float64(n)), N: n, Note: "proportional memory of each VM process"})
	return nil
}

// lineTimer records how late each timestamped output line arrives.
type lineTimer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	delay []time.Duration
}

func (l *lineTimer) Write(p []byte) (int, error) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line)
			return len(p), nil
		}
		if ns, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64); err == nil {
			l.delay = append(l.delay, now.Sub(time.Unix(0, ns)))
		}
	}
}

func timing(name string, d []time.Duration, note string) Result {
	return Result{Name: name, Unit: "ms", Value: ms(pct(d, 0.5)), P90: ms(pct(d, 0.9)), Max: ms(pct(d, 1)), N: len(d), Note: note}
}

func pct(d []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

func ms(d time.Duration) float64 { return round(float64(d) / float64(time.Millisecond)) }

func round(f float64) float64 {
	if f >= 100 {
		return float64(int64(f + 0.5))
	}
	return float64(int64(f*100+0.5)) / 100
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func machine() Machine {
	m := Machine{CPUs: runtime.NumCPU(), MemoryMB: meminfoMB("MemTotal:")}
	m.Hostname, _ = os.Hostname()
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		m.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "model name"); ok && m.CPU == "" {
				m.CPU = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), ":"))
			}
			if strings.HasPrefix(l, "flags") && strings.Contains(l, " hypervisor") {
				m.Virtualized = true
			}
		}
	}
	return m
}

func memAvailableMB() int { return meminfoMB("MemAvailable:") }

func meminfoMB(field string) int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, field); ok {
			kb, _ := strconv.Atoi(strings.Fields(v)[0])
			return kb / 1024
		}
	}
	return 0
}

// pssMB is a process's proportional share of memory: pages shared with
// other clones count only their share.
func pssMB(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Pss:"); ok {
			kb, _ := strconv.Atoi(strings.Fields(v)[0])
			return float64(kb) / 1024
		}
	}
	return 0
}
