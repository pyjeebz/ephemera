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
	rec, secret, err := tf.Create("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, tokenPrefix) {
		t.Errorf("secret %q lacks prefix", secret)
	}
	if name, ok := tf.Check(secret); !ok || name != "laptop" {
		t.Errorf("Check = %q, %v", name, ok)
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
	_, secret2, err := other.Create("ci")
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := tf.Check(secret2); !ok || name != "ci" {
		t.Errorf("new token not picked up: %q, %v", name, ok)
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
