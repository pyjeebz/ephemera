package firecracker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A Destroy whose Kill times out must leave the VM in place, so a later
// Destroy, such as the host's retry after expiry, can signal it again.
func TestDestroyKeepsVMWhenKillTimesOut(t *testing.T) {
	stateDir := t.TempDir()
	id := "sbx_stuck"
	if err := os.MkdirAll(stateDir+"/"+id, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Kill sends SIGKILL and then waits for a process nobody reaps here, so
	// it reports the timeout.
	vm := &VM{cfg: VMConfig{Dir: stateDir + "/" + id}, cmd: cmd, exited: make(chan struct{})}
	b := &Backend{StateDir: stateDir, vms: map[string]*VM{id: vm}, proxies: map[string]*vmProxy{}}

	err := b.Destroy(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "firecracker did not exit") {
		t.Fatalf("Destroy error = %v, want the kill timeout", err)
	}
	if _, err := b.vm(id); err != nil {
		t.Fatalf("Destroy dropped the VM that is still running: %v", err)
	}
}
