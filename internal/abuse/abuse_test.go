package abuse

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportsAddListPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports.jsonl")
	r, err := NewReports(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range []Report{
		{ShareID: "a", Reason: "copyright", Name: "a.mp4", Account: "alice"},
		{ShareID: "b", Reason: "malware", Note: "fake installer", Name: "b.zip", Account: "bob"},
		{ShareID: "a", Reason: "other", Name: "a.mp4", Account: "alice"},
	} {
		got, err := r.Add(rep)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got.ID, "rep_") || got.At.IsZero() {
			t.Fatalf("report not stamped: %+v", got)
		}
	}

	list := r.List(0)
	if len(list) != 3 || list[0].Reason != "other" || list[2].Reason != "copyright" {
		t.Fatalf("List not newest first: %+v", list)
	}
	if got := r.List(2); len(got) != 2 || got[0].Reason != "other" {
		t.Fatalf("List(2) = %+v", got)
	}
	if r.AccountFor("b") != "bob" || r.AccountFor("nope") != "" {
		t.Fatal("AccountFor wrong")
	}

	// a restart reloads everything from the file
	r2, err := NewReports(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.List(0); len(got) != 3 || got[1].Note != "fake installer" {
		t.Fatalf("reloaded = %+v", got)
	}
}

func TestReportsRefuse(t *testing.T) {
	r, _ := NewReports("")
	if _, err := r.Add(Report{ShareID: "a", Reason: "spam"}); !errors.Is(err, ErrBadReason) {
		t.Errorf("unknown reason: %v", err)
	}
	if _, err := r.Add(Report{ShareID: "a", Reason: "other", Note: strings.Repeat("é", MaxNote+1)}); !errors.Is(err, ErrNoteTooLong) {
		t.Errorf("long note: %v", err)
	}
	if _, err := r.Add(Report{ShareID: "a", Reason: "other", Note: strings.Repeat("é", MaxNote)}); err != nil {
		t.Errorf("note at the limit (counted in characters): %v", err)
	}
	if len(r.List(0)) != 1 {
		t.Error("refused reports were kept")
	}
}

func TestSuspensionsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suspended.txt")
	s, err := NewSuspensions(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Suspend("mallory"); err != nil {
		t.Fatal(err)
	}
	if err := s.Suspend("eve"); err != nil {
		t.Fatal(err)
	}
	if !s.Suspended("mallory") || s.Suspended("alice") {
		t.Fatal("Suspended wrong")
	}
	if err := s.Unsuspend("eve"); err != nil {
		t.Fatal(err)
	}
	if err := s.Suspend(""); err == nil {
		t.Error("suspended an empty account")
	}

	s2, err := NewSuspensions(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.List(); len(got) != 1 || got[0] != "mallory" {
		t.Fatalf("reloaded = %v, want [mallory]", got)
	}
}
