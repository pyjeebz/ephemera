package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
)

type doctorReport struct {
	OK     bool    `json:"ok"`
	Checks []check `json:"checks"`
}

// doctorEnv starts a central server and logs in to it through the
// environment. now is the server's clock, so a test can age a host.
func doctorEnv(t *testing.T) (*httptest.Server, *time.Time) {
	t.Helper()
	now := time.Now()
	cs := &control.Server{
		Tokens:     control.StaticToken{Name: "laptop", Token: "eph_good"},
		HostSecret: "s",
		Now:        func() time.Time { return now },
	}
	ts := httptest.NewServer(cs.Handler())
	t.Cleanup(ts.Close)
	t.Setenv("EPHEMERA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("EPHEMERA_SERVER", ts.URL)
	t.Setenv("EPHEMERA_TOKEN", "eph_good")
	return ts, &now
}

func registerHost(t *testing.T, server, name, hostURL string) {
	t.Helper()
	b, _ := json.Marshal(api.RegisterHostRequest{Name: name, URL: hostURL, Capacity: 1})
	req, _ := http.NewRequest(http.MethodPost, server+"/v1/hosts", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer s")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register host: %s", resp.Status)
	}
}

// fakeHost answers /healthz like a host agent.
func fakeHost(t *testing.T) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok\n"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(hs.Close)
	return hs
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
	ts, _ := doctorEnv(t)
	registerHost(t, ts.URL, "h1", fakeHost(t).URL)

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
		ts, now := doctorEnv(t)
		registerHost(t, ts.URL, "h1", fakeHost(t).URL)
		*now = now.Add(control.HostTimeout + time.Second)
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=ok token=ok hosts=fail"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
		if d := rep.Checks[3].Detail; !strings.Contains(d, "h1 is offline") {
			t.Errorf("hosts detail %q", d)
		}
	})

	t.Run("host unreachable from here", func(t *testing.T) {
		ts, _ := doctorEnv(t)
		gone := fakeHost(t)
		registerHost(t, ts.URL, "h1", gone.URL)
		gone.Close()
		code, rep := runDoctorJSON(t)
		if want := "login=ok server=ok token=ok hosts=fail"; code != 1 || statuses(rep) != want {
			t.Fatalf("code %d, checks %s, want %s", code, statuses(rep), want)
		}
		if d := rep.Checks[3].Detail; !strings.Contains(d, "can't reach it") {
			t.Errorf("hosts detail %q", d)
		}
	})

	t.Run("one host of two offline", func(t *testing.T) {
		ts, now := doctorEnv(t)
		registerHost(t, ts.URL, "old", fakeHost(t).URL)
		*now = now.Add(control.HostTimeout + time.Second)
		registerHost(t, ts.URL, "new", fakeHost(t).URL)
		code, rep := runDoctorJSON(t)
		if code != 0 || !rep.OK {
			t.Fatalf("code %d, checks %s: one reachable host is enough", code, statuses(rep))
		}
		if !strings.Contains(statuses(rep), "hosts=warn") {
			t.Errorf("checks %s: the offline host should be a warning", statuses(rep))
		}
	})

	t.Run("bad flag", func(t *testing.T) {
		doctorEnv(t)
		if code := realMain([]string{"doctor", "--nope"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Fatalf("code %d, want 2", code)
		}
	})
}
