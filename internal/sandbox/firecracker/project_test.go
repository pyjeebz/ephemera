package firecracker

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/egress"
)

// testVM is a VM that needs no Firecracker: it has no process to kill, and
// its egress proxy listens on a socket in dir.
func testVM(t *testing.T, dir string) *VM {
	t.Helper()
	return &VM{vsock: filepath.Join(dir, "vsock")}
}

// unlistenableVM is a VM whose egress proxy cannot listen, because its
// socket path is in a directory that does not exist.
func unlistenableVM(t *testing.T) *VM {
	t.Helper()
	return &VM{vsock: filepath.Join(t.TempDir(), "gone", "vsock")}
}

func testProxy(t *testing.T) *vmProxy {
	t.Helper()
	vp := &vmProxy{proxy: &egress.Proxy{}}
	t.Cleanup(func() {
		if vp.ln != nil {
			vp.ln.Close()
		}
	})
	return vp
}

func TestSwapVMBindsTheLoadedVM(t *testing.T) {
	be := &Backend{}
	vp := testProxy(t)
	vm := testVM(t, t.TempDir())
	loaded, reason, err := be.swapVM("sbx", vp, func() (*VM, error) { return vm, nil },
		func() (*VM, error) { t.Fatal("a loaded VM needs no fresh one"); return nil, nil })
	if err != nil || !loaded || reason != "" {
		t.Fatalf("loaded %v reason %q err %v", loaded, reason, err)
	}
	if got, err := be.vm("sbx"); err != nil || got != vm {
		t.Fatalf("vm %p err %v, want %p", got, err, vm)
	}
	if vp.ln == nil {
		t.Fatal("the proxy listens on no socket")
	}
}

func TestSwapVMFallsBackToAFreshVMWhenTheProxyCannotListen(t *testing.T) {
	be := &Backend{}
	vp := testProxy(t)
	fresh := testVM(t, t.TempDir())
	loaded, reason, err := be.swapVM("sbx", vp,
		func() (*VM, error) { return unlistenableVM(t), nil },
		func() (*VM, error) { return fresh, nil })
	if err != nil {
		t.Fatal(err)
	}
	if loaded || !strings.HasPrefix(reason, "egress proxy: ") {
		t.Fatalf("loaded %v reason %q", loaded, reason)
	}
	if got, err := be.vm("sbx"); err != nil || got != fresh {
		t.Fatalf("vm %p err %v, want the fresh VM %p", got, err, fresh)
	}
}

func TestSwapVMFallsBackToAFreshVMWhenTheLoadFails(t *testing.T) {
	be := &Backend{}
	vp := testProxy(t)
	fresh := testVM(t, t.TempDir())
	loaded, reason, err := be.swapVM("sbx", vp,
		func() (*VM, error) { return nil, errors.New("no memory") },
		func() (*VM, error) { return fresh, nil })
	if err != nil {
		t.Fatal(err)
	}
	if loaded || !strings.HasPrefix(reason, "restore failed (no memory)") {
		t.Fatalf("loaded %v reason %q", loaded, reason)
	}
	if got, err := be.vm("sbx"); err != nil || got != fresh {
		t.Fatalf("vm %p err %v, want the fresh VM %p", got, err, fresh)
	}
}

func TestSwapVMDropsASandboxWithNoVMLeft(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fresh func(*testing.T) (*VM, error)
	}{
		{"no fresh VM", func(*testing.T) (*VM, error) { return nil, errors.New("no memory") }},
		{"no fresh proxy", func(t *testing.T) (*VM, error) { return unlistenableVM(t), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := &Backend{vms: map[string]*VM{"sbx": {}}, proxies: map[string]*vmProxy{"sbx": {}}}
			vp := testProxy(t)
			loaded, _, err := be.swapVM("sbx", vp,
				func() (*VM, error) { return unlistenableVM(t), nil },
				func() (*VM, error) { return tc.fresh(t) })
			if err == nil || loaded {
				t.Fatalf("loaded %v err %v, want an error", loaded, err)
			}
			if vm, err := be.vm("sbx"); err == nil {
				t.Fatalf("the sandbox still has the dead VM %p", vm)
			}
			if err := be.Destroy(t.Context(), "sbx"); err != nil {
				t.Fatalf("destroy: %v", err)
			}
		})
	}
}
