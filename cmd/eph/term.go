package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// Host-terminal handling for `eph shell`. An interactive session needs the local
// terminal put into raw mode — no echo, no line buffering, no signal
// interpretation — so every keystroke passes through to the guest's pty, which
// is the thing actually running the shell. These are the few termios ioctls that
// takes, done directly rather than pulling in a terminal library.

// isTerminal reports whether f is a terminal (a tty). A non-terminal stdin means
// there is no interactive session to open.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// termSize returns the terminal's current size, defaulting to 24x80 if it cannot
// be read, so the guest pty starts at a sensible shape.
func termSize(f *os.File) (rows, cols uint16) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Row == 0 || ws.Col == 0 {
		return 24, 80
	}
	return ws.Row, ws.Col
}

// makeRaw switches fd into raw mode and returns the previous settings so the
// caller can restore them. Raw mode is the standard set of termios flags cleared
// to stop the local terminal touching the byte stream: the guest echoes and
// edits, not us.
func makeRaw(fd int) (*unix.Termios, error) {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	old := *t

	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	// Read returns as soon as one byte is available, with no inter-byte timer, so
	// keystrokes reach the guest immediately.
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		return nil, err
	}
	return &old, nil
}

// restoreTerm puts fd back to a previously captured state. Safe to call more than
// once, which the shell command does to be sure the terminal is sane on the way
// out even if the session errored.
func restoreTerm(fd int, old *unix.Termios) {
	if old != nil {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, old)
	}
}
