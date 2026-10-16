package auth

import (
	"errors"
	"strings"
	"testing"

	"ephemera.run/ephemera/internal/hub"
)

func TestNewTokenAndHash(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b {
		t.Fatal("two tokens are equal")
	}
	if !strings.HasPrefix(a, TokenPrefix) || len(a) != len(TokenPrefix)+43 {
		t.Fatalf("token %q: want %q + 43 base64url chars", a, TokenPrefix)
	}
	if Hash(a) != Hash(a) || Hash(a) == Hash(b) || !isHash(Hash(a)) {
		t.Fatal("Hash is not a stable sha256 hex digest")
	}
}

func TestStaticAuthenticate(t *testing.T) {
	alice, bob := NewToken(), NewToken()
	s, err := ParseStatic(strings.NewReader(
		"# senders\n\nalice " + Hash(alice) + "\n  bob\t" + strings.ToUpper(Hash(bob)) + "  \n"))
	if err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]string{alice: "alice", bob: "bob"} {
		acct, err := s.Authenticate(token)
		if err != nil || acct.Name != want {
			t.Errorf("Authenticate(%s's token) = %+v, %v", want, acct, err)
		}
	}
	if _, err := s.Authenticate(""); !errors.Is(err, hub.ErrNoToken) {
		t.Errorf("empty token: %v, want ErrNoToken", err)
	}
	if _, err := s.Authenticate(NewToken()); !errors.Is(err, hub.ErrBadToken) {
		t.Errorf("unknown token: %v, want ErrBadToken", err)
	}
	// the file holds hashes: presenting a hash is not a token
	if _, err := s.Authenticate(Hash(alice)); !errors.Is(err, hub.ErrBadToken) {
		t.Errorf("hash as token: %v, want ErrBadToken", err)
	}
}

func TestParseStaticRefuses(t *testing.T) {
	h := Hash(NewToken())
	for name, file := range map[string]string{
		"empty":         "",
		"only comments": "# nobody\n",
		"no hash":       "alice\n",
		"raw token":     "alice " + NewToken() + "\n",
		"short hash":    "alice " + h[:60] + "\n",
		"extra field":   "alice " + h + " admin\n",
		"duplicate":     "alice " + h + "\nbob " + h + "\n",
	} {
		if _, err := ParseStatic(strings.NewReader(file)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}
