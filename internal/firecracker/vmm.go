package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/jail"
)

// DefaultBinary is the VMM executable looked up on PATH when none is configured.
const DefaultBinary = "firecracker"

// Options configure a single VMM process.
type Options struct {
	// Binary is the firecracker executable. Empty means look up DefaultBinary
	// on PATH.
	Binary string

	// SockPath is the API socket to create. Its parent directory must exist.
	SockPath string

	// Console receives the guest's serial output, which Firecracker writes to
	// the child's stdout/stderr along with its own log lines. Nil discards it.
	Console io.Writer

	// ConsoleIn is the guest's serial input. Firecracker only forwards
	// keystrokes from a real TTY, so this is only useful when wired to one.
	ConsoleIn io.Reader

	// Cleanup lists extra paths the VMM creates that should be removed once it
	// exits — the vsock socket, for instance. Keeping this with the reaper means
	// every exit path cleans up, not just an explicit Shutdown.
	Cleanup []string

	// CgroupFD, when non-nil, is a directory descriptor for a cgroup v2 leaf the
	// VMM is spawned directly into via CLONE_INTO_CGROUP. A pointer rather than
	// an int because 0 is a valid descriptor (stdin), so there is no in-band way
	// to say "unset". The caller owns the descriptor's lifetime.
	CgroupFD *int

	// Jail, when non-nil, confines the VMM to a chroot and its own process tree.
	// The VMM is then reached through the sockets inside the jail rather than
	// SockPath, and JailHelper is the eph-jail binary that enters the namespaces.
	Jail       *jail.Spec
	JailHelper string
}

// VMM is one running firecracker process and the API client bound to it.
//
// Lifecycle: Launch starts the process, Client configures and boots the guest,
// and Shutdown destroys it. A single goroutine reaps the child so the process is
// never left a zombie, and Done closes when it exits for any reason — including
// the guest resetting itself, which is how Firecracker exits normally.
type VMM struct {
	cmd    *exec.Cmd
	sock   string
	client *Client

	done    chan struct{}
	waitErr error
}

// Launch starts a VMM process and waits for its API to answer.
//
// The returned VMM owns the socket path: a stale socket is removed first, since
// Firecracker refuses to start when one already exists.
func Launch(ctx context.Context, o Options) (*VMM, error) {
	cmd, apiSock, err := buildCommand(o)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = o.Console, o.Console
	cmd.Stdin = o.ConsoleIn

	if err := cmd.Start(); err != nil {
		// buildCommand already created the jail directory; nothing will reap it
		// if the process never started, so remove it here.
		if o.Jail != nil {
			_ = o.Jail.Cleanup()
		}
		return nil, fmt.Errorf("firecracker: start: %w", err)
	}

	v := &VMM{
		cmd:    cmd,
		sock:   apiSock,
		client: NewClient(apiSock),
		done:   make(chan struct{}),
	}

	// One reaper for the child's lifetime. Writing waitErr before closing done
	// publishes it safely to everyone blocked in Wait.
	//
	// Cleanup happens here rather than in Shutdown because the sockets' lifetime
	// is exactly the process's: a guest that resets itself exits the VMM without
	// anyone calling Shutdown, and that path must not leave anything behind. A
	// jailed VMM's sockets live inside the jail directory, so removing the
	// directory removes them; a direct VMM's socket is removed by path.
	go func() {
		v.waitErr = cmd.Wait()
		if o.Jail != nil {
			_ = o.Jail.Cleanup()
		} else {
			_ = os.Remove(apiSock)
		}
		for _, p := range o.Cleanup {
			_ = os.Remove(p)
		}
		close(v.done)
	}()

	// Bound the readiness wait so a VMM that dies immediately surfaces as an
	// error here rather than hanging the caller.
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := v.client.WaitReady(readyCtx); err != nil {
		_ = v.Shutdown(context.Background())
		return nil, err
	}

	return v, nil
}

// buildCommand assembles the exec.Cmd for a VMM, in one of two shapes, and
// returns the host path its API socket will appear at.
//
// Direct: exec firecracker with --api-sock, the socket at a host path we clear
// first. Jailed: exec eph-jail, which enters the namespaces and pivots before
// exec'ing firecracker itself, so the socket appears inside the jail directory.
// A cgroup descriptor, when present, is folded into whichever SysProcAttr the
// mode already needs, so the VMM lands in its cgroup either way.
func buildCommand(o Options) (*exec.Cmd, string, error) {
	if o.Jail != nil {
		if o.JailHelper == "" {
			return nil, "", errors.New("firecracker: JailHelper is required for a jailed launch")
		}
		if _, err := exec.LookPath(o.JailHelper); err != nil {
			return nil, "", fmt.Errorf("firecracker: jail helper %q not usable: %w", o.JailHelper, err)
		}
		if err := o.Jail.Prepare(); err != nil {
			return nil, "", err
		}
		cmd := exec.Command(o.JailHelper, o.Jail.HelperArgs()...)
		sp := o.Jail.SysProcAttr()
		if o.CgroupFD != nil {
			sp.UseCgroupFD, sp.CgroupFD = true, *o.CgroupFD
		}
		cmd.SysProcAttr = sp
		return cmd, o.Jail.HostAPISock(), nil
	}

	if o.SockPath == "" {
		return nil, "", errors.New("firecracker: SockPath is required")
	}
	bin := o.Binary
	if bin == "" {
		bin = DefaultBinary
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, "", fmt.Errorf("firecracker: %q not found on PATH: %w", bin, err)
	}
	if err := os.MkdirAll(filepath.Dir(o.SockPath), 0o755); err != nil {
		return nil, "", fmt.Errorf("firecracker: prepare socket dir: %w", err)
	}
	if err := os.Remove(o.SockPath); err != nil && !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("firecracker: clear stale socket: %w", err)
	}
	cmd := exec.Command(bin, "--api-sock", o.SockPath)
	if o.CgroupFD != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: *o.CgroupFD}
	}
	return cmd, o.SockPath, nil
}

// Client returns the API client for this VMM.
func (v *VMM) Client() *Client { return v.client }

// Pid reports the VMM process id.
func (v *VMM) Pid() int { return v.cmd.Process.Pid }

// Done closes when the VMM process exits, whatever the cause.
func (v *VMM) Done() <-chan struct{} { return v.done }

// Wait blocks until the VMM exits and reports why.
//
// A guest that resets itself — `reboot -f` under `reboot=k` — makes Firecracker
// exit 0, so a nil error here is the normal end of a short-lived machine.
func (v *VMM) Wait() error {
	<-v.done
	return v.waitErr
}

// Shutdown destroys the machine and cleans up its socket.
//
// There is no graceful path yet: killing the VMM discards the guest instantly,
// which is what "destroy this sandbox" means. A cooperative shutdown would need
// ACPI or an in-guest agent, and belongs with the agent work in Phase 3.
func (v *VMM) Shutdown(ctx context.Context) error {
	select {
	case <-v.done:
		// Already exited; just clear the socket.
	default:
		if err := v.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("firecracker: kill vmm: %w", err)
		}
		select {
		case <-v.done:
		case <-ctx.Done():
			return fmt.Errorf("firecracker: vmm %d did not exit: %w", v.Pid(), ctx.Err())
		}
	}
	// The reaper removes the socket once the process is gone, and closing done
	// happens after that, so there is nothing left to clean up here.
	return nil
}
