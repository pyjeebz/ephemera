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
// Cache=true opts the share into layer B: the relay may keep
// already-served bytes in volatile RAM for replay while the sender
// is offline. AwayPrefixBytes > 0 opts into layer C: the daemon
// will trickle that many prefix bytes to the relay after announcing,
// for the relay to serve while the sender is offline.
type Send struct {
	ShareID         string `json:"share_id"`
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	MIME            string `json:"mime"`
	TTLSec          int64  `json:"ttl_sec"`
	Cache           bool   `json:"cache,omitempty"`
	AwayPrefixBytes int64  `json:"away_prefix_bytes,omitempty"`
}

// Away carries layer-C prefix bytes: the offset is where these bytes
// belong in the file. Chunks arrive in order, one frame each, in
// the background at the daemon's own pace.
type Away struct {
	ShareID string `json:"share_id"`
	Offset  int64  `json:"offset"`
}

// Ack tells the daemon how many prefix bytes the relay durably
// holds, so a reconnecting daemon resumes the trickle from there.
type Ack struct {
	ShareID string `json:"share_id"`
	Have    int64  `json:"have"`
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
