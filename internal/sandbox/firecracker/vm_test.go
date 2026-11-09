package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/stream"
	"github.com/pyjeebz/ephemera/internal/worktree"
)

// guestImage returns the directory holding vmlinux and rootfs.ext4 built by
// deploy/guest/build-image.sh, or skips the test. These tests need a Linux
// host with KVM and Firecracker.
func guestImage(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("EPHEMERA_GUEST_IMAGE")
	if dir == "" {
		t.Skip("EPHEMERA_GUEST_IMAGE not set (a directory from deploy/guest/build-image.sh)")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm")
	}
	if _, err := exec.LookPath("firecracker"); err != nil {
		t.Skip("firecracker not installed")
	}
	return dir
}

func TestBootUploadExec(t *testing.T) {
	img := guestImage(t)
	ctx := context.Background()
	start := time.Now()
	vm, err := Boot(ctx, VMConfig{
		Dir:    t.TempDir(),
		Kernel: filepath.Join(img, "vmlinux"),
		RootFS: filepath.Join(img, "rootfs.ext4"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Kill()
	t.Logf("boot to guest agent ready: %v", time.Since(start).Round(time.Millisecond))

	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello from the host\n"), 0o644)
	var tarball bytes.Buffer
	if err := worktree.WriteTar(&tarball, src, []string{"hello.txt"}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, "http://guest/files", &tarball)
	resp, err := vm.HTTP.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("upload: %v %v", resp, err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(api.ExecRequest{Argv: []string{"sh", "-c",
		"cat hello.txt; hostname; id -u; pwd; ip -o link show lo 2>/dev/null | grep -o 'state [A-Z]*' || cat /sys/class/net/lo/operstate; exit 3"}})
	resp, err = vm.HTTP.Post("http://guest/exec", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out, errOut strings.Builder
	code := -1
	for code < 0 {
		f, err := stream.Read(resp.Body)
		if err != nil {
			t.Fatalf("stream: %v (stdout so far %q)", err, out.String())
		}
		switch f.Kind {
		case stream.Stdout:
			out.Write(f.Data)
		case stream.Stderr:
			errOut.Write(f.Data)
		case stream.Exit:
			code = f.ExitCode()
		case stream.Error:
			t.Fatalf("exec failed: %s", f.Data)
		}
	}
	t.Logf("guest output:\n%s", out.String())
	if code != 3 {
		t.Errorf("exit code = %d, want 3 (stderr %q)", code, errOut.String())
	}
	for _, want := range []string{"hello from the host", "ephemera", "\n0\n", "/workspace"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if err := vm.Kill(); err != nil {
		t.Fatal(err)
	}
}
