package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/vsock"
	"github.com/pyjeebz/ephemera/internal/wsutil"
)

// desktopWS bridges a browser to a box's graphical desktop over RFB: it upgrades
// to a WebSocket and proxies the raw framebuffer stream to the box's desktop vsock
// port. It is the daemon's own version of what `eph desktop` does locally, so the
// web UI can embed a desktop without a separate bridge process.
//
// videoWS is the same, one port over: the box's H.264 stream instead of RFB, for
// smoother motion. Both are reachable to a browser only on the opt-in -http
// surface, and both travel out of the box over vsock — a private channel — before
// being re-exposed to localhost.
func (s *Server) desktopWS(w http.ResponseWriter, r *http.Request) {
	s.proxyPort(w, r, agent.DesktopPort)
}

func (s *Server) videoWS(w http.ResponseWriter, r *http.Request) {
	s.proxyPort(w, r, agent.VideoPort)
}

// proxyPort upgrades to a WebSocket and pumps bytes both ways between it and a
// guest vsock port.
func (s *Server) proxyPort(w http.ResponseWriter, r *http.Request, port uint32) {
	m, _, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	vsockPath := m.VsockPath()
	if vsockPath == "" {
		writeError(w, http.StatusBadRequest, errors.New("machine has no reachable agent socket"))
		return
	}

	// The timeout bounds only the vsock CONNECT handshake; Dial clears the
	// deadline once attached, so the long-lived stream is not cut off.
	guest, err := vsock.Dial(vsockPath, port, 10*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer guest.Close()

	ws, err := wsutil.Upgrade(w, r)
	if err != nil {
		// The connection is not hijacked on failure, so a normal error is fine.
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer ws.Close()

	wsutil.Proxy(ws, guest)
}
