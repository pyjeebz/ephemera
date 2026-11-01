// Package signin lets people get a sender token from the browser:
// they sign in with Google or GitHub (OAuth 2.0, authorization code
// flow) and the relay mints a token for their account.
//
// The token never travels in a URL. The provider sends the browser
// back to /auth/<provider>/callback, the relay mints the token, parks
// it under a one-time code in an HttpOnly cookie, and redirects to the
// home page; the page then POSTs /auth/claim to collect it, once.
package signin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Accounts turns a person at a provider into a sender token, and
// forgets a token on sign-out. auth.Accounts is one.
type Accounts interface {
	SignIn(provider, subject, label string) (name, token string, err error)
	Revoke(token string) (bool, error)
}

// Limiter is a per-key allowance, such as ratelimit.Limiter.
type Limiter interface {
	Allow(key string) (ok bool, remaining int, resetIn time.Duration)
}

// Provider is one place people can sign in.
type Provider struct {
	Name         string // in URLs and account names: "github", "google"
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scope        string

	// whoami asks the provider who an access token belongs to: their
	// stable id and a login or email to name them by.
	whoami func(ctx context.Context, c *http.Client, accessToken string) (subject, label string, err error)
}

// GitHub signs people in with a GitHub OAuth app. It asks for no
// scope: the public profile is enough.
func GitHub(clientID, clientSecret string) Provider {
	return Provider{
		Name: "github", ClientID: clientID, ClientSecret: clientSecret,
		AuthURL:  "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token",
		whoami: func(ctx context.Context, c *http.Client, token string) (string, string, error) {
			var u struct {
				ID    int64  `json:"id"`
				Login string `json:"login"`
			}
			if err := getJSON(ctx, c, "https://api.github.com/user", token, &u); err != nil {
				return "", "", err
			}
			if u.ID == 0 || u.Login == "" {
				return "", "", errors.New("github: no user in the reply")
			}
			return strconv.FormatInt(u.ID, 10), u.Login, nil
		},
	}
}

// Google signs people in with a Google OAuth client, by their
// verified email address.
func Google(clientID, clientSecret string) Provider {
	return Provider{
		Name: "google", ClientID: clientID, ClientSecret: clientSecret,
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scope:    "openid email",
		whoami: func(ctx context.Context, c *http.Client, token string) (string, string, error) {
			var u struct {
				Sub      string `json:"sub"`
				Email    string `json:"email"`
				Verified bool   `json:"email_verified"`
			}
			if err := getJSON(ctx, c, "https://openidconnect.googleapis.com/v1/userinfo", token, &u); err != nil {
				return "", "", err
			}
			if u.Sub == "" || u.Email == "" || !u.Verified {
				return "", "", errors.New("google: no verified email in the reply")
			}
			return u.Sub, u.Email, nil
		},
	}
}

const (
	stateCookie   = "eph_signin"  // the state sent to the provider
	handoffCookie = "eph_handoff" // the one-time code for /auth/claim
	handoffTTL    = 2 * time.Minute
)

// Server is the /auth/ routes.
type Server struct {
	Providers []Provider
	Accounts  Accounts
	BaseURL   string // the relay's public URL, for the callback address

	// Limit, if set, caps sign-ins per client; Client names the
	// client a request came from (its IP).
	Limit  Limiter
	Client func(*http.Request) string

	HTTP *http.Client // to the providers; nil: a 10s-timeout client

	mu       sync.Mutex
	handoffs map[string]handoff
}

type handoff struct {
	name, token string
	expires     time.Time
}

// Names lists the configured providers, for the web page.
func (s *Server) Names() []string {
	var names []string
	for _, p := range s.Providers {
		names = append(names, p.Name)
	}
	return names
}

// Handler serves GET /auth/{provider}, GET /auth/{provider}/callback,
// POST /auth/claim and POST /auth/signout.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/claim", s.claim)
	mux.HandleFunc("POST /auth/signout", s.signout)
	mux.HandleFunc("GET /auth/{provider}", s.start)
	mux.HandleFunc("GET /auth/{provider}/callback", s.callback)
	return mux
}

