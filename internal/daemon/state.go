package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// stateFile is the on-disk record of an active share: everything a
// restart needs to re-share with the same link id.
type stateFile struct {
	ShareID   string `json:"share_id"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	MIME      string `json:"mime"`
	Cache     bool   `json:"cache,omitempty"`
	TTLSec    int64  `json:"ttl_sec"`    // original, for reference
	ExpiresAt int64  `json:"expires_at"` // unix seconds
}

// ErrNoActiveShares is returned when the state dir holds no
// unexpired share to resume.
var ErrNoActiveShares = errors.New("daemon: no active shares to resume")

// StateDir is the daemon's persistence: one JSON file per share.
type StateDir struct{ dir string }

// NewStateDir ensures the state directory exists.
func NewStateDir(dir string) (*StateDir, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &StateDir{dir: dir}, nil
}

// Save records an active share. Atomic: temp file + rename.
func (s *StateDir) Save(sh Share, ttlSec int64) error {
	st := stateFile{
		ShareID:   sh.ID,
		Path:      sh.Path,
		Name:      sh.Name,
		Size:      sh.Size,
		MIME:      sh.MIME,
		Cache:     sh.Cache,
		TTLSec:    ttlSec,
		ExpiresAt: time.Now().Add(time.Duration(ttlSec) * time.Second).Unix(),
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, st.ShareID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remove drops a share's state (expired or killed).
func (s *StateDir) Remove(shareID string) {
	os.Remove(filepath.Join(s.dir, shareID+".json"))
}

// Active returns all unexpired recorded shares, ready to resume.
// Expired state files are swept. Same share IDs are kept: links
// survive daemon restarts.
func (s *StateDir) Active() ([]Share, []int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, err
	}
	var shares []Share
	var ttls []int64
	now := time.Now().Unix()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var st stateFile
		if json.Unmarshal(b, &st) != nil || st.ShareID == "" {
			continue
		}
		if st.ExpiresAt <= now {
			os.Remove(filepath.Join(s.dir, e.Name()))
			continue
		}
		remain := st.ExpiresAt - now
		sh, err := ValidateShare(st.Path, DetectMIME)
		if err != nil {
			// file moved or deleted: the link is dead, drop it
			os.Remove(filepath.Join(s.dir, e.Name()))
			continue
		}
		// keep the original share id and mime so the link is stable
		sh.ID = st.ShareID
		sh.MIME = st.MIME
		sh.Cache = st.Cache
		shares = append(shares, sh)
		ttls = append(ttls, remain)
	}
	return shares, ttls, nil
}
