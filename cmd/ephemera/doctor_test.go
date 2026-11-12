package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

type doctorReport struct {
	OK     bool    `json:"ok"`
	Checks []check `json:"checks"`
}

// doctorEnv starts a central server and logs in to it through the
// environment. now is the server's clock, so a test can age a host.
func doctorEnv(t *testing.T) (*httptest.Server, *control.Server) {
	t.Helper()
	now := time.Now()
	cs := &control.Server{
		Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"},
		Now:    func() time.Time { return now },
	}
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	t.Setenv("EPHEMERA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("EPHEMERA_SERVER", ts.URL)
	t.Setenv("EPHEMERA_TOKEN", "eph_good")
	return ts, cs
}

// startHost runs a host agent connected to the server, and returns its
// HTTP server and a func that disconnects it and waits until the server
// lists it offline.
func startHost(t *testing.T, cs *control.Server, server, name string) (*httptest.Server, func()) {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(nil)
	hs := &host.Server{Backend: &sandboxtest.Local{Root: t.TempDir()}, Name: name, Key: key}
	hts := hosttest.Serve(t, hs)
	code, err := cs.CreateJoinCode(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	disconnect := hosttest.Connect(t, hs, server, code, api.HostHello{Name: name, URL: hts.URL, Capacity: 1})
	return hts, func() {
		disconnect()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if !online(t, server, name) {
				return
			}
		}
		t.Fatalf("server still lists %s as online", name)
	}
}

// online reports whether the server lists the host as online.
func online(t *testing.T, server, name string) bool {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, server+"/v1/hosts", nil)
	req.Header.Set("Authorization", "Bearer eph_good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var hosts []api.Host
	json.NewDecoder(resp.Body).Decode(&hosts)
	for _, h := range hosts {
		if h.Name == name {
			return h.Online
		}
	}
	return false
}

func runDoctorJSON(t *testing.T) (int, doctorReport) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := realMain([]string{"doctor", "--json"}, strings.NewReader(""), &out, &errOut)
	var rep doctorReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("doctor --json: %v\nout %q\nerr %q", err, out.String(), errOut.String())
	}
	return code, rep
}

// statuses gives each check as name=status, in order.
func statuses(rep doctorReport) string {
	var s []string
	for _, c := range rep.Checks {
		s = append(s, c.Name+"="+c.Status)
	}
	return strings.Join(s, " ")
}

func TestDoctorAllOK(t *testing.T) {
	ts, cs := doctorEnv(t)
	startHost(t, cs, ts.URL, "h1")

	code, rep := runDoctorJSON(t)
	if want := "login=ok server=ok token=ok hosts=ok"; code != 0 || !rep.OK || statuses(rep) != want {
		t.Fatalf("code %d, ok %v, checks %s, want %s", code, rep.OK, statuses(rep), want)
	}
	if !strings.Contains(rep.Checks[2].Detail, "laptop") {
		t.Errorf("token detail %q does not name the token", rep.Checks[2].Detail)
	}

	// The plain output has one line per check.
	var out bytes.Buffer
	if code := realMain([]string{"doctor"}, strings.NewReader(""), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("doctor: code %d\n%s", code, out.String())
	}
	if n := strings.Count(out.String(), "\n"); n != 4 {
		t.Errorf("doctor printed %d lines, want 4:\n%s", n, out.String())
	}
}

