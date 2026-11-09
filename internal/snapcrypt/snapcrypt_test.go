package snapcrypt

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func key(t *testing.T) []byte {
	k := make([]byte, KeySize)
	rand.Read(k)
	return k
}

func roundTrip(t *testing.T, k []byte, data []byte) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	src, enc, dec := filepath.Join(dir, "plain"), filepath.Join(dir, "enc"), filepath.Join(dir, "dec")
	os.WriteFile(src, data, 0o600)
	if err := EncryptFile(k, src, enc); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(k, enc, dec); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dec)
	return enc, got
}

func TestRoundTrip(t *testing.T) {
	k := key(t)
	// Mostly zeros, like VM memory, with data in places and an odd tail.
	data := make([]byte, 5*chunkSize+1234)
	copy(data[chunkSize*2+10:], []byte("hello, memory"))
	rand.Read(data[4*chunkSize : 4*chunkSize+4096])
	data[len(data)-1] = 7
	enc, got := roundTrip(t, k, data)
	if !bytes.Equal(got, data) {
		t.Fatal("decrypted data differs")
	}
	info, _ := os.Stat(enc)
	if info.Size() > 4*chunkSize {
		t.Errorf("encrypted size %d: zero chunks were stored", info.Size())
	}
	raw, _ := os.ReadFile(enc)
	if bytes.Contains(raw, []byte("hello, memory")) {
		t.Error("plaintext visible in the encrypted file")
	}
	for _, n := range []int{0, 1, chunkSize, chunkSize + 1} {
		d := bytes.Repeat([]byte{'x'}, n)
		if _, got := roundTrip(t, k, d); !bytes.Equal(got, d) {
			t.Errorf("size %d: round trip differs", n)
		}
	}
}

func TestRejectsWrongKeyAndTampering(t *testing.T) {
	k := key(t)
	data := bytes.Repeat([]byte("project code "), 300000) // about 4 MB, 4 chunks
	enc, _ := roundTrip(t, k, data)
	dec := filepath.Join(t.TempDir(), "dec")

	if err := DecryptFile(key(t), enc, dec); err == nil {
		t.Error("a wrong key decrypted the file")
	}
	if _, err := os.Stat(dec); !os.IsNotExist(err) {
		t.Error("a failed decryption left output behind")
	}

	raw, _ := os.ReadFile(enc)
	cases := map[string][]byte{
		"flipped byte": func() []byte { b := bytes.Clone(raw); b[len(b)/2] ^= 1; return b }(),
		"truncated":    raw[:len(raw)-chunkSize],
		"extra bytes":  append(bytes.Clone(raw), 0),
		"bad magic":    append([]byte("XXXXXXXX"), raw[8:]...),
	}
	for name, b := range cases {
		p := filepath.Join(t.TempDir(), "enc")
		os.WriteFile(p, b, 0o600)
		if err := DecryptFile(k, p, dec); err == nil {
			t.Errorf("%s: decrypted", name)
		}
	}
}

func TestKeyCheck(t *testing.T) {
	a, b := key(t), key(t)
	if KeyCheck(a) != KeyCheck(a) || KeyCheck(a) == KeyCheck(b) {
		t.Error("KeyCheck must be stable and differ between keys")
	}
	if bytes.Contains([]byte(KeyCheck(a)), a) {
		t.Error("KeyCheck reveals the key")
	}
}

// A sparse file with data far apart comes back exactly, and its holes
// are skipped rather than read.
func TestSparseFile(t *testing.T) {
	k := key(t)
	dir := t.TempDir()
	src, enc, dec := filepath.Join(dir, "sparse"), filepath.Join(dir, "enc"), filepath.Join(dir, "dec")
	f, _ := os.Create(src)
	f.WriteAt([]byte("start"), 0)
	f.WriteAt([]byte("middle"), 3<<30) // 3 GiB in
	f.Truncate(8 << 30)                // 8 GiB, almost all hole
	f.Close()
	start := time.Now()
	if err := EncryptFile(k, src, enc); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("encrypting an 8 GiB sparse file took %v; holes were read", d)
	}
	if err := DecryptFile(k, enc, dec); err != nil {
		t.Fatal(err)
	}
	g, _ := os.Open(dec)
	defer g.Close()
	b := make([]byte, 6)
	g.ReadAt(b[:5], 0)
	if string(b[:5]) != "start" {
		t.Errorf("start = %q", b[:5])
	}
	g.ReadAt(b, 3<<30)
	if string(b) != "middle" {
		t.Errorf("middle = %q", b)
	}
	if info, _ := g.Stat(); info.Size() != 8<<30 {
		t.Errorf("size = %d", info.Size())
	}
}
