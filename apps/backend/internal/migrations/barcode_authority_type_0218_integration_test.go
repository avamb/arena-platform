//go:build integration

// barcode_authority_type_0218_integration_test.go — migration 0218: one
// barcode authority per type. With the database at 0218 a second 'platform'
// authority is refused with 23505; after the Down section runs inside a
// transaction the same insert succeeds. Everything is rolled back.
package migrations_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func section0218(t *testing.T, which string) string {
	t.Helper()
	body, err := os.ReadFile("sql/0218_barcode_authority_type_unique.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(body), "-- +goose Down")
	if !ok {
		t.Fatal("no Down section")
	}
	_, up, ok = strings.Cut(up, "-- +goose Up")
	if !ok {
		t.Fatal("no Up section")
	}
	if which == "up" {
		return up
	}
	return down
}

func TestMigration0218_OneAuthorityPerTypeAndDown(t *testing.T) {
	ctx := context.Background()
	conn := connectDB(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'barcode_authorities_type_unique'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("index barcode_authorities_type_unique: found %d, want 1", n)
	}

	// The seed (migration 0029) has a 'platform' authority; make sure of it.
	if _, err := tx.Exec(ctx, `INSERT INTO barcode_authorities (type, label)
		SELECT 'platform', 'Arena Platform'
		WHERE NOT EXISTS (SELECT 1 FROM barcode_authorities WHERE type = 'platform')`); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT dup`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO barcode_authorities (type, label) VALUES ('platform', 'Second platform')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second platform authority: want 23505, got %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT dup`); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, section0218(t, "down")); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO barcode_authorities (type, label) VALUES ('platform', 'Second platform')`); err != nil {
		t.Fatalf("after Down a second platform authority must be accepted: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM barcode_authorities WHERE label = 'Second platform'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, section0218(t, "up")); err != nil {
		t.Fatalf("Up again: %v", err)
	}
}
