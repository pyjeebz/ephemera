// Package api holds the types shared by the CLI, the central server and the
// host agent.
package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// Network modes for a sandbox.
const (
	NetworkNone  = "none"  // no outbound traffic at all (the default)
	NetworkAllow = "allow" // only the hosts in Spec.Allow
	NetworkOpen  = "open"  // any public host
)

// Spec describes the sandbox to create.
type Spec struct {
	Image    string  `json:"image,omitempty"`
	CPUs     float64 `json:"cpus,omitempty"`
	MemoryMB int     `json:"memory_mb,omitempty"`
	Network  string  `json:"network,omitempty"`
	// Allow lists the hosts a sandbox in "allow" mode may reach:
	// "example.com", "*.example.com", optionally with ":port".
	Allow []string `json:"allow,omitempty"`
}

// Validate checks a spec and fills in defaults that do not depend on the host.
func (s *Spec) Validate() error {
	switch s.Network {
	case "":
		s.Network = NetworkNone
		if len(s.Allow) > 0 {
			s.Network = NetworkAllow
		}
	case NetworkNone, NetworkAllow, NetworkOpen:
	default:
		return fmt.Errorf("network must be %q, %q or %q", NetworkNone, NetworkAllow, NetworkOpen)
	}
	if s.Network == NetworkAllow && len(s.Allow) == 0 {
		return fmt.Errorf("network %q needs at least one allowed host", NetworkAllow)
	}
	if s.Network != NetworkAllow && len(s.Allow) > 0 {
		return fmt.Errorf("allowed hosts only apply to network %q", NetworkAllow)
	}
	if len(s.Allow) > 200 {
		return fmt.Errorf("at most 200 allowed hosts")
	}
	if s.CPUs < 0 || s.CPUs > 64 {
		return fmt.Errorf("cpus must be between 0 and 64")
	}
	if s.MemoryMB < 0 || s.MemoryMB > 256*1024 {
		return fmt.Errorf("memory must be between 0 and 262144 MB")
	}
	return nil
}

// Connection is one outbound connection attempt from a sandbox.
type Connection struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"` // "https" (CONNECT) or "http"
	Host       string    `json:"host"`
	Port       int       `json:"port"`
	Allowed    bool      `json:"allowed"`
	Rule       string    `json:"rule,omitempty"` // the allow-list entry that matched
	IP         string    `json:"ip,omitempty"`
	BytesOut   int64     `json:"bytes_out,omitempty"`
	BytesIn    int64     `json:"bytes_in,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// CreateSandboxRequest is sent by the CLI to the central server.
type CreateSandboxRequest struct {
	Spec Spec `json:"spec"`
}

// Sandbox is the central server's view of a sandbox. Token is set only in the
// response that creates the sandbox: the CLI uses it to talk to the host
// directly, so files and output never pass through the central server.
type Sandbox struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`
	HostURL   string    `json:"host_url"`
	Token     string    `json:"token,omitempty"`
	Spec      Spec      `json:"spec"`
	CreatedAt time.Time `json:"created_at"`
}

// HostCreateRequest is sent by the central server to a host.
type HostCreateRequest struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Spec  Spec   `json:"spec"`
}

// ExecRequest runs one command in a sandbox, in /workspace.
type ExecRequest struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env,omitempty"`
}

// DeleteFilesRequest removes files from a sandbox's workspace, for files
// deleted on the laptop during ephemera dev.
type DeleteFilesRequest struct {
	Paths []string `json:"paths"`
}

// PortUpgrade is the HTTP Upgrade protocol name for a raw TCP connection to
// a port inside a sandbox.
const PortUpgrade = "ephemera-tcp"

// RegisterHostRequest is a host's registration and heartbeat. Sandboxes
// lists the sandboxes the host is running, so the central server can
// reconcile its records with reality.
type RegisterHostRequest struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Capacity  int      `json:"capacity"`
	Sandboxes []string `json:"sandboxes"`
}

// RegisterHostResponse tells a host which of its sandboxes the central
// server does not know about. The host destroys them: nothing may run that
// no one can see or stop.
type RegisterHostResponse struct {
	Destroy []string `json:"destroy,omitempty"`
}

// WhoAmI describes the caller's token.
type WhoAmI struct {
	Name string `json:"name"`
}

// Error is the body of every non-2xx JSON response.
type Error struct {
	Error string `json:"error"`
}

// NewID returns a random identifier such as "sbx_1f2e3d4c5b6a7980".
func NewID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// NewToken returns a random 256-bit bearer token.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
