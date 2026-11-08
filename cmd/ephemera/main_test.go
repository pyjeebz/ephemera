package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/control"
)

func TestLoginLsLogout(t *testing.T) {
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}, HostSecret: "s"}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()

	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("EPHEMERA_CONFIG", cfg)
	t.Setenv("EPHEMERA_SERVER", "")
	t.Setenv("EPHEMERA_TOKEN", "")

	run := func(stdin string, args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := realMain(args, strings.NewReader(stdin), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	if code, _, errOut := run("eph_wrong\n", "login", "--server", ts.URL); code == 0 {
		t.Fatalf("login with a wrong token succeeded: %s", errOut)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("a failed login saved a config")
	}

	code, out, errOut := run("eph_good\n", "login", "--server", ts.URL)
	if code != 0 || !strings.Contains(out, "as laptop") {
		t.Fatalf("login: code %d, out %q, err %q", code, out, errOut)
	}
	if info, err := os.Stat(cfg); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config not saved privately: %v %v", info, err)
	}

	// Later commands use the saved login with no environment variables.
	if code, out, errOut := run("", "ls"); code != 0 || !strings.Contains(out, "ID") {
		t.Fatalf("ls after login: code %d, out %q, err %q", code, out, errOut)
	}

	if code, _, _ := run("", "logout"); code != 0 {
		t.Fatal("logout failed")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Error("logout left the config")
	}
	if code, _, _ := run("", "ls"); code == 0 {
		t.Error("ls after logout succeeded without a token")
	}
}
