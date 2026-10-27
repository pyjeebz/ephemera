// Package relay is the public face of Ephemera: the HTTP server a
// receiver's browser talks to. It maps share ids to pages and Range
// requests to reads on the hub.
//
// Routes:
//
//	GET /            what this server is; any other unknown path 404s
//	GET /:id         receiver page (state from /:id/status)
//	GET /:id/file    the bytes: Range-honoring, streamed in chunks
//	GET /:id/status  JSON: {state, name, size, mime, expires, ...}
//	POST /:id/report a receiver reports the link: {reason, note}
//
// Admin (bearer token): GET /admin/metrics, GET /admin/reports,
// GET /admin/suspended, POST /admin/kill {id, suspend},
// POST /admin/suspend {account}, POST /admin/unsuspend {account}.
package relay

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"

	"ephemera.run/ephemera/internal/abuse"
	"ephemera.run/ephemera/internal/away"
	"ephemera.run/ephemera/internal/cache"
	"ephemera.run/ephemera/internal/metrics"
	httprange "ephemera.run/ephemera/internal/range" // dir range; pkg httprange
	"ephemera.run/ephemera/internal/ratelimit"
	"ephemera.run/ephemera/internal/share"
	"ephemera.run/ephemera/internal/upload"
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

	// TrustedProxies are the reverse proxies in front of the relay.
	// A request from one of them is limited under the client address
	// it forwarded in X-Forwarded-For; from anywhere else the header
	// is ignored, since a client can send whatever it likes. Empty:
	// every request is limited under its peer address.
	TrustedProxies []netip.Prefix

	// ClientIPHeader, if set, names a header the proxy in front of the
	// relay sets to the client's address, like Fly.io's Fly-Client-IP.
	// It wins over X-Forwarded-For. With TrustedProxies set it's read
	// only on requests from them; without, on every request, so leave
	// TrustedProxies empty only where that proxy is the sole way in.
	ClientIPHeader string

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

	// Uploads is the disk store of background uploads. Stored bytes
	// are served from it whether the sender is online or not. Nil
	// disables uploads server-wide.
	Uploads *upload.Store

	// Reports is the link report queue. Nil disables reporting.
	Reports *abuse.Reports

	// ReportLimiter, if set, throttles reports per IP on top of the
	// general Limiter: reports are cheap to send and costly to read.
	ReportLimiter *ratelimit.Limiter

	// Suspensions is the suspended-accounts list admins edit. Nil
	// disables suspension.
	Suspensions *abuse.Suspensions
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
	mux.HandleFunc("POST /{id}/report", s.handleReport)
	mux.HandleFunc("GET /admin/metrics", s.guardAdmin(s.handleMetrics))
	mux.HandleFunc("POST /admin/kill", s.guardAdmin(s.handleKill))
	mux.HandleFunc("GET /admin/reports", s.guardAdmin(s.handleReports))
	mux.HandleFunc("GET /admin/suspended", s.guardAdmin(s.handleSuspended))
	mux.HandleFunc("POST /admin/suspend", s.guardAdmin(s.handleSuspend))
	mux.HandleFunc("POST /admin/unsuspend", s.guardAdmin(s.handleUnsuspend))
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
		ok, rem, resetIn := s.cfg.Limiter.Allow(s.rateKey(r))
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

// clientIP is the address a request came from: the ClientIPHeader if
// it's set and may be read, else its peer, or behind trusted proxies
// the nearest X-Forwarded-For hop that isn't one of them. The walk goes right to left because each
// proxy appends the address it saw: the right end was written by our
// own proxy, the left end is whatever the client claimed. A hop that
// doesn't parse stops the walk at the proxy that forwarded it.
func (s *Server) clientIP(r *http.Request) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
	ip := peer.Addr().Unmap()
	trusted := s.trustedProxy(ip)
	if h := s.cfg.ClientIPHeader; h != "" && (trusted || len(s.cfg.TrustedProxies) == 0) {
		if client, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get(h))); err == nil {
			return client.Unmap().String()
		}
	}
	if !trusted {
		return ip.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		ip = hop.Unmap()
		if !s.trustedProxy(ip) {
			break
		}
	}
	return ip.String()
}

// rateKey is the bucket a request is limited under: the client's IPv4
// address, or the /64 holding its IPv6 one. ISPs hand a subscriber a
// whole /64 (or more), so a bucket per IPv6 address would let one
// client rotate through 2^64 fresh buckets.
func (s *Server) rateKey(r *http.Request) string {
	ip := s.clientIP(r)
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if !addr.Is6() {
		return addr.String()
	}
	p, _ := addr.Prefix(64)
	return p.String()
}

