package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ephemera.run/ephemera/internal/hub"
)

// TestAccountsSignIn: a first sign-in makes the account and a token;
// a later one adds a token to the same account; both survive a
// reload; the file holds hashes, not tokens.
func TestAccountsSignIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	a, err := OpenAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	name, laptop, err := a.SignIn("github", "583231", "Octocat")
	if err != nil || name != "github:octocat" {
		t.Fatalf("first sign-in: %q, %v", name, err)
	}
	name2, browser, err := a.SignIn("github", "583231", "octocat-renamed")
	if err != nil || name2 != name || browser == laptop {
		t.Fatalf("second sign-in: %q, %v", name2, err)
	}

	b, err := OpenAccounts(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{laptop, browser} {
		if acct, err := b.Authenticate(token); err != nil || acct.Name != "github:octocat" {
			t.Errorf("after reload: %+v, %v", acct, err)
		}
	}
	if _, err := b.Authenticate(""); !errors.Is(err, hub.ErrNoToken) {
		t.Errorf("empty token: %v", err)
	}
	if _, err := b.Authenticate(NewToken()); !errors.Is(err, hub.ErrBadToken) {
		t.Errorf("unknown token: %v", err)
	}
	body, _ := os.ReadFile(path)
	if want := Hash(laptop); !strings.Contains(string(body), want) || strings.Contains(string(body), laptop) {
		t.Errorf("file should hold the hash, not the token:\n%s", body)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
}

// TestAccountsRevokeAndCap: signing out forgets one token; past
// MaxTokens the oldest goes.
func TestAccountsRevokeAndCap(t *testing.T) {
	a, _ := OpenAccounts(filepath.Join(t.TempDir(), "accounts.json"))
	var tokens []string
	for range MaxTokens + 1 {
		_, tok, err := a.SignIn("google", "1099", "alice@example.com")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, tok)
	}
	if _, err := a.Authenticate(tokens[0]); !errors.Is(err, hub.ErrBadToken) {
		t.Errorf("oldest token past the cap: %v, want ErrBadToken", err)
	}
	if ok, err := a.Revoke(tokens[1]); !ok || err != nil {
		t.Fatalf("revoke: %v, %v", ok, err)
	}
	if _, err := a.Authenticate(tokens[1]); !errors.Is(err, hub.ErrBadToken) {
		t.Errorf("revoked token: %v, want ErrBadToken", err)
	}
	if _, err := a.Authenticate(tokens[2]); err != nil {
		t.Errorf("other token after a revoke: %v", err)
	}
	if ok, _ := a.Revoke(tokens[1]); ok {
		t.Error("revoking twice reported a known token")
	}
}

// TestAccountsNameTaken: a login someone else had first gets a
// suffix, so two people never share an account name.
func TestAccountsNameTaken(t *testing.T) {
	a, _ := OpenAccounts(filepath.Join(t.TempDir(), "accounts.json"))
	first, _, _ := a.SignIn("github", "1", "sam")
	second, _, _ := a.SignIn("github", "2", "sam")
	if first != "github:sam" || second != "github:sam~2" {
		t.Errorf("names %q, %q", first, second)
	}
	if _, _, err := a.SignIn("github", "3", "has space"); err == nil {
		t.Error("a label with a space made an account")
	}
}
