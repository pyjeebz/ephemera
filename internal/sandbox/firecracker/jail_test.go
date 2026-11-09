package firecracker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/api"
)

func jailedBackend(t *testing.T) *Backend {
	t.Helper()
	img := guestImage(t)
	if os.Geteuid() != 0 {
		t.Skip("the jailer needs root")
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("needs cgroup v2")
	}
	// The jail base must share a filesystem with the image.
	base := filepath.Join(filepath.Dir(img), fmt.Sprintf("jail-test-%d", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(base) })
	return &Backend{ImageDir: img, StateDir: t.TempDir(), DefaultCPUs: 2, DefaultMemoryMB: 512,
		Jailed: true, ChrootBase: base, UIDBase: 310000}
}

func procField(t *testing.T, pid int, field string) string {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, field+":"); ok {
			return strings.Fields(v)[0]
		}
	}
	t.Fatalf("no %s in /proc/%d/status", field, pid)
	return ""
}

func TestJailedVM(t *testing.T) {
	be := jailedBackend(t)
	ctx := context.Background()
	for _, id := range []string{"sbx_jail_a", "sbx_jail_b"} {
		if err := be.Create(ctx, id, api.Spec{}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := be.vm("sbx_jail_a")
	b, _ := be.vm("sbx_jail_b")

	// Each VM runs as an unprivileged user of its own.
	uidA, uidB := procField(t, a.PID(), "Uid"), procField(t, b.PID(), "Uid")
	if uidA == "0" || uidB == "0" || uidA == uidB {
		t.Errorf("uids: %s and %s; want two different non-root users", uidA, uidB)
	}

	// Its whole filesystem view is the jail: list what the Firecracker
	// process sees as / (the jailer pivots its root, so this is the
	// process's own root, not the host's).
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/root", a.PID()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Logf("firecracker sees / as: %v", names)
	allowed := map[string]bool{"vmlinux": true, "rootfs.ext4": true, "scratch.ext4": true, "vm.json": true,
		"vsock.sock": true, "api.sock": true, "snap.mem": true, "snap.vmstate": true,
		"firecracker": true, "firecracker.pid": true, "dev": true, "run": true}
	for _, n := range names {
		if !allowed[n] {
			t.Errorf("unexpected %q visible in the jail", n)
		}
	}

	// cgroup limits are in place.
	cg := a.cfg.Jail.cgroupDir()
	mem, err := os.ReadFile(filepath.Join(cg, "memory.max"))
	if err != nil {
		t.Fatalf("cgroup: %v", err)
	}
	if want := fmt.Sprint((512 + 256) << 20); strings.TrimSpace(string(mem)) != want {
		t.Errorf("memory.max = %s, want %s", mem, want)
	}
	cpu, _ := os.ReadFile(filepath.Join(cg, "cpu.max"))
	if strings.TrimSpace(string(cpu)) != "200000 100000" {
		t.Errorf("cpu.max = %q", cpu)
	}
	procs, _ := os.ReadFile(filepath.Join(cg, "cgroup.procs"))
	if !strings.Contains(string(procs), fmt.Sprint(a.PID())) {
		t.Errorf("firecracker (pid %d) is not in its cgroup: %q", a.PID(), procs)
	}

	// The guest works as before.
	var out bytes.Buffer
	code, err := be.Exec(ctx, "sbx_jail_a", api.ExecRequest{Argv: []string{"sh", "-c", "echo inside; touch /etc/x && echo writable"}}, &out, &out)
	if err != nil || code != 0 || out.String() != "inside\nwritable\n" {
		t.Errorf("exec: code %d err %v out %q", code, err, out.String())
	}

	// Destroy leaves nothing: no jail, no cgroup, the user id is free.
	jailDir := a.jailDir
	for _, id := range []string{"sbx_jail_a", "sbx_jail_b"} {
		if err := be.Destroy(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(jailDir); !os.IsNotExist(err) {
		t.Errorf("jail %s left behind", jailDir)
	}
	if _, err := os.Stat(cg); !os.IsNotExist(err) {
		t.Errorf("cgroup %s left behind", cg)
	}
	if len(be.uids) != 0 {
		t.Errorf("user ids still allocated: %v", be.uids)
	}
}
