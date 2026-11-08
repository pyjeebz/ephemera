package stream

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	io.WriteString(w.To(Stdout), "hello\n")
	io.WriteString(w.To(Stderr), "oops\n")
	big := strings.Repeat("x", maxChunk+10)
	io.WriteString(w.To(Stdout), big)
	w.Exit(-3)

	var out, errOut strings.Builder
	code := 0
	for {
		f, err := Read(&buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch f.Kind {
		case Stdout:
			out.Write(f.Data)
		case Stderr:
			errOut.Write(f.Data)
		case Exit:
			code = f.ExitCode()
		}
	}
	if out.String() != "hello\n"+big {
		t.Errorf("stdout: got %d bytes", out.Len())
	}
	if errOut.String() != "oops\n" {
		t.Errorf("stderr = %q", errOut.String())
	}
	if code != -3 {
		t.Errorf("exit code = %d, want -3", code)
	}
}

func TestReadRejectsTruncatedAndOversized(t *testing.T) {
	if _, err := Read(bytes.NewReader([]byte{Stdout, 0, 0})); err == nil {
		t.Error("truncated header: want error")
	}
	if _, err := Read(bytes.NewReader([]byte{Stdout, 0, 0, 0, 5, 'a'})); err == nil {
		t.Error("truncated payload: want error")
	}
	if _, err := Read(bytes.NewReader([]byte{Stdout, 0xff, 0xff, 0xff, 0xff})); err == nil {
		t.Error("oversized frame: want error")
	}
}
