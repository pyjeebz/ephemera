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
	if err := old.Kill(); err != nil {
		// Only a Firecracker that did not exit in ten seconds reaches here.
		// Its live process still holds the jail directory and user ID that
		// the snapshot restores into, so the sandbox stays on it, where
		// Destroy and expiry can still reach it.
		return false, "", fmt.Errorf("stop the running VM: %w", err)
	}
	if vp != nil {
		vp.ln.Close()
	}
	old.Cleanup()
	cfg := old.cfg
	cfg.Scratch = ""
	fresh := func() (*VM, error) {
		if b.tmpl != nil {
			return Restore(ctx, cfg, b.tmpl)
		}
		scratch := filepath.Join(cfg.Dir, "scratch.ext4")
		if err := makeScratch(ctx, scratch, b.scratchMB()); err != nil {
			return nil, err
		}
		cfg.Scratch = scratch
		return Boot(ctx, cfg)
	}
	loaded, reason, err := b.swapVM(id, vp, func() (*VM, error) {
		return Restore(ctx, cfg, &Snapshot{Dir: plain, CPUs: meta.CPUs, MemoryMB: meta.MemoryMB, Own: true})
	}, fresh)
	if err != nil {
		return false, "", err
	}
	if !loaded {
		return false, reason, nil
	}
	now := time.Now()
	os.Chtimes(filepath.Join(dir, "meta.json"), now, now) // most recently used
	return true, "", nil
}

// swapVM gives the sandbox the VM that load returns, with the sandbox's
// egress proxy listening on it. Every later call on the sandbox goes
// through that proxy, so a VM it cannot listen on is no more use than one
// the snapshot would not fill: both leave the sandbox on a fresh VM from
// fresh, and the reason is returned. When nothing is left to run, the
// sandbox keeps no VM, so no caller acts on one that has stopped.
func (b *Backend) swapVM(id string, vp *vmProxy, load, fresh func() (*VM, error)) (bool, string, error) {
	vm, err := load()
	reason := ""
	if err != nil {
		reason = "restore failed (" + err.Error() + ")"
	} else if err := bindProxy(vp, vm); err != nil {
		reason = "egress proxy: " + err.Error()
	}
	if reason != "" {
		if vm, err = fresh(); err != nil {
			b.dropVM(id)
			return false, "", fmt.Errorf("%s, and a fresh VM failed too: %w", reason, err)
		}
		if err := bindProxy(vp, vm); err != nil {
			b.dropVM(id)
			return false, "", fmt.Errorf("%s, and a fresh VM's egress proxy failed too: %w", reason, err)
		}
	}
	b.mu.Lock()
	if b.vms == nil {
		b.vms = map[string]*VM{}
	}
	b.vms[id] = vm
	b.mu.Unlock()
	return reason == "", reason, nil
}

// bindProxy points the sandbox's egress proxy at vm. A VM the proxy cannot
// listen on is killed, because nothing reaches the sandbox through it.
func bindProxy(vp *vmProxy, vm *VM) error {
	if vp == nil {
		return nil
	}
	if err := vp.listen(vm); err != nil {
		vm.Kill()
		vm.Cleanup()
		return err
	}
	return nil
}

// dropVM forgets the sandbox's VM and proxy, for a swap that left nothing
// running. Later calls answer that the sandbox does not exist, and Destroy
// still removes its directory.
func (b *Backend) dropVM(id string) {
	b.mu.Lock()
	delete(b.vms, id)
	delete(b.proxies, id)
	b.mu.Unlock()
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