func (s *Server) provider(r *http.Request) (Provider, bool) {
	for _, p := range s.Providers {
		if p.Name == r.PathValue("provider") {
			return p, true
		}
	}
	return Provider{}, false
}

func (s *Server) callbackURL(p Provider) string {
	return strings.TrimSuffix(s.BaseURL, "/") + "/auth/" + p.Name + "/callback"
}

func (s *Server) secure() bool { return strings.HasPrefix(s.BaseURL, "https://") }

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/auth",
		MaxAge: int(maxAge.Seconds()), HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/auth", MaxAge: -1, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
}

// start sends the browser to the provider, with a fresh state kept in
// a cookie: the callback must bring the same one back, so nobody can
// sign a visitor in to an account of theirs.
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.Limit != nil && s.Client != nil {
		if ok, _, _ := s.Limit.Allow(s.Client(r)); !ok {
			http.Redirect(w, r, "/#signin-busy", http.StatusSeeOther)
			return
		}
	}
	state := random()
	s.setCookie(w, stateCookie, state, 10*time.Minute)
	q := url.Values{
		"client_id":     {p.ClientID},
		"redirect_uri":  {s.callbackURL(p)},
		"response_type": {"code"},
		"state":         {state},
	}
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	if p.Name == "google" {
		q.Set("prompt", "select_account")
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, p.AuthURL+"?"+q.Encode(), http.StatusSeeOther)
}

// callback finishes a sign-in: checks the state, trades the code for
// an access token, asks who it belongs to, mints a sender token and
// parks it for the page to claim.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	fail := func(why string, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "signin: %s: %s: %v\n", p.Name, why, err)
		}
		http.Redirect(w, r, "/#signin-failed", http.StatusSeeOther)
	}
	c, err := r.Cookie(stateCookie)
	s.clearCookie(w, stateCookie)
	q := r.URL.Query()
	if err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		fail("state mismatch", nil)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		fail("declined", nil) // the person said no at the provider
		return
	}
	ctx := r.Context()
	access, err := s.exchange(ctx, p, q.Get("code"))
	if err != nil {
		fail("code exchange", err)
		return
	}
	subject, label, err := p.whoami(ctx, s.client(), access)
	if err != nil {
		fail("whoami", err)
		return
	}
	name, token, err := s.Accounts.SignIn(p.Name, subject, label)
	if err != nil {
		fail("account", err)
		return
	}
	code := random()
	s.mu.Lock()
	if s.handoffs == nil {
		s.handoffs = map[string]handoff{}
	}
	now := time.Now()
	for k, h := range s.handoffs {
		if now.After(h.expires) {
			delete(s.handoffs, k)
		}
	}
	s.handoffs[code] = handoff{name: name, token: token, expires: now.Add(handoffTTL)}
	s.mu.Unlock()
	s.setCookie(w, handoffCookie, code, handoffTTL)
	http.Redirect(w, r, "/#signed-in", http.StatusSeeOther)
}

// claim hands the page its new token, once.
func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := r.Cookie(handoffCookie)
	s.clearCookie(w, handoffCookie)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "nothing to claim: sign in again"})
		return
	}
	s.mu.Lock()
	h, ok := s.handoffs[c.Value]
	delete(s.handoffs, c.Value)
	s.mu.Unlock()
	if !ok || time.Now().After(h.expires) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that sign-in expired: sign in again"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": h.name, "token": h.token})
}

// signout forgets the token in the body, so it stops working
// everywhere it was copied.
func (s *Server) signout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "want {\"token\": ...}"})
		return
	}
	if _, err := s.Accounts.Revoke(body.Token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "couldn't sign out; try again"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// exchange trades an authorization code for an access token.
func (s *Server) exchange(ctx context.Context, p Provider, code string) (string, error) {
	form := url.Values{
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
		"code":          {code},
		"redirect_uri":  {s.callbackURL(p)},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&t); err != nil {
		return "", fmt.Errorf("token reply (%s): %w", resp.Status, err)
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("no access token (%s): %s", resp.Status, t.Error)
	}
	return t.AccessToken, nil
}

func (s *Server) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func getJSON(ctx context.Context, c *http.Client, u, token string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func random() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