func (s *Server) trustedProxy(ip netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// guardAdmin requires the configured bearer token, compared in
// constant time.
func (s *Server) guardAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			http.Error(w, "admin disabled", http.StatusNotFound)
			return
		}
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.cfg.AdminToken
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
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
// learns on next read. Body: {"id": "...", "suspend": bool}. With
// suspend, the sender's account is suspended too and its other live
// links killed; the account comes from the live share, or from a
// report if the link already expired.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string
		Suspend bool
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id required"})
		return
	}
	info, _, lookupErr := s.cfg.Registry.Lookup(body.ID)
	killed := s.cfg.Registry.Kill(body.ID)
	metrics.AdminKills.Add(1)
	if !body.Suspend {
		status := http.StatusOK
		if !killed {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"killed": killed})
		return
	}

	account := info.Account
	if lookupErr != nil && s.cfg.Reports != nil {
		account = s.cfg.Reports.AccountFor(body.ID)
	}
	if account == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"killed": killed, "error": "no sender account known for this link"})
		return
	}
	also, err := s.suspend(account)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"killed": killed, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"killed": killed, "suspended": account, "also_killed": also})
}

// suspend records an account suspension and kills its live links.
func (s *Server) suspend(account string) ([]string, error) {
	if s.cfg.Suspensions == nil {
		return nil, errors.New("suspensions are disabled on this relay")
	}
	if err := s.cfg.Suspensions.Suspend(account); err != nil {
		return nil, err
	}
	metrics.AccountsSuspended.Add(1)
	return s.cfg.Registry.KillAccount(account), nil
}

// handleSuspend suspends an account. Body: {"account": "..."}
func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	var body struct{ Account string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Account == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "account required"})
		return
	}
	killed, err := s.suspend(body.Account)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suspended": body.Account, "killed": killed})
}

// handleUnsuspend lifts a suspension. Body: {"account": "..."}
func (s *Server) handleUnsuspend(w http.ResponseWriter, r *http.Request) {
	var body struct{ Account string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Account == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "account required"})
		return
	}
	if s.cfg.Suspensions == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "suspensions are disabled on this relay"})
		return
	}
	if err := s.cfg.Suspensions.Unsuspend(body.Account); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unsuspended": body.Account})
}

// handleSuspended lists suspended accounts.
func (s *Server) handleSuspended(w http.ResponseWriter, r *http.Request) {
	list := []string{}
	if s.cfg.Suspensions != nil {
		list = s.cfg.Suspensions.List()
	}
	writeJSON(w, http.StatusOK, list)
}

// handleReports lists reports, newest first. ?limit=N caps it.
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	list := []abuse.Report{}
	if s.cfg.Reports != nil {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list = s.cfg.Reports.List(limit)
	}
	writeJSON(w, http.StatusOK, list)
}

// maxReportBody caps a report request: a reason and a short note.
const maxReportBody = 8 << 10

// handleReport files a receiver's report against a link that still
// exists. Body: {"reason": one of abuse.Reasons, "note": "..."}.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Reports == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "reporting is disabled on this relay"})
		return
	}
	if s.cfg.ReportLimiter != nil {
		if ok, _, resetIn := s.cfg.ReportLimiter.Allow(s.rateKey(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(resetIn.Seconds()))))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many reports; try again later"})
			return
		}
	}
	id := r.PathValue("id")
	info, _, err := s.cfg.Registry.Lookup(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such link"})
		return
	}
	var body struct{ Reason, Note string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body must be JSON: {reason, note}"})
		return
	}
	_, err = s.cfg.Reports.Add(abuse.Report{
		ShareID: id,
		Reason:  body.Reason,
		Note:    strings.TrimSpace(body.Note),
		Name:    info.Name,
		Size:    info.Size,
		MIME:    info.MIME,
		Account: info.Account,
	})
	switch {
	case errors.Is(err, abuse.ErrBadReason), errors.Is(err, abuse.ErrNoteTooLong):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "reasons": abuse.Reasons})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not file the report"})
		return
	}
	metrics.ReportsFiled.Add(1)
	writeJSON(w, http.StatusAccepted, map[string]any{"reported": true})
}

// writeJSON writes v as a JSON response with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handleRoot serves the relay's home page. "GET /" is also the mux's
// catch-all, so anything else that reaches it is a 404.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := receiverFS.ReadFile("receiver/home.html")
	if err != nil {
		http.Error(w, "home page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// stateFor maps a registry result to the receiver page's states:
// live, available (the whole file is on the relay), offline,
// expired, gone(404).
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
	case info.Upload && s.cfg.Uploads != nil && s.cfg.Uploads.Complete(id):
		return stateFor{state: "available", info: info, handle: h}
	case h == nil:
		return stateFor{state: "offline", info: info}
	default:
		return stateFor{state: "live", info: info, handle: h}
	}
}

