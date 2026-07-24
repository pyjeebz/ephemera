// Command eph-agent runs inside an ephemera guest and executes commands on
// behalf of the host.
//
// It listens on AF_VSOCK, which needs no network interface, no IP address, and
// no open port on any host-visible network — the only path in is through the
// VMM's own multiplexer socket, reachable solely by whoever owns the machine.
//
// Go's net package has no vsock support, so the socket is set up with raw
// syscalls. That is the whole reason this file touches x/sys/unix.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/pyjeebz/ephemera/internal/agent"
)

// fallbackPath is used when the agent was started without one. The init script
// normally sets it, but as PID 1 there is no shell profile to fall back on, and
// a missing PATH turns every command into "executable file not found".
const fallbackPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func main() {
	if os.Getenv("PATH") == "" {
		_ = os.Setenv("PATH", fallbackPath)
	}

	lfd, err := listen(agent.Port)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("eph-agent: listening on vsock port %d\n", agent.Port)

	for {
		// SOCK_CLOEXEC keeps the connection out of the commands we exec, so a
		// long-lived child cannot hold the stream open after we close it.
		// SOCK_NONBLOCK lets os.NewFile hand the fd to Go's poller instead of
		// parking an OS thread per connection.
		cfd, _, err := unix.Accept4(lfd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
				continue
			}
			fatal(fmt.Errorf("accept: %w", err))
		}
		go serve(cfd)
	}
}

// listen binds a vsock port inside the guest.
//
// VMADDR_CID_ANY is the guest's own context id: we accept from whatever CID the
// VMM presents, rather than hard-coding the guest_cid the host configured.
func listen(port uint32) (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("socket(AF_VSOCK): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("bind vsock port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("listen: %w", err)
	}
	return fd, nil
}

// serve handles one connection: read a request, run it, stream the result.
//
// A panic here must not take down the agent, which is usually PID 1 — the
// kernel panics if PID 1 dies.
func serve(fd int) {
	f := os.NewFile(uintptr(fd), "vsock-conn")
	defer f.Close()

	w := &frameWriter{enc: json.NewEncoder(f)}
	defer func() {
		if r := recover(); r != nil {
			w.send(agent.Frame{Kind: agent.FrameError, Error: fmt.Sprint(r)})
		}
	}()

	dec := json.NewDecoder(f)
	var req agent.ExecRequest
	if err := dec.Decode(&req); err != nil {
		w.send(agent.Frame{Kind: agent.FrameError, Error: "decode request: " + err.Error()})
		return
	}

	// An interactive session speaks raw bytes, not frames, so it takes over the
	// connection entirely and the frame writer above is not used for it. The
	// decoder may have read past the request into the first keystrokes, so the
	// raw relay starts from its leftover buffer before reading the socket — the
	// same care the vsock handshake needs, for the same reason.
	if req.PTY {
		in := io.MultiReader(dec.Buffered(), f)
		if err := runPTY(req, in, f); err != nil {
			// The stream is raw; there is no frame to report an error in, so the
			// best we can do is write a line the terminal will show.
			fmt.Fprintf(f, "\r\neph-agent: %v\r\n", err)
		}
		return
	}

	if len(req.Cmd) == 0 {
		w.send(agent.Frame{Kind: agent.FrameError, Error: "empty command"})
		return
	}
	if err := run(req, w); err != nil {
		w.send(agent.Frame{Kind: agent.FrameError, Error: err.Error()})
	}
}

// run executes the command, streaming output as it is produced rather than
// buffering it, so a long-running command reports progress.
func run(req agent.ExecRequest, w *frameWriter) error {
	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)
	cmd.Dir = req.Cwd
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", req.Cmd[0], err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(stdout, agent.FrameStdout, w) }()
	go func() { defer wg.Done(); pump(stderr, agent.FrameStderr, w) }()
	wg.Wait() // both pipes hit EOF, so all output is sent before the exit frame

	code := 0
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return fmt.Errorf("wait: %w", err)
		}
		// A non-zero exit is a result, not an agent failure.
		code = ee.ExitCode()
	}
	w.send(agent.Frame{Kind: agent.FrameExit, Code: code})
	return nil
}

// pump forwards one output stream as frames until it closes.
func pump(r io.Reader, kind agent.FrameKind, w *frameWriter) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			w.send(agent.Frame{Kind: kind, Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			return
		}
	}
}

// frameWriter serialises frames from the stdout and stderr goroutines onto one
// connection.
type frameWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (w *frameWriter) send(f agent.Frame) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.enc.Encode(f) // the host hanging up is normal; nothing to do about it
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "eph-agent:", err)
	os.Exit(1)
}
