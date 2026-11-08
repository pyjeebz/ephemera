package control

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tokens checks API tokens.
type Tokens interface {
	// Check returns the token's name, or false if it is not valid.
	Check(token string) (name string, ok bool)
}

// StaticToken accepts a single token, for tests.
type StaticToken struct {
	Name, Token string
}

func (s StaticToken) Check(token string) (string, bool) {
	if s.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) == 1 {
		return s.Name, true
	}
	return "", false
}

// TokenRecord is a stored token. Only a hash of the secret is kept.
type TokenRecord struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Hash    string    `json:"sha256"`
	Created time.Time `json:"created"`
}

// TokenFile keeps tokens in a JSON file. The running server only reads
// it, reloading when it changes; "ephemera-server token" commands write it.
type TokenFile struct {
	Path string

	mu      sync.Mutex
	modTime time.Time
	size    int64
	byHash  map[string]TokenRecord
}

const tokenPrefix = "eph_"

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Check implements Tokens.
func (f *TokenFile) Check(token string) (string, bool) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reloadLocked(); err != nil {
		return "", false
	}
	// The lookup is by hash, so the secret itself is never compared
	// byte by byte.
	rec, ok := f.byHash[hashToken(token)]
	return rec.Name, ok
}

func (f *TokenFile) reloadLocked() error {
	info, err := os.Stat(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		f.byHash, f.modTime, f.size = nil, time.Time{}, 0
		return nil
	}
	if err != nil {
		return err
	}
	if f.byHash != nil && info.ModTime().Equal(f.modTime) && info.Size() == f.size {
		return nil
	}
	recs, err := readTokens(f.Path)
	if err != nil {
		return err
	}
	f.byHash = make(map[string]TokenRecord, len(recs))
	for _, r := range recs {
		f.byHash[r.Hash] = r
	}
	f.modTime, f.size = info.ModTime(), info.Size()
	return nil
}

func readTokens(path string) ([]TokenRecord, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []TokenRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return recs, nil
}

// update runs fn on the token list under an exclusive file lock and saves
// the result.
func (f *TokenFile) update(fn func([]TokenRecord) ([]TokenRecord, error)) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(f.Path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	recs, err := readTokens(f.Path)
	if err != nil {
		return err
	}
	recs, err = fn(recs)
	if err != nil {
		return err
	}
	return writeJSONAtomic(f.Path, recs, 0o600)
}

// Create adds a token and returns its secret, which is shown only once.
func (f *TokenFile) Create(name string) (TokenRecord, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TokenRecord{}, "", errors.New("a token needs a name")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return TokenRecord{}, "", err
	}
	secret := tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	idb := make([]byte, 4)
	rand.Read(idb)
	rec := TokenRecord{ID: "tok_" + hex.EncodeToString(idb), Name: name, Hash: hashToken(secret), Created: time.Now().UTC()}
	err := f.update(func(recs []TokenRecord) ([]TokenRecord, error) {
		return append(recs, rec), nil
	})
	return rec, secret, err
}

// List returns the stored tokens, oldest first.
func (f *TokenFile) List() ([]TokenRecord, error) {
	recs, err := readTokens(f.Path)
	sort.Slice(recs, func(i, j int) bool { return recs[i].Created.Before(recs[j].Created) })
	return recs, err
}

// Revoke deletes a token by id or name.
func (f *TokenFile) Revoke(idOrName string) error {
	return f.update(func(recs []TokenRecord) ([]TokenRecord, error) {
		out := recs[:0]
		for _, r := range recs {
			if r.ID != idOrName && r.Name != idOrName {
				out = append(out, r)
			}
		}
		if len(out) == len(recs) {
			return nil, fmt.Errorf("no token %q", idOrName)
		}
		return out, nil
	})
}

// writeJSONAtomic writes v as JSON to path through a temporary file and a
// rename, so readers never see a partial file.
func writeJSONAtomic(path string, v any, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
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
	return os.Rename(tmp.Name(), path)
}
