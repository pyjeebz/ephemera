package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/record"
)

func TestFirecrackerProjectSnapshot(t *testing.T) {
	be := firecrackerBackend(t)
	be.ProjectSnapshotDir = t.TempDir()
	if be.Jailed {
		// Decrypted files move into the jail: keep them on its filesystem.
		be.ProjectSnapshotDir = filepath.Join(filepath.Dir(be.ImageDir), fmt.Sprintf("snaps-e2e-%d", os.Getpid()))
		t.Cleanup(func() { os.RemoveAll(be.ProjectSnapshotDir) })
	}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"app.js": "console.log(1)"})
	key := make([]byte, 32)
	rand.Read(key)
	snapID := strings.Repeat("c0ffee", 6) + "abcd"
	// A slow setup that leaves a build stamp and a secret on the disk.
	setup := func(k []byte) *client.Setup {
		return &client.Setup{SnapshotID: snapID, Key: k, Argv: []string{"sh", "-c",
			"sleep 3; mkdir -p node_modules/dep && date +%s%N > node_modules/dep/built && echo SECRET-MARKER-4711 > node_modules/dep/secret"}}
	}
	run := func(k []byte) (string, map[string]any, time.Duration, *api.SignedRecord) {
		t.Helper()
		var out bytes.Buffer
		var snapEv map[string]any
		start := time.Now()
		res, err := s.client.Run(context.Background(), client.RunOptions{
			Dir: dir, Setup: setup(k),
			Exec:   api.ExecRequest{Argv: []string{"cat", "node_modules/dep/built"}},
			Stdout: &out, Stderr: &out,
			Events: func(ev string, f map[string]any) {
				switch ev {
				case "snapshot":
					snapEv = f
				case "snapshot_error":
					t.Errorf("saving the snapshot failed: %v", f["error"])
				}
			},
		})
		if err != nil {
			t.Fatalf("run: %v\n%s", err, out.String())
		}
		return strings.TrimSpace(out.String()), snapEv, time.Since(start), res.Record
	}

	first, ev1, d1, rec1 := run(key)
	if ev1["loaded"] != false {
		t.Fatalf("first run loaded a snapshot: %v", ev1)
	}
	second, ev2, d2, rec2 := run(key)
	t.Logf("first run (setup, then snapshot): %v; second (from snapshot): %v", d1.Round(time.Millisecond), d2.Round(time.Millisecond))
	if ev2["loaded"] != true {
		t.Fatalf("second run did not load the snapshot: %v", ev2)
	}
	if second != first {
		t.Errorf("second run's build stamp %q, want %q from the snapshot", second, first)
	}
	if d2 >= d1-2*time.Second {
		t.Errorf("second run took %v; setup (3s) was not skipped", d2)
	}
	r1, _ := record.Verify(*rec1)
	r2, _ := record.Verify(*rec2)
	if r1.SnapshotSaved != snapID || r2.SnapshotLoaded != snapID {
		t.Errorf("records: saved %q, loaded %q", r1.SnapshotSaved, r2.SnapshotLoaded)
	}

	// At rest it is encrypted, and no plaintext is left about.
	snapDir := filepath.Join(be.ProjectSnapshotDir, snapID)
	filepath.Walk(be.ProjectSnapshotDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if strings.HasPrefix(info.Name(), ".save-") || !strings.HasSuffix(p, ".enc") && info.Name() != "meta.json" {
				t.Errorf("unexpected file in the snapshot store: %s", p)
			}
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("SECRET-MARKER-4711")) {
				t.Errorf("%s holds the setup's secret in plaintext", p)
			}
		}
		return nil
	})
	if left, _ := os.ReadDir(be.StateDir); len(left) != 0 {
		t.Errorf("left in the VM directory: %v", left)
	}

	// The wrong key loads nothing; setup runs again.
	other := make([]byte, 32)
	rand.Read(other)
	third, ev3, _, _ := run(other)
	if ev3["loaded"] != false || third == first {
		t.Errorf("wrong key: event %v, stamp %q", ev3, third)
	}
	// That run saved its own snapshot over the old one; run once more with
	// it, then tamper with a file: the snapshot is refused and removed.
	run(other)
	files, _ := filepath.Glob(filepath.Join(snapDir, "*.enc"))
	if len(files) == 0 {
		t.Fatal("no encrypted files")
	}
	b, _ := os.ReadFile(files[0])
	b[len(b)/2] ^= 0xff
	os.WriteFile(files[0], b, 0o600)
	_, ev5, _, _ := run(other)
	if ev5["loaded"] != false || !strings.Contains(fmt.Sprint(ev5["reason"]), "did not decrypt") {
		t.Errorf("tampered snapshot: %v", ev5)
	}
}