func TestDoctorFailures(t *testing.T) {
	t.Run("no login", func(t *testing.T) {
		doctorEnv(t)
		t.Setenv("EPHEMERA_TOKEN", "")
		code, rep := runDoctorJSON(t)
		if want := "login=fail server=skip token=skip hosts=skip"; code != 1 || rep.OK || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
		if rep.Checks[0].Fix == "" {
			t.Error("no fix for a missing login")
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		doctorEnv(t)
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		t.Setenv("EPHEMERA_SERVER", dead.URL)
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=fail token=skip hosts=skip"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
		if d := rep.Checks[1].Detail; !strings.Contains(d, "nothing is listening") {
			t.Errorf("server detail %q", d)
		}
	})

	t.Run("not an ephemera server", func(t *testing.T) {
		doctorEnv(t)
		other := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(other.Close)
		t.Setenv("EPHEMERA_SERVER", other.URL)
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=fail token=skip hosts=skip"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		doctorEnv(t)
		t.Setenv("EPHEMERA_TOKEN", "eph_revoked")
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=ok token=fail hosts=skip"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
		if d := rep.Checks[2].Detail; !strings.Contains(d, "rejected") {
			t.Errorf("token detail %q", d)
		}
	})

	t.Run("no hosts", func(t *testing.T) {
		doctorEnv(t)
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=ok token=ok hosts=fail"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
	})

	t.Run("host offline", func(t *testing.T) {
		ts, cs := doctorEnv(t)
		_, disconnect := startHost(t, cs, ts.URL, "h1")
		disconnect()
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=ok token=ok hosts=fail"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
		if d := rep.Checks[3].Detail; !strings.Contains(d, "h1 is offline") {
			t.Errorf("hosts detail %q", d)
		}
	})

	t.Run("host unreachable from here", func(t *testing.T) {
		ts, cs := doctorEnv(t)
		gone, _ := startHost(t, cs, ts.URL, "h1")
		gone.Close() // still connected, but its address answers nothing
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=ok token=ok hosts=fail"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
		if d := rep.Checks[3].Detail; !strings.Contains(d, "can't reach it") {
			t.Errorf("hosts detail %q", d)
		}
	})

	t.Run("one host of two offline", func(t *testing.T) {
		ts, cs := doctorEnv(t)
		_, disconnect := startHost(t, cs, ts.URL, "old")
		disconnect()
		startHost(t, cs, ts.URL, "new")
		code, rep := runDoctorJSON(t)
		if code != 0 || !rep.OK {
			t.Fatalf("code %d, checks %s: one reachable host is enough", code, statuses(rep))
		}
		if !strings.Contains(statuses(rep), "hosts=warn") {
			t.Errorf("checks %s: the offline host should be a warning", statuses(rep))
		}
	})

	t.Run("host joined, never connected", func(t *testing.T) {
		ts, cs := doctorEnv(t)
		_, key, _ := ed25519.GenerateKey(nil)
		code, _ := cs.CreateJoinCode(context.Background(), "")
		if _, err := host.Join(context.Background(), ts.URL, code, "quiet", key); err != nil {
			t.Fatal(err)
		}
		code2, rep := runDoctorJSON(t)
		if want := "login=ok server=ok token=ok hosts=fail"; code2 != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code2, statuses(rep), want)
		}
		if d := rep.Checks[3].Detail; !strings.Contains(d, "hasn't connected") {
			t.Errorf("hosts detail %q", d)
		}
	})

	t.Run("bad flag", func(t *testing.T) {
		doctorEnv(t)
		if code := realMain([]string{"doctor", "--nope"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Fatalf("code %d, want 2", code)
		}
	})
}

// A server older than this CLI sends hosts without url or online.
func TestDoctorOldServer(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{{time.Second, "hosts=ok"}, {time.Minute, "hosts=fail"}} {
		old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/healthz":
				w.Write([]byte("ok\n"))
			case "/v1/whoami":
				json.NewEncoder(w).Encode(api.WhoAmI{Name: "laptop"})
			case "/v1/hosts":
				fmt.Fprintf(w, `[{"name":"h1","last_seen":%q}]`, time.Now().Add(-tc.age).Format(time.RFC3339Nano))
			}
		}))
		t.Setenv("EPHEMERA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
		t.Setenv("EPHEMERA_SERVER", old.URL)
		t.Setenv("EPHEMERA_TOKEN", "eph_good")
		_, rep := runDoctorJSON(t)
		old.Close()
		if got := statuses(rep); !strings.HasSuffix(got, tc.want) {
			t.Errorf("last seen %s ago: checks %s, want %s", tc.age, got, tc.want)
		}
	}
}
