// Package worktree decides which files of a checkout go to a sandbox and
// packs them as a tar stream.
//
// Only the files git would track are sent: tracked files plus untracked files
// that are not ignored. Ignored files such as .env.local or node_modules stay
// on the laptop unless they are named explicitly.
package worktree

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Files lists the files under dir to send, as slash-separated paths relative
// to dir. include adds files that would otherwise be skipped, such as an
// ignored .env.local. Outside a git repository every file is sent except the
// .git directory.
func Files(dir string, include []string) ([]string, error) {
	files, err := gitFiles(dir)
	if errors.Is(err, errNotGit) {
		files, err = walkFiles(dir)
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f] = true
	}
	for _, inc := range include {
		rel, err := relPath(dir, inc)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			return nil, fmt.Errorf("include %s: %w", inc, err)
		}
		if !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	sort.Strings(files)
	return files, nil
}

var errNotGit = errors.New("not a git repository")

func gitFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return nil, errNotGit
		}
		return nil, fmt.Errorf("git ls-files: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	seen := map[string]bool{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		// A tracked file deleted from the worktree is still in the index.
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

func walkFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

func relPath(dir, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("include %s: must be inside %s", p, dir)
	}
	return filepath.ToSlash(rel), nil
}

// WriteTar writes the named files under dir to w as a tar stream. Regular
// files keep their permission bits; symlinks are sent as links.
func WriteTar(w io.Writer, dir string, files []string) error {
	tw := tar.NewWriter(w)
	for _, name := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		var link string
		switch {
		case info.Mode().IsRegular():
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(full); err != nil {
				return err
			}
		default:
			continue // sockets, devices and the like are never sent
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return tw.Close()
}

// Sanitize copies a tar stream from src to dst, refusing anything that could
// land outside the workspace: absolute paths, ".." components, links that
// point outside it, and entry types other than files, directories and
// symlinks. It also caps the unpacked size.
func Sanitize(dst io.Writer, src io.Reader, maxBytes int64) error {
	tr := tar.NewReader(src)
	tw := tar.NewWriter(dst)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := checkName(hdr.Name); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink:
			if path.IsAbs(hdr.Linkname) || escapes(path.Join(path.Dir(hdr.Name), hdr.Linkname)) {
				return fmt.Errorf("worktree: symlink %s points outside the workspace", hdr.Name)
			}
		default:
			return fmt.Errorf("worktree: %s has unsupported type %q", hdr.Name, hdr.Typeflag)
		}
		total += hdr.Size
		if total > maxBytes {
			return fmt.Errorf("worktree: upload is larger than %d bytes", maxBytes)
		}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	return tw.Close()
}

func checkName(name string) error {
	if name == "" || path.IsAbs(name) || strings.Contains(name, "\\") || escapes(name) {
		return fmt.Errorf("worktree: unsafe path %q", name)
	}
	return nil
}

func escapes(p string) bool {
	p = path.Clean(p)
	return p == ".." || strings.HasPrefix(p, "../")
}
