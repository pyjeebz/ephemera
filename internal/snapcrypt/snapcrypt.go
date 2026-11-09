// Package snapcrypt encrypts snapshot files at rest. A project snapshot
// holds the project's code and dependencies, so the host keeps it only in
// this form; the key stays with the developer and is sent only to restore.
//
// A file is split into 1 MiB chunks. Chunks of zeros, most of a VM's
// memory, are not stored and come back as holes. Every other chunk is
// sealed with AES-256-GCM under a key derived for this file, with its
// index and whether it is the last chunk as additional data, so chunks
// cannot be reordered, dropped, swapped between files or truncated without
// the file failing to decrypt.
package snapcrypt

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// KeySize is the key length: 256 bits.
const KeySize = 32

const (
	chunkSize = 1 << 20
	magic     = "EPHSNAP1"
	kindZero  = 0
	kindData  = 1
)

var errCorrupt = errors.New("snapshot file is corrupt or the key is wrong")

// KeyCheck is a value that shows whether a key is the right one for a
// snapshot without revealing it.
func KeyCheck(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("ephemera snapshot key check"))
	return hex.EncodeToString(m.Sum(nil))
}

// fileKey derives the key for one file from the snapshot key and a random
// salt kept in the file's header.
func fileKey(key, salt []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("ephemera snapshot file\x00"))
	m.Write(salt)
	return m.Sum(nil)
}

func aead(key, salt []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(fileKey(key, salt))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(i uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], i)
	return n
}

func ad(i uint64, last bool) []byte {
	b := make([]byte, 9)
	binary.BigEndian.PutUint64(b, i)
	if last {
		b[8] = 1
	}
	return b
}

// EncryptFile writes src to dst encrypted with key.
func EncryptFile(key []byte, src, dst string) error {
	if len(key) != KeySize {
		return fmt.Errorf("snapshot key must be %d bytes", KeySize)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 4<<20)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		out.Close()
		return err
	}
	g, err := aead(key, salt)
	if err != nil {
		out.Close()
		return err
	}
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(info.Size()))
	w.WriteString(magic)
	w.Write(salt)
	w.Write(hdr[:])

	size := info.Size()
	chunks := (size + chunkSize - 1) / chunkSize
	if chunks == 0 {
		chunks = 1 // an empty file still has one (empty, sealed) chunk
	}
	buf := make([]byte, chunkSize)
	zero := make([]byte, chunkSize)
	for i := int64(0); i < chunks; i++ {
		n, err := io.ReadFull(in, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			out.Close()
			return err
		}
		last := i == chunks-1
		chunk := buf[:n]
		if n > 0 && !last && bytes.Equal(chunk, zero[:n]) {
			w.WriteByte(kindZero)
			continue
		}
		sealed := g.Seal(nil, nonce(uint64(i)), chunk, ad(uint64(i), last))
		w.WriteByte(kindData)
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(sealed)))
		w.Write(l[:])
		w.Write(sealed)
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// DecryptFile writes the decrypted src to dst, as a sparse file. On any
// error dst is removed.
func DecryptFile(key []byte, src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r := bufio.NewReaderSize(in, 4<<20)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
	}()

	head := make([]byte, len(magic)+16+8)
	if _, err := io.ReadFull(r, head); err != nil || string(head[:len(magic)]) != magic {
		return errCorrupt
	}
	salt := head[len(magic) : len(magic)+16]
	size := int64(binary.BigEndian.Uint64(head[len(magic)+16:]))
	if size < 0 || size > 1<<40 {
		return errCorrupt
	}
	g, err := aead(key, salt)
	if err != nil {
		return err
	}
	chunks := (size + chunkSize - 1) / chunkSize
	if chunks == 0 {
		chunks = 1
	}
	for i := int64(0); i < chunks; i++ {
		kind, err := r.ReadByte()
		if err != nil {
			return errCorrupt // truncated
		}
		last := i == chunks-1
		switch kind {
		case kindZero:
			if last {
				return errCorrupt // the last chunk is always sealed
			}
			continue // a hole
		case kindData:
		default:
			return errCorrupt
		}
		var l [4]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return errCorrupt
		}
		n := binary.BigEndian.Uint32(l[:])
		if n > chunkSize+uint32(g.Overhead()) {
			return errCorrupt
		}
		sealed := make([]byte, n)
		if _, err := io.ReadFull(r, sealed); err != nil {
			return errCorrupt
		}
		plain, err := g.Open(nil, nonce(uint64(i)), sealed, ad(uint64(i), last))
		if err != nil {
			return errCorrupt
		}
		if _, err := out.WriteAt(plain, i*chunkSize); err != nil {
			return err
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return errCorrupt // data after the last chunk
	}
	return out.Truncate(size)
}
