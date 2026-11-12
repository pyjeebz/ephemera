package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
)

// hostState is a joined host the server has heard from.
type hostState struct {
	api.HostHello
	ID, Account, Name, PublicKey string // from its join
	lastSeen                     time.Time
	conn                         *hostConn // nil while the host is not connected
}

// hostConn is a host's open connection. Work is sent down it; the host's
// answers come back on the same connection, matched by Req.
type hostConn struct {
	nc  net.Conn
	wmu sync.Mutex
	enc *json.Encoder

	mu      sync.Mutex
	next    int64
	pending map[string]chan api.HostMessage
	closed  bool
}

func (c *hostConn) send(m api.HostMessage) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return c.enc.Encode(m)
}

// call sends m and waits for the host's result.
func (c *hostConn) call(ctx context.Context, m api.HostMessage) (api.HostMessage, error) {
	ch := make(chan api.HostMessage, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return api.HostMessage{}, errHostGone
	}
	c.next++
	m.Req = strconv.FormatInt(c.next, 10)
	c.pending[m.Req] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, m.Req)
		c.mu.Unlock()
	}()
	if err := c.send(m); err != nil {
		return api.HostMessage{}, err
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return api.HostMessage{}, errHostGone
		}
		return res, nil
	case <-ctx.Done():
		return api.HostMessage{}, ctx.Err()
	}
}

// deliver hands a result to whoever is waiting for it. It never blocks,
// and runs under the lock so shut can't close the channel mid-send.
func (c *hostConn) deliver(m api.HostMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch := c.pending[m.Req]; ch != nil && !c.closed {
		delete(c.pending, m.Req)
		select {
		case ch <- m:
		default:
		}
	}
}

// shut fails every call still waiting.
func (c *hostConn) shut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.nc.Close()
}

var errHostGone = errors.New("the host disconnected")

// hostWorkTimeout bounds one create or destroy on a host. A cold boot
// without a snapshot can take a while.
const hostWorkTimeout = 2 * time.Minute

// hostDo sends work to a connected host and waits for its result.
func (s *Server) hostDo(ctx context.Context, id string, m api.HostMessage) (api.HostMessage, error) {
	s.mu.Lock()
	h := s.hosts[id]
	var c *hostConn
	if h != nil {
		c = h.conn
	}
	s.mu.Unlock()
	if c == nil {
		return api.HostMessage{}, fmt.Errorf("host %s is not connected", id)
	}
	ctx, cancel := context.WithTimeout(ctx, hostWorkTimeout)
	defer cancel()
	res, err := c.call(ctx, m)
	if err != nil {
		return res, err
	}
	if !res.OK {
		return res, errors.New(res.Error)
	}
	return res, nil
}

