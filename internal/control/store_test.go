package control_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/control/storetest"
)

func TestFileStores(t *testing.T) {
	storetest.Tokens(t, func(t *testing.T) control.TokenStore {
		return &control.TokenFile{Path: filepath.Join(t.TempDir(), "tokens.json")}
	})
	storetest.State(t, func(t *testing.T) control.StateStore {
		// The suite's dates are fixed, so the file's month of usage is too.
		now := func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) }
		return &control.FileState{Path: filepath.Join(t.TempDir(), "sandboxes.json"), Now: now}
	})
}
