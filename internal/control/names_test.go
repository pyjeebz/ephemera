package control_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/host/hosttest"
)

// A sandbox gets the name it asks for, or one the server makes up. Names
// are unique among running sandboxes, and a sandbox can be destroyed by
// its name.
func TestSandboxNames(t *testing.T) {
	cs := &control.Server{Tokens: control.StaticToken{Name: "laptop", Token: "eph_good"}}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	hs, _ := newHost(t, "h1")
	hts := hosttest.Serve(t, hs)
	hosttest.Connect(t, hs, ts.URL, joinCode(t, cs, ""), api.HostHello{Name: "h1", URL: hts.URL, Capacity: 10})
	ctx := context.Background()
	c := &client.Client{Server: ts.URL, Token: "eph_good"}
	status := func(err error) int {
		var se *client.StatusError
		if errors.As(err, &se) {
			return se.Code
		}
		return 0
	}

	made, err := c.Create(ctx, api.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if api.ValidName(made.Name) != nil {
		t.Errorf("generated name %q isn't a valid name", made.Name)
	}

	named, err := c.CreateNamed(ctx, api.Spec{}, "api-tests")
	if err != nil {
		t.Fatal(err)
	}
	if named.Name != "api-tests" {
		t.Errorf("name = %q, want api-tests", named.Name)
	}
	if _, err := c.CreateNamed(ctx, api.Spec{}, "api-tests"); status(err) != http.StatusConflict {
		t.Errorf("second api-tests: %v, want 409", err)
	}
	for _, bad := range []string{"API", "a_b", "-x", "sbx_1f2e3d4c5b6a7980"} {
		if _, err := c.CreateNamed(ctx, api.Spec{}, bad); status(err) != http.StatusBadRequest {
			t.Errorf("name %q: %v, want 400", bad, err)
		}
	}

	list, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, sb := range list {
		names[sb.Name] = true
	}
	if len(list) != 2 || !names["api-tests"] || !names[made.Name] {
		t.Errorf("listed %+v", list)
	}

	if _, err := c.Destroy(ctx, "api-tests"); err != nil {
		t.Fatalf("destroy by name: %v", err)
	}
	if _, err := c.Destroy(ctx, "api-tests"); status(err) != http.StatusNotFound {
		t.Errorf("destroying it again: %v, want 404", err)
	}
	// Once it's gone, the name is free.
	again, err := c.CreateNamed(ctx, api.Spec{}, "api-tests")
	if err != nil {
		t.Fatalf("reusing an ended sandbox's name: %v", err)
	}
	if again.ID == named.ID {
		t.Error("the new sandbox has the old one's id")
	}
	// Ids still work.
	if _, err := c.Destroy(ctx, made.ID); err != nil {
		t.Errorf("destroy by id: %v", err)
	}
}

// Another account's sandbox can't be found, or blocked, by its name.
func TestSandboxNamesPerAccount(t *testing.T) {
	tokens := &control.TokenFile{Path: t.TempDir() + "/tokens.json"}
	_, alice, err := tokens.CreateToken(context.Background(), "laptop", "alice", control.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, bob, err := tokens.CreateToken(context.Background(), "ci", "bob", control.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	cs := &control.Server{Tokens: tokens}
	ts := httptest.NewServer(cs.Handler())
	defer ts.Close()
	for _, acct := range []string{"alice", "bob"} {
		hs, _ := newHost(t, "h-"+acct)
		hts := hosttest.Serve(t, hs)
		hosttest.Connect(t, hs, ts.URL, joinCode(t, cs, acct), api.HostHello{Name: "h-" + acct, URL: hts.URL, Capacity: 5})
	}
	ctx := context.Background()
	a := &client.Client{Server: ts.URL, Token: alice}
	b := &client.Client{Server: ts.URL, Token: bob}
	if _, err := a.CreateNamed(ctx, api.Spec{}, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateNamed(ctx, api.Spec{}, "web"); err != nil {
		t.Errorf("bob's web while alice has one: %v", err)
	}
	if _, err := b.Destroy(ctx, "web"); err != nil {
		t.Fatalf("bob destroying his web: %v", err)
	}
	if _, err := b.Destroy(ctx, "web"); err == nil {
		t.Error("bob destroyed alice's web by name")
	}
	if l, _ := a.List(ctx); len(l) != 1 || l[0].Name != "web" {
		t.Errorf("alice's sandboxes = %+v", l)
	}
}
