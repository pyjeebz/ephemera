package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"

	"github.com/pyjeebz/ephemera/internal/sandbox"
	"github.com/pyjeebz/ephemera/internal/snapcrypt"
)

var _ sandbox.ProjectSnapshotter = (*Backend)(nil)

// projectMeta describes a stored project snapshot. It holds nothing secret.
type projectMeta struct {
	ID       string    `json:"id"`
	KeyCheck string    `json:"key_check"`
	Image    string    `json:"image"` // the guest image it was made on
	CPUs     int       `json:"cpus"`
	MemoryMB int       `json:"memory_mb"`
	Bytes    int64     `json:"bytes"`
	Created  time.Time `json:"created"`
}

var snapIDRe = regexp.MustCompile(`^[a-f0-9]{32,64}$`)

var snapFiles = []string{fileSnapMem, fileSnapVM, fileScratch}

func (b *Backend) projectDir() string {
	if b.ProjectSnapshotDir != "" {
		return b.ProjectSnapshotDir
	}
	return "/var/lib/ephemera/snapshots"
}

func (b *Backend) maxSnapshotBytes() int64 {
	if b.MaxSnapshotBytes > 0 {
		return b.MaxSnapshotBytes
	}
	return 20 << 30
}

// imageID identifies the guest image, so a snapshot made on another image
// is not restored onto this one.
func (b *Backend) imageID() string {
	h := sha256.New()
	for _, f := range []string{"vmlinux", "rootfs.ext4"} {
		info, err := os.Stat(filepath.Join(b.ImageDir, f))
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s %d %d", f, info.Size(), info.ModTime().UnixNano())
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			fmt.Fprintf(h, " %d", st.Ino)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// SaveSnapshot pauses the VM, saves it, resumes it, and stores the
// snapshot encrypted. The unencrypted copy exists only until it is
// encrypted, in a directory only root can read.
func (b *Backend) SaveSnapshot(ctx context.Context, id, snapID string, key []byte) error {
	if !snapIDRe.MatchString(snapID) {
		return errors.New("invalid snapshot id")
	}
	if len(key) != snapcrypt.KeySize {
		return fmt.Errorf("snapshot key must be %d bytes", snapcrypt.KeySize)
	}
	vm, err := b.vm(id)
	if err != nil {
		return err
	}
	b.snapMu.Lock()
	defer b.snapMu.Unlock()

	work := filepath.Join(b.projectDir(), ".save-"+jailID(id))
	os.RemoveAll(work)
	plain, enc := filepath.Join(work, "plain"), filepath.Join(work, "enc")
	if err := os.MkdirAll(enc, 0o700); err != nil {
		return err
	}
	os.Chmod(b.projectDir(), 0o700)
	defer os.RemoveAll(work)

	snap, err := vm.CreateSnapshot(ctx, plain)
	resumeErr := vm.Resume(ctx) // the sandbox carries on either way
	if err != nil {
		return err
	}
	if resumeErr != nil {
		return resumeErr
	}
	var total int64
	for _, f := range snapFiles {
		src := filepath.Join(plain, f)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(enc, f+".enc")
		if err := snapcrypt.EncryptFile(key, src, dst); err != nil {
			return fmt.Errorf("encrypt %s: %w", f, err)
		}
		os.Remove(src) // no plaintext left once encrypted
		if info, err := os.Stat(dst); err == nil {
			total += info.Size()
		}
	}
	meta := projectMeta{ID: snapID, KeyCheck: snapcrypt.KeyCheck(key), Image: b.imageID(),
		CPUs: snap.CPUs, MemoryMB: snap.MemoryMB, Bytes: total, Created: time.Now().UTC()}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(enc, "meta.json"), mb, 0o600); err != nil {
		return err
	}
	final := filepath.Join(b.projectDir(), snapID)
	os.RemoveAll(final)
	if err := os.Rename(enc, final); err != nil {
		return err
	}
	b.evict()
	return nil
}

// LoadSnapshot replaces the sandbox's fresh VM with one restored from the
// project snapshot. If anything about the snapshot is off, the sandbox
// keeps its fresh VM and the reason is returned.
func (b *Backend) LoadSnapshot(ctx context.Context, id, snapID string, key []byte) (bool, string, error) {
	if !snapIDRe.MatchString(snapID) {
		return false, "", errors.New("invalid snapshot id")
	}
	if len(key) != snapcrypt.KeySize {
		return false, "", fmt.Errorf("snapshot key must be %d bytes", snapcrypt.KeySize)
	}
	old, err := b.vm(id)
	if err != nil {
		return false, "", err
	}
	dir := filepath.Join(b.projectDir(), snapID)
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return false, "no snapshot yet", nil
	}
	var meta projectMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		os.RemoveAll(dir)
		return false, "snapshot metadata was unreadable; removed", nil
	}
	switch {
	case meta.KeyCheck != snapcrypt.KeyCheck(key):
		return false, "the key does not match this snapshot", nil
	case meta.Image != b.imageID():
		os.RemoveAll(dir)
		return false, "made on another guest image; removed", nil
	case meta.CPUs != old.cfg.CPUs || meta.MemoryMB != old.cfg.MemoryMB:
		return false, "made for another sandbox size", nil
	}

	plain := filepath.Join(old.cfg.Dir, "project-snapshot")
	os.RemoveAll(plain)
	if err := os.MkdirAll(plain, 0o700); err != nil {
		return false, "", err
	}
	defer os.RemoveAll(plain)
	for _, f := range snapFiles {
		src := filepath.Join(dir, f+".enc")
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := snapcrypt.DecryptFile(key, src, filepath.Join(plain, f)); err != nil {
			os.RemoveAll(dir) // corrupt: it will be saved afresh
			return false, "snapshot did not decrypt (" + err.Error() + "); removed", nil
		}
	}

	// Swap the VM. The sandbox keeps its id, jail, user ID and network log.
	b.mu.Lock()
	vp := b.proxies[id]
	b.mu.Unlock()
	if vp != nil {
		vp.ln.Close()
	}
	old.Kill()
	old.Cleanup()
	cfg := old.cfg
	cfg.Scratch = ""
	vm, rerr := Restore(ctx, cfg, &Snapshot{Dir: plain, CPUs: meta.CPUs, MemoryMB: meta.MemoryMB, Own: true})
	reason := ""
	if rerr != nil {
		// Keep the sandbox alive: back to a fresh VM.
		reason = "restore failed (" + rerr.Error() + ")"
		if b.tmpl != nil {
			vm, err = Restore(ctx, cfg, b.tmpl)
		} else {
			scratch := filepath.Join(cfg.Dir, "scratch.ext4")
			if err = makeScratch(ctx, scratch, b.scratchMB()); err == nil {
				cfg.Scratch = scratch
				vm, err = Boot(ctx, cfg)
			}
		}
		if err != nil {
			b.mu.Lock()
			delete(b.vms, id)
			b.mu.Unlock()
			return false, "", fmt.Errorf("%s, and a fresh VM failed too: %w", reason, err)
		}
	}
	if vp != nil {
		if err := vp.listen(vm); err != nil {
			vm.Kill()
			vm.Cleanup()
			return false, "", fmt.Errorf("egress proxy: %w", err)
		}
	}
	b.mu.Lock()
	b.vms[id] = vm
	b.mu.Unlock()
	if rerr != nil {
		return false, reason, nil
	}
	now := time.Now()
	os.Chtimes(filepath.Join(dir, "meta.json"), now, now) // most recently used
	return true, "", nil
}

// evict deletes the least recently used project snapshots until they fit
// in MaxSnapshotBytes. Called with snapMu held.
func (b *Backend) evict() {
	entries, err := os.ReadDir(b.projectDir())
	if err != nil {
		return
	}
	type snap struct {
		dir   string
		bytes int64
		used  time.Time
	}
	var snaps []snap
	var total int64
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] == '.' {
			continue
		}
		d := filepath.Join(b.projectDir(), e.Name())
		raw, err := os.ReadFile(filepath.Join(d, "meta.json"))
		info, serr := os.Stat(filepath.Join(d, "meta.json"))
		var m projectMeta
		if err != nil || serr != nil || json.Unmarshal(raw, &m) != nil {
			os.RemoveAll(d)
			continue
		}
		snaps = append(snaps, snap{d, m.Bytes, info.ModTime()})
		total += m.Bytes
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].used.Before(snaps[j].used) })
	for _, s := range snaps {
		if total <= b.maxSnapshotBytes() {
			break
		}
		os.RemoveAll(s.dir)
		total -= s.bytes
	}
}
