package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ephemera.run/ephemera/internal/protocol"
)

// TestValidateShareOwnedID: every share gets a fresh owner key, and
// its id is the url-safe 22-character id that key certifies.
func TestValidateShareOwnedID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(path, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		sh, err := ValidateShare(path, DetectMIME)
		if err != nil {
			t.Fatal(err)
		}
		if len(sh.ID) != 22 {
			t.Fatalf("id %q: len = %d, want 22", sh.ID, len(sh.ID))
		}
		if seen[sh.ID] {
			t.Fatalf("duplicate id %q", sh.ID)
		}
		seen[sh.ID] = true
		if !protocol.OwnsShare(sh.ID, sh.OwnerKey) {
			t.Fatalf("id %q not certified by its owner key", sh.ID)
		}
		for _, c := range sh.ID {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				t.Fatalf("id %q: non-url-safe char %q", sh.ID, c)
			}
		}
	}
}

func TestValidateShare(t *testing.T) {
	dir := t.TempDir()

	// a normal file, 1MB of zeros
	okPath := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(okPath, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(dir, "empty.mp4")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	blockedPath := filepath.Join(dir, "setup.exe")
	if err := os.WriteFile(blockedPath, []byte("MZ..."), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr error
	}{
		{"valid file", okPath, nil},
		{"empty file rejected", emptyPath, ErrEmpty},
		{"blocked ext rejected", blockedPath, ErrBlockedExt},
		{"missing file rejected", filepath.Join(dir, "nope.mp4"), os.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh, err := ValidateShare(tt.path, DetectMIME)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if sh.ID == "" || sh.Name == "" || sh.Size <= 0 || sh.MIME == "" {
					t.Errorf("incomplete share: %+v", sh)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}

	// directories are not shareable
	if _, err := ValidateShare(dir, DetectMIME); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("directory: err = %v, want os.ErrInvalid", err)
	}
}

func TestValidateShareTooLarge(t *testing.T) {
	dir := t.TempDir()
	// sparse 5GB+1 byte: over the cap without 5GB of disk
	big := filepath.Join(dir, "big.mp4")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxShareSize + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ValidateShare(big, DetectMIME); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}
