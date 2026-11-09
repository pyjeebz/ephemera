// Package mcp serves ephemera to coding agents over the Model Context
// Protocol: JSON-RPC 2.0 messages, one per line, on stdin and stdout.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
)

// ProtocolVersion is the MCP revision this server implements.
const ProtocolVersion = "2025-06-18"

// MaxOutput is how much of each output stream a tool result carries; the
// end is kept, since that is where errors usually are.
const MaxOutput = 64 << 10

// Server answers MCP requests.
type Server struct {
	Client  *client.Client
	Dir     string // default worktree for run_command
	Version string
	// SaveRecord stores a sandbox's signed record and returns its path.
	SaveRecord func(*api.SignedRecord) (string, error)
	// VerifyRecord checks a saved record and describes it.
	VerifyRecord func(ctx context.Context, path string) (string, bool, error)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests from r and writes responses to w until r ends or
// ctx is done. Requests are handled concurrently, so a long command does
// not hold up a ping.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	var mu sync.Mutex
	send := func(v any) {
		b, _ := json.Marshal(v)
		mu.Lock()
		defer mu.Unlock()
		w.Write(append(b, '\n'))
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification, such as notifications/initialized
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.handle(ctx, req)
			resp := response{JSONRPC: "2.0", ID: req.ID, Result: result}
			if err != nil {
				resp.Result = nil
				resp.Error = err
			}
			send(resp)
		}()
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := ProtocolVersion
		if p.ProtocolVersion != "" && p.ProtocolVersion <= ProtocolVersion {
			v = p.ProtocolVersion // speak the client's revision if it is older
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ephemera", "version": s.Version},
			"instructions": "Run commands in short-lived, locked-down sandboxes instead of on this machine. " +
				"Each run_command gets a fresh microVM with the current worktree, no network unless you list hosts in " +
				"allow, and is destroyed afterwards with a signed record of what ran.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		return s.call(ctx, p.Name, p.Arguments), nil
	default:
		return nil, &rpcError{-32601, "method not found: " + req.Method}
	}
}

var tools = []map[string]any{
	{
		"name": "run_command",
		"description": "Run a shell command in a fresh sandbox holding the worktree's git-tracked files, and destroy it " +
			"afterwards. Returns the exit code, output, network summary and the path of the sandbox's signed record. " +
			"The sandbox has no network unless hosts are listed in allow.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":         map[string]any{"type": "string", "description": "shell command, run with sh -c in the worktree"},
				"allow":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "hosts it may reach, e.g. registry.npmjs.org or *.github.com"},
				"network_open":    map[string]any{"type": "boolean", "description": "allow any public host instead of a list"},
				"timeout_seconds": map[string]any{"type": "integer", "description": "stop the command after this long (exit code 124)"},
				"include":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "ignored files to send anyway, e.g. .env.local"},
				"env":             map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"dir":             map[string]any{"type": "string", "description": "worktree to sync (default: where ephemera mcp was started)"},
			},
			"required": []string{"command"},
		},
	},
	{
		"name":        "list_sandboxes",
		"description": "List running sandboxes.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "destroy_sandbox",
		"description": "Destroy a sandbox and save its signed record.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []string{"id"},
		},
	},
	{
		"name":        "verify_record",
		"description": "Check a sandbox record's signature against the host keys the central server knows, and show what the sandbox did.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	},
}

func textResult(text string, isError bool, structured any) map[string]any {
	r := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
	if structured != nil {
		r["structuredContent"] = structured
	}
	return r
}

func (s *Server) call(ctx context.Context, name string, args json.RawMessage) map[string]any {
	switch name {
	case "run_command":
		return s.runCommand(ctx, args)
	case "list_sandboxes":
		list, err := s.Client.List(ctx)
		if err != nil {
			return textResult(err.Error(), true, nil)
		}
		var b strings.Builder
		if len(list) == 0 {
			b.WriteString("no sandboxes are running")
		}
		for _, sb := range list {
			fmt.Fprintf(&b, "%s on %s, network %s, up %v\n", sb.ID, sb.Host, sb.Spec.Network, time.Since(sb.CreatedAt).Round(time.Second))
		}
		return textResult(b.String(), false, map[string]any{"sandboxes": list})
	case "destroy_sandbox":
		var a struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(args, &a) != nil || a.ID == "" {
			return textResult("id is required", true, nil)
		}
		sr, err := s.Client.Destroy(ctx, a.ID)
		if err != nil {
			return textResult(err.Error(), true, nil)
		}
		msg := "destroyed " + a.ID
		if sr != nil && s.SaveRecord != nil {
			if p, err := s.SaveRecord(sr); err == nil {
				msg += "; signed record saved to " + p
			}
		}
		return textResult(msg, false, nil)
	case "verify_record":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &a) != nil || a.Path == "" {
			return textResult("path is required", true, nil)
		}
		if s.VerifyRecord == nil {
			return textResult("verifying is not available", true, nil)
		}
		text, ok, err := s.VerifyRecord(ctx, a.Path)
		if err != nil {
			return textResult(err.Error(), true, nil)
		}
		return textResult(text, false, map[string]any{"verified": ok})
	default:
		return textResult("unknown tool "+name, true, nil)
	}
}

