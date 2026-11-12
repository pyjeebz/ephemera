// Package api holds the types shared by the CLI, the central server and the
// host agent.
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	// MaxLifetimeSeconds destroys the sandbox this long after it was
	// created, whatever it is doing. 0 means the host's limit.
	MaxLifetimeSeconds int `json:"max_lifetime_seconds,omitempty"`
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
	if s.MaxLifetimeSeconds < 0 {
		return fmt.Errorf("max lifetime cannot be negative")
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
	// TokenHash is the hex SHA-256 of the sandbox's access token, which
	// the CLI makes and keeps. The central server passes the hash to the
	// host and never sees the token, so it can't open the sandbox.
	TokenHash string `json:"token_hash"`
}

// HashSandboxToken is how a sandbox's access token is passed on and
// checked: the hex SHA-256 of it.
func HashSandboxToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Sandbox is the central server's view of a sandbox. Token is the
// sandbox's access token: the CLI makes it, keeps it, and uses it to talk
// to the host directly, so files and output never pass through the central
// server. The central server only ever sees its hash, and never sends it.
type Sandbox struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`    // its name
	HostID    string    `json:"host_id"` // unique; names are only unique within an account
	HostURL   string    `json:"host_url"`
	Token     string    `json:"token,omitempty"`
	Spec      Spec      `json:"spec"`
	CreatedAt time.Time `json:"created_at"`
	Owner     string    `json:"owner,omitempty"`   // the token's name
	Account   string    `json:"account,omitempty"` // the token's account
}

// Usage is what one token has used today (UTC) and its limits; a zero
// limit means none.
type Usage struct {
	Name             string  `json:"name"`
	Day              string  `json:"day"`
	MinutesToday     float64 `json:"minutes_today"`
	MaxMinutesPerDay int     `json:"max_minutes_per_day,omitempty"`
	Running          int     `json:"running"`
	MaxSandboxes     int     `json:"max_sandboxes,omitempty"`
}

// HostCreateRequest is sent by the central server to a host.
type HostCreateRequest struct {
	ID        string `json:"id"`
	TokenHash string `json:"token_hash"` // see CreateSandboxRequest
	Spec      Spec   `json:"spec"`
}

// ExecRequest runs one command in a sandbox, in /workspace.
type ExecRequest struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env,omitempty"`
	// TimeoutSeconds stops the command after this long, with exit code
	// 124 like timeout(1). 0 means no limit.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// ExitTimedOut is the exit code of a command stopped by its timeout.
const ExitTimedOut = 124

// Record is what happened in a sandbox, written and signed by its host
// when the sandbox is destroyed. It holds no command output, environment
// values or file contents.
type Record struct {
	Version   int       `json:"version"` // 1
	Sandbox   string    `json:"sandbox"`
	Host      string    `json:"host"`
	Backend   string    `json:"backend"`
	Spec      Spec      `json:"spec"`
	Created   time.Time `json:"created"`
	Destroyed time.Time `json:"destroyed"`
	// Why the sandbox ended: "destroyed" (asked to) or "expired" (it
	// reached its maximum lifetime).
	Ended       string       `json:"ended"`
	Execs       []ExecRecord `json:"execs"`
	Syncs       []SyncRecord `json:"syncs"`
	Connections []Connection `json:"connections"`
	// DiskDeleted is the host's statement that everything the sandbox
	// stored was deleted with it.
	DiskDeleted bool `json:"disk_deleted"`
	// SnapshotLoaded and SnapshotSaved name the project snapshot the
	// sandbox started from, or saved after its setup.
	SnapshotLoaded string `json:"snapshot_loaded,omitempty"`
	SnapshotSaved  string `json:"snapshot_saved,omitempty"`
	// HostKey is the host's Ed25519 public key, base64.
	HostKey string `json:"host_key"`
}

// ExecRecord is one command a sandbox ran. Its environment is not kept.
type ExecRecord struct {
	Argv       []string  `json:"argv"`
	Started    time.Time `json:"started"`
	DurationMS int64     `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	Error      string    `json:"error,omitempty"`
}

// SyncRecord is one upload or deletion of files.
type SyncRecord struct {
	Time    time.Time `json:"time"`
	Files   int       `json:"files,omitempty"`
	Bytes   int64     `json:"bytes,omitempty"`
	Deleted int       `json:"deleted,omitempty"`
}

// SignedRecord is a record with its host's signature. The signature covers
// the record's bytes exactly as they appear here.
type SignedRecord struct {
	Record    json.RawMessage `json:"record"`
	Signature string          `json:"signature"` // Ed25519, base64
}

// ProjectSnapshotRequest asks a host to load or save a project snapshot.
// It goes from the CLI straight to the host: the key never passes through
// the central server, and the host keeps it only for the request.
type ProjectSnapshotRequest struct {
	ID  string `json:"id"`  // hex, from the project and its setup inputs
	Key []byte `json:"key"` // 32 bytes; the snapshot is stored encrypted with it
}

// ProjectSnapshotResult says whether a snapshot was loaded, and if not why.
type ProjectSnapshotResult struct {
	Loaded bool   `json:"loaded"`
	Reason string `json:"reason,omitempty"`
}

// Host is a host as the central server knows it.
type Host struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"`
	LastSeen  time.Time `json:"last_seen"`
	// URL is where the CLI reaches the host for files and commands.
	URL string `json:"url"`
	// Online is true while the host checks in often enough to get sandboxes.
	Online bool `json:"online"`
}

