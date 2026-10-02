// Package keepawake asserts the machine does not sleep while a
// share is live. Per the PRD's CI rule this is shell-out only:
// caffeinate on macOS, systemd-inhibit on Linux, nothing on Windows
// yet (the pull-forward must stay a shell, not a port).
package daemon

import (
	"os/exec"
	"runtime"
	"sync"
)

// KeepAwake holds the active inhibitor process for the machine.
// At most one inhibitor runs per daemon; Stop is idempotent.
type KeepAwake struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	active bool
}

// NewKeepAwake returns an unstarted inhibitor.
func NewKeepAwake() *KeepAwake { return &KeepAwake{} }

// Start asserts no system sleep until Stop. Best effort: on
// unsupported platforms or missing binaries it returns nil and the
// share continues (sleep may kill the stream, but never the share).
func (k *KeepAwake) Start() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.active {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// -i: idle sleep off; -s: system sleep off (needs root on
		// desktop macs, harmless if refused)
		cmd = exec.Command("caffeinate", "-is")
	case "linux":
		// systemd-inhibit blocks sleep while the process lives
		cmd = exec.Command("systemd-inhibit", "--what=idle:sleep", "sleep", "infinity")
	default:
		return nil // windows: future
	}
	if err := cmd.Start(); err != nil {
		return nil // missing binary or no systemd: not fatal
	}
	k.cmd = cmd
	k.active = true
	return nil
}

// Stop releases the inhibitor. Idempotent; safe when never started.
func (k *KeepAwake) Stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.active {
		return
	}
	if k.cmd != nil && k.cmd.Process != nil {
		_ = k.cmd.Process.Kill()
		_ = k.cmd.Wait()
	}
	k.cmd = nil
	k.active = false
}
