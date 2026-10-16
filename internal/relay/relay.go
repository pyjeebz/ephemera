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
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"

	"ephemera.run/ephemera/internal/away"
	"ephemera.run/ephemera/internal/cache"
	"ephemera.run/ephemera/internal/metrics"
	httprange "ephemera.run/ephemera/internal/range" // dir range; pkg httprange
	"ephemera.run/ephemera/internal/ratelimit"
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

	// Limiter, if set, throttles receiver requests per IP.
	Limiter *ratelimit.Limiter

	// AdminToken gates /admin/* routes. Empty disables them.
	AdminToken string

	// Cache is layer B: volatile storage of already-served bytes,
	// serving replay while senders are offline. Only shares the
	// sender opted in (Info.Cacheable) use it. Nil disables the
	// feature server-wide.
	Cache *cache.Store

	// Away is layer C: the relay's store of away-mode prefix
	// blobs, uploaded by opt-in daemons. Nil disables away mode
	// server-wide.
	Away *away.Store
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
	mux.HandleFunc("GET /admin/metrics", s.guardAdmin(s.handleMetrics))
	mux.HandleFunc("POST /admin/kill", s.guardAdmin(s.handleKill))
	return s.limited(mux)
}

// limited wraps the mux with per-IP rate limiting. 429s carry
// Retry-After; the bucket metrics feed the admin counters.
func (s *Server) limited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.Requests.Add(1)
		if s.cfg.Limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		ok, rem, resetIn := s.cfg.Limiter.Allow(ip)
		h := w.Header()
		h.Set("X-RateLimit-Limit", strconv.Itoa(int(s.cfg.Limiter.Burst())))
		h.Set("X-RateLimit-Remaining", strconv.Itoa(rem))
		if !ok {
			metrics.RateLimited429.Add(1)
			h.Set("Retry-After", strconv.Itoa(max(1, int(resetIn.Seconds()))))
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// guardAdmin requires the configured bearer token.
func (s *Server) guardAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			http.Error(w, "admin disabled", http.StatusNotFound)
			return
		}
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.cfg.AdminToken
		if got != want {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleMetrics renders the counter snapshot.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(metrics.Snapshot())
}

// handleKill force-expires a share: registry eviction now, daemon
// learns on next read. Body: {"id": "..."}
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}
	killed := s.cfg.Registry.Kill(body.ID)
	metrics.AdminKills.Add(1)
	w.Header().Set("Content-Type", "application/json")
	if !killed {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintln(w, `{"killed":false}`)
		return
	}
	fmt.Fprintln(w, `{"killed":true}`)
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
	// links are private: never index a link page in any state
	w.Header().Set("X-Robots-Tag", "noindex")
	id := r.PathValue("id")
	st := s.lookupState(id)

	switch st.state {
	case "gone":
		http.NotFound(w, r)
		return
	case "expired":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
	w.Write(page)
}

// statusBody is the JSON the receiver page polls.
type statusBody struct {
	State   string `json:"state"` // live | offline | expired
	Name    string `json:"name,omitempty"`
	Size    int64  `json:"size,omitempty"`
	MIME    string `json:"mime,omitempty"`
	Expires int64  `json:"expires,omitempty"` // unix seconds
	Replay  bool   `json:"replay,omitempty"`  // offline but cached bytes replayable
	Prefix  int64  `json:"prefix,omitempty"`  // offline: away-mode prefix bytes watchable
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	metrics.StatusPolls.Add(1)
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
		Replay:  st.state == "offline" && st.info.Cacheable && s.cfg.Cache != nil && s.cacheCoverage(id, 0, st.info.Size) > 0,
		Prefix:  s.offlinePrefix(st, id),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

// handleFile serves the bytes. Range semantics per PRD §9:
// single ranges honored (206), no-range full-body streamed (200),
// unsatisfiable 416, malformed/multi-range ignored as 200 full.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex")
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
		metrics.DarkLinkHits.Add(1)
		// layers B and C: what can we serve while the sender is gone?
		var start, length int64
		switch rng, err := httprange.Parse(r.Header.Get("Range"), st.info.Size); {
		case errors.Is(err, httprange.ErrUnsatisfiable):
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", st.info.Size))
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		case errors.Is(err, httprange.ErrNoRange), errors.Is(err, httprange.ErrMalformed):
			start, length = 0, st.info.Size
		case err != nil:
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		default:
			start, length = rng.Start, rng.Length()
		}

		// layer B first: exact cached ranges replay from RAM
		if s.cfg.Cache != nil && st.info.Cacheable && s.cacheCoverage(id, start, length) == 1 {
			s.serveOfflineRange(w, st.info, start, length)
			s.replayBody(w, id, start, length)
			return
		}

		// layer C: away prefix. The stored prefix covers the head
		// of the file; serve the covered part of the request.
		if s.cfg.Away != nil && st.info.AwayPrefix > 0 {
			if covered := min(length, max(0, s.cfg.Away.Have(id)-start)); covered > 0 {
				s.serveOfflineRange(w, st.info, start, covered)
				s.awayBody(w, id, start, covered)
				if covered < length {
					// the receiver asked past the prefix end:
					// count it, end the body short
					metrics.AwayOutlasts.Add(1)
				}
				return
			}
		}

		// nothing to serve: the honest offline page
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

	// thesis receipt: browsers asking for slices, not files
	if r.Header.Get("Range") != "" {
		metrics.RangeRequests.Add(1)
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
		s.streamBody(w, h, info, 0, size)
		return
	case err != nil:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 206 partial
	w.Header().Set("Content-Range", req.ContentRange(size))
	w.Header().Set("Content-Length", strconv.FormatInt(req.Length(), 10))
	w.WriteHeader(http.StatusPartialContent)
	s.streamBody(w, h, info, req.Start, req.Length())
}

// offlinePrefix reports the away-prefix bytes the relay holds —
// reported in both live (upload progress) and offline states.
func (s *Server) offlinePrefix(st stateFor, id string) int64 {
	if st.info.AwayPrefix <= 0 || s.cfg.Away == nil {
		return 0
	}
	switch st.state {
	case "live", "offline":
		return s.cfg.Away.Have(id)
	default:
		return 0
	}
}

// serveOfflineRange writes the status and headers for an offline
// (replay or prefix) body, honoring range semantics.
func (s *Server) serveOfflineRange(w http.ResponseWriter, info share.Info, start, length int64) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentTypeOf(info))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if start > 0 || length < info.Size {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, info.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
}

