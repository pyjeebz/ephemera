package protocol

// Payload structs for the JSON frame types. Field names on the wire
// are stable once v0 ships; docs/protocol.md is the source of truth.

// Hello is the daemon's first frame after connecting.
type Hello struct {
	Version string `json:"version"`
	Name    string `json:"name"` // sender's display name, optional
}

// Send announces one shared file. Size is advisory: the relay trusts
// the daemon for ranges but re-checks the 5GB abuse cap on every read.
type Send struct {
	ShareID string `json:"share_id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	MIME    string `json:"mime"`
	TTLSec  int64  `json:"ttl_sec"`
}

// Read asks the daemon for one byte range of one share. Offset+Length
// must satisfy Offset+Length <= Size announced in Send.
type Read struct {
	ShareID string `json:"share_id"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
}

// StreamID identifies a receiver's stream within a share. Empty means
// the read is a one-shot probe (HEAD/status), not an open stream.
type StreamID struct {
	ShareID string `json:"share_id"`
	Stream  string `json:"stream"`
}

// Err is the reason a stream or request died.
type Err struct {
	ShareID string `json:"share_id"`
	Code    string `json:"code"` // "gone", "unsatisfiable", "denied", "io"
	Message string `json:"message"`
}
