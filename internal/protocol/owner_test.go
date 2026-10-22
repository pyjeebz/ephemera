package protocol

import (
	"strings"
	"testing"
)

func TestOwnerKeyCertifiesID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		key, id := NewOwnerKey()
		if len(id) != 22 {
			t.Fatalf("id %q: len = %d, want 22", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		if got, ok := ShareIDFor(key); !ok || got != id {
			t.Fatalf("ShareIDFor(key) = %q, %v; want %q", got, ok, id)
		}
		if !OwnsShare(id, key) {
			t.Fatalf("OwnsShare(%q, own key) = false", id)
		}
	}
}

// TestShareIDNeverLooksLikeAFlag: one raw id in 64 starts with '-';
// across 5000 draws (~78 expected) none may reach a command line.
func TestShareIDNeverLooksLikeAFlag(t *testing.T) {
	for range 5000 {
		key, id := NewOwnerKey()
		if strings.HasPrefix(id, "-") {
			t.Fatalf("id %q starts with '-'", id)
		}
		if !OwnsShare(id, key) {
			t.Fatalf("redrawn key does not certify its id %q", id)
		}
	}
}

// TestOwnsShareRefuses: anything but the id's own key is refused —
// another valid key, no key, the id itself, garbage, a truncated key.
func TestOwnsShareRefuses(t *testing.T) {
	key, id := NewOwnerKey()
	other, _ := NewOwnerKey()
	for name, k := range map[string]string{
		"other key": other,
		"empty":     "",
		"the id":    id,
		"garbage":   "not base64url !!",
		"truncated": key[:len(key)-2],
		"padded":    key + "=",
		"oversized": key + strings.Repeat("A", 8),
	} {
		if OwnsShare(id, k) {
			t.Errorf("%s: OwnsShare = true, want false", name)
		}
	}
	if OwnsShare("", key) {
		t.Error("empty id: OwnsShare = true, want false")
	}
}
