//go:build linux

// Command ephemera-guest runs inside every microVM. Started by the kernel
// as PID 1, it mounts the basic filesystems, brings up loopback networking,
// starts itself again as the agent, and reaps orphaned processes. The agent
// serves the guest API on vsock for the host.
package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/pyjeebz/ephemera/internal/guest"
)

const workdir = "/workspace"

var baseEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"HOME=/root",
	"LANG=C.UTF-8",
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("ephemera-guest: ")
	switch {
	case len(os.Args) > 1 && os.Args[1] == "agent":
		agent()
	case os.Getpid() == 1:
		initMain()
	default:
		fmt.Fprintln(os.Stderr, "ephemera-guest runs as PID 1 inside a microVM, or as: ephemera-guest agent")
		os.Exit(2)
	}
}

// initMain is PID 1. Orphaned processes are re-parented to it, so it reaps
// them; the agent runs as a separate process so that reaping never steals
// the exit status of a command the agent is waiting for.
func initMain() {
	if err := setup(); err != nil {
		log.Printf("setup: %v", err)
	}
	for {
		agentPid, err := startAgent()
		if err != nil {
			log.Printf("start agent: %v", err)
			time.Sleep(time.Second)
			continue
		}
		log.Printf("agent started (pid %d), listening on vsock port %d", agentPid, guest.Port)
		for {
			var ws unix.WaitStatus
			pid, err := unix.Wait4(-1, &ws, 0, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if pid == agentPid {
				log.Printf("agent exited (%v); restarting", ws)
				break
			}
		}
	}
}

func startAgent() (int, error) {
	self, err := os.Executable()
	if err != nil {
		self = "/sbin/ephemera-guest"
	}
	cmd := exec.Command(self, "agent")
	cmd.Env = baseEnv
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func setup() error {
	mounts := []struct {
		source, target, fstype, data string
		flags                        uintptr
	}{
		{"proc", "/proc", "proc", "", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"sysfs", "/sys", "sysfs", "", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"devtmpfs", "/dev", "devtmpfs", "mode=0755", unix.MS_NOSUID},
		{"devpts", "/dev/pts", "devpts", "newinstance,ptmxmode=0666,mode=0620", unix.MS_NOSUID | unix.MS_NOEXEC},
		{"tmpfs", "/dev/shm", "tmpfs", "mode=1777", unix.MS_NOSUID | unix.MS_NODEV},
		{"tmpfs", "/run", "tmpfs", "mode=0755", unix.MS_NOSUID | unix.MS_NODEV},
		{"tmpfs", "/tmp", "tmpfs", "mode=1777", unix.MS_NOSUID | unix.MS_NODEV},
	}
	var errs []error
	for _, m := range mounts {
		os.MkdirAll(m.target, 0o755)
		if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil && !errors.Is(err, unix.EBUSY) {
			errs = append(errs, fmt.Errorf("mount %s: %w", m.target, err))
		}
	}
	// The workspace is a disk of its own when the host attaches one,
	// otherwise memory.
	os.MkdirAll(workdir, 0o755)
	if _, err := os.Stat("/dev/vdb"); err == nil {
		if err := unix.Mount("/dev/vdb", workdir, "ext4", unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
			errs = append(errs, fmt.Errorf("mount workspace disk: %w", err))
		}
	} else if err := unix.Mount("tmpfs", workdir, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755,size=50%"); err != nil {
		errs = append(errs, fmt.Errorf("mount workspace: %w", err))
	}
	if err := unix.Sethostname([]byte("ephemera")); err != nil {
		errs = append(errs, err)
	}
	if err := loopbackUp(); err != nil {
		errs = append(errs, fmt.Errorf("loopback: %w", err))
	}
	return errors.Join(errs...)
}

// loopbackUp brings up lo, so servers can listen on 127.0.0.1 even though
// the VM has no other network.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

func agent() {
	ln, err := vsock.Listen(guest.Port, nil)
	if err != nil {
		log.Fatalf("listen on vsock port %d: %v", guest.Port, err)
	}
	srv := &guest.Server{Workdir: workdir, Env: baseEnv}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 30 * time.Second}
	if err := hs.Serve(ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func init() {
	// A stray SIGPIPE from a closed host connection must not kill PID 1.
	signalIgnore(syscall.SIGPIPE)
}
