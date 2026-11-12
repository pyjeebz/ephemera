package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/record"
)

// Identity is what a host keeps after joining: which server, and who it is
// there. The key it joined with stays in its own file.
type Identity struct {
	Server  string    `json:"server"`
	HostID  string    `json:"host_id"`
	Account string    `json:"account"`
	Name    string    `json:"name"`
	Joined  time.Time `json:"joined"`
}

// ErrNotJoined means the host has no identity file yet.
var ErrNotJoined = errors.New("this host hasn't joined a central server; run: ephemera-host join --server URL CODE")

// LoadIdentity reads a host's identity file.
func LoadIdentity(path string) (Identity, error) {
	var id Identity
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return id, ErrNotJoined
	}
	if err != nil {
		return id, err
	}
	if err := json.Unmarshal(b, &id); err != nil {
		return id, fmt.Errorf("%s: %w", path, err)
	}
	if id.Server == "" || id.HostID == "" {
		return id, fmt.Errorf("%s: no server or host id", path)
	}
	return id, nil
}

// SaveIdentity writes a host's identity file, readable only by its owner.
func SaveIdentity(path string, id Identity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(id, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Join joins the central server at serverURL with a one-time code, as a
// host named name with key's public half.
func Join(ctx context.Context, serverURL, code, name string, key ed25519.PrivateKey) (Identity, error) {
	serverURL = strings.TrimRight(serverURL, "/")
	body, _ := json.Marshal(api.JoinRequest{Code: strings.TrimSpace(code), Name: name, PublicKey: record.PublicKey(key)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/v1/hosts/join", bytes.NewReader(body))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var e api.Error
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return Identity{}, errors.New(e.Error)
	}
	var jr api.JoinResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&jr); err != nil {
		return Identity{}, err
	}
	return Identity{Server: serverURL, HostID: jr.HostID, Account: jr.Account, Name: name, Joined: time.Now().UTC()}, nil
}
