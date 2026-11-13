package control_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/pgstore/pgtest"
	"github.com/pyjeebz/ephemera/internal/record"
)

func fingerprint(t *testing.T, hs *host.Server) string {
	t.Helper()
	fp, err := record.FingerprintKey(hs.Key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// The dashboard, signed in with a session, sees and stops its own
// account's hosts and sandboxes, and nobody else's. It can't make
// sandboxes.
func TestDashboardSession(t *testing.T) {
	db, _ := pgtest.Open(t)
	gh := fakeGitHub(t)
	cs := &control.Server{Tokens: db, State: db, Accounts: db, Hosts: db, GitHub: &control.GitHub{
		ClientID: "cid", ClientSecret: "csecret", PublicURL: "https://api.example", DashboardURL: dashboard,
		AuthorizeURL: gh.URL + "/login/oauth/authorize", TokenURL: gh.URL + "/login/oauth/access_token", APIURL: gh.URL,
	}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	w := web{t: t, base: ts.URL, c: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
	octo, other := w.signIn("good"), w.signIn("good2")
	if octo == nil || other == nil {
		t.Fatal("sign-in gave no session")
	}

	hs, _ := newHost(t, "box")
	hosttest.Connect(t, hs, ts.URL, joinCode(t, cs, "gh-99"),
		api.HostHello{Name: "box", URL: "http://192.0.2.1:7701", Capacity: 5, Version: "v9.9.9"})
	tok := decode[api.CreatedToken](t, w.do("POST", "/v1/tokens", session(octo, dashboard), api.CreateTokenRequest{Name: "laptop"}))
	bearer := http.Header{"Authorization": {"Bearer " + tok.Secret}, "Content-Type": {"application/json"}}
	r := w.do("POST", "/v1/sandboxes", bearer, api.CreateSandboxRequest{TokenHash: api.HashSandboxToken("t")})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create with the token: %s", r.Status)
	}
	sb := decode[api.Sandbox](t, r)

	// A session can't make sandboxes, even from the dashboard.
	if r := w.do("POST", "/v1/sandboxes", session(octo, dashboard), api.CreateSandboxRequest{TokenHash: api.HashSandboxToken("u")}); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("create with a session: %s, want 401", r.Status)
	}

	hosts := decode[[]api.Host](t, w.do("GET", "/v1/hosts", session(octo, ""), nil))
	if len(hosts) != 1 || !hosts[0].Online || hosts[0].Running != 1 || hosts[0].Capacity != 5 ||
		hosts[0].Version != "v9.9.9" || hosts[0].Joined.IsZero() || hosts[0].Fingerprint != fingerprint(t, hs) {
		t.Errorf("octo's hosts = %+v", hosts)
	}
	if got := decode[[]api.Host](t, w.do("GET", "/v1/hosts", session(other, ""), nil)); len(got) != 0 {
		t.Errorf("other sees octo's hosts: %+v", got)
	}
	if got := decode[[]api.Sandbox](t, w.do("GET", "/v1/sandboxes", session(octo, ""), nil)); len(got) != 1 || got[0].ID != sb.ID {
		t.Errorf("octo's sandboxes = %+v", got)
	}
	if got := decode[[]api.Sandbox](t, w.do("GET", "/v1/sandboxes", session(other, ""), nil)); len(got) != 0 {
		t.Errorf("other sees octo's sandboxes: %+v", got)
	}
	// The dashboard's usage is the whole account's.
	if u := decode[api.Usage](t, w.do("GET", "/v1/usage", session(octo, ""), nil)); u.Running != 1 || u.Name != "" {
		t.Errorf("octo's usage = %+v", u)
	}

	// Stopping needs the dashboard's origin, and the account's own sandbox.
	if r := w.do("DELETE", "/v1/sandboxes/"+sb.ID, session(octo, ""), nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("stop with no origin: %s, want 403", r.Status)
	}
	if r := w.do("DELETE", "/v1/sandboxes/"+sb.ID, session(other, dashboard), nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("other stopping octo's sandbox: %s, want 404", r.Status)
	}
	if r := w.do("DELETE", "/v1/sandboxes/"+sb.ID, session(octo, dashboard), nil); r.StatusCode/100 != 2 {
		t.Errorf("octo stopping its sandbox: %s", r.Status)
	}

	runs := decode[[]api.Run](t, w.do("GET", "/v1/runs", session(octo, ""), nil))
	if len(runs) != 1 || runs[0].ID != sb.ID || runs[0].Owner != "laptop" || runs[0].Host != "box" ||
		runs[0].EndedAt == nil || runs[0].EndReason != control.EndDestroyed {
		t.Errorf("octo's runs = %+v", runs)
	}
	if got := decode[[]api.Run](t, w.do("GET", "/v1/runs", session(other, ""), nil)); len(got) != 0 {
		t.Errorf("other sees octo's runs: %+v", got)
	}
	// The CLI's token reads the same history.
	if got := decode[[]api.Run](t, w.do("GET", "/v1/runs", bearer, nil)); len(got) != 1 {
		t.Errorf("runs with the token = %+v", got)
	}
	if r := w.do("GET", "/v1/runs?before=yesterday", session(octo, ""), nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("runs before a bad time: %s, want 400", r.Status)
	}

	if r := w.do("DELETE", "/v1/hosts/"+hosts[0].ID, session(other, dashboard), nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("other removing octo's host: %s, want 404", r.Status)
	}
	r = w.do("DELETE", "/v1/hosts/"+hosts[0].ID, session(octo, dashboard), nil)
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Errorf("octo removing its host: %s", r.Status)
	}
}
