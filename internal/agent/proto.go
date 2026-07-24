// Package agent defines the protocol between ephemerad on the host and the
// eph-agent process inside a guest, and the host side of that conversation.
//
// The transport is vsock, so this protocol never touches the network: there is
// no listening TCP port in the guest and no host interface to firewall. That
// matters for ephemera's isolation goals — a machine with no NIC at all can
// still be driven.
package agent

// Port is the guest vsock port eph-agent listens on. Firecracker multiplexes
// host connections to it over a single Unix socket.
const Port = 1024

// DesktopPort is a second guest vsock port the agent bridges to the in-guest VNC
// server, so a graphical desktop streams out over the same private channel as
// exec and the shell — no guest network, no open port. A box with no desktop has
// nothing listening behind it, and the bridge simply drops the connection.
const DesktopPort = 1025

// ExecRequest asks the guest to run one command. It is sent as a single JSON
// object, after which the host half-closes nothing and simply reads frames.
//
// Cmd is passed to execve directly rather than through a shell, so callers that
// want shell semantics ask for them explicitly: ["sh", "-c", "..."]. That keeps
// quoting bugs from silently becoming command injection.
//
// When PTY is set the request is an interactive session instead: the guest runs
// Cmd (or a login shell when Cmd is empty) attached to a pseudo-terminal, and
// the connection becomes a raw, bidirectional byte stream — the host's keystrokes
// in, the terminal's output back — with no frames at all. Rows, Cols, and Term
// give the terminal its initial size and type.
type ExecRequest struct {
	Cmd  []string `json:"cmd,omitempty"`
	Env  []string `json:"env,omitempty"`
	Cwd  string   `json:"cwd,omitempty"`
	PTY  bool     `json:"pty,omitempty"`
	Rows uint16   `json:"rows,omitempty"`
	Cols uint16   `json:"cols,omitempty"`
	Term string   `json:"term,omitempty"`
}

// Frame is one message in the response stream. The stream is a sequence of JSON
// objects terminated by exactly one frame of kind Exit or Error.
//
// Data is []byte so encoding/json base64-encodes it, which keeps command output
// that is not valid UTF-8 (or contains newlines) intact over a JSON stream.
type Frame struct {
	Kind  FrameKind `json:"kind"`
	Data  []byte    `json:"data,omitempty"`
	Code  int       `json:"code,omitempty"`
	Error string    `json:"error,omitempty"`
}

// FrameKind distinguishes the messages in a response stream.
type FrameKind string

const (
	// FrameStdout and FrameStderr carry command output as it is produced.
	FrameStdout FrameKind = "stdout"
	FrameStderr FrameKind = "stderr"

	// FrameExit terminates a successful stream and carries the exit status.
	FrameExit FrameKind = "exit"

	// FrameError terminates a stream that failed before or during exec — the
	// command could not be started, for instance. Distinct from FrameExit so a
	// non-zero exit is not confused with an agent failure.
	FrameError FrameKind = "error"
)
