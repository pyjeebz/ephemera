package upload

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func meta(id string, size int64) Meta {
	return Meta{ID: id, Name: "v.mp4", Size: size, MIME: "video/mp4", Expires: time.Now().Add(time.Hour)}
}

func TestPutInOrderAndRead(t *testing.T) {
	s, err := Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if have, err := s.Reserve(meta("a", 10)); err != nil || have != 0 {
		t.Fatalf("reserve = %d, %v", have, err)
	}
	if got := s.Put("a", 0, []byte("01234")); got != 5 {
		t.Fatalf("put = %d, want 5", got)
	}
	// a gap or an overlap is refused, the reply is the stored length
	if got := s.Put("a", 7, []byte("78")); got != 5 {
		t.Fatalf("put past the end = %d, want 5", got)
	}
	if got := s.Put("a", 3, []byte("34")); got != 5 {
		t.Fatalf("overlapping put = %d, want 5", got)
	}
	// nothing past the reserved size
	if got := s.Put("a", 5, []byte("567890")); got != 5 {
		t.Fatalf("put over size = %d, want 5", got)
	}
	if s.Complete("a") {
		t.Fatal("complete at 5 of 10")
	}
	if got := s.Put("a", 5, []byte("56789")); got != 10 || !s.Complete("a") {
		t.Fatalf("final put = %d, complete %v", got, s.Complete("a"))
	}

	p := make([]byte, 4)
	if err := s.ReadAt("a", p, 3); err != nil || string(p) != "3456" {
		t.Fatalf("read = %q, %v", p, err)
	}
	if err := s.ReadAt("a", make([]byte, 4), 8); err == nil {
		t.Fatal("read past the stored bytes succeeded")
	}
}

func TestBudgetCountsFullSize(t *testing.T) {
	s, err := Open(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(meta("a", 60)); err != nil {
		t.Fatal(err)
	}
	// "a" has stored nothing yet, but its 60 bytes are promised
	if _, err := s.Reserve(meta("b", 50)); !errors.Is(err, ErrFull) {
		t.Fatalf("over budget: %v, want ErrFull", err)
	}
	s.Drop("a")
	if _, err := s.Reserve(meta("b", 50)); err != nil {
		t.Fatalf("after drop: %v", err)
	}
	if s.Reserved() != 50 {
		t.Fatalf("reserved = %d, want 50", s.Reserved())
	}
}

func TestDisabledStoreRefuses(t *testing.T) {
	s, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(meta("a", 1)); !errors.Is(err, ErrFull) {
		t.Fatalf("reserve on a zero budget: %v", err)
	}
}

func TestReserveAgainResumes(t *testing.T) {
	s, _ := Open(t.TempDir(), 1<<20)
	s.Reserve(meta("a", 10))
	s.Put("a", 0, []byte("0123"))
	if have, err := s.Reserve(meta("a", 10)); err != nil || have != 4 {
		t.Fatalf("redial reserve = %d, %v; want 4", have, err)
	}
	// a different size is a different file: start over
	if have, err := s.Reserve(meta("a", 12)); err != nil || have != 0 {
		t.Fatalf("resized reserve = %d, %v; want 0", have, err)
	}
	if s.Reserved() != 12 {
		t.Fatalf("reserved = %d, want 12", s.Reserved())
	}
}

func TestDropDeletesFiles(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, 1<<20)
	s.Reserve(meta("a", 4))
	s.Put("a", 0, []byte("abcd"))
	s.Drop("a")
	left, _ := os.ReadDir(dir)
	if len(left) != 0 {
		t.Fatalf("files left after drop: %v", left)
	}
	if s.Have("a") != 0 {
		t.Fatal("dropped share still has bytes")
	}
}

func TestReopenKeepsLiveDropsExpired(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, 1<<20)
	s.Reserve(meta("live", 8))
	s.Put("live", 0, []byte("abc"))
	old := meta("old", 4)
	old.Expires = time.Now().Add(-time.Second)
	s.Reserve(old)
	s.Put("old", 0, []byte("wxyz"))
	// debris: a data file with no metadata, a half-written metadata file
	os.WriteFile(filepath.Join(dir, "stray.data"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "half.json.tmp"), []byte("{"), 0o600)

	s2, err := Open(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	stored := s2.Stored()
	if len(stored) != 1 || stored[0].ID != "live" || stored[0].Size != 8 {
		t.Fatalf("stored after reopen = %+v", stored)
	}
	if s2.Have("live") != 3 {
		t.Fatalf("have after reopen = %d, want 3", s2.Have("live"))
	}
	p := make([]byte, 3)
	if err := s2.ReadAt("live", p, 0); err != nil || !bytes.Equal(p, []byte("abc")) {
		t.Fatalf("read after reopen = %q, %v", p, err)
	}
	var names []string
	left, _ := os.ReadDir(dir)
	for _, e := range left {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "live.data" || names[1] != "live.json" {
		t.Fatalf("files after reopen = %v, want only live's", names)
	}
}

func TestBadIDsStayInsideTheDir(t *testing.T) {
	s, _ := Open(t.TempDir(), 1<<20)
	for _, id := range []string{"", "../x", "a/b", "a.b"} {
		if _, err := s.Reserve(meta(id, 1)); err == nil {
			t.Errorf("reserve %q succeeded", id)
		}
	}
}
