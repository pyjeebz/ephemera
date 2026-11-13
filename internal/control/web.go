package control

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
	"strconv"
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
)

// GitHub is the GitHub OAuth app people sign in with on the web. Sign-in
// asks only for read:user: their id, login, name and avatar. The access
// token GitHub returns is used once to read those, then dropped.
type GitHub struct {
	ClientID     string
	ClientSecret string
	// PublicURL is where browsers reach this server, e.g.
	// https://api.ephemera.run. GitHub sends people back to
	// PublicURL/auth/github/callback.
	PublicURL string
	// DashboardURL is where people land after signing in. Pages from its
	// origin may call the API with the session cookie.
	DashboardURL string

	// For tests; empty means github.com.
	AuthorizeURL, TokenURL, APIURL string
	Client                         *http.Client
}

// GitHubUser is what sign-in reads from GitHub.
type GitHubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// AccountStore keeps the people who sign in on the web, their sessions,
// and their tokens. pgstore.Store implements it.
type AccountStore interface {
	TokenStore
	// UpsertGitHubAccount makes or updates the account for a GitHub user.
	UpsertGitHubAccount(ctx context.Context, u GitHubUser) (api.Account, error)
	// CreateSession starts a session for an account and returns its
	// secret. Only a hash of it is stored.
	CreateSession(ctx context.Context, accountID string, ttl time.Duration) (string, error)
	// CheckSession returns the account a live session belongs to.
	CheckSession(ctx context.Context, secret string) (api.Account, bool)
	// DeleteSession ends a session.
	DeleteSession(ctx context.Context, secret string) error
	// AccountTokens returns an account's tokens, oldest first.
	AccountTokens(ctx context.Context, accountID string) ([]TokenRecord, error)
	// RevokeAccountToken deletes one of an account's tokens. It returns
	// ErrNotFound if the account has no token with that id.
	RevokeAccountToken(ctx context.Context, accountID, id string) error
}

// ErrNotFound means the thing asked for does not exist, or belongs to
// another account.
var ErrNotFound = errors.New("not found")

// GitHubAccount is the account id of a GitHub user.
func GitHubAccount(id int64) string { return "gh-" + strconv.FormatInt(id, 10) }

const (
	sessionCookie = "ephemera_session"
	stateCookie   = "ephemera_oauth_state"
	sessionTTL    = 30 * 24 * time.Hour
	// SessionPrefix starts every session secret.
	SessionPrefix = "ephs_"
)

// NewSessionSecret returns a new session secret.
func NewSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return SessionPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func (g *GitHub) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// secure reports whether cookies should be Secure: always, unless this
// server is reached over plain HTTP, as in local development.
func (g *GitHub) secure() bool { return !strings.HasPrefix(g.PublicURL, "http://") }

func originOf(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Scheme == "" || p.Host == "" {
		return ""
	}
	return p.Scheme + "://" + p.Host
}

// webEnabled reports whether web sign-in is set up.
func (s *Server) webEnabled() bool { return s.GitHub != nil && s.Accounts != nil }

func (s *Server) webRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/github", s.githubStart)
	mux.HandleFunc("GET /auth/github/callback", s.githubCallback)
	mux.HandleFunc("POST /auth/logout", s.session(s.logout))
	mux.HandleFunc("GET /v1/me", s.session(s.me))
	mux.HandleFunc("GET /v1/tokens", s.session(s.tokenList))
	mux.HandleFunc("POST /v1/tokens", s.session(s.tokenCreate))
	mux.HandleFunc("DELETE /v1/tokens/{id}", s.session(s.tokenRevoke))
	mux.HandleFunc("POST /v1/join-codes", s.session(s.joinCodeCreate))
	s.deviceRoutes(mux)
}

