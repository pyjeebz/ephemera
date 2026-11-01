package hub

import (
	"cmp"
	"errors"
)

// Account is who an announce belongs to, as the relay's
// Authenticator sees it.
type Account struct {
	// Name identifies the sender to the relay operator (admin,
	// metrics). Receivers never see it.
	Name string

	// Limits are this sender's caps. Zero fields fall back to the
	// Bridge's limits, so tiers only set what differs.
	Limits Limits

	// Guest: no token; the Bridge's Guests policy let it in.
	Guest bool
}

// Authenticator decides which senders may announce on this relay.
// The relay ships a static tokens file (package auth); a hosted
// deployment can plug in its own accounts service behind the same
// interface.
type Authenticator interface {
	// Authenticate maps a sender token to its account. It returns
	// ErrNoToken for an empty token and ErrBadToken for one it does
	// not accept. Any other error is transient (the accounts
	// service is down): the announce is dropped without a refusal,
	// so the daemon redials.
	Authenticate(token string) (Account, error)
}

// ErrNoToken and ErrBadToken are the two final auth refusals;
// ErrSuspended refuses an account the operator suspended.
var (
	ErrNoToken   = errors.New("hub: sender token required")
	ErrBadToken  = errors.New("hub: sender token not accepted")
	ErrSuspended = errors.New("hub: sender account suspended")
)

// Suspensions is the operator's list of suspended sender accounts,
// checked after the Authenticator.
type Suspensions interface {
	Suspended(account string) bool
}

// authenticate runs the bridge's Authenticator; nil means an open
// relay where every sender is anonymous.
func (b *Bridge) authenticate(token string) (Account, error) {
	if b.Auth == nil {
		return Account{}, nil
	}
	return b.Auth.Authenticate(token)
}

// Or fills l's zero fields from fallback.
func (l Limits) Or(fallback Limits) Limits {
	return Limits{
		MaxTTLSec: cmp.Or(l.MaxTTLSec, fallback.MaxTTLSec),
		MaxSize:   cmp.Or(l.MaxSize, fallback.MaxSize),
	}
}
