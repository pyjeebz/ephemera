// Package router lets several ephemera dev sessions share a port on the
// laptop. Each session's forwarded port is registered under a name,
// usually its git branch: http://<name>.localhost:3000 reaches that
// session, and plain localhost:3000 reaches the focused one, by default
// the most recent. The router reads the Host header of each connection to
// choose; connections that send none go to the focused session.
//
// One router runs per user. Sessions talk to it over a Unix socket; a
// session's route lasts exactly as long as its registration request is
// open, so a session that dies takes its route with it.
package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Route is one session's registration.
type Route struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`   // the shared port, e.g. 3000
	Target  string `json:"target"` // where the session listens, e.g. 127.0.0.1:53211
	Focused bool   `json:"focused"`
	since   time.Time
}

// Server is the router.
type Server struct {
	// Socket is the Unix socket sessions register on.
	Socket string
	// ListenHosts are the loopback addresses shared ports listen on;
	// default 127.0.0.1 and ::1.
	ListenHosts []string
	// IdleExit stops the router this long after its last route goes;
	// 0 means never.
	IdleExit time.Duration

	mu        sync.Mutex
	routes    map[int][]*Route // by port, newest last
	focus     map[int]string   // by port: the focused name
	listeners map[int][]net.Listener
	idle      *time.Timer
	done      chan struct{}
}

var labelRe = regexp.MustCompile(`[^a-z0-9-]+`)

// Label turns a branch name into a DNS label: "feature/Login" becomes
// "feature-login".
func Label(s string) string {
	l := strings.Trim(labelRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(l) > 63 {
		l = strings.Trim(l[:63], "-")
	}
	return l
}

// Serve runs the router until ctx is done or it has been idle for
// IdleExit.
func (s *Server) Serve(ctx context.Context) error {
	s.routes, s.focus, s.listeners = map[int][]*Route{}, map[int]string{}, map[int][]net.Listener{}
	s.done = make(chan struct{})
	if len(s.ListenHosts) == 0 {
		s.ListenHosts = []string{"127.0.0.1", "::1"}
	}
	// Only one router: if the socket answers, another is running.
	if c, err := net.Dial("unix", s.Socket); err == nil {
		c.Close()
		return errors.New("a router is already running")
	}
	os.Remove(s.Socket)
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		return err
	}
	os.Chmod(s.Socket, 0o600)
	defer os.Remove(s.Socket)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hold", s.hold)
	mux.HandleFunc("GET /routes", s.list)
	mux.HandleFunc("POST /focus", s.setFocus)
	hs := &http.Server{Handler: mux}
	go hs.Serve(ln)
	s.mu.Lock()
	s.armIdle()
	s.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-s.done:
	}
	hs.Close()
	s.mu.Lock()
	for _, lns := range s.listeners {
		for _, l := range lns {
			l.Close()
		}
	}
	s.mu.Unlock()
	return nil
}

// armIdle starts the idle countdown when there are no routes. Locked.
func (s *Server) armIdle() {
	if s.IdleExit <= 0 {
		return
	}
	n := 0
	for _, rs := range s.routes {
		n += len(rs)
	}
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	if n == 0 {
		s.idle = time.AfterFunc(s.IdleExit, func() {
			select {
			case <-s.done:
			default:
				close(s.done)
			}
		})
	}
}

// hold registers a route and keeps it while the request stays open.
func (s *Server) hold(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	port, err := strconv.Atoi(q.Get("port"))
	name := Label(q.Get("name"))
	target := q.Get("target")
	if err != nil || port < 1 || port > 65535 || name == "" || target == "" {
		http.Error(w, "name, port and target are required", http.StatusBadRequest)
		return
	}
	route, err := s.add(name, port, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer s.remove(route)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(route)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done() // the session ended or went away
}

func (s *Server) add(name string, port int, target string) (*Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Two sessions of the same name on one port get distinct names.
	base, n := name, 2
	for s.taken(port, name) {
		name = fmt.Sprintf("%s-%d", base, n)
		n++
	}
	if len(s.routes[port]) == 0 {
		var lns []net.Listener
		var firstErr error
		for _, h := range s.ListenHosts {
			ln, err := net.Listen("tcp", net.JoinHostPort(h, strconv.Itoa(port)))
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			lns = append(lns, ln)
			go s.accept(ln, port)
		}
		if len(lns) == 0 {
			return nil, fmt.Errorf("port %d: %w", port, firstErr)
		}
		s.listeners[port] = lns
	}
	route := &Route{Name: name, Port: port, Target: target, since: time.Now()}
	s.routes[port] = append(s.routes[port], route)
	s.focus[port] = name // the newest session takes plain localhost
	route.Focused = true
	s.armIdle()
	return route, nil
}

func (s *Server) taken(port int, name string) bool {
	for _, r := range s.routes[port] {
		if r.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) remove(route *Route) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.routes[route.Port]
	for i, r := range rs {
		if r == route {
			rs = append(rs[:i], rs[i+1:]...)
			break
		}
	}
	s.routes[route.Port] = rs
	if len(rs) == 0 {
		for _, l := range s.listeners[route.Port] {
			l.Close()
		}
		delete(s.listeners, route.Port)
		delete(s.routes, route.Port)
		delete(s.focus, route.Port)
	} else if s.focus[route.Port] == route.Name {
		s.focus[route.Port] = rs[len(rs)-1].Name // back to the newest left
	}
	s.armIdle()
}

// Routes returns the current routes, sorted by port and name.
func (s *Server) Routes() []Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Route
	for port, rs := range s.routes {
		for _, r := range rs {
			c := *r
			c.Focused = s.focus[port] == r.Name
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.Routes())
}

func (s *Server) setFocus(w http.ResponseWriter, r *http.Request) {
	name := Label(r.URL.Query().Get("name"))
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	s.mu.Lock()
	n := 0
	for p, rs := range s.routes {
		if port != 0 && p != port {
			continue
		}
		for _, rt := range rs {
			if rt.Name == name {
				s.focus[p] = name
				n++
			}
		}
	}
	s.mu.Unlock()
	if n == 0 {
		http.Error(w, "no session named "+name, http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pick chooses the target for a connection with the given Host header.
func (s *Server) pick(port int, host string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := ""
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if n, ok := strings.CutSuffix(host, ".localhost"); ok {
		name = n
	}
	var focused string
	for _, r := range s.routes[port] {
		if name != "" && r.Name == name {
			return r.Target
		}
		if r.Name == s.focus[port] {
			focused = r.Target
		}
	}
	if name != "" {
		// A name that is not a session is an error, never another
		// session's page.
		return ""
	}
	return focused
}

// peekWait is how long a connection may take to send its first request
// before it is routed without a Host header.
const peekWait = 300 * time.Millisecond

func (s *Server) accept(ln net.Listener, port int) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go s.route(c, port)
	}
}

func (s *Server) route(c net.Conn, port int) {
	defer c.Close()
	br := bufio.NewReaderSize(c, 16<<10)
	c.SetReadDeadline(time.Now().Add(peekWait))
	host := sniffHost(br)
	c.SetReadDeadline(time.Time{})
	target := s.pick(port, host)
	if target == "" {
		msg := fmt.Sprintf("ephemera: no dev session at %s; run ephemera routes to see the sessions\n", host)
		fmt.Fprintf(c, "HTTP/1.1 404 Not Found\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
		return
	}
	up, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	go func() {
		io.Copy(up, br) // includes the bytes already peeked
		if cw, ok := up.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	io.Copy(c, up)
}

// sniffHost reads ahead to the end of the first request's headers, without
// consuming anything, and returns its Host header if it is HTTP. It waits
// only as long as bytes keep arriving, up to the read deadline.
func sniffHost(br *bufio.Reader) string {
	for {
		n := br.Buffered()
		if n > 0 {
			b, _ := br.Peek(n)
			if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
				return hostHeader(b[:i])
			}
			if n >= br.Size() {
				return hostHeader(b)
			}
		}
		if _, err := br.Peek(n + 1); err != nil {
			// Timed out or closed: route by what is there, if anything.
			b, _ := br.Peek(br.Buffered())
			return hostHeader(b)
		}
	}
}

func hostHeader(head []byte) string {
	lines := strings.Split(string(head), "\r\n")
	if len(lines) == 0 || !strings.Contains(lines[0], " HTTP/1.") {
		return ""
	}
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Client talks to the router.
type Client struct {
	Socket string
}

func (c *Client) http() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.Socket)
		},
	}}
}

// Running reports whether a router answers.
func (c *Client) Running() bool {
	conn, err := net.DialTimeout("unix", c.Socket, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Hold registers a route and keeps it until ctx is done. It returns once
// the route is in place, with the name the router gave it.
func (c *Client) Hold(ctx context.Context, name string, port int, target string) (Route, <-chan error, error) {
	q := url.Values{"name": {name}, "port": {strconv.Itoa(port)}, "target": {target}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://router/hold?"+q.Encode(), nil)
	if err != nil {
		return Route{}, nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return Route{}, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return Route{}, nil, errors.New(strings.TrimSpace(string(msg)))
	}
	var r Route
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		resp.Body.Close()
		return Route{}, nil, err
	}
	gone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body) // returns when either side ends
		resp.Body.Close()
		gone <- err
	}()
	return r, gone, nil
}

// Routes lists the router's routes.
func (c *Client) Routes(ctx context.Context) ([]Route, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://router/routes", nil)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Route
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// Focus makes name the session plain localhost reaches, on port or, if 0,
// every port it has.
func (c *Client) Focus(ctx context.Context, name string, port int) error {
	q := url.Values{"name": {name}, "port": {strconv.Itoa(port)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://router/focus?"+q.Encode(), nil)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.New(strings.TrimSpace(string(msg)))
	}
	return nil
}
