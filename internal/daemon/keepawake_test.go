package daemon

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestKeepAwakeStartStop(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no inhibitor on this platform")
	}
	// systemd-inhibit may not exist in containers: skip, not fail
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("systemd-inhibit"); err != nil {
			t.Skip("systemd-inhibit not present (container?)")
		}
	}

	k := NewKeepAwake()
	if err := k.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !k.active {
		t.Fatal("not active after start")
	}
	// second start is a no-op, not a second process
	if err := k.Start(); err != nil {
		t.Fatalf("second start: %v", err)
	}
	k.Stop()
	if k.active {
		t.Fatal("still active after stop")
	}
	// stop again: idempotent
	k.Stop()
}

func TestKeepAwakeUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only behavior")
	}
	k := NewKeepAwake()
	if err := k.Start(); err != nil {
		t.Fatalf("windows start should be nil: %v", err)
	}
	k.Stop()
}
