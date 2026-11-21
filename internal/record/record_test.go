package record

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
)

func TestSignVerify(t *testing.T) {
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "keys", "host.key"))
	if err != nil {
		t.Fatal(err)
	}
	rec := api.Record{Version: 1, Sandbox: "sbx_1", Host: "h", Created: time.Now().UTC(),
		Execs: []api.ExecRecord{{Argv: []string{"npm", "test"}, ExitCode: 0}}, DiskDeleted: true}
	sr, err := Sign(key, rec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(sr)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sandbox != "sbx_1" || got.HostKey != PublicKey(key) || !got.DiskDeleted {
		t.Errorf("record = %+v", got)
	}

	// Saved indented for people to read, as the CLI does, it still verifies.
	pretty, err := json.MarshalIndent(sr, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var reread api.SignedRecord
	if err := json.Unmarshal(pretty, &reread); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(reread.Record, sr.Record) {
		t.Fatal("indenting did not change the bytes; this test checks nothing")
	}
	if _, err := Verify(reread); err != nil {
		t.Errorf("an indented record did not verify: %v", err)
	}

	// Any change breaks the signature.
	tampered := sr
	tampered.Record = bytes.Replace(sr.Record, []byte(`"exit_code":0`), []byte(`"exit_code":1`), 1)
	if _, err := Verify(tampered); err == nil {
		t.Error("a changed record verified")
	}
	// So does a valid signature from another key.
	other, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "other.key"))
	forged, _ := Sign(other, rec)
	forged.Record = sr.Record
	if _, err := Verify(forged); err == nil {
		t.Error("a record signed by a different key verified")
	}
}

func TestKeyIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.key")
	a, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if PublicKey(a) != PublicKey(b) {
		t.Error("the key changed when loaded again")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", info.Mode().Perm())
	}
	os.WriteFile(path, []byte("junk"), 0o600)
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Error("a junk key file loaded")
	}
}

func TestSummarize(t *testing.T) {
	code := func(n int) *int { return &n }
	conns := []api.Connection{{Host: "a", Allowed: true}, {Host: "b"}, {Host: "c", Allowed: true}}
	for _, tc := range []struct {
		name  string
		execs []api.ExecRecord
		want  *int
	}{
		{"no command", nil, nil},
		{"the last one counts", []api.ExecRecord{{ExitCode: 1}, {ExitCode: 0}}, code(0)},
		{"a failure", []api.ExecRecord{{ExitCode: 0}, {ExitCode: 2}}, code(2)},
		{"timed out", []api.ExecRecord{{ExitCode: api.ExitTimedOut, Error: TimedOut}}, code(api.ExitTimedOut)},
		{"could not run", []api.ExecRecord{{ExitCode: 0}, {ExitCode: -1, Error: "the guest is gone"}}, nil},
	} {
		got := Summarize(api.Record{Execs: tc.execs, Connections: conns})
		if (got.ExitCode == nil) != (tc.want == nil) || got.ExitCode != nil && *got.ExitCode != *tc.want {
			t.Errorf("%s: exit code %v, want %v", tc.name, got.ExitCode, tc.want)
		}
		if got.ConnectionsAllowed != 2 || got.ConnectionsBlocked != 1 {
			t.Errorf("%s: %d allowed, %d blocked, want 2 and 1", tc.name, got.ConnectionsAllowed, got.ConnectionsBlocked)
		}
	}
	if got := Summarize(api.Record{}); got.ConnectionsAllowed != 0 || got.ConnectionsBlocked != 0 || got.ExitCode != nil {
		t.Errorf("empty record: %+v", got)
	}
}
