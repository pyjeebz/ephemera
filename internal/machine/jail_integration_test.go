package machine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/jail"
)

// buildJailHelper compiles eph-jail into a temp dir so the integration tests can
// point Config.JailHelper at it. In a real install it sits beside ephemerad; in
// `go test` the running binary is the test binary, so there is nothing to find
// beside it, and we build our own.
func buildJailHelper(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	out := filepath.Join(t.TempDir(), "eph-jail")

	cmd := exec.Command("go", "build", "-o", out, "./cmd/eph-jail")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build eph-jail: %v\n%s", err, b)
	}
	return out
}

func requireJail(t *testing.T) {
	t.Helper()
	if err := jail.Available(); err != nil {
		t.Skipf("jailing not available: %v", err)
	}
}

func TestJailedMachineBootsAndIsConfined(t *testing.T) {
	cfg := requireVM(t)
	requireJail(t)
	cfg.Init = AgentInit
	cfg.Jail = true
	cfg.JailHelper = buildJailHelper(t)

	var console bytes.Buffer
	cfg.Console = &console

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Destroy(context.Background()) }()

	jailDir, ok := m.Jailed()
	if !ok {
		t.Fatal("a jailed machine reported no jail")
	}

	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("agent never came up in the jail: %v\nconsole:\n%s", err, console.String())
	}

	// The command runs in the guest, over vsock — proving the whole path works
	// through the jail's relocated sockets.
	t.Run("exec works through the jail", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := m.Exec(ctx, []string{"echo", "jailed"}, &out, nil); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if strings.TrimSpace(out.String()) != "jailed" {
			t.Errorf("exec output = %q", out.String())
		}
	})

	pid := m.Pid()

	t.Run("the VMM is in its own user, mount, and pid namespaces", func(t *testing.T) {
		for _, ns := range []string{"user", "mnt", "pid"} {
			if sameNS(t, pid, ns) {
				t.Errorf("VMM shares the host %s namespace; it should be isolated", ns)
			}
		}
		// Network is shared on purpose, so the host firewall keeps applying.
		if !sameNS(t, pid, "net") {
			t.Error("VMM is in a separate network namespace; the host firewall would stop applying")
		}
	})

	t.Run("the VMM cannot see the host filesystem", func(t *testing.T) {
		// Its mount namespace contains only what eph-jail bound in — the images,
		// a few device nodes, proc. A host-visible root would be dozens.
		mi, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "mountinfo"))
		if err != nil {
			t.Skipf("cannot read mountinfo: %v", err)
		}
		lines := strings.Count(strings.TrimSpace(string(mi)), "\n") + 1
		if lines > 12 {
			t.Errorf("VMM sees %d mounts; a real chroot should expose only a handful:\n%s", lines, mi)
		}
		// The jail root should be the machine's own directory.
		if !strings.Contains(string(mi), filepath.Base(jailDir)) {
			t.Errorf("jail dir %s not the root of the VMM's mount namespace", jailDir)
		}
	})
}

func TestJailIsRemovedWithTheMachine(t *testing.T) {
	cfg := requireVM(t)
	requireJail(t)
	cfg.Init = AgentInit
	cfg.Jail = true
	cfg.JailHelper = buildJailHelper(t)
	cfg.Console = &bytes.Buffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("agent never came up: %v", err)
	}
	jailDir, _ := m.Jailed()

	if err := m.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(jailDir); !os.IsNotExist(err) {
		t.Errorf("jail %s survived Destroy", filepath.Base(jailDir))
	}
}

// sameNS reports whether pid shares namespace ns with this process.
func sameNS(t *testing.T, pid int, ns string) bool {
	t.Helper()
	mine, err := os.Readlink(filepath.Join("/proc/self/ns", ns))
	if err != nil {
		t.Fatalf("read own %s ns: %v", ns, err)
	}
	theirs, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "ns", ns))
	if err != nil {
		t.Fatalf("read vmm %s ns: %v", ns, err)
	}
	return mine == theirs
}
