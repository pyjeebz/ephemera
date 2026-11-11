package version

import "testing"

func TestString(t *testing.T) {
	if got := String("v1.2.3"); got != "v1.2.3" {
		t.Errorf("stamped: %q", got)
	}
	// A test binary has no module version, so it falls back to "dev".
	if got := String("dev"); got != "dev" {
		t.Errorf("unstamped: %q", got)
	}
}
