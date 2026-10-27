package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"ephemera.run/ephemera/internal/protocol"
)

// Abuse floor, mirrored from the PRD: the relay re-checks these on
// every read, but the daemon refuses up front so honest mistakes
// never reach the network.
const (
	MaxShareSize = int64(5 << 30) // 5GB
	MinShareSize = int64(1)
)

// ErrTooLarge, ErrEmpty and ErrBlockedExt are the up-front rejection
// reasons for a share attempt.
var (
	ErrTooLarge   = errors.New("file exceeds the 5GB share limit")
	ErrEmpty      = errors.New("empty files cannot be shared")
	ErrBlockedExt = errors.New("file type is blocked")
)

// blockedExts are never shareable, regardless of size: executables
// and installers are the phishing vector the PRD calls out.
var blockedExts = map[string]bool{
	".exe": true, ".msi": true, ".bat": true, ".cmd": true, ".com": true,
	".scr": true, ".app": true, ".dmg": true, ".pkg": true, ".deb": true,
	".rpm": true, ".jar": true, ".apk": true, ".sh": true, ".ps1": true,
	".vbs": true, ".lnk": true,
}

// Share is one announced file: everything the daemon needs to serve
// ranged reads without re-asking the caller.
type Share struct {
	ID string

	// OwnerKey is the secret the ID is derived from. It goes to the
	// relay on every announce to prove ownership, and to the state
	// file for resume; it never appears in the link.
	OwnerKey string

	Path string
	Name string
	Size int64
	MIME string

	// Cache: the sender opts this share into layer B — the relay
	// may replay already-served bytes from volatile RAM while the
	// sender is offline.
	Cache bool

	// AwayPrefixBytes: layer C opt-in. The daemon trickles this
	// many prefix bytes to the relay post-announce; the relay
	// serves them while the sender is offline. 0 = off.
	AwayPrefixBytes int64

	// Upload: send the whole file to the relay in the background.
	// Once it's all there the link no longer needs this machine.
	Upload bool
}

// ValidateShare checks path against the abuse floor and returns the
// filled Share. The file must exist and be a regular file.
func ValidateShare(path string, mimeFn func(string, []byte) string) (Share, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Share{}, err
	}
	if !st.Mode().IsRegular() {
		return Share{}, os.ErrInvalid
	}
	if st.Size() > MaxShareSize {
		return Share{}, ErrTooLarge
	}
	if st.Size() < MinShareSize {
		return Share{}, ErrEmpty
	}
	ext := strings.ToLower(filepath.Ext(path))
	if blockedExts[ext] {
		return Share{}, ErrBlockedExt
	}

	head := make([]byte, 512)
	f, err := os.Open(path)
	if err != nil {
		return Share{}, err
	}
	n, _ := f.Read(head)
	f.Close()

	// the id is a 22-character base64url hash of the owner key:
	// ~128 bits, unguessable, no '+' or '/' to escape
	key, id := protocol.NewOwnerKey()
	return Share{
		ID:       id,
		OwnerKey: key,
		Path:     path,
		Name:     filepath.Base(path),
		Size:     st.Size(),
		MIME:     mimeFn(filepath.Base(path), head[:n]),
	}, nil
}
