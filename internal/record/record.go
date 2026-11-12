// Package record signs and checks sandbox records. A host signs each
// sandbox's record with its own Ed25519 key when the sandbox is destroyed;
// anyone with the record can check it was written by that host and not
// changed since.
package record

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

// Sign serializes the record compactly and signs exactly those bytes.
func Sign(key ed25519.PrivateKey, rec api.Record) (api.SignedRecord, error) {
	rec.HostKey = PublicKey(key)
	b, err := json.Marshal(rec)
	if err != nil {
		return api.SignedRecord{}, err
	}
	return api.SignedRecord{Record: b, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))}, nil
}

// PublicKey returns the key's public half, base64.
func PublicKey(key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

// Verify checks the signature against the public key the record names and
// returns the record. On its own this proves only that the record is
// intact; trusting it also needs the key to be the host's, which the
// caller checks against the central server's list of hosts.
//
// The signature covers the record in compact form, as Sign wrote it, so a
// record that was only re-indented, say when saved for people to read,
// still verifies; any change to its content does not.
func Verify(sr api.SignedRecord) (api.Record, error) {
	var rec api.Record
	var compact bytes.Buffer
	if err := json.Compact(&compact, sr.Record); err != nil {
		return rec, fmt.Errorf("record: %w", err)
	}
	if err := json.Unmarshal(compact.Bytes(), &rec); err != nil {
		return rec, fmt.Errorf("record: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(rec.HostKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return rec, errors.New("record: invalid host key")
	}
	sig, err := base64.StdEncoding.DecodeString(sr.Signature)
	if err != nil {
		return rec, errors.New("record: invalid signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), compact.Bytes(), sig) {
		return rec, errors.New("record: signature does not match; the record was changed or not signed by this key")
	}
	return rec, nil
}

const pemType = "EPHEMERA HOST KEY"

// LoadOrCreateKey reads the host's signing key from path, creating one,
// readable only by its owner, if there is none.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil || blk.Type != pemType || len(blk.Bytes) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not an ephemera host key", path)
		}
		return ed25519.NewKeyFromSeed(blk.Bytes), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: key.Seed()})
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

// Fingerprint is a short, stable name for a public key (base64 Ed25519, as
// PublicKey writes it): "SHA256:" and the unpadded base64 of the SHA-256 of
// its DER SubjectPublicKeyInfo. People compare it to check a host is the
// one they joined.
func Fingerprint(publicKey string) (string, error) {
	pub, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", errors.New("not an Ed25519 public key")
	}
	return FingerprintKey(ed25519.PublicKey(pub))
}

// FingerprintKey is Fingerprint for a parsed public key, such as a TLS
// certificate's.
func FingerprintKey(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// Certificate makes a self-signed TLS certificate for key, valid for 30
// days. Hosts serve with one, and the CLI checks its public key against
// the host's pinned fingerprint rather than any name or authority, so
// the certificate can be made fresh at every start.
func Certificate(key ed25519.PrivateKey) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "ephemera host"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
