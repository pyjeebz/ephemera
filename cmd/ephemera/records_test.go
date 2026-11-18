package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/record"
)

// Records are filed under the sandbox's name, and verify finds them by
// name, by id, by path, or the newest of all.
func TestFindRecord(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EPHEMERA_RECORDS", dir)
	key := newKey(t)
	save := func(id, name string, age time.Duration) string {
		sr, err := record.Sign(key, api.Record{Version: 1, Sandbox: id, Host: "h1", Ended: "destroyed"})
		if err != nil {
			t.Fatal(err)
		}
		p, err := saveRecord(&sr, "", name)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := save("sbx_aaaa", "api-tests", 2*time.Hour)
	recent := save("sbx_bbbb", "api-tests", time.Hour)
	other := save("sbx_cccc", "lint", 30*time.Minute)
	unnamed := save("sbx_dddd", "", 10*time.Minute)
	bad := save("sbx_eeee", "../escape", 5*time.Minute)

	if filepath.Base(recent) != "api-tests-sbx_bbbb.json" || filepath.Base(unnamed) != "sbx_dddd.json" {
		t.Errorf("file names: %s, %s", recent, unnamed)
	}
	if filepath.Dir(bad) != dir || filepath.Base(bad) != "sbx_eeee.json" {
		t.Errorf("a bad name made it into the path: %s", bad)
	}
	if n := nameOfRecord(recent); n != "api-tests" {
		t.Errorf("nameOfRecord = %q", n)
	}
	if n := nameOfRecord(unnamed); n != "" {
		t.Errorf("nameOfRecord of an unnamed record = %q", n)
	}

	for what, want := range map[string]string{
		"api-tests": recent, // the newest of that name
		"lint":      other,
		"sbx_aaaa":  old,
		"sbx_dddd":  unnamed,
		"":          bad, // the newest of all
		old:         old,
	} {
		got, err := findRecord(what)
		if err != nil || got != want {
			t.Errorf("findRecord(%q) = %q, %v; want %q", what, got, err, want)
		}
	}
	for what, msg := range map[string]string{
		"docs":          "no record for a sandbox named docs",
		"sbx_aaa":       "no record for sandbox sbx_aaa", // not a prefix match
		"Bad Name":      "not a record file, a sandbox id or a sandbox name",
		"missing.json":  "no record file at missing.json",
		dir + "/x.json": "no record file at",
	} {
		if _, err := findRecord(what); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("findRecord(%q) error = %v; want %q", what, err, msg)
		}
	}

	t.Setenv("EPHEMERA_RECORDS", t.TempDir())
	if _, err := findRecord(""); err == nil || !strings.Contains(err.Error(), "no records in") {
		t.Errorf("empty folder: %v", err)
	}
}
