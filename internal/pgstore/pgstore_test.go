package pgstore_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
	"github.com/pyjeebz/ephemera/internal/control/storetest"
	"github.com/pyjeebz/ephemera/internal/pgstore"
	"github.com/pyjeebz/ephemera/internal/pgstore/pgtest"
)

func TestStore(t *testing.T) {
	storetest.Tokens(t, func(t *testing.T) control.TokenStore { s, _ := pgtest.Open(t); return s })
	storetest.State(t, func(t *testing.T) control.StateStore { s, _ := pgtest.Open(t); return s })
	storetest.Accounts(t, func(t *testing.T) control.AccountStore { s, _ := pgtest.Open(t); return s })
	storetest.Hosts(t, func(t *testing.T) control.HostStore { s, _ := pgtest.Open(t); return s })
}

// Opening a database again applies each migration once.
func TestOpenTwice(t *testing.T) {
	_, url := pgtest.Open(t)
	ctx := context.Background()
	again, err := pgstore.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("migrations/*.sql")
	if n != len(files) {
		t.Errorf("%d migrations recorded, want %d", n, len(files))
	}
}

// Two accounts may each have a token with the same name; revoking by that
// name then asks for the id.
func TestSameNameInTwoAccounts(t *testing.T) {
	s, _ := pgtest.Open(t)
	ctx := context.Background()
	a, _, err := s.CreateToken(ctx, "laptop", "alice", control.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken(ctx, "laptop", "bob", control.Limits{}); err != nil {
		t.Fatalf("same name in another account: %v", err)
	}
	if err := s.RevokeToken(ctx, "laptop"); err == nil {
		t.Error("revoking an ambiguous name succeeded")
	}
	if err := s.RevokeToken(ctx, a.ID); err != nil {
		t.Errorf("revoking by id: %v", err)
	}
}

// Runs keep their names after they end; sandboxes from before names have
// none.
func TestRunNames(t *testing.T) {
	s, _ := pgtest.Open(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	for i, name := range []string{"api-tests", "", "api-tests"} {
		sb := api.Sandbox{ID: fmt.Sprintf("sbx_%d", i), Name: name, Account: "alice", Owner: "laptop",
			Host: "h1", CreatedAt: at.Add(time.Duration(i) * time.Second)}
		if err := s.StartSandbox(ctx, sb); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := s.EndSandbox(ctx, sb.ID, at.Add(time.Minute), control.EndDestroyed); err != nil {
				t.Fatal(err)
			}
		}
	}
	runs, err := s.AccountRuns(ctx, "alice", at.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, r.ID+"="+r.Name)
	}
	if want := []string{"sbx_2=api-tests", "sbx_1=", "sbx_0=api-tests"}; !slices.Equal(got, want) {
		t.Errorf("runs = %v, want %v", got, want)
	}
}
