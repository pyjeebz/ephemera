package signin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeAccounts records sign-ins and hands out numbered tokens.
type fakeAccounts struct {
	signIns []string
	revoked []string
}

func (f *fakeAccounts) SignIn(provider, subject, label string) (string, string, error) {
	f.signIns = append(f.signIns, provider+"/"+subject+"/"+label)
	return provider + ":" + label, "eph_token" + subject, nil
}

func (f *fakeAccounts) Revoke(token string) (bool, error) {
	f.revoked = append(f.revoked, token)
	return true, nil
}

// fakeProvider is an OAuth server that approves everyone: its
// authorize page bounces straight back with a code.
func fakeProvider(t *testing.T) (Provider, *httptest.Server) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "id" || q.Get("response_type") != "code" {
			t.Errorf("authorize query %v", q)
		}
		back, _ := url.Parse(q.Get("redirect_uri"))
		back.RawQuery = url.Values{"code": {"c0de"}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "c0de" || r.Form.Get("client_secret") != "secret" || r.Form.Get("grant_type") != "authorization_code" {
			json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "at"})
	})
	ps := httptest.NewServer(mux)
	t.Cleanup(ps.Close)
	return Provider{
		Name: "fake", ClientID: "id", ClientSecret: "secret",
		AuthURL: ps.URL + "/authorize", TokenURL: ps.URL + "/token",
		whoami: func(_ context.Context, _ *http.Client, token string) (string, string, error) {
			if token != "at" {
				t.Errorf("whoami with %q", token)
			}
			return "42", "sam", nil
		},
	}, ps
}

// relayWith serves the sign-in routes plus a stand-in home page.
func relayWith(t *testing.T, s *Server) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/auth/", s.Handler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("home")) })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	s.BaseURL = ts.URL
	return ts
}

func browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// TestSignInFlow: start → provider → callback → home, then the page
// claims its token, once; the token never shows in a URL.
func TestSignInFlow(t *testing.T) {
	p, _ := fakeProvider(t)
	accts := &fakeAccounts{}
	s := &Server{Providers: []Provider{p}, Accounts: accts}
	ts := relayWith(t, s)
	c := browser()

	var urls []string
	c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		urls = append(urls, req.URL.String())
		return nil
	}
	resp, err := c.Get(ts.URL + "/auth/fake")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if last := urls[len(urls)-1]; !strings.HasSuffix(last, "/#signed-in") {
		t.Fatalf("ended at %s, want the home page signed in", last)
	}
	for _, u := range urls {
		if strings.Contains(u, "eph_token") {
			t.Fatalf("token in a URL: %s", u)
		}
	}
	if len(accts.signIns) != 1 || accts.signIns[0] != "fake/42/sam" {
		t.Fatalf("sign-ins %v", accts.signIns)
	}

	claim := func() (int, map[string]string) {
		resp, err := c.Post(ts.URL+"/auth/claim", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if code, body := claim(); code != 200 || body["token"] != "eph_token42" || body["name"] != "fake:sam" {
		t.Fatalf("claim: %d %v", code, body)
	}
	if code, _ := claim(); code != http.StatusNotFound {
		t.Fatalf("second claim: %d, want 404", code)
	}

	// signing out revokes the token
	resp, err = c.Post(ts.URL+"/auth/signout", "application/json", strings.NewReader(`{"token":"eph_token42"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || len(accts.revoked) != 1 || accts.revoked[0] != "eph_token42" {
		t.Fatalf("signout: %d, revoked %v", resp.StatusCode, accts.revoked)
	}
}

// TestSignInRefusesForeignState: a callback whose state doesn't match
// the browser's own cookie (someone else's sign-in link) mints
// nothing.
func TestSignInRefusesForeignState(t *testing.T) {
	p, _ := fakeProvider(t)
	accts := &fakeAccounts{}
	s := &Server{Providers: []Provider{p}, Accounts: accts}
	ts := relayWith(t, s)
	c := browser()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := c.Get(ts.URL + "/auth/fake/callback?code=c0de&state=attacker")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/#signin-failed" || len(accts.signIns) != 0 {
		t.Fatalf("foreign state: Location %q, sign-ins %v", loc, accts.signIns)
	}
	if resp, _ := http.Get(ts.URL + "/auth/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown provider: %d", resp.StatusCode)
	}
}

type denyAll struct{}

func (denyAll) Allow(string) (bool, int, time.Duration) { return false, 0, time.Minute }

// TestSignInLimited: a client over its sign-in allowance is sent home
// before it reaches the provider.
func TestSignInLimited(t *testing.T) {
	p, _ := fakeProvider(t)
	s := &Server{Providers: []Provider{p}, Accounts: &fakeAccounts{}, Limit: denyAll{}, Client: func(*http.Request) string { return "1.2.3.4" }}
	ts := relayWith(t, s)
	c := browser()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(ts.URL + "/auth/fake")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/#signin-busy" {
		t.Fatalf("Location %q", loc)
	}
}
