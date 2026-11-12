// Package storetest checks that a control.TokenStore and control.StateStore
// behave the way the central server expects. Each store runs it in its own
// tests, so the file store and Postgres stay interchangeable.
package storetest

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
)

// Tokens checks a TokenStore. open returns a new, empty store.
func Tokens(t *testing.T, open func(t *testing.T) control.TokenStore) {
	ctx := context.Background()

	t.Run("create, check, revoke", func(t *testing.T) {
		ts := open(t)
		if _, ok := ts.Check(ctx, "eph_nothing"); ok {
			t.Fatal("an empty store accepted a token")
		}
		rec, secret, err := ts.CreateToken(ctx, "laptop", "", control.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(secret, control.TokenPrefix) || rec.Hash != control.HashToken(secret) {
			t.Errorf("secret %q, record %+v", secret, rec)
		}
		p, ok := ts.Check(ctx, secret)
		if !ok || p.Name != "laptop" || p.Account != control.DefaultAccount {
			t.Errorf("Check = %+v, %v", p, ok)
		}
		if _, ok := ts.Check(ctx, secret+"x"); ok {
			t.Error("an altered token was accepted")
		}
		if err := ts.RevokeToken(ctx, rec.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := ts.Check(ctx, secret); ok {
			t.Error("a revoked token was accepted")
		}
		if err := ts.RevokeToken(ctx, "no-such-token"); err == nil {
			t.Error("revoking an unknown token: want an error")
		}
	})

	t.Run("accounts", func(t *testing.T) {
		ts := open(t)
		_, secret, err := ts.CreateToken(ctx, "ci", "alice", control.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if p, _ := ts.Check(ctx, secret); p.Account != "alice" {
			t.Errorf("account = %q, want alice", p.Account)
		}
		if _, _, err := ts.CreateToken(ctx, "x", "Not Valid", control.Limits{}); err == nil {
			t.Error("an account with capitals and a space was accepted")
		}
		if _, _, err := ts.CreateToken(ctx, "ci", "alice", control.Limits{}); err == nil {
			t.Error("a second token with the same name in one account was accepted")
		}
		if _, _, err := ts.CreateToken(ctx, " ", "", control.Limits{}); err == nil {
			t.Error("a token with no name was accepted")
		}
	})

	t.Run("limits and list", func(t *testing.T) {
		ts := open(t)
		_, secret, err := ts.CreateToken(ctx, "agent", "", control.Limits{MaxSandboxes: 2})
		if err != nil {
			t.Fatal(err)
		}
		if p, _ := ts.Check(ctx, secret); p.MaxSandboxes != 2 {
			t.Errorf("limits not stored: %+v", p)
		}
		if err := ts.SetTokenLimits(ctx, "agent", control.Limits{MaxSandboxes: 5, MaxMinutesPerDay: 60}); err != nil {
			t.Fatal(err)
		}
		if p, _ := ts.Check(ctx, secret); p.MaxSandboxes != 5 || p.MaxMinutesPerDay != 60 {
			t.Errorf("limits not changed: %+v", p)
		}
		if err := ts.SetTokenLimits(ctx, "agent", control.Limits{MaxSandboxes: -1}); err == nil {
			t.Error("negative limits were accepted")
		}
		if _, _, err := ts.CreateToken(ctx, "later", "", control.Limits{}); err != nil {
			t.Fatal(err)
		}
		recs, err := ts.ListTokens(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 2 || recs[0].Name != "agent" || recs[1].Name != "later" {
			t.Errorf("list = %+v, want agent then later", recs)
		}
	})
}

// State checks a StateStore. open returns a new, empty store.
func State(t *testing.T, open func(t *testing.T) control.StateStore) {
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)

	t.Run("sandboxes", func(t *testing.T) {
		st := open(t)
		a := api.Sandbox{ID: "sbx_a", Host: "h1", HostURL: "http://h1:7701", Token: "secret",
			Spec: api.Spec{Image: "node", CPUs: 2, MemoryMB: 1024}, CreatedAt: at, Owner: "laptop", Account: "alice"}
		b := api.Sandbox{ID: "sbx_b", Host: "h2", HostURL: "http://h2:7701", CreatedAt: at.Add(time.Second), Owner: "ci"}
		for _, sb := range []api.Sandbox{a, b} {
			if err := st.StartSandbox(ctx, sb); err != nil {
				t.Fatal(err)
			}
		}
		running, err := st.RunningSandboxes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sort.Slice(running, func(i, j int) bool { return running[i].ID < running[j].ID })
		if len(running) != 2 {
			t.Fatalf("running = %+v", running)
		}
		got := running[0]
		if got.Token != "" {
			t.Error("the sandbox's access token was stored")
		}
		a.Token = ""
		if got.ID != a.ID || got.Host != a.Host || got.HostURL != a.HostURL || got.Owner != a.Owner ||
			got.Account != a.Account || !reflect.DeepEqual(got.Spec, a.Spec) || !got.CreatedAt.Equal(a.CreatedAt) {
			t.Errorf("stored %+v, want %+v", got, a)
		}
		if running[1].Account != control.DefaultAccount && running[1].Account != "" {
			t.Errorf("a sandbox with no account came back in %q", running[1].Account)
		}

		if err := st.EndSandbox(ctx, "sbx_a", at.Add(time.Minute), control.EndDestroyed); err != nil {
			t.Fatal(err)
		}
		if err := st.EndSandbox(ctx, "sbx_a", at.Add(2*time.Minute), control.EndGone); err != nil {
			t.Errorf("ending a sandbox twice: %v", err)
		}
		running, _ = st.RunningSandboxes(ctx)
		if len(running) != 1 || running[0].ID != "sbx_b" {
			t.Errorf("after ending sbx_a, running = %+v", running)
		}
	})

	t.Run("usage", func(t *testing.T) {
		st := open(t)
		for _, u := range []control.Usage{
			{Account: "alice", Owner: "laptop", Day: "2026-11-10", Seconds: 30},
			{Account: "alice", Owner: "laptop", Day: "2026-11-11", Seconds: 60},
			{Account: "alice", Owner: "laptop", Day: "2026-11-11", Seconds: 15},
			{Account: "bob", Owner: "laptop", Day: "2026-11-11", Seconds: 5},
		} {
			if err := st.AddUsage(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		got, err := st.UsageSince(ctx, "2026-11-11")
		if err != nil {
			t.Fatal(err)
		}
		sum := map[string]float64{}
		for _, u := range got {
			if u.Day < "2026-11-11" {
				t.Errorf("usage from before the day asked for: %+v", u)
			}
			sum[u.Account+"/"+u.Owner+"/"+u.Day] += u.Seconds
		}
		want := map[string]float64{"alice/laptop/2026-11-11": 75, "bob/laptop/2026-11-11": 5}
		if len(sum) != len(want) || sum["alice/laptop/2026-11-11"] != 75 || sum["bob/laptop/2026-11-11"] != 5 {
			t.Errorf("usage = %v, want %v", sum, want)
		}
	})
}
