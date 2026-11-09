//go:build linux

// Command ephemera-guest runs inside every microVM. Started by the kernel
// as PID 1, it mounts the basic filesystems, brings up loopback networking,
// starts itself again as the agent, and reaps orphaned processes. The agent
// serves the guest API on vsock for the host.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

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

// scratchDisk is the per-VM writable disk the host attaches. Everything the
// sandbox writes lands on it, and the host deletes it with the VM.
const scratchDisk = "/dev/vdb"

func setup() error {
	var errs []error
	writableRoot := false
	if err := overlayRoot(); err == nil {
		writableRoot = true
	} else if !errors.Is(err, errNoScratch) {
		errs = append(errs, fmt.Errorf("writable root: %w", err))
	}

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
	for _, m := range mounts {
		os.MkdirAll(m.target, 0o755)
		if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil && !errors.Is(err, unix.EBUSY) {
			errs = append(errs, fmt.Errorf("mount %s: %w", m.target, err))
		}
	}
	// With a writable root the workspace is a directory on the scratch
	// disk. Without one, it is memory.
	if writableRoot {
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			errs = append(errs, err)
		}
	} else {
		os.MkdirAll(workdir, 0o755)
		if err := unix.Mount("tmpfs", workdir, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755,size=50%"); err != nil {
			errs = append(errs, fmt.Errorf("mount workspace: %w", err))
		}
	}
	if err := unix.Sethostname([]byte("ephemera")); err != nil {
		errs = append(errs, err)
	}
	if err := loopbackUp(); err != nil {
		errs = append(errs, fmt.Errorf("loopback: %w", err))
	}
	return errors.Join(errs...)
}

var errNoScratch = errors.New("no scratch disk")

// overlayRoot makes the root filesystem writable: the shared read-only image
// is the lower layer, the scratch disk holds the changes, and the result
// becomes the new root with pivot_root.
func overlayRoot() error {
	if _, err := os.Stat(scratchDisk); err != nil {
		return errNoScratch
	}
	// The read-only root has no writable place for mount points. The
	// kernel mounts a writable /dev at boot, so build them under it, in
	// memory. (Mount it first if the kernel did not.)
	if _, err := os.Stat("/dev/null"); err != nil {
		if err := unix.Mount("devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"); err != nil {
			return fmt.Errorf("mount /dev: %w", err)
		}
	}
	const base = "/dev/.ephemera"
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", base, "tmpfs", 0, "mode=0700"); err != nil {
		return fmt.Errorf("tmpfs on %s: %w", base, err)
	}
	scratch, root := base+"/scratch", base+"/root"
	for _, d := range []string{scratch, root} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	if err := unix.Mount(scratchDisk, scratch, "ext4", unix.MS_NODEV, ""); err != nil {
		return fmt.Errorf("mount scratch disk: %w", err)
	}
	for _, d := range []string{scratch + "/upper", scratch + "/work"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	opts := "lowerdir=/,upperdir=" + scratch + "/upper,workdir=" + scratch + "/work"
	if err := unix.Mount("overlay", root, "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}
	if err := os.MkdirAll(root+"/.oldroot", 0o700); err != nil {
		return err
	}
	if err := unix.PivotRoot(root, root+"/.oldroot"); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}
	// The overlay keeps the scratch disk in use; the old root goes.
	if err := unix.Unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	return os.Remove("/.oldroot")
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
	// Programs reach the network, if the sandbox has any, only through
	// this proxy, which forwards to the host's egress proxy over vsock.
	eln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", guest.EgressPort))
	if err != nil {
		log.Printf("egress proxy: %v", err)
	} else {
		go guest.ServeEgress(eln, func() (net.Conn, error) {
			return vsock.Dial(vsock.Host, guest.EgressPort, nil)
		})
	}
	srv := &guest.Server{Workdir: workdir, Env: baseEnv, Restore: restore}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 30 * time.Second}
	if err := hs.Serve(ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// restore runs right after the VM is restored from a snapshot.
func restore(req guest.RestoreRequest) error {
	tv := unix.NsecToTimeval(req.UnixNano)
	if err := unix.Settimeofday(&tv); err != nil {
		return fmt.Errorf("set clock: %w", err)
	}
	return reseed(req.Seed)
}

// reseed mixes the host's randomness into the kernel's pool, credits it,
// and forces the generator to reseed now, so this clone's random numbers
// differ from every other clone of the same snapshot.
func reseed(seed []byte) error {
	f, err := os.OpenFile("/dev/urandom", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	// struct rand_pool_info { int entropy_count; int buf_size; __u32 buf[]; }
	buf := make([]byte, 8+len(seed))
	binary.LittleEndian.PutUint32(buf[0:], uint32(len(seed)*8))
	binary.LittleEndian.PutUint32(buf[4:], uint32(len(seed)))
	copy(buf[8:], seed)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDADDENTROPY, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return fmt.Errorf("add entropy: %w", errno)
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDRESEEDCRNG, 0); errno != 0 {
		return fmt.Errorf("reseed: %w", errno)
	}
	return nil
}

func init() {
	// A stray SIGPIPE from a closed host connection must not kill PID 1.
	signalIgnore(syscall.SIGPIPE)
}
