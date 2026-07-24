package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/pyjeebz/ephemera/internal/agent"
)

// runPTY gives the connection an interactive session: it allocates a
// pseudo-terminal, runs a shell attached to it, and relays raw bytes between the
// terminal and the connection until the shell exits or the host hangs up.
//
// This is what makes a machine feel like a computer rather than a place to fire
// one-off commands — a real terminal, with job control, line editing, and full-
// screen programs, over the same vsock the exec path uses.
func runPTY(req agent.ExecRequest, in io.Reader, conn *os.File) error {
	// Allocate a pty pair. Opening /dev/ptmx yields the master; unlocking it and
	// asking for its number names the matching slave under /dev/pts.
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open /dev/ptmx: %w", err)
	}
	masterFile := os.NewFile(uintptr(master), "pty-master")
	defer masterFile.Close()

	if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		return fmt.Errorf("unlock pty: %w", err)
	}
	ptn, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		return fmt.Errorf("get pty number: %w", err)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", ptn)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", slavePath, err)
	}
	defer slave.Close()

	if req.Rows > 0 && req.Cols > 0 {
		_ = unix.IoctlSetWinsize(master, unix.TIOCSWINSZ, &unix.Winsize{Row: req.Rows, Col: req.Cols})
	}

	argv := req.Cmd
	if len(argv) == 0 {
		// A real box has bash; fall back to sh only if it somehow does not.
		if _, err := os.Stat("/bin/bash"); err == nil {
			argv = []string{"/bin/bash"}
		} else {
			argv = []string{"/bin/sh"}
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = ptyEnv(req)
	// Setsid + Setctty makes the slave the shell's controlling terminal, which is
	// what gives it job control and signal handling (Ctrl-C, Ctrl-Z). Ctty
	// defaults to the child's stdin, which is the slave.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", argv[0], err)
	}
	// The child holds its own copy of the slave now; the parent does not need one.
	_ = slave.Close()

	// Relay both directions over the session framing. Host frames carry either
	// keystrokes (written to the pty) or resize events (applied to the pty);
	// pty output goes back as data frames. The session ends when the shell exits
	// (master read fails) or the host hangs up (frame read fails); either way,
	// close both ends so the other side unblocks, then reap the shell.
	go func() {
		reader := agent.NewSessionReader(in)
	relay:
		for {
			kind, data, ws, err := reader.Next()
			if err != nil {
				break
			}
			switch kind {
			case agent.FrameData:
				if _, err := masterFile.Write(data); err != nil {
					break relay
				}
			case agent.FrameResize:
				_ = unix.IoctlSetWinsize(master, unix.TIOCSWINSZ, &unix.Winsize{Row: ws.Rows, Col: ws.Cols})
			}
		}
		_ = masterFile.Close() // hang up the pty: the shell gets SIGHUP
	}()

	sw := agent.NewSessionWriter(conn)
	buf := make([]byte, 32*1024)
	for {
		n, err := masterFile.Read(buf)
		if n > 0 {
			if werr := sw.WriteData(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = conn.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil
}

// ptyEnv builds the environment for the interactive shell: a working PATH, a
// home, and the terminal type the client reported so full-screen programs render
// correctly.
func ptyEnv(req agent.ExecRequest) []string {
	term := req.Term
	if term == "" {
		term = "xterm-256color"
	}
	env := []string{
		"PATH=" + fallbackPath,
		"HOME=/root",
		"TERM=" + term,
	}
	return append(env, req.Env...)
}
