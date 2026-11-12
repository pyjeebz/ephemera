// Package pgtest gives tests a fresh Postgres database.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pyjeebz/ephemera/internal/pgstore"
)

// Open opens a store on a new, empty database, dropped when the test ends,
// and returns the database's URL too. It needs EPHEMERA_TEST_POSTGRES, the
// URL of a server where it may create databases; without it the test is
// skipped.
func Open(t *testing.T) (*pgstore.Store, string) {
	t.Helper()
	admin := os.Getenv("EPHEMERA_TEST_POSTGRES")
	if admin == "" {
		t.Skip("EPHEMERA_TEST_POSTGRES is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	rand.Read(b)
	name := "ephemera_test_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	s, err := pgstore.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		conn.Close(ctx)
	})
	return s, u.String()
}
