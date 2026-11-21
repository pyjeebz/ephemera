package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "tokens.json")
	tf := &TokenFile{Path: path}

	if _, ok := tf.Check(context.Background(), "eph_nothing"); ok {
		t.Fatal("no tokens yet: Check succeeded")
	}
	rec, secret, err := tf.CreateToken(context.Background(), "laptop", "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, TokenPrefix) {
		t.Errorf("secret %q lacks prefix", secret)
	}
	if p, ok := tf.Check(context.Background(), secret); !ok || p.Name != "laptop" {
		t.Errorf("Check = %+v, %v", p, ok)
	}
	if _, ok := tf.Check(context.Background(), secret+"x"); ok {
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
	_, secret2, err := other.CreateToken(context.Background(), "ci", "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := tf.Check(context.Background(), secret2); !ok || p.Name != "ci" {
		t.Errorf("new token not picked up: %+v, %v", p, ok)
	}

	if err := other.RevokeToken(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := tf.Check(context.Background(), secret); ok {
		t.Error("revoked token still accepted")
	}
	if err := other.RevokeToken(context.Background(), "no-such-token"); err == nil {
		t.Error("revoking an unknown token: want error")
	}
	recs, _ := tf.ListTokens(context.Background())
	if len(recs) != 1 || recs[0].Name != "ci" {
		t.Errorf("List = %+v", recs)
	}
}

func TestTokenNamesUniqueAndLimits(t *testing.T) {
	tf := &TokenFile{Path: filepath.Join(t.TempDir(), "tokens.json")}
	_, secret, err := tf.CreateToken(context.Background(), "agent", "", Limits{MaxSandboxes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tf.CreateToken(context.Background(), "agent", "", Limits{}); err == nil {
		t.Error("a second token with the same name was created")
	}
	if p, _ := tf.Check(context.Background(), secret); p.MaxSandboxes != 2 {
		t.Errorf("limits = %+v", p.Limits)
	}
	if err := tf.SetTokenLimits(context.Background(), "agent", Limits{MaxSandboxes: 5, MaxMinutesPerDay: 60}); err != nil {
		t.Fatal(err)
	}
	if p, _ := tf.Check(context.Background(), secret); p.MaxSandboxes != 5 || p.MaxMinutesPerDay != 60 {
		t.Errorf("limits after SetLimits = %+v", p.Limits)
	}
	if err := tf.SetTokenLimits(context.Background(), "agent", Limits{MaxSandboxes: -1}); err == nil {
		t.Error("negative limits accepted")
	}
}

// touchCounter is a TokenFile that counts writes of tokens' last use.
type touchCounter struct {
	*TokenFile
	mu sync.Mutex
	n  int
}

func (c *touchCounter) TouchToken(ctx context.Context, id string, at time.Time) error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.TokenFile.TouchToken(ctx, id, at)
}

func (c *touchCounter) writes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// A token's last use is written when it makes a request, at most once a
// minute.
func TestTokenLastUsed(t *testing.T) {
	ctx := context.Background()
	tc := &touchCounter{TokenFile: &TokenFile{Path: filepath.Join(t.TempDir(), "tokens.json")}}
	_, secret, err := tc.CreateToken(ctx, "laptop", "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	later := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	s := &Server{Tokens: tc, Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	get := func(token string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	lastUsed := func() *time.Time {
		t.Helper()
		recs, err := tc.ListTokens(ctx)
		if err != nil || len(recs) != 1 {
			t.Fatalf("tokens = %+v, %v", recs, err)
		}
		return recs[0].LastUsed
	}

	if get("eph_wrong") != http.StatusUnauthorized || tc.writes() != 0 || lastUsed() != nil {
		t.Fatalf("a bad token: %d writes, last used %v", tc.writes(), lastUsed())
	}
	first := now
	for range 3 {
		if code := get(secret); code != http.StatusOK {
			t.Fatalf("usage: %d", code)
		}
	}
	later(59 * time.Second)
	get(secret)
	if n := tc.writes(); n != 1 {
		t.Errorf("4 requests within a minute: %d writes, want 1", n)
	}
	if got := lastUsed(); got == nil || !got.Equal(first) {
		t.Errorf("last used %v, want %v", got, first)
	}
	later(time.Second)
	get(secret)
	if n := tc.writes(); n != 2 {
		t.Errorf("a minute later: %d writes, want 2", n)
	}
	if got := lastUsed(); got == nil || !got.Equal(first.Add(time.Minute)) {
		t.Errorf("last used %v, want %v", got, first.Add(time.Minute))
	}
}
