package host

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/record"
)

// ConnectConfig says how to reach the central server, and as which host.
type ConnectConfig struct {
	ServerURL string
	HostID    string        // from the join
	Hello     api.HostHello // URL and Capacity; the rest is filled in
	// Heartbeat is how often the host says it's alive; 15 s if zero.
	Heartbeat time.Duration
	// OnWelcome, if set, runs each time the server accepts the host, after
	// the host has destroyed any sandboxes the server didn't know.
	OnWelcome func()
}

// maxMessage caps one message on the connection. Results carry signed
// records, which list every connection a sandbox made.
const maxMessage = 16 << 20

// conn is one open connection to a central server.
type conn struct {
	rw     io.ReadWriteCloser
	wmu    sync.Mutex
	enc    *json.Encoder
	nextID atomic.Int64
	mu     sync.Mutex
	acks   map[string]chan []string
}

func (c *conn) send(m api.HostMessage) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.enc.Encode(m)
}

// Connect keeps a connection open to the central server until ctx is done,
// dialing again with a growing pause (1 s doubling to 60 s, with jitter)
// whenever it drops. The server sends work down this connection, so it
// never needs to reach the host.
func (s *Server) Connect(ctx context.Context, cfg ConnectConfig) {
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = 15 * time.Second
	}
	cfg.Hello.Protocol, cfg.Hello.HostID = api.HostProtocol, cfg.HostID
	if s.Key != nil {
		cfg.Hello.PublicKey = record.PublicKey(s.Key)
	}
	wait := time.Second
	for {
		start := time.Now()
		err := s.connectOnce(ctx, cfg)
		if ctx.Err() != nil {
			return
		}
		s.log().Warn("connection to central server lost; reconnecting", "server", cfg.ServerURL, "err", err)
		// A connection that lasted a while starts the pause over.
		if time.Since(start) > time.Minute {
			wait = time.Second
		}
		pause := wait/2 + time.Duration(rand.Int64N(int64(wait)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		wait = min(wait*2, time.Minute)
	}
}

// upgradeTransport speaks HTTP/1.1 only, since connection upgrades do not
// exist in HTTP/2.
var upgradeTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	// Offer only HTTP/1.1, in the handshake too: a proxy that is offered
	// h2, as Fly's is, answers in h2, which has no upgrades.
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	t.TLSClientConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	return t
}()

func (s *Server) connectOnce(ctx context.Context, cfg ConnectConfig) error {
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, strings.TrimRight(cfg.ServerURL, "/")+"/v1/hosts/connect", nil)
	if err != nil {
		cancel()
		return err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", api.HostUpgrade)
	resp, err := (&http.Client{Transport: upgradeTransport}).Do(req)
	cancel()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		var e api.Error
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		return fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	rw, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return errors.New("connection was not upgraded")
	}
	c := &conn{rw: rw, enc: json.NewEncoder(rw), acks: map[string]chan []string{}}
	defer rw.Close()
	// Closing the connection is how everything below stops.
	stop := context.AfterFunc(ctx, func() { rw.Close() })
	defer stop()

	// The server sends a fresh challenge; signing it, with the server's
	// name as dialed, proves this host holds the key it joined with.
	r := bufio.NewReaderSize(rw, 64<<10)
	var challenge api.HostMessage
	if err := readMessage(r, &challenge); err != nil {
		return err
	}
	if challenge.Type != "challenge" || challenge.Nonce == "" {
		return fmt.Errorf("server answered %q, not a challenge: %s", challenge.Type, challenge.Error)
	}
	if s.Key == nil {
		return errors.New("host has no key")
	}
	hello := cfg.Hello
	hello.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(s.Key, api.HostSignedText(req.URL.Host, challenge.Nonce)))
	if err := c.send(api.HostMessage{Type: "hello", Hello: &hello, Sandboxes: s.ids()}); err != nil {
		return err
	}
	var welcome api.HostMessage
	if err := readMessage(r, &welcome); err != nil {
		return err
	}
	if welcome.Type == "refused" && welcome.Removed {
		// No one can see or stop these sandboxes any more.
		if n := len(s.ids()); n > 0 {
			s.log().Warn("central server has no record of this host; destroying its sandboxes", "count", n)
			if err := s.DestroyAll(ctx); err != nil {
				s.log().Error("destroy sandboxes", "err", err)
			}
		}
	}
	if welcome.Type != "welcome" {
		return fmt.Errorf("server answered %q, not welcome: %s", welcome.Type, welcome.Error)
	}
	s.destroyUnknown(ctx, welcome.Destroy)
	s.log().Info("connected to central server", "server", cfg.ServerURL, "host_id", cfg.HostID)

	s.mu.Lock()
	if s.conns == nil {
		s.conns = map[*conn]struct{}{}
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	if cfg.OnWelcome != nil {
		cfg.OnWelcome()
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		beats := time.NewTicker(cfg.Heartbeat)
		defer beats.Stop()
		for {
			select {
			case <-done:
				return
			case <-beats.C:
			}
			if _, err := s.beat(ctx, c); err != nil {
				rw.Close()
				return
			}
		}
	}()

	for {
		var m api.HostMessage
		if err := readMessage(r, &m); err != nil {
			return err
		}
		switch m.Type {
		case "ack":
			c.mu.Lock()
			ch := c.acks[m.Req]
			delete(c.acks, m.Req)
			c.mu.Unlock()
			if ch != nil {
				ch <- m.Destroy
			}
		case "create", "destroy":
			// Work runs on its own, so a slow create doesn't hold up
			// heartbeats or other work.
			go s.handle(ctx, c, m)
		}
	}
}

