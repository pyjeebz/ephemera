// Package storetest checks that a control.TokenStore and control.StateStore
// behave the way the central server expects. Each store runs it in its own
// tests, so the file store and Postgres stay interchangeable.
package storetest

import (
	"context"
	"crypto/ed25519"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/record"
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

// Accounts checks an AccountStore. open returns a new, empty store.
func Accounts(t *testing.T, open func(t *testing.T) control.AccountStore) {
	ctx := context.Background()

	t.Run("github accounts", func(t *testing.T) {
		as := open(t)
		a, err := as.UpsertGitHubAccount(ctx, control.GitHubUser{ID: 42, Login: "octo", Name: "Octo Cat", AvatarURL: "https://a/1"})
		if err != nil {
			t.Fatal(err)
		}
		if a.ID != "gh-42" || a.Login != "octo" || a.Name != "Octo Cat" || a.AvatarURL != "https://a/1" {
			t.Errorf("account = %+v", a)
		}
		// A renamed GitHub user keeps their account.
		b, err := as.UpsertGitHubAccount(ctx, control.GitHubUser{ID: 42, Login: "octo-renamed"})
		if err != nil {
			t.Fatal(err)
		}
		if b.ID != "gh-42" || b.Login != "octo-renamed" || b.Name != "" {
			t.Errorf("after rename, account = %+v", b)
		}
	})

	t.Run("sessions", func(t *testing.T) {
		as := open(t)
		a, err := as.UpsertGitHubAccount(ctx, control.GitHubUser{ID: 7, Login: "seven"})
		if err != nil {
			t.Fatal(err)
		}
		secret, err := as.CreateSession(ctx, a.ID, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(secret, control.SessionPrefix) {
			t.Errorf("session secret %q lacks its prefix", secret)
		}
		if got, ok := as.CheckSession(ctx, secret); !ok || got.ID != a.ID || got.Login != "seven" {
			t.Errorf("CheckSession = %+v, %v", got, ok)
		}
		if _, ok := as.CheckSession(ctx, secret+"x"); ok {
			t.Error("an altered session was accepted")
		}
		if err := as.DeleteSession(ctx, secret); err != nil {
			t.Fatal(err)
		}
		if _, ok := as.CheckSession(ctx, secret); ok {
			t.Error("a deleted session was accepted")
		}
		old, err := as.CreateSession(ctx, a.ID, -time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := as.CheckSession(ctx, old); ok {
			t.Error("an expired session was accepted")
		}
	})

	t.Run("each account's tokens", func(t *testing.T) {
		as := open(t)
		mine, _, err := as.CreateToken(ctx, "laptop", "gh-1", control.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		theirs, _, err := as.CreateToken(ctx, "laptop", "gh-2", control.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		recs, err := as.AccountTokens(ctx, "gh-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1 || recs[0].ID != mine.ID {
			t.Errorf("gh-1's tokens = %+v", recs)
		}
		if err := as.RevokeAccountToken(ctx, "gh-1", theirs.ID); err != control.ErrNotFound {
			t.Errorf("revoking another account's token: %v, want ErrNotFound", err)
		}
		if err := as.RevokeAccountToken(ctx, "gh-1", mine.ID); err != nil {
			t.Errorf("revoking its own token: %v", err)
		}
		if recs, _ := as.AccountTokens(ctx, "gh-2"); len(recs) != 1 {
			t.Error("gh-1's revoke touched gh-2's token")
		}
	})
}

// Hosts checks a HostStore. open returns a new, empty store.
func Hosts(t *testing.T, open func(t *testing.T) control.HostStore) {
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(nil)
	pub := record.PublicKey(key)

	t.Run("join codes", func(t *testing.T) {
		hs := open(t)
		code, err := hs.CreateJoinCode(ctx, "alice", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(code, control.JoinCodePrefix) || len(code) != len(control.JoinCodePrefix)+26 {
			t.Errorf("code %q", code)
		}
		h, err := hs.JoinHost(ctx, code, control.HostRecord{Name: "box", PublicKey: pub})
		if err != nil {
			t.Fatal(err)
		}
		if h.ID == "" || h.Account != "alice" || h.Name != "box" || h.PublicKey != pub || h.Joined.IsZero() {
			t.Errorf("joined %+v", h)
		}
		if _, err := hs.JoinHost(ctx, code, control.HostRecord{Name: "other", PublicKey: pub}); err != control.ErrBadCode {
			t.Errorf("a code used twice: %v, want ErrBadCode", err)
		}
		old, _ := hs.CreateJoinCode(ctx, "alice", -time.Minute)
		if _, err := hs.JoinHost(ctx, old, control.HostRecord{Name: "late", PublicKey: pub}); err != control.ErrBadCode {
			t.Errorf("an expired code: %v, want ErrBadCode", err)
		}
		again, _ := hs.CreateJoinCode(ctx, "alice", time.Hour)
		if _, err := hs.JoinHost(ctx, again, control.HostRecord{Name: "BOX", PublicKey: pub}); err != control.ErrNameTaken {
			t.Errorf("same name in the account: %v, want ErrNameTaken", err)
		}
		if _, err := hs.JoinHost(ctx, again, control.HostRecord{Name: "bad name!", PublicKey: pub}); err == nil {
			t.Error("a bad host name was accepted")
		}
		// The same name is fine in another account.
		bobs, _ := hs.CreateJoinCode(ctx, "bob", time.Hour)
		if _, err := hs.JoinHost(ctx, bobs, control.HostRecord{Name: "box", PublicKey: pub}); err != nil {
			t.Errorf("same name in another account: %v", err)
		}
	})

	t.Run("lookup and removal", func(t *testing.T) {
		hs := open(t)
		code, _ := hs.CreateJoinCode(ctx, "alice", time.Hour)
		h, err := hs.JoinHost(ctx, code, control.HostRecord{Name: "box", PublicKey: pub})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := hs.Host(ctx, h.ID); err != nil || got.ID != h.ID || got.Account != "alice" {
			t.Errorf("Host = %+v, %v", got, err)
		}
		if _, err := hs.Host(ctx, "host_none"); err != control.ErrNotFound {
			t.Errorf("unknown host: %v, want ErrNotFound", err)
		}
		if list, _ := hs.AccountHosts(ctx, "alice"); len(list) != 1 {
			t.Errorf("alice's hosts = %+v", list)
		}
		if list, _ := hs.AccountHosts(ctx, "bob"); len(list) != 0 {
			t.Errorf("bob's hosts = %+v", list)
		}
		if err := hs.RemoveHost(ctx, "bob", h.ID); err != control.ErrNotFound {
			t.Errorf("bob removing alice's host: %v, want ErrNotFound", err)
		}
		if err := hs.RemoveHost(ctx, "alice", h.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := hs.Host(ctx, h.ID); err != control.ErrNotFound {
			t.Errorf("removed host still found: %v", err)
		}
	})
}
