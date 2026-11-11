// Package version reports which ephemera a binary is.
package version

import "runtime/debug"

// String is the stamped version when the release build set one, else the
// module version Go recorded, as with "go install ...@v0.1.0", else "dev".
func String(stamped string) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
