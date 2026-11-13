package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Pin is a host key the CLI has seen, and trusts from then on.
type Pin struct {
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	FirstSeen   time.Time `json:"first_seen"`
}

// Pins are the host keys the CLI has pinned, by host id, kept in a file
// readable only by its owner: the CLI's known_hosts.
type Pins struct {
	Path string

	mu sync.Mutex
	m  map[string]Pin
}

// LoadPins reads the pin file. A missing file is no pins yet.
func LoadPins(path string) (*Pins, error) {
	p := &Pins{Path: path, m: map[string]Pin{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	var f struct {
		Hosts map[string]Pin `json:"hosts"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	if f.Hosts != nil {
		p.m = f.Hosts
	}
	return p, nil
}

// Get returns the pin for a host id.
func (p *Pins) Get(id string) (Pin, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.m[id]
	return pin, ok
}

// Set pins a host's key and saves the file.
func (p *Pins) Set(id string, pin Pin) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[id] = pin
	b, _ := json.MarshalIndent(struct {
		Hosts map[string]Pin `json:"hosts"`
	}{p.m}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o700); err != nil {
		return err
	}
	tmp := p.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.Path)
}

// IDs returns the pinned host ids, sorted.
func (p *Pins) IDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.m))
	for id := range p.m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// HostKeyChangedError means a host presented, or the server reported, a
// key other than the one pinned for it. Either the host was rebuilt and
// joined again, or something is between the CLI and the host.
type HostKeyChangedError struct {
	Name, Pinned, Now string
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("host %s's key changed: pinned %s, now %s. If you rebuilt or re-joined it, accept the new key with: ephemera trust %s. If not, don't: something may be between you and the host",
		e.Name, e.Pinned, e.Now, e.Name)
}

// ErrUnknownHost is returned in strict mode for a host with no pin.
var ErrUnknownHost = errors.New("unknown host")