// joinCodeCreate makes a one-time code for adding a host to the account.
func (s *Server) joinCodeCreate(w http.ResponseWriter, r *http.Request) {
	code, err := s.hostRecords().CreateJoinCode(r.Context(), account(r).ID, JoinCodeTTL)
	if err != nil {
		s.log().Error("store: join code", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not make a join code; try again")
		return
	}
	writeJSON(w, http.StatusCreated, api.JoinCode{Code: code, Expires: time.Now().Add(JoinCodeTTL).UTC()})
}

// cors lets the dashboard's origin call the API with the session cookie.
// Every other origin gets no CORS headers, so browsers keep its pages out.
func (s *Server) cors(next http.Handler) http.Handler {
	allowed := originOf(s.GitHub.DashboardURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && o == allowed {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// githubStart sends the browser to GitHub to sign in.
func (s *Server) githubStart(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		host.WriteError(w, http.StatusInternalServerError, "could not start sign-in")
		return
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	g := s.GitHub
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: "/auth/github",
		MaxAge: 600, HttpOnly: true, Secure: g.secure(), SameSite: http.SameSiteLaxMode})
	q := url.Values{
		"client_id":    {g.ClientID},
		"redirect_uri": {strings.TrimRight(g.PublicURL, "/") + "/auth/github/callback"},
		"scope":        {"read:user"},
		"state":        {state},
	}
	http.Redirect(w, r, or(g.AuthorizeURL, "https://github.com/login/oauth/authorize")+"?"+q.Encode(), http.StatusFound)
}

// githubCallback finishes sign-in: it checks the state, trades the code
// for a token, reads the user, and starts a session.
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	g := s.GitHub
	// The state must be the one this browser was given, so a sign-in
	// started elsewhere can't be finished here.
	c, err := r.Cookie(stateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		host.WriteError(w, http.StatusBadRequest, "this sign-in expired or was started somewhere else; start again from the dashboard")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/auth/github", MaxAge: -1,
		HttpOnly: true, Secure: g.secure(), SameSite: http.SameSiteLaxMode})
	if e := r.URL.Query().Get("error"); e != "" {
		host.WriteError(w, http.StatusForbidden, "GitHub sign-in was cancelled: %s", e)
		return
	}
	user, err := s.githubUser(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		s.log().Warn("github sign-in failed", "err", err)
		host.WriteError(w, http.StatusBadGateway, "GitHub sign-in failed; try again")
		return
	}
	acct, err := s.Accounts.UpsertGitHubAccount(r.Context(), user)
	if err != nil {
		s.log().Error("store: account", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not save the account; try again")
		return
	}
	secret, err := s.Accounts.CreateSession(r.Context(), acct.ID, sessionTTL)
	if err != nil {
		s.log().Error("store: session", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not start a session; try again")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: secret, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: g.secure(), SameSite: http.SameSiteLaxMode})
	s.log().Info("signed in", "account", acct.ID, "login", acct.Login)
	http.Redirect(w, r, g.DashboardURL, http.StatusSeeOther)
}

// githubUser trades an OAuth code for an access token and reads the user
// with it. The token is not kept.
func (s *Server) githubUser(ctx context.Context, code string) (GitHubUser, error) {
	g := s.GitHub
	if code == "" {
		return GitHubUser{}, errors.New("no code")
	}
	form := url.Values{"client_id": {g.ClientID}, "client_secret": {g.ClientSecret}, "code": {code},
		"redirect_uri": {strings.TrimRight(g.PublicURL, "/") + "/auth/github/callback"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, or(g.TokenURL, "https://github.com/login/oauth/access_token"), strings.NewReader(form.Encode()))
	if err != nil {
		return GitHubUser{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.client().Do(req)
	if err != nil {
		return GitHubUser{}, err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&tok)
	resp.Body.Close()
	if err != nil {
		return GitHubUser{}, fmt.Errorf("token reply: %w", err)
	}
	if tok.AccessToken == "" {
		return GitHubUser{}, fmt.Errorf("no access token: %s", or(tok.Error, resp.Status))
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, or(g.APIURL, "https://api.github.com")+"/user", nil)
	if err != nil {
		return GitHubUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err = g.client().Do(req)
	if err != nil {
		return GitHubUser{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return GitHubUser{}, fmt.Errorf("reading the user: %s", resp.Status)
	}
	var u GitHubUser
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&u); err != nil {
		return GitHubUser{}, fmt.Errorf("user reply: %w", err)
	}
	if u.ID <= 0 || u.Login == "" {
		return GitHubUser{}, errors.New("GitHub returned no user id")
	}
	return u, nil
}

type accountKey struct{}

// session runs h for a request with a live session cookie. A request that
// changes something must also come from the dashboard's origin, so another
// site can't use a signed-in browser's cookie.
func (s *Server) session(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			o := r.Header.Get("Origin")
			if o == "" || (o != originOf(s.GitHub.DashboardURL) && o != originOf(s.GitHub.PublicURL)) {
				host.WriteError(w, http.StatusForbidden, "request from another site refused")
				return
			}
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || !strings.HasPrefix(c.Value, SessionPrefix) {
			host.WriteError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		acct, ok := s.Accounts.CheckSession(r.Context(), c.Value)
		if !ok {
			host.WriteError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), accountKey{}, acct)))
	}
}

// tokenOrSession runs h for the CLI with a token, or for the dashboard
// with a session, acting for the whole account. Only for endpoints that
// read or stop things: a session can't make sandboxes, since only the CLI
// holds a sandbox's token.
func (s *Server) tokenOrSession(h http.HandlerFunc) http.HandlerFunc {
	tok := s.user(h)
	if !s.webEnabled() {
		return tok
	}
	ses := s.session(func(w http.ResponseWriter, r *http.Request) {
		p := Principal{Account: account(r).ID, Dashboard: true}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil || r.Header.Get("Authorization") != "" {
			tok(w, r)
			return
		}
		ses(w, r)
	}
}

func account(r *http.Request) api.Account {
	a, _ := r.Context().Value(accountKey{}).(api.Account)
	return a
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Accounts.DeleteSession(r.Context(), c.Value); err != nil {
			s.log().Error("store: delete session", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.GitHub.secure(), SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, account(r))
}

// runList lists the account's runs, newest first. ?before= (RFC 3339)
// pages back from the oldest one already shown.
func (s *Server) runList(rs RunStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		before := s.now().Add(time.Minute)
		if b := r.URL.Query().Get("before"); b != "" {
			t, err := time.Parse(time.RFC3339Nano, b)
			if err != nil {
				host.WriteError(w, http.StatusBadRequest, "before must be a time like 2026-11-12T09:41:00Z")
				return
			}
			before = t
		}
		runs, err := rs.AccountRuns(r.Context(), principal(r).Account, before, runPage)
		if err != nil {
			s.log().Error("store: list runs", "err", err)
			host.WriteError(w, http.StatusServiceUnavailable, "could not list runs; try again")
			return
		}
		if runs == nil {
			runs = []api.Run{}
		}
		writeJSON(w, http.StatusOK, runs)
	}
}

// runPage is how many runs one request returns.
const runPage = 100

func tokenOut(t TokenRecord) api.Token {
	kind := t.Kind
	if kind == "" {
		kind = "api"
	}
	return api.Token{ID: t.ID, Name: t.Name, Kind: kind, Created: t.Created,
		MaxSandboxes: t.MaxSandboxes, MaxMinutesPerDay: t.MaxMinutesPerDay}
}

func (s *Server) tokenList(w http.ResponseWriter, r *http.Request) {
	recs, err := s.Accounts.AccountTokens(r.Context(), account(r).ID)
	if err != nil {
		s.log().Error("store: list tokens", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not list tokens; try again")
		return
	}
	out := make([]api.Token, 0, len(recs))
	for _, t := range recs {
		out = append(out, tokenOut(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateTokenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		host.WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	limits := Limits{MaxSandboxes: req.MaxSandboxes, MaxMinutesPerDay: req.MaxMinutesPerDay}
	if _, _, err := CheckTokenInput(req.Name, account(r).ID, limits); err != nil {
		host.WriteError(w, http.StatusBadRequest, "%v", err)
		return
	}
	rec, secret, err := s.Accounts.CreateToken(r.Context(), req.Name, account(r).ID, limits)
	if err != nil {
		// The input was checked above, so what's left is a name in use or
		// the database.
		host.WriteError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, http.StatusCreated, api.CreatedToken{Token: tokenOut(rec), Secret: secret})
}

func (s *Server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	err := s.Accounts.RevokeAccountToken(r.Context(), account(r).ID, r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		host.WriteError(w, http.StatusNotFound, "no token %s", r.PathValue("id"))
		return
	}
	if err != nil {
		s.log().Error("store: revoke token", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not revoke the token; try again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
