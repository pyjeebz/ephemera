package machine

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is the whole point of a persist disk: boot a machine, change something,
// throw the machine away, boot a *new* machine on the same disk, and find the
// change still there. It proves the overlay's writable upper lived on the disk
// rather than in RAM.
func TestPersistDiskKeepsStateAcrossBoots(t *testing.T) {
	base := requireVM(t)
	base.Init = AgentInit

	disk := filepath.Join(t.TempDir(), "persist.ext4")
	if err := MakePersistDisk(disk, 256); err != nil {
		t.Fatalf("MakePersistDisk: %v", err)
	}

	// First boot: write a marker, then destroy the machine.
	writeMarker := func() {
		cfg := base
		cfg.PersistDisk = disk
		cfg.Console = &bytes.Buffer{}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		m, err := Boot(ctx, cfg)
		if err != nil {
			t.Fatalf("first Boot: %v", err)
		}
		defer func() { _ = m.Destroy(context.Background()) }()
		if err := m.WaitAgent(ctx); err != nil {
			t.Fatalf("first agent never came up: %v", err)
		}
		if _, err := m.Exec(ctx, []string{"sh", "-c", "echo persisted-42 > /root/note && sync"}, nil, nil); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
	writeMarker()

	// Second boot on the same disk: the marker should still be there, even though
	// this is a brand-new machine with a fresh kernel and RAM.
	cfg := base
	cfg.PersistDisk = disk
	cfg.Console = &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("second Boot: %v", err)
	}
	defer func() { _ = m.Destroy(context.Background()) }()
	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("second agent never came up: %v", err)
	}

	var out bytes.Buffer
	if _, err := m.Exec(ctx, []string{"cat", "/root/note"}, &out, nil); err != nil {
		t.Fatalf("read marker on second boot: %v", err)
	}
	if strings.TrimSpace(out.String()) != "persisted-42" {
		t.Errorf("marker did not survive: got %q, want persisted-42 — state was not persisted", out.String())
	}
}

// The opposite guarantee: with no persist disk, changes must NOT survive — the
// default is an ephemeral, tmpfs-backed root.
func TestEphemeralMachineForgetsChanges(t *testing.T) {
	cfg := requireVM(t)
	cfg.Init = AgentInit
	cfg.Console = &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	m, err := Boot(ctx, cfg)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { _ = m.Destroy(context.Background()) }()
	if err := m.WaitAgent(ctx); err != nil {
		t.Fatalf("agent never came up: %v", err)
	}

	// The root is an overlay whose upper is tmpfs; a write goes to RAM, and the
	// underlying disk stays untouched. We cannot easily reboot the same machine,
	// so instead assert the root really is a RAM-backed overlay.
	var out bytes.Buffer
	if _, err := m.Exec(ctx, []string{"sh", "-c", "grep ' / ' /proc/mounts"}, &out, nil); err != nil {
		t.Fatalf("read mounts: %v", err)
	}
	if !strings.Contains(out.String(), "overlay") || !strings.Contains(out.String(), "upperdir=/mnt/upper") {
		t.Errorf("root is not a RAM overlay: %q", out.String())
	}
}
