package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/host"
)

// Signing a CLI in through the browser, after the OAuth device flow
// (RFC 8628). The CLI asks for a code, shows the short user code and
// opens the approval page; the person, signed in, checks the code matches
// and approves; the CLI, polling with its secret device code, gets a new
// device token. The token is made only when the CLI collects it, so the
// server never holds it in the clear.

// DeviceLogin is a sign-in waiting for approval.
type DeviceLogin struct {
	DeviceHash string // SHA-256 of the device code; the code itself stays with the CLI
	UserCode   string // without the dash
	Device     string
	Account    string // set when approved or denied
	Status     string // pending, approved or denied
	Created    time.Time
	Expires    time.Time
}

// DeviceStore keeps pending device sign-ins. pgstore.Store implements it.
type DeviceStore interface {
	CreateDeviceLogin(ctx context.Context, d DeviceLogin) error
	// DeviceLoginByCode returns a pending, unexpired sign-in by user code,
	// or ErrNotFound.
	DeviceLoginByCode(ctx context.Context, userCode string, now time.Time) (DeviceLogin, error)
	// DecideDeviceLogin approves or denies a pending, unexpired sign-in
	// for an account, or returns ErrNotFound.
	DecideDeviceLogin(ctx context.Context, userCode, accountID string, approve bool, now time.Time) error
	// TakeDeviceLogin returns a sign-in by its device code's hash. One that
	// has been decided is deleted as it's returned, so its token can be
	// collected once. ErrNotFound means unknown or expired.
	TakeDeviceLogin(ctx context.Context, deviceHash string, now time.Time) (DeviceLogin, error)
	// CreateDeviceToken makes a device token in an account, named after the
	// device, with a number added if the name is taken.
	CreateDeviceToken(ctx context.Context, name, accountID string) (TokenRecord, string, error)
}

const (
	deviceTTL      = 10 * time.Minute
	deviceInterval = 2 // seconds between polls
	// DevicePrefix starts every device code.
	DevicePrefix = "ephd_"
	// userAlphabet has no look-alikes: no 0/O, 1/I/L.
	userAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	// approveMisses is how many unknown codes an account may try an hour.
	approveMisses = 10
)

// NewUserCode returns 8 letters and digits with no look-alikes.
func NewUserCode() (string, error) {
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(userAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = userAlphabet[n.Int64()]
	}
	return string(b), nil
}

// UserCodeDisplay writes a user code as people see it: K7QM-2XRT.
func UserCodeDisplay(code string) string {
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// normalCode turns what a person typed back into a stored user code.
func normalCode(s string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
}

var deviceName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// cleanDevice keeps a device name short and plain, as it becomes a token
// name.
func cleanDevice(s string) string {
	s = strings.Trim(deviceName.ReplaceAllString(strings.TrimSpace(s), "-"), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "cli"
	}
	return s
}

func (s *Server) devices() (DeviceStore, bool) {
	d, ok := s.Accounts.(DeviceStore)
	return d, ok
}

func (s *Server) deviceRoutes(mux *http.ServeMux) {
	if _, ok := s.devices(); !ok {
		return
	}
	mux.HandleFunc("POST /v1/device/codes", s.deviceCodes)
	mux.HandleFunc("POST /v1/device/token", s.deviceToken)
	mux.HandleFunc("GET /v1/device", s.session(s.deviceInfo))
	mux.HandleFunc("POST /v1/device/approve", s.session(s.deviceApprove))
}

func (s *Server) deviceCodes(w http.ResponseWriter, r *http.Request) {
	var req api.DeviceCodeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		host.WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		host.WriteError(w, http.StatusInternalServerError, "could not make a code")
		return
	}
	code := DevicePrefix + base64.RawURLEncoding.EncodeToString(b)
	user, err := NewUserCode()
	if err != nil {
		host.WriteError(w, http.StatusInternalServerError, "could not make a code")
		return
	}
	now := s.now().UTC()
	d := DeviceLogin{DeviceHash: HashToken(code), UserCode: user, Device: cleanDevice(req.Device),
		Status: "pending", Created: now, Expires: now.Add(deviceTTL)}
	store, _ := s.devices()
	if err := store.CreateDeviceLogin(r.Context(), d); err != nil {
		s.log().Error("store: device login", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "could not start the sign-in; try again")
		return
	}
	page := originOf(s.GitHub.DashboardURL) + "/device?code=" + url.QueryEscape(UserCodeDisplay(user))
	writeJSON(w, http.StatusOK, api.DeviceCode{DeviceCode: code, UserCode: UserCodeDisplay(user),
		VerificationURI: page, Interval: deviceInterval, ExpiresIn: int(deviceTTL.Seconds())})
}

