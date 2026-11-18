package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/config"
	"github.com/pyjeebz/ephemera/internal/record"
)

// saveRecord writes a sandbox's signed record to path, or to the records
// directory, and returns where it went. In the directory, the file is
// named for the sandbox first, api-tests-sbx_1f2e3d4c5b6a7980.json, so a
// listing reads by name and ephemera verify api-tests can find it.
func saveRecord(sr *api.SignedRecord, path, name string) (string, error) {
	if path == "" {
		rec, err := record.Verify(*sr)
		if err != nil {
			return "", err
		}
		dir, err := config.RecordsDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, recordFile(name, rec.Sandbox))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(sr, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o600)
}

// recordFile is a record's file name in the records directory. A bad name
// (one the server didn't give) is left out rather than trusted in a path.
func recordFile(name, id string) string {
	if name == "" || api.ValidName(name) != nil {
		return id + ".json"
	}
	return name + "-" + id + ".json"
}

// nameOfRecord is the sandbox name in a record file's name, or "". Names
// never hold an underscore and ids always start with sbx_, so the split
// is unambiguous.
func nameOfRecord(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	i := strings.LastIndex(base, "-sbx_")
	if i <= 0 || api.ValidName(base[:i]) != nil {
		return ""
	}
	return base[:i]
}

// findRecord turns what someone asked to verify into a record file: a
// path, a sandbox id, a sandbox name (its newest record), or nothing (the
// newest record of all).
func findRecord(what string) (string, error) {
	if what != "" {
		if st, err := os.Stat(what); err == nil && !st.IsDir() {
			return what, nil
		}
		if strings.HasSuffix(what, ".json") || strings.ContainsRune(what, os.PathSeparator) {
			return "", fmt.Errorf("no record file at %s", what)
		}
	}
	dir, err := config.RecordsDir()
	if err != nil {
		return "", err
	}
	var pattern string
	switch {
	case what == "":
		pattern = "*.json"
	case strings.Contains(what, "_"): // an id
		pattern = "*" + what + ".json"
	default:
		if err := api.ValidName(what); err != nil {
			return "", fmt.Errorf("%q is not a record file, a sandbox id or a sandbox name", what)
		}
		pattern = what + "-sbx_*.json"
	}
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return "", err
	}
	newest, at := "", time.Time{}
	for _, m := range matches {
		// "*id.json" also matches longer ids that end the same way.
		if strings.Contains(what, "_") && filepath.Base(m) != what+".json" && !strings.HasSuffix(m, "-"+what+".json") {
			continue
		}
		if st, err := os.Stat(m); err == nil && st.ModTime().After(at) {
			newest, at = m, st.ModTime()
		}
	}
	if newest == "" {
		switch {
		case what == "":
			return "", fmt.Errorf("no records in %s yet; a sandbox's record is saved there when it ends", dir)
		case strings.Contains(what, "_"):
			return "", fmt.Errorf("no record for sandbox %s in %s", what, dir)
		default:
			return "", fmt.Errorf("no record for a sandbox named %s in %s; records are saved when a sandbox ends, on the machine that ran it", what, dir)
		}
	}
	return newest, nil
}

func cmdVerify(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "Usage: ephemera verify [<name> | <id> | <record.json>]")
		return 2
	}
	what := ""
	if len(args) == 1 {
		what = args[0]
	}
	path, err := findRecord(what)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	text, ok, err := verifyFile(ctx, c, path)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	fmt.Fprint(stdout, text)
	if !ok {
		return 1
	}
	return 0
}

// verifyFile checks a saved record: its signature, and that the signing
// key is the one the central server has for the host. It returns a
// description of the record and whether it is fully verified.
func verifyFile(ctx context.Context, c *client.Client, path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	var sr api.SignedRecord
	if err := json.Unmarshal(b, &sr); err != nil {
		return "", false, fmt.Errorf("not a sandbox record: %w", err)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Record %s\n", path)
	rec, err := record.Verify(sr)
	if err != nil {
		fmt.Fprintf(&out, "NOT VERIFIED: %v\n", err)
		return out.String(), false, nil
	}
	// The signature only proves the record matches the key inside it.
	// The key must also be the host's, as the central server knows it.
	trusted := "the central server could not be asked about this host's key"
	verified := false
	if hosts, err := c.Hosts(ctx); err == nil {
		trusted = fmt.Sprintf("host %q is not known to the central server", rec.Host)
		for _, h := range hosts {
			if h.Name != rec.Host {
				continue
			}
			if h.PublicKey == rec.HostKey {
				trusted, verified = "the key is the one the central server has for this host", true
			} else {
				trusted = "the key does NOT match the central server's key for this host"
			}
		}
	}
	if verified {
		fmt.Fprintf(&out, "Verified: signed by host %s; %s.\n\n", rec.Host, trusted)
	} else {
		fmt.Fprintf(&out, "Signature is intact, but NOT fully verified: %s.\n\n", trusted)
	}
	printRecord(&out, rec, nameOfRecord(path))
	return out.String(), verified, nil
}

// printRecord describes a record. name comes from the file's name, as the
// signed record itself holds only the id.
func printRecord(w io.Writer, rec api.Record, name string) {
	if name != "" {
		fmt.Fprintf(w, "Sandbox %s (%s) on %s (%s backend)\n", name, rec.Sandbox, rec.Host, rec.Backend)
	} else {
		fmt.Fprintf(w, "Sandbox %s on %s (%s backend)\n", rec.Sandbox, rec.Host, rec.Backend)
	}
	life := rec.Destroyed.Sub(rec.Created).Round(time.Millisecond)
	fmt.Fprintf(w, "  created    %s\n  destroyed  %s (%s, after %v)\n", rec.Created.Format(time.RFC3339), rec.Destroyed.Format(time.RFC3339), rec.Ended, life)
	network := rec.Spec.Network
	if len(rec.Spec.Allow) > 0 {
		network += ": " + strings.Join(rec.Spec.Allow, ", ")
	}
	fmt.Fprintf(w, "  network    %s\n", network)
	fmt.Fprintf(w, "  disk       %s\n", map[bool]string{true: "deleted", false: "NOT confirmed deleted"}[rec.DiskDeleted])
	fmt.Fprintf(w, "\nCommands (%d):\n", len(rec.Execs))
	for _, e := range rec.Execs {
		status := fmt.Sprintf("exit %d", e.ExitCode)
		if e.Error != "" {
			status += ", " + e.Error
		}
		fmt.Fprintf(w, "  %s  %-8s %6dms  %s\n", e.Started.Format("15:04:05"), status, e.DurationMS, strings.Join(e.Argv, " "))
	}
	fmt.Fprintf(w, "\nFile syncs (%d):\n", len(rec.Syncs))
	for _, s := range rec.Syncs {
		if s.Deleted > 0 {
			fmt.Fprintf(w, "  %s  deleted %d\n", s.Time.Format("15:04:05"), s.Deleted)
		} else {
			fmt.Fprintf(w, "  %s  %d files, %d bytes\n", s.Time.Format("15:04:05"), s.Files, s.Bytes)
		}
	}
	fmt.Fprintf(w, "\nConnections (%d):\n", len(rec.Connections))
	for _, c := range rec.Connections {
		state := "allowed"
		if !c.Allowed {
			state = "BLOCKED"
		}
		fmt.Fprintf(w, "  %s  %-8s %s:%d", c.Time.Format("15:04:05"), state, c.Host, c.Port)
		if c.Error != "" {
			fmt.Fprintf(w, "  (%s)", c.Error)
		}
		fmt.Fprintln(w)
	}
}
