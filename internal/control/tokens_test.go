package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "tokens.json")
	tf := &TokenFile{Path: path}

	if _, ok := tf.Check("eph_nothing"); ok {
		t.Fatal("no tokens yet: Check succeeded")
	}
	rec, secret, err := tf.Create("laptop", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, tokenPrefix) {
		t.Errorf("secret %q lacks prefix", secret)
	}
	if p, ok := tf.Check(secret); !ok || p.Name != "laptop" {
		t.Errorf("Check = %+v, %v", p, ok)
	}
	if _, ok := tf.Check(secret + "x"); ok {
		t.Error("altered token accepted")
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), secret) {
		t.Error("token file contains the secret itself")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", info.Mode().Perm())
	}

	// A second process (here, a second TokenFile) adds a token; the
	// running server picks it up without a restart.
	other := &TokenFile{Path: path}
	_, secret2, err := other.Create("ci", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := tf.Check(secret2); !ok || p.Name != "ci" {
		t.Errorf("new token not picked up: %+v, %v", p, ok)
	}

	if err := other.Revoke(rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := tf.Check(secret); ok {
		t.Error("revoked token still accepted")
	}
	if err := other.Revoke("no-such-token"); err == nil {
		t.Error("revoking an unknown token: want error")
	}
	recs, _ := tf.List()
	if len(recs) != 1 || recs[0].Name != "ci" {
		t.Errorf("List = %+v", recs)
	}
}

func TestTokenNamesUniqueAndLimits(t *testing.T) {
	tf := &TokenFile{Path: filepath.Join(t.TempDir(), "tokens.json")}
	_, secret, err := tf.Create("agent", Limits{MaxSandboxes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tf.Create("agent", Limits{}); err == nil {
		t.Error("a second token with the same name was created")
	}
	if p, _ := tf.Check(secret); p.MaxSandboxes != 2 {
		t.Errorf("limits = %+v", p.Limits)
	}
	if err := tf.SetLimits("agent", Limits{MaxSandboxes: 5, MaxMinutesPerDay: 60}); err != nil {
		t.Fatal(err)
	}
	if p, _ := tf.Check(secret); p.MaxSandboxes != 5 || p.MaxMinutesPerDay != 60 {
		t.Errorf("limits after SetLimits = %+v", p.Limits)
	}
	if err := tf.SetLimits("agent", Limits{MaxSandboxes: -1}); err == nil {
		t.Error("negative limits accepted")
	}
}
