package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyjeebz/ephemera/internal/api"
)

// fakeHost accepts creates and destroys like a host agent.
func fakeHost(t *testing.T) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(hs.Close)
	return hs
}

// call sends a request with a bearer token and decodes a JSON reply into out.
func call(t *testing.T, method, url, token string, body, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode/100 == 2 && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
	}
	return resp.StatusCode
}

func TestAccountsSeeOnlyTheirOwn(t *testing.T) {
	state := t.TempDir()
	tf := &TokenFile{Path: filepath.Join(state, "tokens.json")}
	_, alice, err := tf.CreateToken(context.Background(), "alice-laptop", "alice", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, aliceCI, err := tf.CreateToken(context.Background(), "alice-ci", "alice", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, bob, err := tf.CreateToken(context.Background(), "bob-laptop", "bob", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tf.CreateToken(context.Background(), "x", "Not Valid", Limits{}); err == nil {
		t.Error("an account name with capitals and a space was accepted")
	}

	cs := &Server{Tokens: tf, HostSecret: "s", StatePath: filepath.Join(state, "sandboxes.json")}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	if code := call(t, http.MethodPost, ts.URL+"/v1/hosts", "s",
		api.RegisterHostRequest{Name: "h1", URL: fakeHost(t).URL, Capacity: 10}, nil); code != http.StatusOK {
		t.Fatalf("register host: %d", code)
	}

	var who api.WhoAmI
	call(t, http.MethodGet, ts.URL+"/v1/whoami", alice, nil, &who)
	if who.Name != "alice-laptop" || who.Account != "alice" {
		t.Errorf("whoami = %+v", who)
	}

	var sb api.Sandbox
	if code := call(t, http.MethodPost, ts.URL+"/v1/sandboxes", alice, api.CreateSandboxRequest{}, &sb); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if sb.Account != "alice" {
		t.Errorf("sandbox account = %q, want alice", sb.Account)
	}

	list := func(token string) []api.Sandbox {
		var out []api.Sandbox
		if code := call(t, http.MethodGet, ts.URL+"/v1/sandboxes", token, nil, &out); code != http.StatusOK {
			t.Fatalf("list: %d", code)
		}
		return out
	}
	if got := list(bob); len(got) != 0 {
		t.Errorf("bob sees %d of alice's sandboxes", len(got))
	}
	// Tokens in the same account see each other's sandboxes.
	if got := list(aliceCI); len(got) != 1 || got[0].ID != sb.ID {
		t.Errorf("alice's other token sees %+v", got)
	}

	// Another account's sandbox is not found, not forbidden.
	if code := call(t, http.MethodDelete, ts.URL+"/v1/sandboxes/"+sb.ID, bob, nil, nil); code != http.StatusNotFound {
		t.Errorf("bob destroying alice's sandbox: %d, want 404", code)
	}
	if got := list(alice); len(got) != 1 {
		t.Fatal("bob's destroy removed alice's sandbox")
	}
	var bobUsage api.Usage
	call(t, http.MethodGet, ts.URL+"/v1/usage", bob, nil, &bobUsage)
	if bobUsage.Running != 0 {
		t.Errorf("bob's usage counts alice's sandbox: %+v", bobUsage)
	}

	if code := call(t, http.MethodDelete, ts.URL+"/v1/sandboxes/"+sb.ID, aliceCI, nil, nil); code/100 != 2 {
		t.Errorf("alice destroying her own sandbox: %d", code)
	}
	if got := list(alice); len(got) != 0 {
		t.Errorf("sandbox still listed after destroy: %+v", got)
	}
}

// Files written before accounts existed belong to the default account.
func TestNoAccountIsLocal(t *testing.T) {
	state := t.TempDir()
	tokens := filepath.Join(state, "tokens.json")
	old := `[{"id":"tok_1","name":"laptop","sha256":"` + HashToken("eph_old") + `","created":"2026-11-01T00:00:00Z"}]`
	if err := os.WriteFile(tokens, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := (&TokenFile{Path: tokens}).Check(context.Background(), "eph_old"); !ok || p.Account != DefaultAccount {
		t.Errorf("old token: %+v, %v", p, ok)
	}

	sandboxes := filepath.Join(state, "sandboxes.json")
	if err := os.WriteFile(sandboxes, []byte(`{"sandboxes":[{"id":"sbx_1","host":"h1","created_at":"2026-11-01T00:00:00Z","owner":"laptop"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := &Server{Tokens: StaticToken{Name: "laptop", Token: "eph_old"}, StatePath: sandboxes}
	if err := cs.Load(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	var out []api.Sandbox
	call(t, http.MethodGet, ts.URL+"/v1/sandboxes", "eph_old", nil, &out)
	if len(out) != 1 || out[0].Account != DefaultAccount {
		t.Errorf("old sandbox not in the default account: %+v", out)
	}
}