func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request) {
	var req api.DeviceTokenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil || !strings.HasPrefix(req.DeviceCode, DevicePrefix) {
		host.WriteError(w, http.StatusBadRequest, "a device_code is required")
		return
	}
	store, _ := s.devices()
	d, err := store.TakeDeviceLogin(r.Context(), HashToken(req.DeviceCode), s.now())
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusOK, api.DeviceToken{Status: "expired"})
		return
	case err != nil:
		s.log().Error("store: take device login", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "try again")
		return
	case d.Status == "pending":
		writeJSON(w, http.StatusOK, api.DeviceToken{Status: "pending"})
		return
	case d.Status == "denied":
		writeJSON(w, http.StatusOK, api.DeviceToken{Status: "denied"})
		return
	}
	rec, secret, err := store.CreateDeviceToken(r.Context(), d.Device, d.Account)
	if err != nil {
		s.log().Error("store: device token", "err", err)
		host.WriteError(w, http.StatusServiceUnavailable, "the sign-in was approved but the token couldn't be made; run ephemera login again")
		return
	}
	s.log().Info("device signed in", "account", d.Account, "token", rec.Name)
	writeJSON(w, http.StatusOK, api.DeviceToken{Status: "ok", Token: secret, Name: rec.Name, Account: d.Account})
}

// misses counts unknown codes tried at the approval page, per account, so
// codes can't be guessed through it.
type misses struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

// tooMany reports whether an account has used up its misses this hour,
// and, with add, counts one more.
func (m *misses) tooMany(account string, now time.Time, add bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string][]time.Time{}
	}
	recent := m.m[account][:0]
	for _, t := range m.m[account] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if add {
		recent = append(recent, now)
	}
	m.m[account] = recent
	return len(recent) > approveMisses
}

func (s *Server) deviceInfo(w http.ResponseWriter, r *http.Request) {
	acct := account(r).ID
	if s.misses.tooMany(acct, s.now(), false) {
		host.WriteError(w, http.StatusTooManyRequests, "too many codes that don't match; wait a while and try again")
		return
	}
	store, _ := s.devices()
	d, err := store.DeviceLoginByCode(r.Context(), normalCode(r.URL.Query().Get("code")), s.now())
	if errors.Is(err, ErrNotFound) {
		s.misses.tooMany(acct, s.now(), true)
		host.WriteError(w, http.StatusNotFound, "no sign-in is waiting with that code; it may have expired, so run ephemera login again")
		return
	}
	if err != nil {
		host.WriteError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	writeJSON(w, http.StatusOK, api.DeviceInfo{UserCode: UserCodeDisplay(d.UserCode), Device: d.Device, Created: d.Created})
}

func (s *Server) deviceApprove(w http.ResponseWriter, r *http.Request) {
	acct := account(r).ID
	if s.misses.tooMany(acct, s.now(), false) {
		host.WriteError(w, http.StatusTooManyRequests, "too many codes that don't match; wait a while and try again")
		return
	}
	var req api.DeviceApproval
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		host.WriteError(w, http.StatusBadRequest, "bad request: %v", err)
		return
	}
	store, _ := s.devices()
	err := store.DecideDeviceLogin(r.Context(), normalCode(req.UserCode), acct, req.Approve, s.now())
	if errors.Is(err, ErrNotFound) {
		s.misses.tooMany(acct, s.now(), true)
		host.WriteError(w, http.StatusNotFound, "no sign-in is waiting with that code; it may have expired, so run ephemera login again")
		return
	}
	if err != nil {
		host.WriteError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
