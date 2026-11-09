package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/mcp"
	"github.com/pyjeebz/ephemera/internal/record"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

func TestRunReturnsSignedRecord(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"a.txt": "a", "b.txt": "bb"})
	res, err := s.client.Run(context.Background(), client.RunOptions{
		Dir:    dir,
		Exec:   api.ExecRequest{Argv: []string{"sh", "-c", "cat a.txt; exit 2"}, Env: []string{"SECRET=hunter2"}},
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Record == nil {
		t.Fatal("no record")
	}
	rec, err := record.Verify(*res.Record)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Sandbox != res.SandboxID || rec.Host != "test-host" || rec.Ended != "destroyed" || !rec.DiskDeleted {
		t.Errorf("record: %+v", rec)
	}
	if len(rec.Execs) != 1 || rec.Execs[0].ExitCode != 2 || rec.Execs[0].Argv[2] != "cat a.txt; exit 2" {
		t.Errorf("execs: %+v", rec.Execs)
	}
	if len(rec.Syncs) != 1 || rec.Syncs[0].Files < 2 {
		t.Errorf("syncs: %+v", rec.Syncs)
	}
	if strings.Contains(string(res.Record.Record), "hunter2") {
		t.Error("the record contains an environment value")
	}
	// The central server lists the key that signed it.
	hosts, err := s.client.Hosts(context.Background())
	if err != nil || len(hosts) != 1 || hosts[0].PublicKey != rec.HostKey {
		t.Errorf("hosts = %+v, %v; want the record's key", hosts, err)
	}
}

func TestCommandTimeout(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	var stderr bytes.Buffer
	start := time.Now()
	res, err := s.client.Run(context.Background(), client.RunOptions{
		Dir:    worktree(t, map[string]string{"x": "x"}),
		Exec:   api.ExecRequest{Argv: []string{"sleep", "30"}, TimeoutSeconds: 1},
		Stdout: io.Discard, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != api.ExitTimedOut || time.Since(start) > 10*time.Second {
		t.Errorf("exit %d after %v; want %d soon after 1s", res.ExitCode, time.Since(start), api.ExitTimedOut)
	}
	if !strings.Contains(stderr.String(), "timed out") {
		t.Errorf("stderr = %q", stderr.String())
	}
	rec, _ := record.Verify(*res.Record)
	if len(rec.Execs) != 1 || rec.Execs[0].Error != "timed out" {
		t.Errorf("execs: %+v", rec.Execs)
	}
}

func TestSandboxExpires(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	ctx := context.Background()
	sb, err := s.client.Create(ctx, api.Spec{MaxLifetimeSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); be.Live() != 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the sandbox outlived its lifetime")
		}
	}
	_, err = s.client.Exec(ctx, sb, api.ExecRequest{Argv: []string{"true"}}, io.Discard, io.Discard)
	var se *client.StatusError
	if !errors.As(err, &se) || se.Code != 410 {
		t.Errorf("exec after expiry: %v, want 410", err)
	}
	// Its record is still collected.
	sr, err := s.client.Destroy(ctx, sb.ID)
	if err != nil || sr == nil {
		t.Fatalf("destroy after expiry: %v, %v", sr, err)
	}
	rec, err := record.Verify(*sr)
	if err != nil || rec.Ended != "expired" {
		t.Errorf("record: %+v, %v", rec, err)
	}
}

// mcpSession drives an MCP server the way an agent would.
type mcpSession struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
	id  int
}

func (m *mcpSession) call(method string, params any) map[string]any {
	m.t.Helper()
	m.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.id, "method": method, "params": params})
	m.in.Write(append(b, '\n'))
	if !m.out.Scan() {
		m.t.Fatalf("%s: no response", method)
	}
	var resp map[string]any
	if err := json.Unmarshal(m.out.Bytes(), &resp); err != nil {
		m.t.Fatalf("%s: %v", method, err)
	}
	if resp["error"] != nil {
		m.t.Fatalf("%s: %v", method, resp["error"])
	}
	return resp["result"].(map[string]any)
}

func TestMCP(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"greeting.txt": "hello from the worktree\n"})
	var saved []*api.SignedRecord
	srv := &mcp.Server{Client: s.client, Dir: dir, Version: "test",
		SaveRecord: func(sr *api.SignedRecord) (string, error) { saved = append(saved, sr); return "/records/x.json", nil }}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { srv.Serve(context.Background(), inR, outW); outW.Close() }()
	defer inW.Close()
	m := &mcpSession{t: t, in: inW, out: bufio.NewScanner(outR)}

	init := m.call("initialize", map[string]any{"protocolVersion": mcp.ProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"}})
	if init["protocolVersion"] != mcp.ProtocolVersion || init["capabilities"].(map[string]any)["tools"] == nil {
		t.Errorf("initialize: %v", init)
	}
	inW.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))

	names := []string{}
	for _, tl := range m.call("tools/list", map[string]any{})["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "run_command,list_sandboxes,destroy_sandbox,verify_record" {
		t.Errorf("tools: %v", names)
	}

	res := m.call("tools/call", map[string]any{"name": "run_command", "arguments": map[string]any{
		"command": "cat greeting.txt; echo oops >&2; exit 3", "env": map[string]string{"X": "1"}}})
	sc := res["structuredContent"].(map[string]any)
	if res["isError"] != false || sc["exit_code"] != float64(3) || sc["stdout"] != "hello from the worktree\n" || sc["stderr"] != "oops\n" {
		t.Errorf("run_command: %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "exit code: 3") || !strings.Contains(text, "signed record: /records/x.json") {
		t.Errorf("text: %q", text)
	}
	if len(saved) != 1 {
		t.Errorf("%d records saved, want 1", len(saved))
	}

	res = m.call("tools/call", map[string]any{"name": "run_command", "arguments": map[string]any{}})
	if res["isError"] != true {
		t.Errorf("run_command without a command: %v", res)
	}
	if be.Live() != 0 {
		t.Error("a sandbox was left behind")
	}
}

// On a host without project snapshots, setup simply runs every time,
// before the command, and a failing setup stops the run.
func TestSetupWithoutSnapshots(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"x": "x"})
	var out bytes.Buffer
	var events []string
	res, err := s.client.Run(context.Background(), client.RunOptions{
		Dir:    dir,
		Setup:  &client.Setup{Argv: []string{"sh", "-c", "echo setting-up; mkdir -p deps && echo ok > deps/ready"}, SnapshotID: strings.Repeat("ab", 20), Key: make([]byte, 32)},
		Exec:   api.ExecRequest{Argv: []string{"cat", "deps/ready"}},
		Stdout: &out, Stderr: &out,
		Events: func(ev string, f map[string]any) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || out.String() != "setting-up\nok\n" {
		t.Errorf("exit %d, output %q", res.ExitCode, out.String())
	}
	if !strings.Contains(strings.Join(events, ","), "snapshot") {
		t.Errorf("events %v lack the snapshot attempt", events)
	}

	_, err = s.client.Run(context.Background(), client.RunOptions{
		Dir:    dir,
		Setup:  &client.Setup{Argv: []string{"sh", "-c", "exit 4"}},
		Exec:   api.ExecRequest{Argv: []string{"echo", "must not run"}},
		Stdout: &out, Stderr: &out,
	})
	if !errors.Is(err, client.ErrSetupFailed) {
		t.Errorf("failed setup: %v, want ErrSetupFailed", err)
	}
	if strings.Contains(out.String(), "must not run") {
		t.Error("the command ran after setup failed")
	}
}
