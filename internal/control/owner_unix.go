//go:build unix

package control

import (
	"os"
	"path/filepath"
	"syscall"
)

// matchDirOwner gives path the owner of its directory, when running as
// root and they differ.
func matchDirOwner(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	st, ok := dir.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return nil
	}
	return os.Lchown(path, int(st.Uid), int(st.Gid))
}