// hostConnect takes a host's connection and keeps it until it drops. The
// host proves who it is by signing a fresh challenge with the key it
// joined with.
func (s *Server) hostConnect(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), api.HostUpgrade) {
		host.WriteError(w, http.StatusUpgradeRequired, "Upgrade: %s required", api.HostUpgrade)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		host.WriteError(w, http.StatusInternalServerError, "connection cannot be upgraded")
		return
	}
	nc, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", api.HostUpgrade)
	if err := buf.Flush(); err != nil {
		nc.Close()
		return
	}
	c := &hostConn{nc: nc, enc: json.NewEncoder(nc), pending: map[string]chan api.HostMessage{}}
	defer c.shut()
	rd := buf.Reader

	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return
	}
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)
	if err := c.send(api.HostMessage{Type: "challenge", Nonce: nonce}); err != nil {
		return
	}
	nc.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hello api.HostMessage
	if err := host.ReadMessage(rd, &hello); err != nil {
		return
	}
	rec, msg, removed := s.checkHello(r.Context(), hello, r.Host, nonce)
	if msg != "" {
		c.send(api.HostMessage{Type: "refused", Error: msg, Removed: removed})
		s.log().Warn("host refused", "err", msg)
		return
	}
	id, name := rec.ID, rec.Name

	s.mu.Lock()
	if s.hosts == nil {
		s.hosts = map[string]*hostState{}
	}
	old := s.hosts[id]
	s.hosts[id] = &hostState{HostHello: *hello.Hello, ID: id, Account: rec.Account, Name: name,
		PublicKey: rec.PublicKey, lastSeen: s.now(), conn: c}
	s.mu.Unlock()
	if old != nil && old.conn != nil {
		old.conn.shut() // a host reconnecting replaces its old connection
	}
	destroy := s.reconcile(r.Context(), id, hello.Sandboxes)
	if err := c.send(api.HostMessage{Type: "welcome", Destroy: destroy}); err != nil {
		return
	}
	s.log().Info("host connected", "host", name, "id", id, "account", rec.Account, "url", hello.Hello.URL, "version", hello.Hello.Version)
	defer func() {
		s.mu.Lock()
		if h := s.hosts[id]; h != nil && h.conn == c {
			h.conn = nil
		}
		s.mu.Unlock()
		s.log().Info("host disconnected", "host", name)
	}()

	for {
		// A host that stops talking is taken for gone.
		nc.SetReadDeadline(time.Now().Add(HostTimeout))
		var m api.HostMessage
		if err := host.ReadMessage(rd, &m); err != nil {
			return
		}
		switch m.Type {
		case "heartbeat":
			s.mu.Lock()
			if h := s.hosts[id]; h != nil && h.conn == c {
				h.lastSeen = s.now()
			}
			s.mu.Unlock()
			destroy := s.reconcile(r.Context(), id, m.Sandboxes)
			if err := c.send(api.HostMessage{Type: "ack", Req: m.Req, Destroy: destroy}); err != nil {
				return
			}
		case "result":
			c.deliver(m)
		}
	}
}

// checkHello checks a host's hello: its protocol, its address, and its
// signature over the challenge with the key it joined with. It returns the
// joined host, or why it is refused, and whether that is because the
// server doesn't know the host at all.
func (s *Server) checkHello(ctx context.Context, m api.HostMessage, serverHost, nonce string) (HostRecord, string, bool) {
	if m.Type != "hello" || m.Hello == nil {
		return HostRecord{}, "the first message must be hello", false
	}
	if m.Hello.Protocol != api.HostProtocol {
		return HostRecord{}, fmt.Sprintf("protocol %d not supported (this server speaks %d); upgrade ephemera-host", m.Hello.Protocol, api.HostProtocol), false
	}
	u, err := url.Parse(m.Hello.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return HostRecord{}, "an http(s) url is required", false
	}
	rec, err := s.hostRecords().Host(ctx, m.Hello.HostID)
	if errors.Is(err, ErrNotFound) {
		return HostRecord{}, "this server has no record of this host: it was removed, or joined another server; run ephemera-host join again", true
	}
	if err != nil {
		s.log().Error("store: host", "err", err)
		return HostRecord{}, "the central server could not look the host up; try again", false
	}
	pub, err := base64.StdEncoding.DecodeString(rec.PublicKey)
	sig, err2 := base64.StdEncoding.DecodeString(m.Hello.Signature)
	if err != nil || err2 != nil || len(pub) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(pub), api.HostSignedText(serverHost, nonce), sig) {
		return HostRecord{}, "the signature does not match the key this host joined with", false
	}
	return rec, "", false
}

