package abuse

import (
	"bufio"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
)

// Suspensions is the set of sender accounts an operator has
// suspended on this relay. It is the relay's own list, checked after
// the authenticator: it works the same with a static tokens file and
// with a hosted accounts service.
type Suspensions struct {
	mu       sync.RWMutex
	accounts map[string]bool
	path     string // one account per line; "" = memory only
}

// NewSuspensions opens the list. With a path, it loads from and
// rewrites that file; with "", the list lives in memory only.
func NewSuspensions(path string) (*Suspensions, error) {
	s := &Suspensions{accounts: make(map[string]bool), path: path}
	if path == "" {
		return s, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name := strings.TrimSpace(sc.Text()); name != "" && !strings.HasPrefix(name, "#") {
			s.accounts[name] = true
		}
	}
	return s, sc.Err()
}

// Suspended reports whether account is suspended.
func (s *Suspensions) Suspended(account string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accounts[account]
}

// Suspend adds account to the list. Persisted before it takes effect
// in memory, so a failed write never leaves a suspension that a
// restart would silently undo.
func (s *Suspensions) Suspend(account string) error {
	return s.set(account, true)
}

// Unsuspend removes account from the list.
func (s *Suspensions) Unsuspend(account string) error {
	return s.set(account, false)
}

// List returns the suspended accounts, sorted.
func (s *Suspensions) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.accounts))
	for a := range s.accounts {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (s *Suspensions) set(account string, on bool) error {
	if account == "" {
		return errors.New("abuse: empty account")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]bool, len(s.accounts)+1)
	for a := range s.accounts {
		next[a] = true
	}
	if on {
		next[account] = true
	} else {
		delete(next, account)
	}
	if s.path != "" {
		if err := writeList(s.path, next); err != nil {
			return err
		}
	}
	s.accounts = next
	return nil
}

// writeList rewrites the suspensions file atomically.
func writeList(path string, accounts map[string]bool) error {
	names := make([]string, 0, len(accounts))
	for a := range accounts {
		names = append(names, a)
	}
	sort.Strings(names)
	var body strings.Builder
	body.WriteString("# suspended sender accounts, one per line\n")
	for _, a := range names {
		body.WriteString(a + "\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
