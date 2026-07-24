package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/pyjeebz/ephemera/internal/vsock"
)

// DialTimeout bounds a single connection attempt to the guest agent.
const DialTimeout = 5 * time.Second

// Exec runs one command in the guest and streams its output.
//
// It returns the command's exit status, which may be non-zero without an error:
// a command that fails is a result, not a transport problem. A non-nil error
// means the command could not be run or the stream broke.
func Exec(ctx context.Context, udsPath string, req ExecRequest, stdout, stderr io.Writer) (int, error) {
	conn, err := vsock.Dial(udsPath, Port, DialTimeout)
	if err != nil {
		return -1, err
	}
	defer conn.Close()

	// The agent protocol has no cancel message, so cancellation is expressed by
	// closing the connection out from under the reader.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return -1, fmt.Errorf("agent: send request: %w", err)
	}

	dec := json.NewDecoder(conn)
	for {
		var f Frame
		if err := dec.Decode(&f); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return -1, fmt.Errorf("agent: %w", ctxErr)
			}
			if errors.Is(err, io.EOF) {
				return -1, errors.New("agent: guest closed the stream before reporting an exit status")
			}
			return -1, fmt.Errorf("agent: read frame: %w", err)
		}

		switch f.Kind {
		case FrameStdout:
			if stdout != nil {
				if _, err := stdout.Write(f.Data); err != nil {
					return -1, fmt.Errorf("agent: write stdout: %w", err)
				}
			}
		case FrameStderr:
			if stderr != nil {
				if _, err := stderr.Write(f.Data); err != nil {
					return -1, fmt.Errorf("agent: write stderr: %w", err)
				}
			}
		case FrameExit:
			return f.Code, nil
		case FrameError:
			return -1, fmt.Errorf("agent: %s", f.Error)
		default:
			return -1, fmt.Errorf("agent: unknown frame kind %q", f.Kind)
		}
	}
}

// Shell opens an interactive session in the guest and relays between the local
// terminal (in and out) and a shell running on a pseudo-terminal inside the
// machine. Resize events sent on the resize channel are forwarded so the guest
// pty follows the local terminal's size; resize may be nil for a fixed size.
// It returns when the shell exits or the connection drops.
//
// The caller is responsible for putting the local terminal into raw mode and
// restoring it; this function only moves bytes.
func Shell(ctx context.Context, udsPath string, req ExecRequest, in io.Reader, out io.Writer, resize <-chan WinSize) error {
	req.PTY = true
	conn, err := vsock.Dial(udsPath, Port, DialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Cancellation closes the connection, which ends both relays.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	// Marshal without a trailing newline: the stream switches to binary frames
	// immediately after the request, and an Encoder's newline would be read as a
	// frame kind. The guest's JSON decoder stops cleanly at the closing brace.
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("agent: encode shell request: %w", err)
	}
	if _, err := conn.Write(body); err != nil {
		return fmt.Errorf("agent: send shell request: %w", err)
	}

	sw := NewSessionWriter(conn)

	// Local keystrokes to the guest, as data frames. This read blocks on the
	// local input and only unblocks when the process exits, which is fine for a
	// foreground command like `eph shell`.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				if werr := sw.WriteData(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Resize events as resize frames, until the channel closes or the session ends.
	go func() {
		for {
			select {
			case ws, ok := <-resize:
				if !ok {
					return
				}
				_ = sw.WriteResize(ws)
			case <-done:
				return
			}
		}
	}()

	// Guest output to the local terminal, in the foreground. Returns when the
	// shell exits (the guest closes the connection).
	reader := NewSessionReader(conn)
	for {
		kind, data, _, rerr := reader.Next()
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return fmt.Errorf("agent: shell session: %w", rerr)
		}
		if kind == FrameData {
			if _, werr := out.Write(data); werr != nil {
				return werr
			}
		}
	}
}

// WaitReady blocks until the guest agent accepts a connection.
//
// The VMM's socket exists from the moment vsock is configured, so connecting is
// not proof of anything — the handshake only succeeds once something is
// listening on the guest port, which is what makes this a real readiness probe
// for the guest rather than for Firecracker.
func WaitReady(ctx context.Context, udsPath string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var last error
	for {
		conn, err := vsock.Dial(udsPath, Port, DialTimeout)
		if err == nil {
			return conn.Close()
		}
		last = err

		select {
		case <-ctx.Done():
			return fmt.Errorf("agent: never became ready: %w (last attempt: %v)", ctx.Err(), last)
		case <-ticker.C:
		}
	}
}
