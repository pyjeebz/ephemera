package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/record"
)

// HostRecord is a host that joined an account.
type HostRecord struct {
	ID        string    `json:"id"`
	Account   string    `json:"account"`
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"` // Ed25519, base64
	Joined    time.Time `json:"joined"`
}

// HostStore keeps the hosts that joined, and the one-time codes they join
// with. FileHosts keeps them in a JSON file; pgstore.Store in Postgres.
type HostStore interface {
	// CreateJoinCode makes a code a new host can join the account with,
	// once, within ttl. Only a hash of it is kept.
	CreateJoinCode(ctx context.Context, account string, ttl time.Duration) (string, error)
	// JoinHost uses up a code and records the host in the code's account.
	// It returns ErrBadCode for a code that is unknown, used or expired,
	// and ErrNameTaken if the account already has a host of that name.
	JoinHost(ctx context.Context, code string, h HostRecord) (HostRecord, error)
	// Host returns a host by id, or ErrNotFound.
	Host(ctx context.Context, id string) (HostRecord, error)
	// AccountHosts returns an account's hosts, by name.
	AccountHosts(ctx context.Context, account string) ([]HostRecord, error)
	// RemoveHost removes one of an account's hosts, or returns ErrNotFound.
	RemoveHost(ctx context.Context, account, id string) error
}

var (
	// ErrBadCode means a join code is unknown, used or expired.
	ErrBadCode = errors.New("this join code is unknown, used or expired; make a new one")
	// ErrNameTaken means the account already has a host with that name.
	ErrNameTaken = errors.New("the account already has a host with this name; pick another with --name")
)

// JoinCodeTTL is how long a join code works.
const JoinCodeTTL = 15 * time.Minute

// JoinCodePrefix starts every join code.
const JoinCodePrefix = "ej_"

var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewJoinCode returns a new join code: the prefix and 128 random bits.
func NewJoinCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return JoinCodePrefix + lowerBase32.EncodeToString(b), nil
}

// NewHostID returns a new host id.
func NewHostID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "host_" + hex.EncodeToString(b)
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// CheckHost validates a joining host's name and key.
func CheckHost(h HostRecord) error {
	if !hostName.MatchString(h.Name) {
		return errors.New("a host name is letters, digits, dots, dashes and underscores, at most 63")
	}
	if _, err := record.Fingerprint(h.PublicKey); err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	return nil
}

// CreateJoinCode makes a one-time code for a new host to join account with.
func (s *Server) CreateJoinCode(ctx context.Context, account string) (string, error) {
	return s.hostRecords().CreateJoinCode(ctx, account, JoinCodeTTL)
}

// FileHosts keeps hosts and join codes in a JSON file, or only in memory
// when Path is empty.
type FileHosts struct {
	Path string

	mu sync.Mutex
	st fileHosts
}

type fileHosts struct {
	Hosts []HostRecord `json:"hosts"`
	Codes []joinCode   `json:"join_codes,omitempty"`
}

type joinCode struct {
	Hash    string    `json:"sha256"`
	Account string    `json:"account"`
	Expires time.Time `json:"expires"`
}

func (f *FileHosts) change(fn func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Path == "" {
		return fn()
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	// A file lock, as "ephemera-server join-code create" writes the same
	// file as the running server.
	unlock, err := lockFile(f.Path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if err := f.read(); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	now := time.Now()
	codes := f.st.Codes[:0]
	for _, c := range f.st.Codes {
		if c.Expires.After(now) {
			codes = append(codes, c)
		}
	}
	f.st.Codes = codes
	if err := writeJSONAtomic(f.Path, f.st, 0o600); err != nil {
		return err
	}
	for _, p := range []string{f.Path, f.Path + ".lock"} {
		if err := matchDirOwner(p); err != nil {
			return err
		}
	}
	return nil
}

// read loads the file; with a file, every read sees the latest.
func (f *FileHosts) read() error {
	if f.Path == "" {
		return nil
	}
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		f.st = fileHosts{}
		return nil
	}
	if err != nil {
		return err
	}
	var st fileHosts
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("%s: %w", f.Path, err)
	}
	f.st = st
	return nil
}

func (f *FileHosts) view(fn func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.read(); err != nil {
		return err
	}
	fn()
	return nil
}

// CreateJoinCode implements HostStore.
func (f *FileHosts) CreateJoinCode(_ context.Context, account string, ttl time.Duration) (string, error) {
	account = accountOr(account)
	if !ValidAccount(account) {
		return "", fmt.Errorf("account %q: use lowercase letters, digits and dashes, at most 39", account)
	}
	code, err := NewJoinCode()
	if err != nil {
		return "", err
	}
	return code, f.change(func() error {
		f.st.Codes = append(f.st.Codes, joinCode{Hash: HashToken(code), Account: account, Expires: time.Now().Add(ttl).UTC()})
		return nil
	})
}

// JoinHost implements HostStore.
func (f *FileHosts) JoinHost(_ context.Context, code string, h HostRecord) (HostRecord, error) {
	if err := CheckHost(h); err != nil {
		return HostRecord{}, err
	}
	hash := HashToken(code)
	err := f.change(func() error {
		now := time.Now()
		found := -1
		for i, c := range f.st.Codes {
			if subtle.ConstantTimeCompare([]byte(c.Hash), []byte(hash)) == 1 && c.Expires.After(now) {
				found = i
			}
		}
		if found < 0 {
			return ErrBadCode
		}
		h.Account = f.st.Codes[found].Account
		for _, o := range f.st.Hosts {
			if o.Account == h.Account && strings.EqualFold(o.Name, h.Name) {
				return ErrNameTaken
			}
		}
		f.st.Codes = append(f.st.Codes[:found], f.st.Codes[found+1:]...)
		h.ID, h.Joined = NewHostID(), now.UTC().Truncate(time.Second)
		f.st.Hosts = append(f.st.Hosts, h)
		return nil
	})
	return h, err
}

// Host implements HostStore.
func (f *FileHosts) Host(_ context.Context, id string) (HostRecord, error) {
	var out HostRecord
	found := false
	err := f.view(func() {
		for _, h := range f.st.Hosts {
			if h.ID == id {
				out, found = h, true
			}
		}
	})
	if err == nil && !found {
		err = ErrNotFound
	}
	return out, err
}

// AccountHosts implements HostStore.
func (f *FileHosts) AccountHosts(_ context.Context, account string) ([]HostRecord, error) {
	var out []HostRecord
	err := f.view(func() {
		for _, h := range f.st.Hosts {
			if h.Account == account {
				out = append(out, h)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// RemoveHost implements HostStore.
func (f *FileHosts) RemoveHost(_ context.Context, account, id string) error {
	return f.change(func() error {
		for i, h := range f.st.Hosts {
			if h.ID == id && h.Account == account {
				f.st.Hosts = append(f.st.Hosts[:i], f.st.Hosts[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}
