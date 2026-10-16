package cli

import (
	"os"
	"strings"
	"testing"
)

// withStdin runs fn with os.Stdin reading input.
func withStdin(t *testing.T, input string, fn func() error) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(input)
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	return fn()
}

func TestLoginLogout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(tokenEnv, "")

	if tok, err := senderToken(); err != nil || tok != "" {
		t.Fatalf("before login: %q, %v; want none", tok, err)
	}
	if err := withStdin(t, "not-a-token\n", func() error { return runLogin(nil) }); err == nil {
		t.Fatal("login accepted a value without the token prefix")
	}

	const token = "eph_testtoken"
	if err := withStdin(t, "  "+token+"\n", func() error { return runLogin(nil) }); err != nil {
		t.Fatalf("login: %v", err)
	}
	path, _ := tokenFile()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", st.Mode().Perm())
	}
	if tok, _ := senderToken(); tok != token {
		t.Fatalf("after login: %q, want %q", tok, token)
	}

	// the env var wins over the stored login (CI, scripts)
	t.Setenv(tokenEnv, "eph_fromenv")
	if tok, _ := senderToken(); tok != "eph_fromenv" {
		t.Fatalf("with %s set: %q", tokenEnv, tok)
	}
	t.Setenv(tokenEnv, "")

	if err := runLogout(nil); err != nil {
		t.Fatal(err)
	}
	if tok, _ := senderToken(); tok != "" {
		t.Fatalf("after logout: %q, want none", tok)
	}
	if err := runLogout(nil); err != nil {
		t.Fatalf("second logout: %v", err)
	}
}

func TestServeRefusesWithoutAuthChoice(t *testing.T) {
	err := runServe([]string{"-addr", "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "-tokens") {
		t.Fatalf("serve without -tokens or -open: %v", err)
	}
	if err := runServe([]string{"-tokens", "x", "-open"}); err == nil {
		t.Fatal("serve accepted both -tokens and -open")
	}
}
