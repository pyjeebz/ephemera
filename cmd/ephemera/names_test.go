package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
	"github.com/pyjeebz/ephemera/internal/sandbox/sandboxtest"
)

// Sandboxes are listed by name and removed by name, and a bad --name is
// refused before anything is made.
func TestNames(t *testing.T) {
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	t.Setenv("EPHEMERA_SERVER", ts.URL)
	t.Setenv("EPHEMERA_TOKEN", "eph_good")
	t.Setenv("EPHEMERA_CONFIG", filepath.Join(t.TempDir(), "c.json"))
	hs := &host.Server{Backend: &sandboxtest.Local{Root: t.TempDir()}, Name: "h1", Key: newKey(t)}
	code, err := cs.CreateJoinCode(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	hosttest.Connect(t, hs, ts.URL, code, api.HostHello{Name: "h1", URL: "http://h1", Capacity: 5})
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := realMain(args, strings.NewReader(""), &out, &errOut)
		return code, out.String() + errOut.String()
	}

	if code, out := run("run", "--name", "Bad_Name", "--", "true"); code != 2 || !strings.Contains(out, "lowercase letters, digits and hyphens") {
		t.Errorf("run --name Bad_Name: code %d\n%s", code, out)
	}
	c := &client.Client{Server: ts.URL, Token: "eph_good"}
	sb, err := c.CreateNamed(context.Background(), api.Spec{}, "api-tests")
	if err != nil {
		t.Fatal(err)
	}
	code2, out := run("ls")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); code2 != 0 || len(lines) != 2 ||
		!strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "api-tests") || !strings.Contains(lines[1], sb.ID) {
		t.Errorf("ls: code %d\n%s", code2, out)
	}
	if code, out := run("rm", "api-tests"); code != 0 {
		t.Errorf("rm api-tests: code %d\n%s", code, out)
	}
	if list, _ := c.List(context.Background()); len(list) != 0 {
		t.Errorf("after rm, running = %+v", list)
	}
}
