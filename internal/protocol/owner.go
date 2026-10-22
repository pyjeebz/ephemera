package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// Share ids are self-certifying: the id is a hash of a secret owner
// key that only the sender holds. The daemon proves ownership by
// sending the key in TSend; the relay recomputes the id and refuses
// a mismatch. Knowing a link (the id) never lets anyone else
// announce it, and the check needs no relay-side state, so it holds
// across relay restarts too.

// ownerKeyLen is the raw owner key size in bytes.
const ownerKeyLen = 32

// shareIDLen is the raw share id size: 128 bits of the key hash.
const shareIDLen = 16

// idDomain separates share id hashes from any other use of the key.
const idDomain = "ephemera share id v1\x00"

// NewOwnerKey returns a fresh base64url owner key and the share id
// it certifies. Keys whose id would start with '-' are redrawn: one
// in 64 would, and command lines read such an id as a flag
// (`ephemera kill -aNt...`). The relay's check is unchanged.
func NewOwnerKey() (key, shareID string) {
	var b [ownerKeyLen]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic("protocol: crypto/rand unavailable: " + err.Error())
		}
		if shareID = shareIDOf(b[:]); shareID[0] != '-' {
			return base64.RawURLEncoding.EncodeToString(b[:]), shareID
		}
	}
}

// ShareIDFor derives the share id an owner key certifies. ok is false
// when key is not a well-formed owner key.
func ShareIDFor(key string) (id string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != ownerKeyLen {
		return "", false
	}
	return shareIDOf(raw), true
}

// OwnsShare reports whether key is the owner key behind shareID.
func OwnsShare(shareID, key string) bool {
	want, ok := ShareIDFor(key)
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(shareID)) == 1
}

func shareIDOf(raw []byte) string {
	h := sha256.New()
	h.Write([]byte(idDomain))
	h.Write(raw)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:shareIDLen])
}
