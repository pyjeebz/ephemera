package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/config"
)

// setupInputs are files that, when present, decide what setup installs;
// a change to any of them means a new snapshot.
var setupInputs = []string{
	"package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb",
	"requirements.txt", "requirements-dev.txt", "pyproject.toml", "poetry.lock", "Pipfile.lock", "uv.lock",
	"go.mod", "go.sum", "Cargo.toml", "Cargo.lock", "Gemfile", "Gemfile.lock", "composer.json", "composer.lock",
}

// buildSetup works out a setup command's snapshot id and key. The id
// depends on the project, the command, the sandbox size and the setup
// inputs' contents; the key is the project's own, kept on this machine.
func buildSetup(dir, command string, extra []string, spec api.Spec) (*client.Setup, []string, error) {
	root := dir
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
		root = strings.TrimSpace(string(out))
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256([]byte(abs))
	projectID := hex.EncodeToString(sum[:16])
	key, err := config.SnapshotKey(projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot key: %w", err)
	}

	var inputs []string
	for _, f := range setupInputs {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			inputs = append(inputs, f)
		}
	}
	for _, f := range extra {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return nil, nil, fmt.Errorf("--setup-input %s: %w", f, err)
		}
		inputs = append(inputs, f)
	}
	sort.Strings(inputs)

	h := sha256.New()
	fmt.Fprintf(h, "ephemera setup v1\x00%s\x00%s\x00%g\x00%d\x00", projectID, command, spec.CPUs, spec.MemoryMB)
	for i, f := range inputs {
		if i > 0 && inputs[i-1] == f {
			continue
		}
		fh, err := os.Open(filepath.Join(dir, f))
		if err != nil {
			return nil, nil, err
		}
		fsum := sha256.New()
		_, err = io.Copy(fsum, fh)
		fh.Close()
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(h, "%s\x00%x\x00", f, fsum.Sum(nil))
	}
	return &client.Setup{
		Argv:       []string{"sh", "-c", command},
		SnapshotID: hex.EncodeToString(h.Sum(nil))[:40],
		Key:        key,
	}, inputs, nil
}

// printSetupEvent reports snapshot progress in plain text.
func printSetupEvent(w io.Writer, ev string, m map[string]any) {
	switch ev {
	case "snapshot":
		if m["loaded"] == true {
			fmt.Fprintln(w, "ephemera: started from the project snapshot; setup skipped")
		} else {
			fmt.Fprintf(w, "ephemera: running setup (%v)\n", m["reason"])
		}
	case "snapshot_saved":
		fmt.Fprintln(w, "ephemera: saved a project snapshot; next time setup is skipped")
	case "snapshot_error":
		fmt.Fprintf(w, "ephemera: could not save a project snapshot: %v\n", m["error"])
	}
}
