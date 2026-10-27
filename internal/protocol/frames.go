package protocol

// Payload structs for the JSON frame types. Field names on the wire
// are stable once v0 ships; docs/protocol.md is the source of truth.

// Hello is the daemon's first frame after connecting. Token is the
// sender's credential; relays that require accounts refuse an empty
// or unknown one.
type Hello struct {
	Version string `json:"version"`
	Name    string `json:"name"` // sender's display name, optional
	Token   string `json:"token,omitempty"`
}

// Send announces one shared file. Size is advisory: the relay trusts
// the daemon for ranges but re-checks the 5GB abuse cap on every read.
// Cache=true opts the share into layer B: the relay may keep
// already-served bytes in volatile RAM for replay while the sender
// is offline. AwayPrefixBytes > 0 opts into layer C: the daemon
// will trickle that many prefix bytes to the relay after announcing,
// for the relay to serve while the sender is offline.
// Upload=true asks for a background upload: the daemon sends the
// whole file the same way, and the relay keeps it on disk until the
// link expires, so the link outlives the sender being online.
// OwnerKey proves the daemon owns ShareID (see owner.go); the relay
// refuses an announce whose key does not certify the id.
type Send struct {
	ShareID         string `json:"share_id"`
	OwnerKey        string `json:"owner_key"`
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	MIME            string `json:"mime"`
	TTLSec          int64  `json:"ttl_sec"`
	Cache           bool   `json:"cache,omitempty"`
	AwayPrefixBytes int64  `json:"away_prefix_bytes,omitempty"`
	Upload          bool   `json:"upload,omitempty"`
}

// Away carries uploaded bytes, layer C's prefix or a background
// upload's whole file: the offset is where these bytes belong in the
// file. Chunks arrive in order, one frame each, in the background at
// the daemon's own pace.
type Away struct {
	ShareID string `json:"share_id"`
	Offset  int64  `json:"offset"`
}

// Ack tells the daemon how many bytes of the share the relay holds,
// all from offset 0. Acks are cumulative: a later one covers every
// burst before it.
type Ack struct {
	ShareID string `json:"share_id"`
	Have    int64  `json:"have"`
}

// Welcome tells the daemon its announce was accepted and gives the
// link receivers open: the relay knows its public address, the
// daemon only knows the one it dialed. Link is empty when the relay
// doesn't know one. Upload says whether the relay took the background
// upload the announce asked for; Have is how many bytes of the share
// (upload or away prefix) it already holds, where the daemon resumes.
type Welcome struct {
	ShareID string `json:"share_id"`
	Link    string `json:"link,omitempty"`
	Upload  bool   `json:"upload,omitempty"`
	Have    int64  `json:"have,omitempty"`
}

// Read asks the daemon for one byte range of one share. Offset+Length
// must satisfy Offset+Length <= Size announced in Send.
type Read struct {
	ShareID string `json:"share_id"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
}

// Err is the reason a stream or request died.
type Err struct {
	ShareID string `json:"share_id"`
	Code    string `json:"code"` // "gone", "unsatisfiable", "denied", "io"
	Message string `json:"message"`
}
