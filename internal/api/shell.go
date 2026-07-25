package api

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/wsutil"
)

// shellWS bridges a browser terminal to an interactive shell in a box. It
// upgrades to a WebSocket and runs the guest's pty session over vsock — the same
// path `eph ssh` uses — so the web terminal and the CLI terminal are the same
// thing behind different front doors.
//
// The browser tags each message it sends so the daemon can tell keystrokes from
// resizes over one socket, mirroring the session's own framing:
//
//	data:   [0x00][utf-8 bytes]        keystrokes
//	resize: [0x01][uint16 rows][cols]  terminal size changed
//
// The daemon writes the shell's output back as raw binary messages (all output,
// so no tag needed). Reachable to a browser only on the opt-in -http surface.
func (s *Server) shellWS(w http.ResponseWriter, r *http.Request) {
	m, _, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if m.VsockPath() == "" {
		writeError(w, http.StatusBadRequest, errors.New("machine has no reachable agent socket"))
		return
	}

	ws, err := wsutil.Upgrade(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer ws.Close()

	// The shell runs until it exits or the socket drops; cancelling closes the
	// vsock connection inside Shell, which ends the session.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Browser keystrokes reach Shell as an io.Reader; a pipe carries them.
	pr, pw := io.Pipe()
	resize := make(chan agent.WinSize, 1)

	go func() {
		defer cancel()
		defer pw.Close()
		for {
			msg, err := ws.ReadBinary()
			if err != nil {
				return
			}
			if len(msg) == 0 {
				continue
			}
			switch msg[0] {
			case agent.FrameData:
				if _, err := pw.Write(msg[1:]); err != nil {
					return
				}
			case agent.FrameResize:
				if len(msg) >= 5 {
					ws := agent.WinSize{
						Rows: binary.BigEndian.Uint16(msg[1:3]),
						Cols: binary.BigEndian.Uint16(msg[3:5]),
					}
					// Keep only the latest size, and never block: a dropped
					// intermediate resize is harmless (the terminal re-sends), a
					// blocked reader would wedge the whole session.
					select {
					case <-resize:
					default:
					}
					select {
					case resize <- ws:
					default:
					}
				}
			}
		}
	}()

	// A default size is fine: the browser sends its real size the moment it
	// connects, and the guest pty reflows on that first resize.
	req := agent.ExecRequest{Rows: 24, Cols: 80, Term: "xterm-256color"}
	if err := m.Shell(ctx, req, pr, wsWriter{ws}, resize); err != nil && ctx.Err() == nil {
		s.log.Debug("shell session ended", "id", m.ID, "err", err)
	}
}

// wsWriter adapts a WebSocket to io.Writer, sending each write as one binary
// message — which is how the shell's output reaches the browser terminal.
type wsWriter struct{ ws *wsutil.Conn }

func (w wsWriter) Write(p []byte) (int, error) {
	if err := w.ws.WriteBinary(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
