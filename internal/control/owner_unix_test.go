//go:build unix

package control

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Tokens made with sudo stay readable by a server running as its own user.
func TestTokenFileKeepsDirOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := filepath.Join(t.TempDir(), "server")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const uid, gid = 64123, 64124
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	f := &TokenFile{Path: filepath.Join(dir, "tokens.json")}
	if _, _, err := f.CreateToken(context.Background(), "laptop", "", Limits{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tokens.json", "tokens.json.lock"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != uid || st.Gid != gid {
			t.Errorf("%s owned by %d:%d, want %d:%d", name, st.Uid, st.Gid, uid, gid)
		}
	}
}