type runArgs struct {
	Command        string            `json:"command"`
	Allow          []string          `json:"allow"`
	NetworkOpen    bool              `json:"network_open"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Include        []string          `json:"include"`
	Env            map[string]string `json:"env"`
	Dir            string            `json:"dir"`
}

func (s *Server) runCommand(ctx context.Context, raw json.RawMessage) map[string]any {
	var a runArgs
	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Command) == "" {
		return textResult("command is required", true, nil)
	}
	if a.NetworkOpen && len(a.Allow) > 0 {
		return textResult("use either network_open or allow, not both", true, nil)
	}
	if a.TimeoutSeconds < 0 {
		return textResult("timeout_seconds cannot be negative", true, nil)
	}
	spec := api.Spec{}
	switch {
	case a.NetworkOpen:
		spec.Network = api.NetworkOpen
	case len(a.Allow) > 0:
		spec.Network, spec.Allow = api.NetworkAllow, a.Allow
	}
	exec := api.ExecRequest{Argv: []string{"sh", "-c", a.Command}, TimeoutSeconds: a.TimeoutSeconds}
	if a.TimeoutSeconds > 0 {
		spec.MaxLifetimeSeconds = a.TimeoutSeconds + 300
	}
	for k, v := range a.Env {
		exec.Env = append(exec.Env, k+"="+v)
	}
	dir := a.Dir
	if dir == "" {
		dir = s.Dir
	}
	stdout, stderr := &tailBuffer{max: MaxOutput}, &tailBuffer{max: MaxOutput}
	res, err := s.Client.Run(ctx, client.RunOptions{Dir: dir, Include: a.Include, Spec: spec, Exec: exec, Stdout: stdout, Stderr: stderr})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			err = errors.New("cancelled; the sandbox was destroyed")
		}
		return textResult("ephemera could not run the command: "+err.Error(), true, nil)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "exit code: %d\n", res.ExitCode)
	if res.ExitCode == api.ExitTimedOut && a.TimeoutSeconds > 0 {
		fmt.Fprintf(&b, "(the command timed out after %ds)\n", a.TimeoutSeconds)
	}
	fmt.Fprintf(&b, "--- stdout%s ---\n%s\n", stdout.note(), stdout.String())
	fmt.Fprintf(&b, "--- stderr%s ---\n%s\n", stderr.note(), stderr.String())
	allowed, blocked := 0, []string{}
	for _, c := range res.Connections {
		if c.Allowed {
			allowed++
		} else {
			blocked = append(blocked, fmt.Sprintf("%s:%d", c.Host, c.Port))
		}
	}
	if len(res.Connections) > 0 {
		fmt.Fprintf(&b, "network: %d allowed, %d blocked", allowed, len(blocked))
		if len(blocked) > 0 {
			fmt.Fprintf(&b, " (blocked: %s; add them to allow if the command needs them)", strings.Join(dedupe(blocked), ", "))
		}
		b.WriteString("\n")
	}
	recPath := ""
	if res.Record != nil && s.SaveRecord != nil {
		if p, err := s.SaveRecord(res.Record); err == nil {
			recPath = p
			fmt.Fprintf(&b, "signed record: %s\n", p)
		}
	}
	return textResult(b.String(), false, map[string]any{
		"exit_code": res.ExitCode, "stdout": stdout.String(), "stderr": stderr.String(),
		"sandbox": res.SandboxID, "duration_ms": res.Duration.Milliseconds(),
		"network": map[string]any{"allowed": allowed, "blocked": blocked}, "record": recPath,
	})
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	dropped int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.dropped += over
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func (t *tailBuffer) note() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dropped == 0 {
		return ""
	}
	return fmt.Sprintf(", first %d bytes left out", t.dropped)
}