// awayBody writes prefix bytes from the away store to the response.
// Precondition: the store covers [start, start+length).
func (s *Server) awayBody(w io.Writer, id string, start, length int64) {
	remaining := length
	offset := start
	for remaining > 0 {
		n := min(s.cfg.ChunkSize, remaining)
		data := s.cfg.Away.Read(id, offset, n)
		if data == nil {
			return // store shrank: end the body short
		}
		if _, err := w.Write(data); err != nil {
			return
		}
		metrics.BytesServed.Add(n)
		metrics.AwayBytesServed.Add(n)
		offset += n
		remaining -= n
	}
}

// streamBody relays bytes from the daemon to the HTTP response in
// chunk-bounded reads, writing through to the layer-B cache when
// the share opted in. A daemon drop mid-stream just ends the body:
// browsers treat it like a network hiccup and the page recovers via
// /status polling. Served bytes are counted — the thesis receipt.
func (s *Server) streamBody(w io.Writer, h share.Handle, sh share.Info, start, length int64) {
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
		metrics.BytesServed.Add(n)
		s.cacheWriteThrough(sh, offset, data)
		offset += n
		remaining -= n
	}
}

// cacheWriteThrough stores served bytes for replay, grid-aligned.
// Unaligned chunks (range tails) are stored as-is with their own
// offsets: Get matches exactly, coverage walks the grid.
func (s *Server) cacheWriteThrough(sh share.Info, offset int64, data []byte) {
	if s.cfg.Cache == nil || !sh.Cacheable {
		return
	}
	metrics.CacheMisses.Add(0) // no-op: keep metric registered even when unused
	s.cfg.Cache.Put(sh.ID, offset, data)
}

// cacheCoverage answers: what fraction of [start, start+length) is
// currently in the cache? It walks the same chunking streamBody
// used when the bytes were served live, so the probes match the
// stored entries exactly.
func (s *Server) cacheCoverage(id string, start, length int64) float64 {
	if s.cfg.Cache == nil {
		return 0
	}
	cached := int64(0)
	remaining := length
	offset := start
	for remaining > 0 {
		n := min(s.cfg.ChunkSize, remaining)
		if s.cfg.Cache.Get(id, offset, n) != nil {
			cached += n
		}
		offset += n
		remaining -= n
	}
	if length == 0 {
		return 0
	}
	return float64(cached) / float64(length)
}

// replayBody writes cached bytes to the response. Precondition:
// coverage == 1 (the caller checked).
func (s *Server) replayBody(w io.Writer, id string, start, length int64) {
	remaining := length
	offset := start
	for remaining > 0 {
		n := min(s.cfg.ChunkSize, remaining)
		data := s.cfg.Cache.Get(id, offset, n)
		if data == nil {
			// coverage said this was cached; treat a miss as end
			return
		}
		if _, err := w.Write(data); err != nil {
			return
		}
		metrics.BytesServed.Add(n)
		metrics.BytesCachedReplay.Add(n)
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
