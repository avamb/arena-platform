//go:build integration

// rich_text_0168_integration_test.go — migration 0168 (spec 43, RT-02) on a
// migrated database: the format columns and their CHECKs, the wider note
// limit, and the guard of the Down, which must refuse while any text is
// stored as 'art' (dropping the flag would leave ART that the old code shows
// as raw tags). Everything runs in a transaction that is rolled back.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// downGuard is the DO block of the Down section of 0168.
func downGuard(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("sql/0168_rich_text.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, ok := strings.Cut(string(body), "-- +goose Down")
	if !ok {
		t.Fatal("no Down section")
	}
	_, block, ok := strings.Cut(down, "-- +goose StatementBegin")
	if !ok {
		t.Fatal("no guard block in the Down section")
	}
	block, _, ok = strings.Cut(block, "-- +goose StatementEnd")
	if !ok {
		t.Fatal("the guard block is not closed")
	}
	if !strings.Contains(block, "RAISE EXCEPTION") {
		t.Fatalf("the Down guard does not refuse:\n%s", block)
	}
	return block
}

func TestMigration0168_FormatChecksNoteLimitAndDownGuard(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, integrationDSN(t))
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var org string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM organizations ORDER BY created_at LIMIT 1`).Scan(&org); err != nil {
		t.Skipf("no organization in the database (seed it first): %v", err)
	}

	// The columns default to 'text' and refuse any other value.
	var event string
	if err := tx.QueryRow(ctx, `INSERT INTO events (org_id, name) VALUES ($1, 'mig 0168') RETURNING id::text`, org).Scan(&event); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	var format string
	if err := tx.QueryRow(ctx, `SELECT description_format FROM events WHERE id = $1`, event).Scan(&format); err != nil || format != "text" {
		t.Fatalf("default description_format = %q (%v)", format, err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT bad`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE events SET description_format = 'rtf' WHERE id = $1`, event); err == nil {
		t.Fatal("description_format accepted 'rtf'")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT bad`); err != nil {
		t.Fatal(err)
	}

	// A note may be up to 8000 characters of HTML now, not 2000.
	long := strings.Repeat("a", 7000)
	if _, err := tx.Exec(ctx, `INSERT INTO event_email_notes (event_id, org_id, note, note_format) VALUES ($1, $2, $3, 'art')`, event, org, long); err != nil {
		t.Fatalf("a 7000-character note is refused: %v", err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT over`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE event_email_notes SET note = $2 WHERE event_id = $1`, event, strings.Repeat("a", 8001)); err == nil {
		t.Fatal("a 8001-character note is accepted")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT over`); err != nil {
		t.Fatal(err)
	}

	// The Down refuses while a text is stored as 'art' ...
	guard := downGuard(t)
	if _, err := tx.Exec(ctx, `UPDATE events SET description_format = 'art' WHERE id = $1`, event); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT down`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, guard); err == nil || !strings.Contains(err.Error(), "0168 down refused") {
		t.Fatalf("the Down did not refuse with an art description: %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT down`); err != nil {
		t.Fatal(err)
	}
	// ... or a note ...
	if _, err := tx.Exec(ctx, `UPDATE events SET description_format = 'text'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT down2`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, guard); err == nil || !strings.Contains(err.Error(), "0168 down refused") {
		t.Fatalf("the Down did not refuse with an art note: %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT down2`); err != nil {
		t.Fatal(err)
	}
	// ... and goes through when nothing is 'art'.
	if _, err := tx.Exec(ctx, `UPDATE event_email_notes SET note_format = 'text'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, guard); err != nil {
		t.Fatalf("the Down refused although every text is plain: %v", err)
	}
}
