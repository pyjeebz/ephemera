package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/vsock"
	"github.com/pyjeebz/ephemera/internal/wsutil"
)

// desktopWS bridges a browser to a box's graphical desktop: it upgrades to a
// WebSocket and proxies the RFB stream to the machine's desktop vsock port. It is
// the daemon's own version of what `eph desktop` does locally, so the web UI can
// embed a desktop without a separate bridge process.
//
// This is reachable on whichever listeners the daemon serves; a browser reaches
// it only when the daemon was started with -http (a localhost TCP surface, off by
// default). The pixels still travel out of the box over vsock — a private
// channel — and are only ever re-exposed to localhost.
func (s *Server) desktopWS(w http.ResponseWriter, r *http.Request) {
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
	// deadline once attached, so the long-lived RFB stream is not cut off.
	guest, err := vsock.Dial(vsockPath, agent.DesktopPort, 10*time.Second)
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
