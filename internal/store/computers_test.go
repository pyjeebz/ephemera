package store

import (
	"path/filepath"
	"testing"
)

func TestComputerCreateAndGet(t *testing.T) {
	s, err := OpenComputers(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Create("mylaptop", s.DiskPath("mylaptop"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.Name != "mylaptop" || rec.DiskPath == "" {
		t.Errorf("record = %+v", rec)
	}
	got, err := s.Get("mylaptop")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "mylaptop" {
		t.Errorf("Get returned %+v", got)
	}
}

func TestComputerNamesAreValidated(t *testing.T) {
	s, _ := OpenComputers(t.TempDir())
	for _, bad := range []string{"", "has space", "has/slash", "..", "a.b", strLen(65)} {
		if _, err := s.Create(bad, "/x"); err == nil {
			t.Errorf("Create accepted invalid name %q", bad)
		}
	}
	for _, ok := range []string{"a", "My_Laptop-2", strLen(64)} {
		if _, err := s.Create(ok, "/x"); err != nil {
			t.Errorf("Create rejected valid name %q: %v", ok, err)
		}
	}
}

func TestComputerCreateRejectsDuplicate(t *testing.T) {
	s, _ := OpenComputers(t.TempDir())
	if _, err := s.Create("dev", "/x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("dev", "/y"); err == nil {
		t.Fatal("Create accepted a duplicate name")
	}
}

// Records outlive the daemon: a new store over the same directory sees them.
func TestComputersPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := OpenComputers(dir)
	if _, err := s1.Create("keep", filepath.Join(dir, "keep.ext4")); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenComputers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Get("keep"); err != nil {
		t.Errorf("a reopened store lost the computer: %v", err)
	}
}

func TestComputerRemoveForgetsIt(t *testing.T) {
	s, _ := OpenComputers(t.TempDir())
	s.Create("gone", s.DiskPath("gone"))
	if err := s.Remove("gone"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := s.Get("gone"); err == nil {
		t.Error("Get found a removed computer")
	}
	if err := s.Remove("never"); err == nil {
		t.Error("Remove of an unknown computer should error")
	}
}

func strLen(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
