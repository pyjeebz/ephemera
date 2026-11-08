package worktree

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestFilesInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, ".gitignore", "node_modules/\n.env.local\n")
	write(t, dir, "main.go", "package main")
	write(t, dir, "pkg/lib.go", "package pkg")
	write(t, dir, "untracked.txt", "new")
	write(t, dir, "node_modules/x/index.js", "ignored")
	write(t, dir, ".env.local", "SECRET=1")
	write(t, dir, "gone.txt", "soon deleted")
	git(t, dir, "add", ".gitignore", "main.go", "pkg/lib.go", "gone.txt")
	os.Remove(filepath.Join(dir, "gone.txt"))

	got, err := Files(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "main.go", "pkg/lib.go", "untracked.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Files = %v, want %v", got, want)
	}

	got, err = Files(dir, []string{".env.local"})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{".env.local", ".gitignore", "main.go", "pkg/lib.go", "untracked.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Files with include = %v, want %v", got, want)
	}

	if _, err := Files(dir, []string{"../outside"}); err == nil {
		t.Error("include outside the worktree: want error")
	}
}

func TestFilesOutsideGit(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "a")
	write(t, dir, ".git/config", "not really a repo")
	write(t, dir, "b/c.txt", "c")
	got, err := Files(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "b/c.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Files = %v, want %v", got, want)
	}
}

func TestWriteTarAndSanitize(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "run.sh", "#!/bin/sh\necho hi\n")
	os.Chmod(filepath.Join(dir, "run.sh"), 0o755)
	write(t, dir, "docs/readme.md", "# hi")
	os.Symlink("docs/readme.md", filepath.Join(dir, "README.md"))

	var raw, clean bytes.Buffer
	if err := WriteTar(&raw, dir, []string{"README.md", "docs/readme.md", "run.sh"}); err != nil {
		t.Fatal(err)
	}
	if err := Sanitize(&clean, &raw, 1<<20); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&clean)
	modes := map[string]int64{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[hdr.Name] = hdr.Mode
	}
	if modes["run.sh"]&0o111 == 0 {
		t.Error("run.sh lost its executable bit")
	}
	if len(modes) != 3 {
		t.Errorf("got %d entries, want 3", len(modes))
	}
}

func TestSanitizeRejectsUnsafeEntries(t *testing.T) {
	cases := map[string]tar.Header{
		"absolute":       {Name: "/etc/passwd", Typeflag: tar.TypeReg},
		"dotdot":         {Name: "a/../../x", Typeflag: tar.TypeReg},
		"abs symlink":    {Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		"escape symlink": {Name: "a/l", Typeflag: tar.TypeSymlink, Linkname: "../../etc"},
		"hard link":      {Name: "h", Typeflag: tar.TypeLink, Linkname: "x"},
		"device":         {Name: "d", Typeflag: tar.TypeChar},
	}
	for name, hdr := range cases {
		var raw bytes.Buffer
		tw := tar.NewWriter(&raw)
		hdr := hdr
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		tw.Close()
		if err := Sanitize(io.Discard, &raw, 1<<20); err == nil {
			t.Errorf("%s: want error", name)
		}
	}

	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	tw.WriteHeader(&tar.Header{Name: "big", Typeflag: tar.TypeReg, Size: 10, Mode: 0o644})
	tw.Write([]byte("0123456789"))
	tw.Close()
	if err := Sanitize(io.Discard, &raw, 5); err == nil {
		t.Error("over the size cap: want error")
	}
}