// lineTimer records when each output line arrives.
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

func percentile(d []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

// TestOutputLatency measures how fast output reaches the CLI: each line
// carries the guest's clock when it was printed (the guest's clock is set
// from the host's on restore; here both run on one machine). Run with
// EPHEMERA_BENCH=1.
func TestOutputLatency(t *testing.T) {
	if os.Getenv("EPHEMERA_BENCH") != "1" {
		t.Skip("set EPHEMERA_BENCH=1")
	}
	be := firecrackerBackend(t)
	s := newStack(t, be, "unused")
	ctx := context.Background()
	sb, err := s.client.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.client.Destroy(ctx, sb.ID)

	// Lines printed at two different gaps: if delivery waited for the next
	// line, the latency would follow the gap.
	for _, gap := range []string{"0.01", "0.05"} {
		lt := &lineTimer{}
		script := "import time,sys\nfor _ in range(150):\n    sys.stdout.write('%d\\n' % time.time_ns()); sys.stdout.flush(); time.sleep(" + gap + ")\n"
		if _, err := s.client.Exec(ctx, sb, api.ExecRequest{Argv: []string{"python3", "-c", script}}, lt, io.Discard); err != nil {
			t.Fatal(err)
		}
		// Delay relative to the fastest line: a constant clock offset
		// between guest and host cancels out, leaving the variation.
		min := percentile(lt.delay, 0)
		var extra []time.Duration
		for _, d := range lt.delay {
			extra = append(extra, d-min)
		}
		t.Logf("a line every %ss, %d lines: each line's delay beyond the fastest: p50 %v, p90 %v, p99 %v, max %v", gap, len(lt.delay),
			percentile(extra, 0.5).Round(10*time.Microsecond), percentile(extra, 0.9).Round(10*time.Microsecond),
			percentile(extra, 0.99).Round(10*time.Microsecond), percentile(extra, 1).Round(10*time.Microsecond))
	}
	// The guest's clock against the host's, to read the figures above.
	var probe bytes.Buffer
	before := time.Now()
	s.client.Exec(ctx, sb, api.ExecRequest{Argv: []string{"python3", "-c", "import time; print(time.time_ns())"}}, &probe, io.Discard)
	after := time.Now()
	if ns, err := strconv.ParseInt(strings.TrimSpace(probe.String()), 10, 64); err == nil {
		mid := before.Add(after.Sub(before) / 2)
		t.Logf("guest clock minus host clock: about %v (round trip %v)", time.Unix(0, ns).Sub(mid).Round(100*time.Microsecond), after.Sub(before).Round(100*time.Microsecond))
	}

	// Time from asking to run a command to its first byte of output.
	var first []time.Duration
	for i := 0; i < 30; i++ {
		pr, pw := io.Pipe()
		start := time.Now()
		got := make(chan time.Duration, 1)
		go func() {
			bufio.NewReader(pr).ReadByte()
			got <- time.Since(start)
			io.Copy(io.Discard, pr)
		}()
		s.client.Exec(ctx, sb, api.ExecRequest{Argv: []string{"echo", "hi"}}, pw, io.Discard)
		pw.Close()
		first = append(first, <-got)
	}
	t.Logf("command start to first output byte (echo, 30 runs): p50 %v, p90 %v, max %v",
		percentile(first, 0.5).Round(10*time.Microsecond), percentile(first, 0.9).Round(10*time.Microsecond),
		percentile(first, 1).Round(10*time.Microsecond))
}
