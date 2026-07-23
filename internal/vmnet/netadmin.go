package vmnet

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
)

// The daemon holds no capability. Creating a TAP needs CAP_NET_ADMIN, so the
// daemon shells out to eph-netadmin — a small setcap'd binary that is the only
// privileged thing in the system. Everything in this file is the daemon side of
// that boundary: how it finds the helper and how it calls it. The privileged
// work itself is in tap.go, which only ever runs inside the helper.

// HelperName is the privileged TAP-management binary.
const HelperName = "eph-netadmin"

// HelperPath finds eph-netadmin. Like the jail helper it looks beside the
// running binary first — they are installed together — then falls back to PATH.
func HelperPath(configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("vmnet: helper %q not usable: %w", configured, err)
		}
		return configured, nil
	}
	if exe, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(exe), HelperName)
		if _, err := os.Stat(beside); err == nil {
			return beside, nil
		}
	}
	return HelperName, nil
}

// Available checks the helper is present and carries CAP_NET_ADMIN, so a daemon
// that cannot actually build networks says so at startup rather than on the
// first machine. It runs the helper's own `check`, which is the honest test:
// the capability lives on the helper, not on us, so only the helper can report
// whether it is really there.
func Available(helper string) error {
	path, err := HelperPath(helper)
	if err != nil {
		return err
	}
	out, err := exec.Command(path, "check").CombinedOutput()
	if err != nil {
		return fmt.Errorf("vmnet: %s cannot manage interfaces (run build/host-setup.sh): %s: %w",
			HelperName, trimmed(out), err)
	}
	return nil
}

// createTapVia asks the helper to create and configure a machine's TAP.
func createTapVia(helper, name string, addr, mask netip.Addr) error {
	return runHelper(helper, "create",
		"-tap", name,
		"-addr", addr.String(),
		"-mask", mask.String(),
	)
}

// removeTapVia asks the helper to remove a TAP.
func removeTapVia(helper, name string) error {
	return runHelper(helper, "destroy", "-tap", name)
}

func runHelper(helper string, args ...string) error {
	path, err := HelperPath(helper)
	if err != nil {
		return err
	}
	out, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("vmnet: %s %v: %s: %w", HelperName, args, trimmed(out), err)
	}
	return nil
}

func trimmed(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "(no output)"
	}
	return s
}
