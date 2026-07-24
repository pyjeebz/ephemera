package machine

import (
	"fmt"
	"os"
	"os/exec"
)

// DefaultPersistMiB is the size of a new persist disk. It is sparse, so the file
// only takes real space as the computer writes to it.
const DefaultPersistMiB = 4096

// MakePersistDisk creates an empty, writable ext4 image at path — the disk a
// persistent computer keeps its changes on.
//
// It needs no privilege: mkfs.ext4 writes the filesystem into a plain file, with
// no mount and no root, the same trick the base image build uses. Unlike the
// base, this image is journaled and mounted read-write, because it is written to
// all the time and must survive an unclean stop — which the journal replays on
// the next mount.
func MakePersistDisk(path string, sizeMiB int) error {
	if sizeMiB <= 0 {
		sizeMiB = DefaultPersistMiB
	}

	// O_EXCL: never silently clobber an existing computer's disk.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("machine: create persist disk %s: %w", path, err)
	}
	if err := f.Truncate(int64(sizeMiB) << 20); err != nil {
		f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("machine: size persist disk: %w", err)
	}
	f.Close()

	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "eph-persist", path).CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("machine: format persist disk: %s: %w", string(out), err)
	}
	return nil
}
