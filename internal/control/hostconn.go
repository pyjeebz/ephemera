package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// hostState is a host the server has heard from.
type hostState struct {
	api.HostHello
	lastSeen time.Time
	conn     *hostConn // nil while the host is not connected
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
func (s *Server) hostDo(ctx context.Context, name string, m api.HostMessage) (api.HostMessage, error) {
	s.mu.Lock()
	h := s.hosts[name]
	var c *hostConn
	if h != nil {
		c = h.conn
	}
	s.mu.Unlock()
	if c == nil {
		return api.HostMessage{}, fmt.Errorf("host %s is not connected", name)
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

// hostConnect takes a host's connection and keeps it until it drops.
func (s *Server) hostConnect(w http.ResponseWriter, r *http.Request) {
	if !tokenOK(r, s.HostSecret) {
		host.WriteError(w, http.StatusUnauthorized, "invalid host secret")
		return
	}
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

	nc.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hello api.HostMessage
	if err := host.ReadMessage(rd, &hello); err != nil {
		return
	}
	if msg := checkHello(hello); msg != "" {
		c.send(api.HostMessage{Type: "error", Error: msg})
		s.log().Warn("host refused", "err", msg)
		return
	}
	name := hello.Hello.Name

	s.mu.Lock()
	if s.hosts == nil {
		s.hosts = map[string]*hostState{}
	}
	old := s.hosts[name]
	s.hosts[name] = &hostState{HostHello: *hello.Hello, lastSeen: s.now(), conn: c}
	s.mu.Unlock()
	if old != nil && old.conn != nil {
		old.conn.shut() // a host reconnecting replaces its old connection
	}
	destroy := s.reconcile(r.Context(), name, hello.Sandboxes)
	if err := c.send(api.HostMessage{Type: "welcome", Destroy: destroy}); err != nil {
		return
	}
	s.log().Info("host connected", "host", name, "url", hello.Hello.URL, "version", hello.Hello.Version)
	defer func() {
		s.mu.Lock()
		if h := s.hosts[name]; h != nil && h.conn == c {
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
			if h := s.hosts[name]; h != nil && h.conn == c {
				h.lastSeen = s.now()
			}
			s.mu.Unlock()
			destroy := s.reconcile(r.Context(), name, m.Sandboxes)
			if err := c.send(api.HostMessage{Type: "ack", Req: m.Req, Destroy: destroy}); err != nil {
				return
			}
		case "result":
			c.deliver(m)
		}
	}
}

func checkHello(m api.HostMessage) string {
	if m.Type != "hello" || m.Hello == nil {
		return "the first message must be hello"
	}
	if m.Hello.Protocol != api.HostProtocol {
		return fmt.Sprintf("protocol %d not supported (this server speaks %d); upgrade ephemera-host", m.Hello.Protocol, api.HostProtocol)
	}
	u, err := url.Parse(m.Hello.URL)
	if m.Hello.Name == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "name and an http(s) url are required"
	}
	return ""
}

// reconcile compares what a host says it runs with the server's records.
// It returns the sandboxes the host should destroy, because the server has
// no record of them, and drops records of sandboxes the host no longer has.
func (s *Server) reconcile(ctx context.Context, name string, reported []string) []string {
	have := make(map[string]bool, len(reported))
	for _, id := range reported {
		have[id] = true
	}
	var destroy []string
	var gone []*api.Sandbox
	s.mu.Lock()
	for _, id := range reported {
		if sb, ok := s.sandboxes[id]; !ok || sb.Host != name {
			destroy = append(destroy, id)
		}
	}
	for id, sb := range s.sandboxes {
		if sb.Host == name && !have[id] && s.now().Sub(sb.CreatedAt) > createGrace {
			gone = append(gone, sb)
		}
	}
	s.mu.Unlock()
	for _, id := range destroy {
		s.log().Warn("host runs a sandbox with no record; destroying it", "sandbox", id, "host", name)
	}
	for _, sb := range gone {
		s.log().Info("sandbox gone from its host; record dropped", "sandbox", sb.ID, "host", name)
		s.end(ctx, sb, EndGone)
	}
	return destroy
}
