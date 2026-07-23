package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startFakeGuest serves the guest side of the protocol behind a stand-in for
// Firecracker's multiplexer, so the handshake and the frame stream are both
// exercised the way they are in a real machine.
func startFakeGuest(t *testing.T, handle func(req ExecRequest, enc *json.Encoder)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "agent")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "v.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	})

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				if _, err := br.ReadString('\n'); err != nil { // CONNECT
					return
				}
				if _, err := io.WriteString(c, "OK 1\n"); err != nil {
					return
				}
				if handle == nil {
					return
				}
				var req ExecRequest
				if err := json.NewDecoder(br).Decode(&req); err != nil {
					return
				}
				handle(req, json.NewEncoder(c))
			}(c)
		}
	}()
	return sock
}

func TestExecStreamsOutputAndReturnsExitStatus(t *testing.T) {
	sock := startFakeGuest(t, func(_ ExecRequest, enc *json.Encoder) {
		_ = enc.Encode(Frame{Kind: FrameStdout, Data: []byte("out")})
		_ = enc.Encode(Frame{Kind: FrameStderr, Data: []byte("err")})
		_ = enc.Encode(Frame{Kind: FrameExit, Code: 0})
	})

	var stdout, stderr bytes.Buffer
	code, err := Exec(context.Background(), sock, ExecRequest{Cmd: []string{"true"}}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if stdout.String() != "out" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "err")
	}
}

func TestExecReportsNonZeroExitWithoutAnError(t *testing.T) {
	sock := startFakeGuest(t, func(_ ExecRequest, enc *json.Encoder) {
		_ = enc.Encode(Frame{Kind: FrameExit, Code: 42})
	})

	// A failing command is a result, not a transport failure — callers must be
	// able to tell the two apart.
	code, err := Exec(context.Background(), sock, ExecRequest{Cmd: []string{"false"}}, nil, nil)
	if err != nil {
		t.Fatalf("Exec returned an error for a non-zero exit: %v", err)
	}
	if code != 42 {
		t.Errorf("code = %d, want 42", code)
	}
}

func TestExecSendsTheCommandVerbatim(t *testing.T) {
	got := make(chan ExecRequest, 1)
	sock := startFakeGuest(t, func(req ExecRequest, enc *json.Encoder) {
		got <- req
		_ = enc.Encode(Frame{Kind: FrameExit})
	})

	want := ExecRequest{Cmd: []string{"sh", "-c", "echo 'quoted arg'"}, Cwd: "/tmp"}
	if _, err := Exec(context.Background(), sock, want, nil, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	select {
	case req := <-got:
		if strings.Join(req.Cmd, "\x00") != strings.Join(want.Cmd, "\x00") {
			t.Errorf("cmd = %q, want %q", req.Cmd, want.Cmd)
		}
		if req.Cwd != want.Cwd {
			t.Errorf("cwd = %q, want %q", req.Cwd, want.Cwd)
		}
	case <-time.After(time.Second):
		t.Fatal("guest never received the request")
	}
}

func TestExecPreservesBinaryOutput(t *testing.T) {
	// Frames carry []byte precisely so output that is not valid UTF-8 survives
	// the JSON transport.
	raw := []byte{0x00, 0x01, 0xff, 0xfe, '\n', 'o', 'k'}
	sock := startFakeGuest(t, func(_ ExecRequest, enc *json.Encoder) {
		_ = enc.Encode(Frame{Kind: FrameStdout, Data: raw})
		_ = enc.Encode(Frame{Kind: FrameExit})
	})

	var stdout bytes.Buffer
	if _, err := Exec(context.Background(), sock, ExecRequest{Cmd: []string{"x"}}, &stdout, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !bytes.Equal(stdout.Bytes(), raw) {
		t.Errorf("got % x, want % x", stdout.Bytes(), raw)
	}
}

func TestExecSurfacesAnAgentError(t *testing.T) {
	sock := startFakeGuest(t, func(_ ExecRequest, enc *json.Encoder) {
		_ = enc.Encode(Frame{Kind: FrameError, Error: "exec: \"nope\": not found"})
	})

	_, err := Exec(context.Background(), sock, ExecRequest{Cmd: []string{"nope"}}, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q lost the agent's message", err)
	}
}

func TestExecFailsWhenTheStreamEndsWithoutAnExit(t *testing.T) {
	// A guest that dies mid-command must not look like a success.
	sock := startFakeGuest(t, func(_ ExecRequest, enc *json.Encoder) {
		_ = enc.Encode(Frame{Kind: FrameStdout, Data: []byte("partial")})
	})

	_, err := Exec(context.Background(), sock, ExecRequest{Cmd: []string{"x"}}, io.Discard, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "exit status") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestExecStopsWhenTheContextIsCancelled(t *testing.T) {
	// A guest that never answers: cancelling has to unblock the reader, which
	// is why the client closes the connection on ctx.Done.
	sock := startFakeGuest(t, func(_ ExecRequest, _ *json.Encoder) {
		time.Sleep(5 * time.Second)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := Exec(ctx, sock, ExecRequest{Cmd: []string{"sleep"}}, nil, nil); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation took %s — the reader was not unblocked", elapsed)
	}
}

func TestWaitReadySucceedsWhenTheAgentAnswers(t *testing.T) {
	sock := startFakeGuest(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := WaitReady(ctx, sock); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReadyGivesUpWhenNothingIsThere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := WaitReady(ctx, filepath.Join(t.TempDir(), "absent.sock"))
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "never became ready") {
		t.Errorf("unhelpful error: %v", err)
	}
}
