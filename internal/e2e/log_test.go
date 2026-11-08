package e2e

import (
	"io"
	"log/slog"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
