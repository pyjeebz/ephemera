package pgstore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

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
