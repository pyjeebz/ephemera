// Package auth holds the relay's built-in sender authenticator: a
// static tokens file for self-hosted relays.
//
// The file holds one sender per line, a name and the SHA-256 of
// that sender's token, so a leaked file does not leak tokens:
//
//	# name  sha256(token)
//	alice   3f1a…
//
// `relay token <name>` mints a token and prints its line.
package auth

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"ephemera.run/ephemera/internal/hub"
)

// TokenPrefix marks sender tokens so secret scanners can find them.
const TokenPrefix = "eph_"

// NewToken returns a fresh sender token: the prefix plus 256 random
// bits, base64url.
func NewToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// Hash is the tokens-file form of a token: hex SHA-256.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ValidName reports whether name can go in a tokens file: non-empty,
// no whitespace, no comment marker.
func ValidName(name string) bool {
	return name != "" && !strings.ContainsAny(name, " \t\r\n#")
}

// Static authenticates senders against a fixed set of token hashes.
type Static struct {
	names map[string]string // token hash -> account name
}

// LoadStatic reads a tokens file.
func LoadStatic(path string) (*Static, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := ParseStatic(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// ParseStatic reads tokens-file lines. Blank lines and lines starting
// with # are skipped. A file with no senders is an error: it would
// refuse everyone, which is never what an operator meant.
func ParseStatic(r io.Reader) (*Static, error) {
	s := &Static{names: make(map[string]string)}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !ValidName(fields[0]) || !isHash(fields[1]) {
			return nil, fmt.Errorf("line %d: want `<name> <sha256 hex>`", n)
		}
		hash := strings.ToLower(fields[1])
		if prev, dup := s.names[hash]; dup {
			return nil, fmt.Errorf("line %d: token already listed for %s", n, prev)
		}
		s.names[hash] = fields[0]
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(s.names) == 0 {
		return nil, errors.New("no senders listed")
	}
	return s, nil
}

// Authenticate maps a token to its account name. Static accounts
// carry no limits of their own: the relay's apply.
func (s *Static) Authenticate(token string) (hub.Account, error) {
	if token == "" {
		return hub.Account{}, hub.ErrNoToken
	}
	name, ok := s.names[Hash(token)]
	if !ok {
		return hub.Account{}, hub.ErrBadToken
	}
	return hub.Account{Name: name}, nil
}

func isHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}

// Chain tries each authenticator in turn and takes the first that
// accepts the token. An empty chain accepts no token, which with a
// guest policy means every sender is a guest.
type Chain []hub.Authenticator

// Authenticate returns the first account that takes the token. A
// transient error from any authenticator is returned as is, so the
// sender redials instead of being refused.
func (c Chain) Authenticate(token string) (hub.Account, error) {
	if token == "" {
		return hub.Account{}, hub.ErrNoToken
	}
	for _, a := range c {
		acct, err := a.Authenticate(token)
		if errors.Is(err, hub.ErrBadToken) {
			continue
		}
		return acct, err
	}
	return hub.Account{}, hub.ErrBadToken
}
