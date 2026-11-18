package api

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "7", "api-tests", "pr-1234", "amber-dune-42", strings.Repeat("a", MaxNameLen)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "API", "a_b", "sbx_1f2e3d4c5b6a7980", "a b", "café", "a.b", strings.Repeat("a", MaxNameLen+1)} {
		if ValidName(bad) == nil {
			t.Errorf("ValidName(%q) = nil, want an error", bad)
		}
	}
}

func TestNewName(t *testing.T) {
	for range 200 {
		n := NewName()
		if err := ValidName(n); err != nil {
			t.Fatalf("NewName() = %q: %v", n, err)
		}
		if parts := strings.Split(n, "-"); len(parts) != 3 || len(parts[2]) != 2 {
			t.Fatalf("NewName() = %q, want adjective-noun-NN", n)
		}
	}
}

func TestNameWordsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range append(append([]string{}, nameAdjectives...), nameNouns...) {
		if seen[w] {
			t.Errorf("%q is in the name words twice", w)
		}
		seen[w] = true
		if ValidName(w) != nil || strings.Contains(w, "-") {
			t.Errorf("%q isn't a plain lowercase word", w)
		}
	}
}
