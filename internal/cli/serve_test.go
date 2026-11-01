package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ephemera.run/ephemera/internal/auth"
	"ephemera.run/ephemera/internal/hub"
)

// TestSenderAuth: -open stands alone; -guests works with or without a
// tokens file; a relay with none of them refuses to start.
func TestSenderAuth(t *testing.T) {
	tokens := filepath.Join(t.TempDir(), "tokens")
	alice := auth.NewToken()
	os.WriteFile(tokens, []byte("alice "+auth.Hash(alice)+"\n"), 0o600)

	for name, tc := range map[string]struct {
		f       serveFlags
		wantErr bool
		open    bool
	}{
		"nothing":         {f: serveFlags{}, wantErr: true},
		"open":            {f: serveFlags{open: true}, open: true},
		"open and guests": {f: serveFlags{open: true, guests: true}, wantErr: true},
		"open and tokens": {f: serveFlags{open: true, tokens: tokens}, wantErr: true},
		"tokens":          {f: serveFlags{tokens: tokens}},
		"guests":          {f: serveFlags{guests: true}},
		"both":            {f: serveFlags{guests: true, tokens: tokens}},
	} {
		a, err := senderAuth(tc.f)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if err != nil {
			continue
		}
		if tc.open != (a == nil) {
			t.Errorf("%s: authenticator %v", name, a)
			continue
		}
		if a == nil {
			continue
		}
		if _, err := a.Authenticate(""); !errors.Is(err, hub.ErrNoToken) {
			t.Errorf("%s: empty token: %v", name, err)
		}
		_, err = a.Authenticate(alice)
		if tc.f.tokens != "" && err != nil {
			t.Errorf("%s: alice's token: %v", name, err)
		}
		if tc.f.tokens == "" && !errors.Is(err, hub.ErrBadToken) {
			t.Errorf("%s: a token with no tokens file: %v", name, err)
		}
	}
}

// TestSenderPolicy: what /config says for an open relay, a relay with
// guests, and one that needs a token.
func TestSenderPolicy(t *testing.T) {
	b := &hub.Bridge{Limits: hub.Limits{MaxTTLSec: 86400}}
	p := senderPolicy(b, nil)
	if p.Guest == nil || *p.Guest != p.Max || p.Max.MaxTTLSec != 86400 || p.Max.MaxSize != hub.DefaultMaxSize {
		t.Errorf("open relay: %+v", p)
	}
	if p := senderPolicy(b, auth.Chain{}); p.Guest != nil {
		t.Errorf("tokens only: guest %+v, want none", p.Guest)
	}
	b.Guests = &hub.Guests{Limits: hub.Limits{MaxTTLSec: 3600, MaxSize: 1 << 30}}
	if p := senderPolicy(b, auth.Chain{}); p.Guest == nil || p.Guest.MaxTTLSec != 3600 || p.Guest.MaxSize != 1<<30 || p.Max.MaxTTLSec != 86400 {
		t.Errorf("guests: %+v (guest %+v)", p, p.Guest)
	}
}