// handle does one piece of work from the server and sends the result.
func (s *Server) handle(ctx context.Context, c *conn, m api.HostMessage) {
	res := api.HostMessage{Type: "result", Req: m.Req}
	var err error
	switch m.Type {
	case "create":
		if m.Create == nil {
			err = errors.New("create without a sandbox")
		} else {
			err = s.Create(ctx, *m.Create)
		}
	case "destroy":
		res.Record, err = s.Destroy(ctx, m.Sandbox)
	}
	if err != nil {
		res.Error = err.Error()
	} else {
		res.OK = true
	}
	if m.Req != "" {
		c.send(res)
	}
}

// beat sends one heartbeat on c, waits for the server's ack, and destroys
// any sandbox the server says it doesn't know.
func (s *Server) beat(ctx context.Context, c *conn) ([]string, error) {
	req := strconv.FormatInt(c.nextID.Add(1), 10)
	ch := make(chan []string, 1)
	c.mu.Lock()
	c.acks[req] = ch
	c.mu.Unlock()
	if err := c.send(api.HostMessage{Type: "heartbeat", Req: req, Sandboxes: s.ids()}); err != nil {
		return nil, err
	}
	select {
	case destroy := <-ch:
		s.destroyUnknown(ctx, destroy)
		return destroy, nil
	case <-time.After(30 * time.Second):
		return nil, errors.New("no answer to a heartbeat")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Heartbeat sends a heartbeat on every open connection now and waits for
// the answers. Tests use it instead of waiting for the next one.
func (s *Server) Heartbeat(ctx context.Context) error {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	var errs []error
	for _, c := range conns {
		if _, err := s.beat(ctx, c); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// destroyUnknown destroys sandboxes the central server has no record of.
func (s *Server) destroyUnknown(ctx context.Context, ids []string) {
	for _, id := range ids {
		s.log().Warn("central server has no record of sandbox; destroying it", "sandbox", id)
		if err := s.Backend.Destroy(ctx, id); err != nil {
			s.log().Error("destroy unknown sandbox", "sandbox", id, "err", err)
			continue
		}
		s.forget(id)
	}
}

// readMessage reads one line of JSON.
func readMessage(r *bufio.Reader, m *api.HostMessage) error {
	line, err := readLine(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(line, m)
}

// readLine reads up to a newline, refusing lines over maxMessage.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxMessage {
			return nil, errors.New("message too long")
		}
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// ReadMessage reads one message from a host connection. The central
// server uses it for its side.
func ReadMessage(r *bufio.Reader, m *api.HostMessage) error { return readMessage(r, m) }
