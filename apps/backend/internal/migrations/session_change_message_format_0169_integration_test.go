//go:build integration

// session_change_message_format_0169_integration_test.go — migration 0169
// (spec 44, RT-11) on a migrated database: session_changes.message_format
// defaults to 'text', accepts only 'text' and 'art', and the Down refuses
// while any journal row holds an art message. Everything runs in a
// transaction that is rolled back.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// guard0169 is the DO block of the Down section of 0169.
func guard0169(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("sql/0169_session_change_message_format.sql")
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
	if !ok || !strings.Contains(block, "RAISE EXCEPTION") {
		t.Fatalf("the Down guard does not refuse:\n%s", block)
	}
	return block
}

func TestMigration0169_MessageFormatDefaultCheckAndDownGuard(t *testing.T) {
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
	var event, venue, session string
	if err := tx.QueryRow(ctx, `INSERT INTO events (org_id, name) VALUES ($1, 'mig 0169') RETURNING id::text`, org).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO venues (org_id, name, timezone) VALUES ($1, 'mig 0169', 'UTC') RETURNING id::text`, org).Scan(&venue); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO sessions (event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		VALUES ($1, $2, now() + interval '1 day', now() + interval '2 days', 10, 'scheduled', 'EUR', 'override') RETURNING id::text`, event, venue).Scan(&session); err != nil {
		t.Fatal(err)
	}

	run := func(cols, vals string) (string, error) {
		if _, err := tx.Exec(ctx, "SAVEPOINT sp"); err != nil {
			t.Fatal(err)
		}
		q := "INSERT INTO session_changes (session_id, event_id, org_id, kinds, old_state, new_state, message" + cols +
			") VALUES ($1, $2, $3, ARRAY['date'], '{}'::jsonb, '{}'::jsonb, 'm'" + vals + ") RETURNING message_format"
		var got string
		if err := tx.QueryRow(ctx, q, session, event, org).Scan(&got); err != nil {
			if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT sp"); rerr != nil {
				t.Fatal(rerr)
			}
			return "", err
		}
		return got, nil
	}

	// A row that names no format is 'text': what every row written before the migration is.
	if got, err := run("", ""); err != nil || got != "text" {
		t.Fatalf("default message_format = %q (%v), want text", got, err)
	}
	if got, err := run(", message_format", ", 'art'"); err != nil || got != "art" {
		t.Fatalf("art row: %q (%v)", got, err)
	}
	if _, err := run(", message_format", ", 'markdown'"); err == nil {
		t.Fatal("message_format 'markdown' was accepted")
	}

	// The Down refuses while an art row exists ...
	guard := guard0169(t)
	if _, err := tx.Exec(ctx, "SAVEPOINT down"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, guard); err == nil || !strings.Contains(err.Error(), "0169 down refused") {
		t.Fatalf("the Down guard did not refuse with an art row: %v", err)
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT down"); err != nil {
		t.Fatal(err)
	}
	// ... and passes once the art rows are converted to text.
	if _, err := tx.Exec(ctx, `UPDATE session_changes SET message_format = 'text' WHERE session_id = $1`, session); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, guard); err != nil {
		t.Fatalf("the Down guard refused without an art row: %v", err)
	}
}
