// Package config stores the CLI's login: which central server to use and
// the token for it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultServer is used when no server is configured.
const DefaultServer = "http://127.0.0.1:7700"

// Config is the saved login.
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// Path returns the config file location: $EPHEMERA_CONFIG, or
// ephemera/config.json in the user's config directory.
func Path() (string, error) {
	if p := os.Getenv("EPHEMERA_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ephemera", "config.json"), nil
}

// Load reads the saved login. A missing file is an empty config.
func Load() (Config, error) {
	var c Config
	p, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

// Resolve returns the server and token to use: environment variables
// first, then the saved login, then the default server.
func Resolve() (server, token string, err error) {
	c, err := Load()
	server, token = c.Server, c.Token
	if v := os.Getenv("EPHEMERA_SERVER"); v != "" {
		server = v
	}
	if v := os.Getenv("EPHEMERA_TOKEN"); v != "" {
		token = v
	}
	if server == "" {
		server = DefaultServer
	}
	return server, token, err
}

// Save writes the login, readable only by the user.
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Remove deletes the saved login. It is not an error if there is none.
func Remove() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
