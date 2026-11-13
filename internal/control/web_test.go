package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/pgstore/pgtest"
)

const dashboard = "https://dash.example"

// fakeGitHub answers the two calls sign-in makes. Code "good" is user 99
// (octo), "good2" is user 100 (other).
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	users := map[string]string{
		"gho_one": `{"id":99,"login":"octo","name":"Octo Cat"}`,
		"gho_two": `{"id":100,"login":"other"}`,
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			r.ParseForm()
			tok := map[string]string{"good": "gho_one", "good2": "gho_two"}[r.Form.Get("code")]
			if r.Form.Get("client_secret") != "csecret" || tok == "" {
				json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": tok})
		case "/user":
			u, ok := users[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			if !ok {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			io.WriteString(w, u)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	return gh
}

type web struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (w web) do(method, path string, header http.Header, body any) *http.Response {
	w.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.base+path, rd)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := w.c.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	return resp
}

// signIn goes through GitHub sign-in with code and returns the session
// cookie, or nil if sign-in failed.
func (w web) signIn(code string) *http.Cookie {
	w.t.Helper()
	start := w.do("GET", "/auth/github", nil, nil)
	start.Body.Close()
	if start.StatusCode != http.StatusFound {
		w.t.Fatalf("start: %s", start.Status)
	}
	loc, _ := url.Parse(start.Header.Get("Location"))
	q := loc.Query()
	if q.Get("client_id") != "cid" || q.Get("scope") != "read:user" || q.Get("state") == "" ||
		q.Get("redirect_uri") != "https://api.example/auth/github/callback" {
		w.t.Errorf("authorize URL %s", loc)
	}
	var state *http.Cookie
	for _, c := range start.Cookies() {
		if c.Name == "ephemera_oauth_state" {
			state = c
		}
	}
	if state == nil || !state.HttpOnly {
		w.t.Fatalf("no HttpOnly state cookie: %v", start.Cookies())
	}
	cb := w.do("GET", "/auth/github/callback?code="+code+"&state="+q.Get("state"),
		http.Header{"Cookie": {state.String()}}, nil)
	cb.Body.Close()
	for _, c := range cb.Cookies() {
		if c.Name == "ephemera_session" && c.Value != "" {
			if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != dashboard {
				w.t.Errorf("callback: %s to %q", cb.Status, cb.Header.Get("Location"))
			}
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
				w.t.Errorf("session cookie attributes: %+v", c)
			}
			return c
		}
	}
	return nil
}

func session(c *http.Cookie, origin string) http.Header {
	h := http.Header{"Cookie": {c.Name + "=" + c.Value}, "Content-Type": {"application/json"}}
	if origin != "" {
		h.Set("Origin", origin)
	}
	return h
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGitHubSignIn(t *testing.T) {
	db, dbURL := pgtest.Open(t)
	gh := fakeGitHub(t)
	cs := &control.Server{Tokens: db, State: db, Accounts: db, GitHub: &control.GitHub{
		ClientID: "cid", ClientSecret: "csecret", PublicURL: "https://api.example", DashboardURL: dashboard,
		AuthorizeURL: gh.URL + "/login/oauth/authorize", TokenURL: gh.URL + "/login/oauth/access_token", APIURL: gh.URL,
	}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	w := web{t: t, base: ts.URL, c: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}

	// A callback without the browser's state, or with another one, is refused.
	if r := w.do("GET", "/auth/github/callback?code=good&state=forged", nil, nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("callback with no state cookie: %s", r.Status)
	}
	if r := w.do("GET", "/auth/github/callback?code=good&state=forged",
		http.Header{"Cookie": {"ephemera_oauth_state=real"}}, nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("callback with the wrong state: %s", r.Status)
	}
	if c := w.signIn("bad"); c != nil {
		t.Fatal("a bad code started a session")
	}

	octo := w.signIn("good")
	if octo == nil {
		t.Fatal("sign-in gave no session")
	}
	me := decode[api.Account](t, w.do("GET", "/v1/me", session(octo, ""), nil))
	if me.ID != "gh-99" || me.Login != "octo" || me.Name != "Octo Cat" {
		t.Errorf("me = %+v", me)
	}

	// Making a token needs the dashboard's origin.
	req := api.CreateTokenRequest{Name: "laptop", MaxSandboxes: 3}
	for _, origin := range []string{"", "https://evil.example"} {
		if r := w.do("POST", "/v1/tokens", session(octo, origin), req); r.StatusCode != http.StatusForbidden {
			t.Errorf("create token with origin %q: %s", origin, r.Status)
		}
	}
	r := w.do("POST", "/v1/tokens", session(octo, dashboard), req)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create token: %s", r.Status)
	}
	created := decode[api.CreatedToken](t, r)
	if !strings.HasPrefix(created.Secret, control.TokenPrefix) || created.MaxSandboxes != 3 {
		t.Errorf("created = %+v", created)
	}
	// The token works for the CLI, in the GitHub account.
	who := decode[api.WhoAmI](t, w.do("GET", "/v1/whoami", http.Header{"Authorization": {"Bearer " + created.Secret}}, nil))
	if who.Account != "gh-99" || who.Name != "laptop" {
		t.Errorf("whoami with the new token = %+v", who)
	}
	list := w.do("GET", "/v1/tokens", session(octo, ""), nil)
	body, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if strings.Contains(string(body), created.Secret) || strings.Contains(string(body), "sha256") {
		t.Error("listing tokens shows the secret or its hash")
	}

	// Another GitHub user sees none of it and can't revoke it.
	other := w.signIn("good2")
	if toks := decode[[]api.Token](t, w.do("GET", "/v1/tokens", session(other, ""), nil)); len(toks) != 0 {
		t.Errorf("other sees %d of octo's tokens", len(toks))
	}
	if r := w.do("DELETE", "/v1/tokens/"+created.ID, session(other, dashboard), nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("other revoking octo's token: %s", r.Status)
	}
	if r := w.do("DELETE", "/v1/tokens/"+created.ID, session(octo, dashboard), nil); r.StatusCode != http.StatusNoContent {
		t.Errorf("octo revoking their token: %s", r.Status)
	}
	if r := w.do("GET", "/v1/whoami", http.Header{"Authorization": {"Bearer " + created.Secret}}, nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token still works: %s", r.Status)
	}

	// Only the dashboard's origin gets CORS headers.
	pre := w.do("OPTIONS", "/v1/tokens", http.Header{"Origin": {dashboard}}, nil)
	if pre.StatusCode != http.StatusNoContent || pre.Header.Get("Access-Control-Allow-Origin") != dashboard ||
		pre.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("preflight from the dashboard: %s %v", pre.Status, pre.Header)
	}
	if r := w.do("GET", "/v1/me", http.Header{"Origin": {"https://evil.example"}}, nil); r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("another origin got CORS headers")
	}

	if r := w.do("POST", "/auth/logout", session(octo, dashboard), nil); r.StatusCode != http.StatusNoContent {
		t.Errorf("logout: %s", r.Status)
	}
	if r := w.do("GET", "/v1/me", session(octo, ""), nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("session still works after logout: %s", r.Status)
	}

	// GitHub's access tokens are never written down.
	conn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var dump string
	if err := conn.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(a::text, ' '), '') || (SELECT coalesce(string_agg(s::text, ' '), '') FROM sessions s) FROM accounts a`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, "gho_") {
		t.Error("a GitHub access token is in the database")
	}
}

// Without GitHub settings, the sign-in routes don't exist.
func TestSignInOff(t *testing.T) {
	ts := httptest.NewServer((&control.Server{Tokens: control.StaticToken{Name: "x", Token: "eph_x"}}).Handler())
	defer ts.Close()
	for _, p := range []string{"/auth/github", "/v1/me", "/v1/tokens"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s with sign-in off: %s, want 404", p, resp.Status)
		}
	}
}

// The CLI signs in through the browser: it gets a code, the signed-in
// person approves it from the dashboard's origin, and the CLI collects a
// device token, once.
func TestDeviceLogin(t *testing.T) {
	db, _ := pgtest.Open(t)
	gh := fakeGitHub(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	cs := &control.Server{Tokens: db, State: db, Accounts: db, Now: func() time.Time { return time.Unix(0, clock.Load()) },
		GitHub: &control.GitHub{ClientID: "cid", ClientSecret: "csecret", PublicURL: "https://api.example", DashboardURL: dashboard,
			AuthorizeURL: gh.URL + "/login/oauth/authorize", TokenURL: gh.URL + "/login/oauth/access_token", APIURL: gh.URL}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	w := web{t: t, base: ts.URL, c: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	octo := w.signIn("good")
	ctx := context.Background()

	start := func() (chan api.DeviceCode, chan error, chan api.DeviceToken) {
		codes, errs, toks := make(chan api.DeviceCode, 1), make(chan error, 1), make(chan api.DeviceToken, 1)
		go func() {
			tok, err := client.DeviceLogin(ctx, ts.URL, "jeebz laptop!", func(dc api.DeviceCode) { codes <- dc })
			errs <- err
			toks <- tok
		}()
		return codes, errs, toks
	}

	codes, errs, toks := start()
	dc := <-codes
	if !strings.HasPrefix(dc.VerificationURI, dashboard+"/device?code=") || len(dc.UserCode) != 9 || dc.UserCode[4] != '-' {
		t.Errorf("code %+v", dc)
	}
	info := decode[api.DeviceInfo](t, w.do("GET", "/v1/device?code="+strings.ToLower(dc.UserCode), session(octo, ""), nil))
	if info.Device != "jeebz-laptop" || info.UserCode != dc.UserCode {
		t.Errorf("info = %+v", info)
	}
	approve := api.DeviceApproval{UserCode: dc.UserCode, Approve: true}
	if r := w.do("POST", "/v1/device/approve", session(octo, "https://evil.example"), approve); r.StatusCode != http.StatusForbidden {
		t.Errorf("approve from another site: %s", r.Status)
	}
	if r := w.do("POST", "/v1/device/approve", session(octo, dashboard), approve); r.StatusCode != http.StatusNoContent {
		t.Fatalf("approve: %s", r.Status)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	tok := <-toks
	if tok.Account != "gh-99" || tok.Name != "jeebz-laptop" || !strings.HasPrefix(tok.Token, control.TokenPrefix) {
		t.Errorf("token = %+v", tok)
	}
	who := decode[api.WhoAmI](t, w.do("GET", "/v1/whoami", http.Header{"Authorization": {"Bearer " + tok.Token}}, nil))
	if who.Account != "gh-99" {
		t.Errorf("whoami = %+v", who)
	}
	toksList := decode[[]api.Token](t, w.do("GET", "/v1/tokens", session(octo, ""), nil))
	if len(toksList) != 1 || toksList[0].Kind != "device" {
		t.Errorf("tokens = %+v, want one device token", toksList)
	}
	// The code is spent: collecting again gets nothing.
	again := decode[api.DeviceToken](t, w.do("POST", "/v1/device/token", http.Header{"Content-Type": {"application/json"}},
		api.DeviceTokenRequest{DeviceCode: dc.DeviceCode}))
	if again.Status != "expired" || again.Token != "" {
		t.Errorf("second collect = %+v", again)
	}

	// Cancelled in the browser.
	codes, errs, _ = start()
	dc = <-codes
	w.do("POST", "/v1/device/approve", session(octo, dashboard), api.DeviceApproval{UserCode: dc.UserCode, Approve: false})
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("denied login: %v", err)
	}

	// Not approved within 10 minutes.
	codes, errs, _ = start()
	dc = <-codes
	clock.Add(int64(11 * time.Minute))
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired login: %v", err)
	}
	if r := w.do("POST", "/v1/device/approve", session(octo, dashboard), api.DeviceApproval{UserCode: dc.UserCode, Approve: true}); r.StatusCode != http.StatusNotFound {
		t.Errorf("approving an expired code: %s", r.Status)
	}

	// Guessing codes is capped: 10 misses an hour.
	for i := 0; i < 10; i++ {
		w.do("GET", "/v1/device?code=AAAA-AAAA", session(octo, ""), nil)
	}
	if r := w.do("GET", "/v1/device?code=AAAA-AAAA", session(octo, ""), nil); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("11th wrong code: %s, want 429", r.Status)
	}
}

// Sign-in returns to ?next= when it's a path on the dashboard's site, and
// to the dashboard otherwise, so it can't send people to another site.
func TestSignInNext(t *testing.T) {
	db, _ := pgtest.Open(t)
	gh := fakeGitHub(t)
	cs := &control.Server{Tokens: db, State: db, Accounts: db, GitHub: &control.GitHub{
		ClientID: "cid", ClientSecret: "csecret", PublicURL: "https://api.example", DashboardURL: dashboard + "/dashboard",
		AuthorizeURL: gh.URL + "/login/oauth/authorize", TokenURL: gh.URL + "/login/oauth/access_token", APIURL: gh.URL,
	}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	w := web{t: t, base: ts.URL, c: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
	for next, want := range map[string]string{
		"/device?code=K7QM-2XRT":     dashboard + "/device?code=K7QM-2XRT",
		"":                           dashboard + "/dashboard",
		"//evil.example/x":           dashboard + "/dashboard",
		"https://evil.example/":      dashboard + "/dashboard",
		"/\\evil.example":            dashboard + "/dashboard",
		"/ok\r\nSet-Cookie: x=y":     dashboard + "/dashboard",
		"javascript:alert(1)":        dashboard + "/dashboard",
		"/dashboard/hosts?tab=a#top": dashboard + "/dashboard/hosts?tab=a#top",
	} {
		start := w.do("GET", "/auth/github?next="+url.QueryEscape(next), nil, nil)
		start.Body.Close()
		loc, _ := url.Parse(start.Header.Get("Location"))
		var cookies []string
		for _, c := range start.Cookies() {
			cookies = append(cookies, c.Name+"="+c.Value)
		}
		cb := w.do("GET", "/auth/github/callback?code=good&state="+loc.Query().Get("state"),
			http.Header{"Cookie": {strings.Join(cookies, "; ")}}, nil)
		cb.Body.Close()
		if got := cb.Header.Get("Location"); cb.StatusCode != http.StatusSeeOther || got != want {
			t.Errorf("next %q: %s to %q, want %q", next, cb.Status, got, want)
		}
	}
}