// uploaded is how many bytes of a share the relay holds on disk, all
// from offset 0: what it can serve without the sender.
func (s *Server) uploaded(info share.Info) int64 {
	if !info.Upload || s.cfg.Uploads == nil {
		return 0
	}
	return s.cfg.Uploads.Have(info.ID)
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

	// live / available / offline: serve the receiver app; it polls /status
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
	State   string `json:"state"` // live | available | offline | expired
	Name    string `json:"name,omitempty"`
	Size    int64  `json:"size,omitempty"`
	MIME    string `json:"mime,omitempty"`
	Expires int64  `json:"expires,omitempty"` // unix seconds
	Replay  bool   `json:"replay,omitempty"`  // offline but cached bytes replayable
	Prefix  int64  `json:"prefix,omitempty"`  // offline: away-mode prefix bytes watchable

	// Upload: the file is uploading to the relay in the background.
	// Uploaded is how much of it is there so far.
	Upload   bool  `json:"upload,omitempty"`
	Uploaded int64 `json:"uploaded,omitempty"`
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

		Upload:   st.info.Upload,
		Uploaded: s.uploaded(st.info),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

// handleFile serves the bytes. Range semantics per PRD §9:
// single ranges honored (206), no-range full-body streamed (200),
// unsatisfiable 416, malformed/multi-range ignored as 200 full.
// Bytes the relay holds from a background upload come off its disk;
// the rest stream live from the sender, or, with the sender offline,
// from layers B and C when they cover the range.
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
	}
	info := st.info
	size := info.Size

	// 5GB abuse re-check: announced size is re-validated here
	if size <= 0 || size > hubMaxShareSize {
		http.Error(w, "share size out of policy", http.StatusForbidden)
		return
	}

	var start, length int64
	ranged := false
	switch rng, err := httprange.Parse(r.Header.Get("Range"), size); {
	case errors.Is(err, httprange.ErrUnsatisfiable):
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	case errors.Is(err, httprange.ErrNoRange), errors.Is(err, httprange.ErrMalformed):
		start, length = 0, size
	case err != nil:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	default:
		start, length, ranged = rng.Start, rng.Length(), true
		// thesis receipt: browsers asking for slices, not files
		metrics.RangeRequests.Add(1)
	}

	// uploaded bytes: off the relay's disk, sender online or not
	if stored := s.uploaded(info); start+length <= stored {
		s.writeHeaders(w, info, start, length, ranged)
		s.uploadBody(w, id, start, length)
		return
	}

	if st.handle != nil {
		s.writeHeaders(w, info, start, length, ranged)
		s.streamBody(w, st.handle, info, start, length)
		return
	}

	metrics.DarkLinkHits.Add(1)
	// layers B and C: what can we serve while the sender is gone?

	// layer B first: exact cached ranges replay from RAM
	if s.cfg.Cache != nil && info.Cacheable && s.cacheCoverage(id, start, length) == 1 {
		s.writeHeaders(w, info, start, length, ranged)
		s.replayBody(w, id, start, length)
		return
	}

	// a partial upload or layer C's away prefix covers the head of
	// the file; serve the covered part of the request
	if covered := min(length, max(0, s.uploaded(info)-start)); covered > 0 {
		s.writeHeaders(w, info, start, covered, true)
		s.uploadBody(w, id, start, covered)
		if covered < length {
			metrics.AwayOutlasts.Add(1) // asked past what's stored
		}
		return
	}
	if s.cfg.Away != nil && info.AwayPrefix > 0 {
		if covered := min(length, max(0, s.cfg.Away.Have(id)-start)); covered > 0 {
			s.writeHeaders(w, info, start, covered, ranged || covered < length)
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
}

// writeHeaders writes the status and headers for a body of length
// bytes at start: 206 with Content-Range when the request asked for a
// range or the body is short of the whole file, else 200.
func (s *Server) writeHeaders(w http.ResponseWriter, info share.Info, start, length int64, partial bool) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentTypeOf(info))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if partial || start > 0 || length < info.Size {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, info.Size))
		w.WriteHeader(http.StatusPartialContent)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// uploadBody writes stored upload bytes to the response.
// Precondition: the store covers [start, start+length).
func (s *Server) uploadBody(w io.Writer, id string, start, length int64) {
	buf := make([]byte, min(s.cfg.ChunkSize, length))
	for offset, end := start, start+length; offset < end; {
		p := buf[:min(int64(len(buf)), end-offset)]
		if err := s.cfg.Uploads.ReadAt(id, p, offset); err != nil {
			return // dropped (expiry, kill): end the body short
		}
		if _, err := w.Write(p); err != nil {
			return
		}
		metrics.BytesServed.Add(int64(len(p)))
		metrics.UploadBytesServed.Add(int64(len(p)))
		offset += int64(len(p))
	}
}

// offlinePrefix reports the bytes from the start of the file the
// relay holds: a partial upload's, else the away prefix's. Reported
// in both live (upload progress) and offline states.
func (s *Server) offlinePrefix(st stateFor, id string) int64 {
	if st.state != "live" && st.state != "offline" {
		return 0
	}
	if st.info.Upload {
		return s.uploaded(st.info)
	}
	if st.info.AwayPrefix <= 0 || s.cfg.Away == nil {
		return 0
	}
	return s.cfg.Away.Have(id)
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

// RestoreUploads puts the shares in the upload store back in the
// share table after a restart, offline until their senders redial,
// so what was uploaded keeps being served. It returns how many.
func RestoreUploads(reg *share.Registry, st *upload.Store) int {
	n := 0
	for _, m := range st.Stored() {
		err := reg.Register(share.Info{
			ID: m.ID, Name: m.Name, Size: m.Size, MIME: m.MIME,
			Expires: m.Expires, Upload: true, Account: m.Account,
		}, nil)
		if err == nil {
			n++
		}
	}
	return n
}
