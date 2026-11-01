package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ephemera.run/ephemera/internal/hub"
)

// MaxTokens is how many tokens one account keeps: each sign-in mints
// one (a browser, a laptop's CLI, ...) and the oldest goes past this.
const MaxTokens = 5

// Accounts holds the senders who signed in on the web. An account is
// a person at a sign-in provider (GitHub user 583231, say); it keeps
// the hashes of the tokens it was given, never the tokens. The set
// lives in one JSON file under the relay's data directory, rewritten
// whole on each change.
type Accounts struct {
	mu     sync.Mutex
	path   string
	byKey  map[string]*Account // provider:subject -> account
	byName map[string]*Account
	byHash map[string]*Account // token hash -> account
}

// Account is one signed-in sender.
type Account struct {
	// Name is how the operator knows the account (reports, suspend):
	// the provider and the login or email it had at its first sign-in,
	// like "github:octocat". It never changes, so a rename at the
	// provider can't shake off a suspension.
	Name     string    `json:"name"`
	Provider string    `json:"provider"`
	Subject  string    `json:"subject"` // the provider's stable user id
	Label    string    `json:"label"`   // login or email at the latest sign-in
	Created  time.Time `json:"created"`
	Tokens   []Token   `json:"tokens"`
}

// Token is one token an account was given.
type Token struct {
	Hash    string    `json:"hash"` // as in a tokens file: hex SHA-256
	Created time.Time `json:"created"`
}

// OpenAccounts loads the accounts file at path; a missing file is an
// empty set.
func OpenAccounts(path string) (*Accounts, error) {
	a := &Accounts{path: path, byKey: map[string]*Account{}, byName: map[string]*Account{}, byHash: map[string]*Account{}}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Account
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, acct := range list {
		a.index(acct)
	}
	return a, nil
}

func (a *Accounts) index(acct *Account) {
	a.byKey[acct.Provider+":"+acct.Subject] = acct
	a.byName[acct.Name] = acct
	for _, t := range acct.Tokens {
		a.byHash[t.Hash] = acct
	}
}

// SignIn mints a token for the person subject at provider, making
// their account on first sign-in. label is their login or email
// there. It returns the account's name and the new token.
func (a *Accounts) SignIn(provider, subject, label string) (name, token string, err error) {
	if provider == "" || subject == "" || !ValidName(label) {
		return "", "", fmt.Errorf("auth: can't make an account from %q/%q/%q", provider, subject, label)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	acct := a.byKey[provider+":"+subject]
	if acct == nil {
		acct = &Account{Name: a.freeName(provider + ":" + strings.ToLower(label)), Provider: provider, Subject: subject, Created: time.Now().UTC()}
	}
	acct.Label = label
	token = NewToken()
	acct.Tokens = append(acct.Tokens, Token{Hash: Hash(token), Created: time.Now().UTC()})
	var dropped []Token
	if n := len(acct.Tokens) - MaxTokens; n > 0 {
		dropped = slices.Clone(acct.Tokens[:n])
		acct.Tokens = acct.Tokens[n:]
	}
	for _, t := range dropped {
		delete(a.byHash, t.Hash)
	}
	a.index(acct)
	if err := a.save(); err != nil {
		return "", "", err
	}
	return acct.Name, token, nil
}

// freeName is name, or name~2, ~3... when another person already has
// it (a login given up at the provider and taken by someone else).
func (a *Accounts) freeName(name string) string {
	for i, try := 2, name; ; i++ {
		if a.byName[try] == nil {
			return try
		}
		try = fmt.Sprintf("%s~%d", name, i)
	}
}

// Authenticate maps a token to its account.
func (a *Accounts) Authenticate(token string) (hub.Account, error) {
	if token == "" {
		return hub.Account{}, hub.ErrNoToken
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	acct := a.byHash[Hash(token)]
	if acct == nil {
		return hub.Account{}, hub.ErrBadToken
	}
	return hub.Account{Name: acct.Name}, nil
}

// Revoke forgets one token (signing out). It reports whether the
// token was known.
func (a *Accounts) Revoke(token string) (bool, error) {
	h := Hash(token)
	a.mu.Lock()
	defer a.mu.Unlock()
	acct := a.byHash[h]
	if acct == nil {
		return false, nil
	}
	delete(a.byHash, h)
	acct.Tokens = slices.DeleteFunc(acct.Tokens, func(t Token) bool { return t.Hash == h })
	return true, a.save()
}

// save writes the file whole: to a temporary file first, synced,
// then renamed over the old one, so a crash leaves one or the other.
func (a *Accounts) save() error {
	list := make([]*Account, 0, len(a.byName))
	for _, acct := range a.byName {
		list = append(list, acct)
	}
	slices.SortFunc(list, func(x, y *Account) int { return strings.Compare(x.Name, y.Name) })
	body, err := json.MarshalIndent(list, "", "\t")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".accounts-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.path)
}
