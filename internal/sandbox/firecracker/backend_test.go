package firecracker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// uidHeld reports whether the backend still has the user ID allocated.
func uidHeld(b *Backend, uid int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.uids[uid]
}

// TestDestroyAfterKillTimeout checks that Destroy releases the jail and the
// directory of a VM that does not exit, and that the user ID stays allocated
// until the process is gone. Kill reports the timeout when the process it
// signalled is never waited for.
func TestDestroyAfterKillTimeout(t *testing.T) {
	stateDir := t.TempDir()
	chrootBase := t.TempDir()
	uid := 310000
	id := "sbx_stuck"

	dir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scratch.ext4"), []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	jailDir := filepath.Join(chrootBase, "firecracker", jailID(id))
	if err := os.MkdirAll(filepath.Join(jailDir, "root"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	vm := &VM{
		cfg:     VMConfig{Dir: dir, Jail: &Jail{ID: jailID(id), UID: uid, GID: uid, CgroupParent: cgroupParent}},
		cmd:     cmd,
		jailDir: jailDir,
		exited:  make(chan struct{}),
	}
	b := &Backend{StateDir: stateDir, ChrootBase: chrootBase, UIDBase: uid, uids: map[int]bool{uid: true}}
	b.vms = map[string]*VM{id: vm}
	b.proxies = map[string]*vmProxy{}

	err := b.Destroy(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "firecracker did not exit") {
		t.Fatalf("Destroy error = %v, want the kill timeout", err)
	}
	if _, err := os.Stat(jailDir); !os.IsNotExist(err) {
		t.Errorf("jail %s remains after Destroy: %v", jailDir, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory %s remains after Destroy: %v", dir, err)
	}
	// The process has not been reaped, so the user ID must not go to a new VM.
	if !uidHeld(b, uid) {
		t.Error("user ID was freed while the process is still running")
	}

	close(vm.exited)
	deadline := time.Now().Add(5 * time.Second)
	for uidHeld(b, uid) {
		if time.Now().After(deadline) {
			t.Fatal("user ID was not freed after the process exited")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
