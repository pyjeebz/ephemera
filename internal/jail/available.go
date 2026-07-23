package jail

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HelperName is the binary that enters the namespaces.
const HelperName = "eph-jail"

// HelperPath finds the eph-jail binary. It looks next to the running executable
// first — the daemon and its helper are built and installed together — and falls
// back to PATH. An explicit path is returned unchanged.
func HelperPath(configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("jail: helper %q not usable: %w", configured, err)
		}
		return configured, nil
	}
	if exe, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(exe), HelperName)
		if _, err := os.Stat(beside); err == nil {
			return beside, nil
		}
	}
	// Fall back to PATH by returning the bare name; the launcher's LookPath
	// resolves it, and reports a clean error if it is genuinely absent.
	return HelperName, nil
}

// Available reports whether unprivileged jailing is possible on this host: the
// user-namespace machinery has to be enabled, or the clone that builds the jail
// fails with a permission error that looks like nothing in particular.
//
// It reads the kernel's knobs rather than trial-cloning, so the check is cheap
// and has no side effects. Two gates exist and either can be closed: Debian's
// unprivileged_userns_clone, and the max_user_namespaces limit.
func Available() error {
	// Debian/Ubuntu carry this extra switch; absent means "not gated here".
	if v, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(v)) == "0" {
			return fmt.Errorf("jail: unprivileged user namespaces are disabled (kernel.unprivileged_userns_clone=0)")
		}
	}
	if v, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if n, perr := strconv.Atoi(strings.TrimSpace(string(v))); perr == nil && n == 0 {
			return fmt.Errorf("jail: user namespaces are exhausted or disabled (user.max_user_namespaces=0)")
		}
	}
	return nil
}
