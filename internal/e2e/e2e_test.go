// Package e2e runs the CLI client, central server and host agent together.
package e2e

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/sandbox"
	"github.com/pyjeebz/ephemera/internal/sandbox/docker"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

const (
	userToken  = "user-token"
	hostSecret = "host-secret"
)

type stack struct {
	control *control.Server
	host    *host.Server
	client  *client.Client
}

func newStack(t *testing.T, be sandbox.Backend, image string) *stack {
	t.Helper()
	cs := &control.Server{Tokens: control.StaticToken{Name: "test", Token: userToken}, HostSecret: hostSecret, Log: discardLog()}
	cts := httptest.NewServer(cs.Handler())
	t.Cleanup(cts.Close)

	hs := &host.Server{Backend: be, Secret: hostSecret, DefaultImage: image, Log: discardLog()}
	hts := httptest.NewServer(hs.Handler())
	t.Cleanup(hts.Close)
	t.Cleanup(func() { hs.DestroyAll(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hs.Register(ctx, http.DefaultClient, cts.URL, hostSecret,
		api.RegisterHostRequest{Name: "test-host", URL: hts.URL, Capacity: 10}, 50*time.Millisecond)

	c := &client.Client{Server: cts.URL, Token: userToken}
	// Wait for the host to register.
	deadline := time.Now().Add(5 * time.Second)
	for {
		sb, err := c.Create(context.Background(), api.Spec{})
		if err == nil {
			c.Destroy(context.Background(), sb.ID)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host never registered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &stack{control: cs, host: hs, client: c}
}

func worktree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := exec.LookPath("git"); err == nil {
		exec.Command("git", "-C", dir, "init", "-q").Run()
	}
	return dir
}

func TestRunEndToEnd(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{
		".gitignore": ".env.local\n",
		".env.local": "SECRET=1\n",
		"hello.txt":  "hello from the worktree\n",
		"check.sh":   "#!/bin/sh\ncat hello.txt\necho \"greeting=$GREETING\"\nif [ -e .env.local ]; then echo leaked; fi\necho to-stderr >&2\nexit 3\n",
	})

	var stdout, stderr bytes.Buffer
	var events []string
	res, err := s.client.Run(context.Background(), client.RunOptions{
		Dir:    dir,
		Exec:   api.ExecRequest{Argv: []string{"sh", "check.sh"}, Env: []string{"GREETING=hi"}},
		Stdout: &stdout,
		Stderr: &stderr,
		Events: func(ev string, _ map[string]any) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if want := "hello from the worktree\ngreeting=hi\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "to-stderr\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
	if strings.Join(events, ",") != "sandbox,synced,destroyed" {
		t.Errorf("events = %v", events)
	}
	if be.Live() != 0 || s.host.Live() != 0 {
		t.Errorf("sandbox left behind: backend=%d host=%d", be.Live(), s.host.Live())
	}
	if list, _ := s.client.List(context.Background()); len(list) != 0 {
		t.Errorf("central server still lists %d sandboxes", len(list))
	}
}

func TestRunDestroysOnCancel(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	dir := worktree(t, map[string]string{"a.txt": "a"})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	_, err := s.client.Run(ctx, client.RunOptions{
		Dir:    dir,
		Exec:   api.ExecRequest{Argv: []string{"sleep", "30"}},
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if be.Live() != 0 {
		t.Error("cancelled run left its sandbox behind")
	}
}

func TestAuth(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	ctx := context.Background()

	bad := &client.Client{Server: s.client.Server, Token: "wrong"}
	if _, err := bad.Create(ctx, api.Spec{}); err == nil {
		t.Error("create with a wrong token: want error")
	}

	a, err := s.client.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.client.Destroy(ctx, a.ID)
	b, err := s.client.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.client.Destroy(ctx, b.ID)

	// One sandbox's token must not work on another sandbox.
	crossed := *b
	crossed.Token = a.Token
	_, err = s.client.Exec(ctx, &crossed, api.ExecRequest{Argv: []string{"true"}}, &bytes.Buffer{}, &bytes.Buffer{})
	var se *client.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Errorf("exec with another sandbox's token: err = %v, want 401", err)
	}

	// The token is returned once, never by list.
	list, _ := s.client.List(ctx)
	for _, sb := range list {
		if sb.Token != "" {
			t.Error("list returned a sandbox token")
		}
	}
}

func TestHostRefusesUnsafeUpload(t *testing.T) {
	be := &sandboxtest.Local{Root: t.TempDir()}
	s := newStack(t, be, "unused")
	ctx := context.Background()
	sb, err := s.client.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.client.Destroy(ctx, sb.ID)

	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644})
	tw.Write([]byte("x"))
	tw.Close()

	req, _ := http.NewRequest(http.MethodPut, sb.HostURL+"/v1/sandboxes/"+sb.ID+"/files", &raw)
	req.Header.Set("Authorization", "Bearer "+sb.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(be.Root, "..", "escape.txt")); err == nil {
		t.Error("file escaped the sandbox directory")
	}
}

func dockerAvailable() bool {
	return exec.Command("docker", "info").Run() == nil
}

func TestDockerBackend(t *testing.T) {
	if testing.Short() || !dockerAvailable() {
		t.Skip("docker not available")
	}
	const image = "alpine:3.20"
	if err := ensureImage(image); err != nil {
		t.Skip(err)
	}
	s := newStack(t, &docker.Backend{}, image)
	dir := worktree(t, map[string]string{
		"hello.txt": "hello from docker\n",
		"net.sh":    "#!/bin/sh\nif wget -q -T 3 -O /dev/null http://1.1.1.1; then echo online; else echo offline; fi\n",
	})

	var stdout, stderr bytes.Buffer
	res, err := s.client.Run(context.Background(), client.RunOptions{
		Dir:    dir,
		Exec:   api.ExecRequest{Argv: []string{"sh", "-c", "cat hello.txt && sh net.sh && exit 7"}},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("%v\nstderr: %s", err, stderr.String())
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}
	if want := "hello from docker\noffline\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q (network must be off by default)", stdout.String(), want)
	}
	if strings.TrimSpace(dockerPS(res.SandboxID)) != "" {
		t.Errorf("container for %s still exists", res.SandboxID)
	}
}

// ensureImage pulls image unless it is already present.
func ensureImage(image string) error {
	if exec.Command("docker", "image", "inspect", image).Run() == nil {
		return nil
	}
	if out, err := exec.Command("docker", "pull", "-q", image).CombinedOutput(); err != nil {
		return fmt.Errorf("cannot pull %s: %v\n%s", image, err, out)
	}
	return nil
}

// dockerPS lists containers belonging to a sandbox.
func dockerPS(id string) string {
	out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=ephemera.sandbox="+id).Output()
	return string(out)
}