// hostJoin makes a host from a one-time join code.
func (s *Server) hostJoin(w http.ResponseWriter, r *http.Request) {
	var req api.JoinRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		host.WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	h := HostRecord{Name: req.Name, PublicKey: req.PublicKey}
	if err := CheckHost(h); err != nil {
		host.WriteError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if !strings.HasPrefix(req.Code, JoinCodePrefix) {
		host.WriteError(w, http.StatusUnauthorized, "%v", ErrBadCode)
		return
	}
	h, err := s.hostRecords().JoinHost(r.Context(), req.Code, h)
	switch {
	case errors.Is(err, ErrBadCode):
		host.WriteError(w, http.StatusUnauthorized, "%v", err)
		return
	case errors.Is(err, ErrNameTaken):
		host.WriteError(w, http.StatusConflict, "%v", err)
		return
	case err != nil:
		s.log().Error("store: join host", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not save the host; try again")
		return
	}
	s.log().Info("host joined", "host", h.Name, "id", h.ID, "account", h.Account)
	writeJSON(w, http.StatusCreated, api.JoinResponse{HostID: h.ID, Account: h.Account})
}

// hostRemove removes one of the caller's hosts. Its sandboxes are destroyed
// first, then it is disconnected, and it can't connect again.
func (s *Server) hostRemove(w http.ResponseWriter, r *http.Request) {
	id, account := r.PathValue("id"), principal(r).Account
	if err := s.hostRecords().RemoveHost(r.Context(), account, id); errors.Is(err, ErrNotFound) {
		host.WriteError(w, http.StatusNotFound, "no host %s", id)
		return
	} else if err != nil {
		s.log().Error("store: remove host", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not remove the host; try again")
		return
	}
	s.mu.Lock()
	var on []*api.Sandbox
	for _, sb := range s.sandboxes {
		if sb.HostID == id {
			on = append(on, sb)
		}
	}
	s.mu.Unlock()
	for _, sb := range on {
		s.hostDo(r.Context(), id, api.HostMessage{Type: "destroy", Sandbox: sb.ID})
		s.end(r.Context(), sb, EndDestroyed)
	}
	s.mu.Lock()
	h := s.hosts[id]
	delete(s.hosts, id)
	s.mu.Unlock()
	if h != nil && h.conn != nil {
		h.conn.shut()
	}
	s.log().Info("host removed", "id", id, "account", account)
	w.WriteHeader(http.StatusNoContent)
}

// hostList lists the caller's hosts, with their public keys so records can
// be checked. Hosts that joined but haven't connected since the server
// started are listed offline.
func (s *Server) hostList(w http.ResponseWriter, r *http.Request) {
	recs, err := s.hostRecords().AccountHosts(r.Context(), principal(r).Account)
	if err != nil {
		s.log().Error("store: list hosts", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not list hosts; try again")
		return
	}
	out := make([]api.Host, 0, len(recs))
	s.mu.Lock()
	for _, rec := range recs {
		h := api.Host{ID: rec.ID, Name: rec.Name, PublicKey: rec.PublicKey}
		if st := s.hosts[rec.ID]; st != nil {
			h.LastSeen, h.URL, h.Online = st.lastSeen.UTC(), st.URL, st.conn != nil
		}
		out = append(out, h)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// reconcile compares what a host says it runs with the server's records.
// It returns the sandboxes the host should destroy, because the server has
// no record of them, and drops records of sandboxes the host no longer has.
func (s *Server) reconcile(ctx context.Context, id string, reported []string) []string {
	have := make(map[string]bool, len(reported))
	for _, id := range reported {
		have[id] = true
	}
	var destroy []string
	var gone []*api.Sandbox
	s.mu.Lock()
	for _, sid := range reported {
		if sb, ok := s.sandboxes[sid]; !ok || sb.HostID != id {
			destroy = append(destroy, sid)
		}
	}
	for sid, sb := range s.sandboxes {
		if sb.HostID == id && !have[sid] && s.now().Sub(sb.CreatedAt) > createGrace {
			gone = append(gone, sb)
		}
	}
	s.mu.Unlock()
	for _, sid := range destroy {
		s.log().Warn("host runs a sandbox with no record; destroying it", "sandbox", sid, "host", id)
	}
	for _, sb := range gone {
		s.log().Info("sandbox gone from its host; record dropped", "sandbox", sb.ID, "host", id)
		s.end(ctx, sb, EndGone)
	}
	return destroy
}
