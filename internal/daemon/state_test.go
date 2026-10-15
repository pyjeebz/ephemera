package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateSaveActiveRemove(t *testing.T) {
	dir := t.TempDir()
	sd, err := NewStateDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(path, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	sh, err := ValidateShare(path, DetectMIME)
	if err != nil {
		t.Fatal(err)
	}

	// nothing active yet
	shares, _, err := sd.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 0 {
		t.Fatalf("fresh state dir has %d shares", len(shares))
	}

	// save, read back with same id and remaining ttl
	if err := sd.Save(sh, 3600); err != nil {
		t.Fatal(err)
	}
	shares, ttls, err := sd.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 || shares[0].ID != sh.ID {
		t.Fatalf("resume: %+v", shares)
	}
	if shares[0].Path != path || shares[0].MIME != sh.MIME {
		t.Errorf("resume fields: %+v", shares[0])
	}
	if ttls[0] <= 0 || ttls[0] > 3600 {
		t.Errorf("resume ttl = %d", ttls[0])
	}

	// remove: gone
	sd.Remove(sh.ID)
	shares, _, _ = sd.Active()
	if len(shares) != 0 {
		t.Fatalf("post-remove: %d", len(shares))
	}
}

func TestStateSweepsExpired(t *testing.T) {
	dir := t.TempDir()
	sd, _ := NewStateDir(dir)

	path := filepath.Join(dir, "v.mp4")
	os.WriteFile(path, make([]byte, 512), 0o644)
	sh, _ := ValidateShare(path, DetectMIME)

	// save with 1s ttl, backdate the file's expiry, expect sweep
	if err := sd.Save(sh, 1); err != nil {
		t.Fatal(err)
	}
	st := stateFile{
		ShareID: sh.ID, OwnerKey: sh.OwnerKey, Path: sh.Path, Name: sh.Name,
		Size: sh.Size, MIME: sh.MIME, TTLSec: 1,
		ExpiresAt: time.Now().Add(-time.Second).Unix(),
	}
	b, _ := os.ReadFile(filepath.Join(dir, sh.ID+".json"))
	_ = b
	// write an expired record directly
	path2 := filepath.Join(dir, sh.ID+".json")
	os.WriteFile(path2, mustJSON(t, st), 0o600)

	shares, _, err := sd.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 0 {
		t.Fatalf("expired share resumed: %d", len(shares))
	}
	if _, err := os.Stat(path2); !os.IsNotExist(err) {
		t.Error("expired state file not swept")
	}
}

func TestStateDropsMovedFile(t *testing.T) {
	dir := t.TempDir()
	sd, _ := NewStateDir(dir)

	path := filepath.Join(dir, "v.mp4")
	os.WriteFile(path, make([]byte, 512), 0o644)
	sh, _ := ValidateShare(path, DetectMIME)
	if err := sd.Save(sh, 3600); err != nil {
		t.Fatal(err)
	}

	// file vanishes
	os.Remove(path)

	shares, _, err := sd.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 0 {
		t.Fatalf("resumed a share whose file is gone: %+v", shares)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestStateDropsUnownedRecord: a record without a valid owner key
// (written before ownership, or tampered with) can never be
// re-announced, so resume drops it instead of redialing forever.
func TestStateDropsUnownedRecord(t *testing.T) {
	dir := t.TempDir()
	sd, _ := NewStateDir(dir)

	path := filepath.Join(dir, "v.mp4")
	os.WriteFile(path, make([]byte, 512), 0o644)
	sh, _ := ValidateShare(path, DetectMIME)
	other, _ := ValidateShare(path, DetectMIME)

	for name, key := range map[string]string{"no key": "", "wrong key": other.OwnerKey} {
		st := stateFile{
			ShareID: sh.ID, OwnerKey: key, Path: sh.Path, Name: sh.Name,
			Size: sh.Size, MIME: sh.MIME, TTLSec: 3600,
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}
		rec := filepath.Join(dir, sh.ID+".json")
		os.WriteFile(rec, mustJSON(t, st), 0o600)

		shares, _, err := sd.Active()
		if err != nil {
			t.Fatal(err)
		}
		if len(shares) != 0 {
			t.Fatalf("%s: unowned record resumed", name)
		}
		if _, err := os.Stat(rec); !os.IsNotExist(err) {
			t.Errorf("%s: unowned record not dropped", name)
		}
	}
}
