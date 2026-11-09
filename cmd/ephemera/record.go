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
// directory, and returns where it went.
func saveRecord(sr *api.SignedRecord, path string) (string, error) {
	if path == "" {
		rec, err := record.Verify(*sr)
		if err != nil {
			return "", err
		}
		dir, err := config.RecordsDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, rec.Sandbox+".json")
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

func cmdVerify(ctx context.Context, c *client.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage: ephemera verify <record.json>")
		return 2
	}
	text, ok, err := verifyFile(ctx, c, args[0])
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
	printRecord(&out, rec)
	return out.String(), verified, nil
}

func printRecord(w io.Writer, rec api.Record) {
	fmt.Fprintf(w, "Sandbox %s on %s (%s backend)\n", rec.Sandbox, rec.Host, rec.Backend)
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
