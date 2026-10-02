// Package relay is the public face of Ephemera: the HTTP server a
// receiver's browser talks to. It maps share ids to pages and Range
// requests to reads on the hub.
//
// Routes:
//
//	GET /            placeholder
//	GET /:id         receiver page (state from /:id/status)
//	GET /:id/file    the bytes: Range-honoring, streamed in chunks
//	GET /:id/status  JSON: {state, name, size, mime, expires}
package relay

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	httprange "ephemera.run/ephemera/internal/range" // dir range; pkg httprange
	"ephemera.run/ephemera/internal/share"
)

//go:embed all:receiver
var receiverFS embed.FS

// ServerConfig tunes the HTTP surface.
type ServerConfig struct {
	// Registry is the share table.
	Registry *share.Registry

	// ChunkSize is the max bytes relayed per daemon read while
	// streaming a body. Bounded so a slow receiver can not pin a
	// giant daemon read. Defaults to 256 KiB.
	ChunkSize int64
}

// Server is the relay HTTP handler.
type Server struct {
	cfg ServerConfig
}

// New builds the relay HTTP handler.
func New(cfg ServerConfig) http.Handler {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 256 << 10
	}
	s := &Server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /{id}", s.handlePage)
	mux.HandleFunc("GET /{id}/file", s.handleFile)
	mux.HandleFunc("GET /{id}/status", s.handleStatus)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, "<!doctype html><title>Ephemera</title><p>The link that doesn't upload.</p>")
}

// stateFor maps a registry result to the receiver page's four
// states: live, offline, expired, gone(404).
type stateFor struct {
	state  string
	info   share.Info
	handle share.Handle
}

func (s *Server) lookupState(id string) stateFor {
	info, h, err := s.cfg.Registry.Lookup(id)
	switch {
	case errors.Is(err, share.ErrNotFound):
		return stateFor{state: "gone"}
	case errors.Is(err, share.ErrGone):
		return stateFor{state: "expired"}
	case err != nil:
		return stateFor{state: "gone"}
	case h == nil:
		return stateFor{state: "offline", info: info}
	default:
		return stateFor{state: "live", info: info, handle: h}
	}
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st := s.lookupState(id)

	switch st.state {
	case "gone":
		http.NotFound(w, r)
		return
	case "expired":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Robots", "noindex")
		w.WriteHeader(http.StatusGone)
		fmt.Fprintln(w, `<!doctype html><title>Expired — Ephemera</title>
<p>This link has expired. Ephemera links leave no trace.</p>`)
		return
	}

	// live / offline: serve the receiver app; it polls /status
	page, err := receiverFS.ReadFile("receiver/index.html")
	if err != nil {
		http.Error(w, "receiver page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if st.state == "offline" {
		w.Header().Set("Robots", "noindex")
	}
	w.Write(page)
}

// statusBody is the JSON the receiver page polls.
type statusBody struct {
	State   string `json:"state"` // live | offline | expired
	Name    string `json:"name,omitempty"`
	Size    int64  `json:"size,omitempty"`
	MIME    string `json:"mime,omitempty"`
	Expires int64  `json:"expires,omitempty"` // unix seconds
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st := s.lookupState(id)

	switch st.state {
	case "gone":
		http.NotFound(w, r)
		return
	case "expired":
		// 410: the page stops polling on this
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(statusBody{State: "expired"})
		return
	}
	body := statusBody{
		State:   st.state,
		Name:    st.info.Name,
		Size:    st.info.Size,
		MIME:    st.info.MIME,
		Expires: st.info.Expires.Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

// handleFile serves the bytes. Range semantics per PRD §9:
// single ranges honored (206), no-range full-body streamed (200),
// unsatisfiable 416, malformed/multi-range ignored as 200 full.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st := s.lookupState(id)

	switch st.state {
	case "gone":
		http.NotFound(w, r)
		return
	case "expired":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		fmt.Fprintln(w, "This link has expired.")
		return
	case "offline":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "This link streams live from the sender's device. It'll return when the sender is back online.")
		return
	}

	info, h := st.info, st.handle
	size := info.Size

	// 5GB abuse re-check: announced size is re-validated here
	if size <= 0 || size > hubMaxShareSize {
		http.Error(w, "share size out of policy", http.StatusForbidden)
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentTypeOf(info))

	req, err := httprange.Parse(r.Header.Get("Range"), size)
	switch {
	case errors.Is(err, httprange.ErrUnsatisfiable):
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	case errors.Is(err, httprange.ErrMalformed), errors.Is(err, httprange.ErrNoRange):
		// 200 full body
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
		s.streamBody(w, h, 0, size)
		return
	case err != nil:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 206 partial
	w.Header().Set("Content-Range", req.ContentRange(size))
	w.Header().Set("Content-Length", strconv.FormatInt(req.Length(), 10))
	w.WriteHeader(http.StatusPartialContent)
	s.streamBody(w, h, req.Start, req.Length())
}

// streamBody relays bytes from the daemon to the HTTP response in
// cfg.ChunkSize bounded reads. A daemon drop mid-stream just ends
// the body: browsers treat it like a network hiccup and the page
// recovers via /status polling.
func (s *Server) streamBody(w io.Writer, h share.Handle, start, length int64) {
	remaining := length
	offset := start
	for remaining > 0 {
		n := min(s.cfg.ChunkSize, remaining)
		data, err := h.IssueRead(offset, n)
		if err != nil {
			// partial body delivered so far is all the receiver
			// gets; the connection just ends
			return
		}
		if int64(len(data)) < n {
			return // short read: end the body
		}
		if _, err := w.Write(data); err != nil {
			return // receiver hung up; stop reading from the daemon
		}
		offset += n
		remaining -= n
	}
}

// contentTypeOf fills gaps the daemon's announcement may have.
func contentTypeOf(info share.Info) string {
	if info.MIME != "" {
		return info.MIME
	}
	return mime.TypeByExtension(strings.ToLower(path.Ext(info.Name)))
}

// hubMaxShareSize mirrors the daemon's abuse cap. The relay does
// not import the daemon package (it must not); the constant is
// duplicated by design and asserted equal in a test.
const hubMaxShareSize = int64(5 << 30)