// DeleteFilesRequest removes files from a sandbox's workspace, for files
// deleted on the laptop during ephemera dev.
type DeleteFilesRequest struct {
	Paths []string `json:"paths"`
}

// PortUpgrade is the HTTP Upgrade protocol name for a raw TCP connection to
// a port inside a sandbox.
const PortUpgrade = "ephemera-tcp"

// HostUpgrade is the Upgrade token of a host's connection to the central
// server. The host dials out and keeps the connection open; the server
// sends it work down that connection, so the server never needs to reach
// the host.
const HostUpgrade = "ephemera-host/1"

// HostProtocol is the version of the messages on that connection.
const HostProtocol = 1

// HostMessage is one message on a host's connection, sent as one line of
// JSON either way. Type says which fields are set:
//
//	challenge  server to host, first: Nonce, which the host signs
//	hello      host to server, the reply: Hello and Sandboxes
//	welcome    server to host, the reply: Destroy
//	refused    server to host, instead of welcome: Error. Removed is set
//	           when the server doesn't know the host, and the host then
//	           destroys its sandboxes, since no one can see or stop them
//	heartbeat  host to server, every 15 s: Req and Sandboxes
//	ack        server to host, the reply: Req and Destroy
//	create     server to host: Req and Create
//	destroy    server to host: Sandbox, and Req when it wants a result
//	result     host to server, the reply to create or destroy: Req, OK,
//	           Error, and Record for a destroy
//
// Destroy lists sandboxes the host runs that the server has no record of.
// The host destroys them: nothing may run that no one can see or stop.
// Either side ignores types it doesn't know.
type HostMessage struct {
	Type      string             `json:"type"`
	Req       string             `json:"req,omitempty"`
	Nonce     string             `json:"nonce,omitempty"`
	Hello     *HostHello         `json:"hello,omitempty"`
	Sandboxes []string           `json:"sandboxes,omitempty"`
	Destroy   []string           `json:"destroy,omitempty"`
	Create    *HostCreateRequest `json:"create,omitempty"`
	Sandbox   string             `json:"sandbox,omitempty"`
	OK        bool               `json:"ok,omitempty"`
	Removed   bool               `json:"removed,omitempty"`
	Error     string             `json:"error,omitempty"`
	Record    *SignedRecord      `json:"record,omitempty"`
}

// HostHello introduces a host when it connects. Signature is the host
// key's Ed25519 signature over HostSignedText, base64: it proves the host
// holds the key it joined with.
type HostHello struct {
	HostID    string `json:"host_id"`
	Signature string `json:"signature"`
	Name      string `json:"name,omitempty"` // from the join; this is ignored
	// URL is where the CLI reaches the host for files and commands.
	URL      string `json:"url"`
	Capacity int    `json:"capacity"`
	// PublicKey is the host's Ed25519 key for signing records, base64.
	PublicKey string `json:"public_key,omitempty"`
	Version   string `json:"version,omitempty"`
	Protocol  int    `json:"protocol"`
}

// WhoAmI describes the caller's token.
type WhoAmI struct {
	Name    string `json:"name"`
	Account string `json:"account,omitempty"`
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

// HostSignedText is what a host signs to connect: the protocol, the
// server's host name as the host dialed it, and the server's fresh
// challenge. Naming the server keeps a signature made for one server from
// working on another.
func HostSignedText(serverHost, nonce string) []byte {
	return []byte("ephemera-host-connect-v1\n" + serverHost + "\n" + nonce)
}

// JoinRequest is a new host joining an account with a one-time code.
type JoinRequest struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"` // Ed25519, base64
}

// JoinResponse says which host the server made, in which account.
type JoinResponse struct {
	HostID  string `json:"host_id"`
	Account string `json:"account"`
}

// JoinCode is a one-time code for a new host.
type JoinCode struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// Account is a person signed in on the web.
type Account struct {
	ID        string `json:"id"` // "gh-" and their GitHub user id
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// Token is an API token as the dashboard shows it. The secret is shown
// once, when it is made, and never again.
type Token struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Created          time.Time `json:"created"`
	MaxSandboxes     int       `json:"max_sandboxes,omitempty"`
	MaxMinutesPerDay int       `json:"max_minutes_per_day,omitempty"`
}

// CreateTokenRequest asks for a new API token in the caller's account.
type CreateTokenRequest struct {
	Name             string `json:"name"`
	MaxSandboxes     int    `json:"max_sandboxes,omitempty"`
	MaxMinutesPerDay int    `json:"max_minutes_per_day,omitempty"`
}

// CreatedToken is a new token with its secret.
type CreatedToken struct {
	Token
	Secret string `json:"secret"`
}
