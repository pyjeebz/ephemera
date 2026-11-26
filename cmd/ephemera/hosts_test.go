package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

// ephemera trust pins a host's key by name.
func TestTrust(t *testing.T) {
	ts, cs := doctorEnv(t)
	startHost(t, cs, ts.URL, "box")
	pins := filepath.Join(filepath.Dir(os.Getenv("EPHEMERA_CONFIG")), "known_hosts.json")

	var out, errOut bytes.Buffer
	if code := realMain([]string{"trust", "box"}, nil, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "Pinned box (host_") {
		t.Fatalf("exit %d, %q %q", code, out.String(), errOut.String())
	}
	if b, err := os.ReadFile(pins); err != nil || !strings.Contains(string(b), `"name": "box"`) {
		t.Errorf("pins = %s, %v", b, err)
	}

	errOut.Reset()
	if code := realMain([]string{"trust", "nope"}, nil, &out, &errOut); code != exitFailure || !strings.Contains(errOut.String(), `no host "nope"`) {
		t.Errorf("unknown host: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := realMain([]string{"trust"}, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "Usage: ephemera trust <host>") {
		t.Errorf("no host: exit %d, %q", code, errOut.String())
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Second:                   "1 second",
		40 * time.Second:              "40 seconds",
		90 * time.Second:              "90 seconds",
		12 * time.Minute:              "12 minutes",
		3 * time.Hour:                 "3 hours",
		4*24*time.Hour + 12*time.Hour: "4 days",
	} {
		if got := ago(d); got != want {
			t.Errorf("ago(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestNoHostNote(t *testing.T) {
	now := time.Now()
	host := func(name string, online bool, seen time.Duration) api.Host {
		h := api.Host{Name: name, URL: "https://" + name + ":7701", Online: online}
		if seen > 0 {
			h.LastSeen = now.Add(-seen)
		}
		return h
	}
	for _, tc := range []struct {
		name  string
		hosts []api.Host
		want  string
	}{
		{"none joined", nil, "no host has joined this account yet"},
		{"one online", []api.Host{host("lab", true, time.Second)}, ""},
		{"only host offline", []api.Host{host("lab", false, 108*time.Hour)},
			"your only host, lab, is offline: it was last seen 4 days ago; on that machine: systemctl status ephemera-host"},
		{"never connected", []api.Host{host("lab", false, 0)}, "your only host, lab, is offline: it joined but has never connected"},
		{"two offline", []api.Host{host("old", false, 72*time.Hour), host("new", false, 3*time.Hour)},
			"none of your 2 hosts is online; the most recent, new, was last seen 3 hours ago"},
		{"one of two online", []api.Host{host("old", false, 72*time.Hour), host("new", true, time.Second)}, ""},
	} {
		got := noHostNote(tc.hosts)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ephemera hosts says when an offline host was last seen, and run says it
// instead of the generic "no host" error.
func TestOfflineHost(t *testing.T) {
	ts, cs := doctorEnv(t)
	_, disconnect := startHost(t, cs, ts.URL, "box")
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := realMain(args, strings.NewReader(""), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	if code, out, _ := run("hosts"); code != 0 || !strings.Contains(out, "LAST SEEN") || !regexp.MustCompile(`box .* yes +now `).MatchString(out) {
		t.Fatalf("hosts, online: exit %d\n%s", code, out)
	}
	disconnect()
	if code, out, _ := run("hosts"); code != 0 || !regexp.MustCompile(`box .* no +\d+ seconds? ago `).MatchString(out) {
		t.Fatalf("hosts, offline: exit %d\n%s", code, out)
	}

	code, _, errOut := run("run", "--dir", t.TempDir(), "--", "true")
	if code != exitFailure || !strings.Contains(errOut, "no host can take a sandbox: your only host, box, is offline: it was last seen") {
		t.Errorf("run: exit %d, %q", code, errOut)
	}
}

// A login that works but has no online host says so.
func TestLoginNotesOfflineHost(t *testing.T) {
	ts, _ := doctorEnv(t)
	t.Setenv("EPHEMERA_SERVER", "")
	t.Setenv("EPHEMERA_TOKEN", "")
	var out, errOut bytes.Buffer
	code := realMain([]string{"login", "--server", ts.URL}, strings.NewReader("eph_good\n"), &out, &errOut)
	if code != 0 || !strings.Contains(errOut.String(), "ephemera: note: no host has joined this account yet") {
		t.Errorf("login: exit %d, out %q, err %q", code, out.String(), errOut.String())
	}
}
