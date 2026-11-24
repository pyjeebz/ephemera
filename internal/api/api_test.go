package api

import "testing"

// A bad allow-list entry must fail the shared contract, so every producer
// rejects it before a run exists.
func TestSpecValidateAllowPattern(t *testing.T) {
	good := Spec{Network: NetworkAllow, Allow: []string{"example.com", "*.example.com", "*", "db.example.com:5432"}}
	if err := good.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	for _, bad := range []string{"1.2.3.4", "1.2.3.4:443", "x.com/pkg", "x.com:abc", "x.com:0", "a*.example.com"} {
		s := Spec{Network: NetworkAllow, Allow: []string{"example.com", bad}}
		if err := s.Validate(); err == nil {
			t.Errorf("Validate() with allow entry %q: want error", bad)
		}
	}
	// Network defaults to allow when entries are given, so a bad entry with
	// an empty Network is still rejected.
	s := Spec{Allow: []string{"http://x.com"}}
	if err := s.Validate(); err == nil {
		t.Error("Validate() with no network and a path entry: want error")
	}
}
