package machine

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
)

// This drives an interactive shell in a real guest without a real terminal:
// feed it a command and 'exit' as input, and read what the pty sends back. It
// proves the whole PTY path — the guest allocating a pseudo-terminal, running a
// shell on it, and relaying bytes — works end to end.
func TestShellRunsAnInteractiveSession(t *testing.T) {
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

	// Run a command whose output is the *result* of shell evaluation, not just
	// the echoed input — so the check proves the shell actually ran it. Then exit
	// so the session ends and Shell returns.
	input := strings.NewReader("echo result-$((6*7))\nexit\n")
	var out bytes.Buffer
	if err := m.Shell(ctx, agent.ExecRequest{Rows: 24, Cols: 80, Term: "xterm-256color"}, input, &out, nil); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if !strings.Contains(out.String(), "result-42") {
		t.Errorf("shell did not evaluate the command; output was:\n%q", out.String())
	}
}
