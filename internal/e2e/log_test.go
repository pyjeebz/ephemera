package e2e

import (
	"crypto/ed25519"
	"io"
	"log/slog"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testKey is a fresh host signing key.
func testKey() ed25519.PrivateKey {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return key
}
