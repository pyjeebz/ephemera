package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/record"
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

func TestVerify(t *testing.T) {
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}, HostSecret: "s"}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	t.Setenv("EPHEMERA_SERVER", ts.URL)
	t.Setenv("EPHEMERA_TOKEN", "eph_good")
	t.Setenv("EPHEMERA_CONFIG", filepath.Join(t.TempDir(), "c.json"))

	key := newKey(t)
	// The host registers its public key with the central server.
	body, _ := json.Marshal(api.RegisterHostRequest{Name: "h1", URL: "http://h1", PublicKey: record.PublicKey(key)})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/hosts", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode/100 != 2 {
		t.Fatalf("register: %v %v", resp, err)
	}

	// Records are written the way the CLI saves them.
	write := func(sr api.SignedRecord) string {
		p, err := saveRecord(&sr, filepath.Join(t.TempDir(), "rec.json"))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	verify := func(path string) (int, string) {
		var out, errOut bytes.Buffer
		code := realMain([]string{"verify", path}, strings.NewReader(""), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	rec := api.Record{Version: 1, Sandbox: "sbx_1", Host: "h1", Ended: "destroyed", DiskDeleted: true,
		Execs: []api.ExecRecord{{Argv: []string{"npm", "test"}, ExitCode: 0}}}

	good, _ := record.Sign(key, rec)
	if code, out := verify(write(good)); code != 0 || !strings.Contains(out, "Verified: signed by host h1") || !strings.Contains(out, "npm test") {
		t.Errorf("good record: code %d\n%s", code, out)
	}

	tampered := good
	tampered.Record = bytes.Replace(good.Record, []byte(`"disk_deleted":true`), []byte(`"disk_deleted":false`), 1)
	if code, out := verify(write(tampered)); code == 0 || !strings.Contains(out, "NOT VERIFIED") {
		t.Errorf("tampered record: code %d\n%s", code, out)
	}

	// Signed correctly, but not by the key the central server has for h1.
	impostor, _ := record.Sign(newKey(t), rec)
	if code, out := verify(write(impostor)); code == 0 || !strings.Contains(out, "does NOT match") {
		t.Errorf("record from another key: code %d\n%s", code, out)
	}
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	k, err := record.LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}
