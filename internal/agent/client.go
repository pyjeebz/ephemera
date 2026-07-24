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

// Shell opens an interactive session in the guest and relays raw bytes between
// the local terminal (in and out) and a shell running on a pseudo-terminal
// inside the machine. It returns when the shell exits or the connection drops.
//
// The caller is responsible for putting the local terminal into raw mode and
// restoring it; this function only moves bytes.
func Shell(ctx context.Context, udsPath string, req ExecRequest, in io.Reader, out io.Writer) error {
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

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("agent: send shell request: %w", err)
	}

	// Local keystrokes to the guest, in the background. This copy blocks reading
	// the local input and only unblocks when the process exits, which is fine for
	// a foreground command like `eph shell`; the important direction is the other
	// one, which returns cleanly when the shell ends.
	go func() { _, _ = io.Copy(conn, in) }()

	// Guest output to the local terminal, in the foreground. Returns when the
	// shell exits (the guest closes the connection).
	if _, err := io.Copy(out, conn); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("agent: shell session: %w", err)
	}
	return nil
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
