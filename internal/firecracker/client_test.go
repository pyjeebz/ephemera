package firecracker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// call records one request the client made.
type call struct {
	method string
	path   string
	body   map[string]any
}

// fakeVMM stands in for the firecracker process: a real HTTP server on a real
// Unix socket, so the client's transport is exercised rather than mocked.
type fakeVMM struct {
	mu     sync.Mutex
	calls  []call
	status int    // when non-zero, every request gets this status
	fault  string // and this fault_message
}

func startFakeVMM(t *testing.T) (*fakeVMM, string) {
	t.Helper()
	// Unix socket paths are limited to ~108 bytes, so keep it short rather than
	// nesting under a long test name.
	dir, err := os.MkdirTemp("", "fcvmm")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "api.sock")

	f := &fakeVMM{}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return f, sock
}

func (f *fakeVMM) serve(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	f.calls = append(f.calls, call{method: r.Method, path: r.URL.Path, body: body})
	status, fault := f.status, f.fault
	f.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"fault_message": fault})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		_ = json.NewEncoder(w).Encode(InstanceInfo{ID: "anonymous", State: "Running", VmmVersion: "1.16.1"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeVMM) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeVMM) failWith(status int, fault string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.fault = status, fault
}

func TestClientSendsTheBootSequence(t *testing.T) {
	vmm, sock := startFakeVMM(t)
	c := NewClient(sock)
	ctx := context.Background()

	if err := c.SetBootSource(ctx, BootSource{KernelImagePath: "/k/vmlinux", BootArgs: "console=ttyS0"}); err != nil {
		t.Fatalf("SetBootSource: %v", err)
	}
	if err := c.SetDrive(ctx, Drive{DriveID: "rootfs", PathOnHost: "/r/rootfs.ext4", IsRootDevice: true}); err != nil {
		t.Fatalf("SetDrive: %v", err)
	}
	if err := c.SetMachineConfig(ctx, MachineConfig{VcpuCount: 2, MemSizeMib: 512}); err != nil {
		t.Fatalf("SetMachineConfig: %v", err)
	}
	if err := c.SetVsock(ctx, Vsock{GuestCID: 3, UDSPath: "/r/v.sock"}); err != nil {
		t.Fatalf("SetVsock: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := vmm.recorded()
	want := []struct{ method, path string }{
		{"PUT", "/boot-source"},
		{"PUT", "/drives/rootfs"}, // drive id belongs in the path, not just the body
		{"PUT", "/machine-config"},
		{"PUT", "/vsock"},
		{"PUT", "/actions"},
	}
	if len(got) != len(want) {
		t.Fatalf("made %d calls, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].method != w.method || got[i].path != w.path {
			t.Errorf("call %d: got %s %s, want %s %s", i, got[i].method, got[i].path, w.method, w.path)
		}
	}

	if got[0].body["kernel_image_path"] != "/k/vmlinux" {
		t.Errorf("kernel path not sent: %+v", got[0].body)
	}
	if got[1].body["is_root_device"] != true {
		t.Errorf("root device flag not sent: %+v", got[1].body)
	}
	if got[2].body["vcpu_count"] != float64(2) || got[2].body["mem_size_mib"] != float64(512) {
		t.Errorf("machine config not sent: %+v", got[2].body)
	}
	// The action type is the only thing distinguishing a boot from any other
	// action, so it is worth asserting explicitly.
	if got[4].body["action_type"] != "InstanceStart" {
		t.Errorf("action_type = %v, want InstanceStart", got[4].body["action_type"])
	}
}

func TestClientSurfacesTheVmmsFaultMessage(t *testing.T) {
	vmm, sock := startFakeVMM(t)
	vmm.failWith(http.StatusBadRequest, "Kvm error: Permission denied")

	err := NewClient(sock).Start(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	// A bare "400 Bad Request" is useless; the VMM's own explanation is the
	// whole reason we decode the fault body.
	if !strings.Contains(err.Error(), "Kvm error: Permission denied") {
		t.Errorf("error %q does not carry the fault message", err)
	}
}

func TestInfoDecodesInstanceState(t *testing.T) {
	_, sock := startFakeVMM(t)
	info, err := NewClient(sock).Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.State != "Running" || info.VmmVersion != "1.16.1" {
		t.Errorf("got %+v", info)
	}
}

func TestWaitReadyReturnsOnceTheApiAnswers(t *testing.T) {
	_, sock := startFakeVMM(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := NewClient(sock).WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReadyGivesUpWhenNothingIsListening(t *testing.T) {
	// A path with no server behind it: the poll must end with the deadline
	// rather than spinning forever.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := NewClient(filepath.Join(t.TempDir(), "absent.sock")).WaitReady(ctx)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "never became ready") {
		t.Errorf("unhelpful error: %v", err)
	}
}
