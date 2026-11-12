package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

// TokenStore keeps API tokens. TokenFile keeps them in a JSON file;
// pgstore.Store keeps them in Postgres.
type TokenStore interface {
	Tokens
	// CreateToken adds a token to an account (empty for DefaultAccount)
	// and returns its secret, which is shown only once.
	CreateToken(ctx context.Context, name, account string, limits Limits) (TokenRecord, string, error)
	// ListTokens returns the tokens, oldest first.
	ListTokens(ctx context.Context) ([]TokenRecord, error)
	// SetTokenLimits changes the limits of the token with this id or name.
	SetTokenLimits(ctx context.Context, idOrName string, limits Limits) error
	// RevokeToken deletes the token with this id or name.
	RevokeToken(ctx context.Context, idOrName string) error
}

// StateStore keeps what the central server must not lose on a restart:
// which sandboxes are running, and the sandbox-seconds each token used.
// FileState keeps them in a JSON file; pgstore.Store keeps them in
// Postgres, along with finished sandboxes.
type StateStore interface {
	// StartSandbox records a new sandbox. Its Token is never stored.
	StartSandbox(ctx context.Context, sb api.Sandbox) error
	// EndSandbox records that a sandbox ended, and why: one of the End
	// reasons.
	EndSandbox(ctx context.Context, id string, at time.Time, reason string) error
	// RunningSandboxes returns the sandboxes that have not ended.
	RunningSandboxes(ctx context.Context) ([]api.Sandbox, error)
	// AddUsage adds sandbox-seconds to a token's UTC day.
	AddUsage(ctx context.Context, u Usage) error
	// UsageSince returns usage from this UTC day (YYYY-MM-DD) on.
	UsageSince(ctx context.Context, day string) ([]Usage, error)
}

// Why a sandbox ended.
const (
	EndDestroyed = "destroyed" // someone destroyed it
	EndLimit     = "limit"     // its token used up the day's minutes
	EndGone      = "gone"      // its host stopped reporting it
	EndFailed    = "failed"    // the host could not create it
)

// Usage is the sandbox-seconds one token used on one UTC day.
type Usage struct {
	Account string  `json:"account"`
	Owner   string  `json:"owner"` // the token's name
	Day     string  `json:"day"`   // YYYY-MM-DD
	Seconds float64 `json:"seconds"`
}

// FileState keeps the running sandboxes and a month of usage in a JSON
// file. It forgets finished sandboxes.
type FileState struct {
	Path string
	Now  func() time.Time // for tests; nil is time.Now

	mu     sync.Mutex
	loaded bool
	st     fileState
}

type fileState struct {
	Sandboxes []api.Sandbox `json:"sandboxes"`
	// Usage is from before accounts: token name to day to seconds, all in
	// DefaultAccount. It is read, then written as UsageByAccount.
	Usage map[string]map[string]float64 `json:"usage,omitempty"`
	// UsageByAccount is account to token name to day to seconds.
	UsageByAccount map[string]map[string]map[string]float64 `json:"usage_by_account,omitempty"`
}

// usageKeep is how much usage FileState keeps.
const usageKeep = 31 * 24 * time.Hour

func (f *FileState) loadLocked() error {
	if f.loaded {
		return nil
	}
	b, err := os.ReadFile(f.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var st fileState
	if len(b) > 0 {
		if err := json.Unmarshal(b, &st); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	if st.UsageByAccount == nil {
		st.UsageByAccount = map[string]map[string]map[string]float64{}
	}
	for owner, days := range st.Usage {
		for day, secs := range days {
			addUsage(st.UsageByAccount, Usage{Account: DefaultAccount, Owner: owner, Day: day, Seconds: secs})
		}
	}
	st.Usage = nil
	for i := range st.Sandboxes {
		st.Sandboxes[i].Account = accountOr(st.Sandboxes[i].Account) // saved before accounts
	}
	f.st, f.loaded = st, true
	return nil
}

func addUsage(m map[string]map[string]map[string]float64, u Usage) {
	if m[u.Account] == nil {
		m[u.Account] = map[string]map[string]float64{}
	}
	if m[u.Account][u.Owner] == nil {
		m[u.Account][u.Owner] = map[string]float64{}
	}
	m[u.Account][u.Owner][u.Day] += u.Seconds
}

// saveLocked writes the file, dropping usage older than usageKeep.
func (f *FileState) saveLocked() error {
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	cutoff := now().UTC().Add(-usageKeep).Format(dayFormat)
	for acct, owners := range f.st.UsageByAccount {
		for owner, days := range owners {
			for day := range days {
				if day < cutoff {
					delete(days, day)
				}
			}
			if len(days) == 0 {
				delete(owners, owner)
			}
		}
		if len(owners) == 0 {
			delete(f.st.UsageByAccount, acct)
		}
	}
	sort.Slice(f.st.Sandboxes, func(i, j int) bool { return f.st.Sandboxes[i].ID < f.st.Sandboxes[j].ID })
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(f.Path, f.st, 0o600)
}

// change loads the file, runs fn and saves the result.
func (f *FileState) change(fn func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.loadLocked(); err != nil {
		return err
	}
	fn()
	return f.saveLocked()
}

// StartSandbox implements StateStore.
func (f *FileState) StartSandbox(_ context.Context, sb api.Sandbox) error {
	sb.Token = ""
	return f.change(func() {
		for i := range f.st.Sandboxes {
			if f.st.Sandboxes[i].ID == sb.ID {
				f.st.Sandboxes[i] = sb
				return
			}
		}
		f.st.Sandboxes = append(f.st.Sandboxes, sb)
	})
}

// EndSandbox implements StateStore. The sandbox is forgotten.
func (f *FileState) EndSandbox(_ context.Context, id string, _ time.Time, _ string) error {
	return f.change(func() {
		out := f.st.Sandboxes[:0]
		for _, sb := range f.st.Sandboxes {
			if sb.ID != id {
				out = append(out, sb)
			}
		}
		f.st.Sandboxes = out
	})
}

// RunningSandboxes implements StateStore.
func (f *FileState) RunningSandboxes(context.Context) ([]api.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.loadLocked(); err != nil {
		return nil, err
	}
	return append([]api.Sandbox(nil), f.st.Sandboxes...), nil
}

// AddUsage implements StateStore.
func (f *FileState) AddUsage(_ context.Context, u Usage) error {
	return f.change(func() { addUsage(f.st.UsageByAccount, u) })
}

// UsageSince implements StateStore.
func (f *FileState) UsageSince(_ context.Context, since string) ([]Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.loadLocked(); err != nil {
		return nil, err
	}
	var out []Usage
	for acct, owners := range f.st.UsageByAccount {
		for owner, days := range owners {
			for day, secs := range days {
				if day >= since {
					out = append(out, Usage{Account: acct, Owner: owner, Day: day, Seconds: secs})
				}
			}
		}
	}
	return out, nil
}

// memState keeps nothing, for a server with no StateStore.
type memState struct{}

func (memState) StartSandbox(context.Context, api.Sandbox) error             { return nil }
func (memState) EndSandbox(context.Context, string, time.Time, string) error { return nil }
func (memState) RunningSandboxes(context.Context) ([]api.Sandbox, error)     { return nil, nil }
func (memState) AddUsage(context.Context, Usage) error                       { return nil }
func (memState) UsageSince(context.Context, string) ([]Usage, error)         { return nil, nil }
