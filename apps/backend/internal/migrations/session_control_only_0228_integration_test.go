//go:build integration

// session_control_only_0228_integration_test.go — migration 0228 (MA-40) on
// a migrated database: sessions.sales_mode accepts 'control_only', a control
// session never shares an event with a sold session, an existing session is
// not flipped to control_only next to sold sessions, an event with a live
// control session is never published, a soft-deleted control session no
// longer blocks, and the Down section restores the 0221 constraints.
// Everything runs in a transaction that is rolled back.
package migrations_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func section0228(t *testing.T, which string) string {
	t.Helper()
	body, err := os.ReadFile("sql/0228_session_control_only.sql")
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

func TestMigration0228_SessionControlOnlyInvariantAndDown(t *testing.T) {
	ctx := context.Background()
	conn := connectDB(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var org string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM organizations ORDER BY created_at LIMIT 1`).Scan(&org); err != nil {
		t.Skipf("no organization in the database (seed it first): %v", err)
	}
	var venue string
	if err := tx.QueryRow(ctx, `INSERT INTO venues (org_id, name, timezone) VALUES ($1, 'mig 0228', 'UTC') RETURNING id::text`, org).Scan(&venue); err != nil {
		t.Fatal(err)
	}
	newEvent := func(name string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO events (org_id, name) VALUES ($1, $2) RETURNING id::text`, org, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// run executes one statement under a savepoint and reports the SQLSTATE
	// of a failure ("" on success). id receives a RETURNING id when wanted.
	run := func(id *string, sql string, args ...any) string {
		t.Helper()
		if _, err := tx.Exec(ctx, "SAVEPOINT sp"); err != nil {
			t.Fatal(err)
		}
		var err error
		if id != nil {
			err = tx.QueryRow(ctx, sql, args...).Scan(id)
		} else {
			_, err = tx.Exec(ctx, sql, args...)
		}
		if err != nil {
			if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT sp"); rerr != nil {
				t.Fatal(rerr)
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				return pgErr.Code
			}
			t.Fatalf("%s: %v", sql, err)
		}
		if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT sp"); err != nil {
			t.Fatal(err)
		}
		return ""
	}
	const insertSession = `INSERT INTO sessions (event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source, sales_mode)
		VALUES ($1, $2, now() + interval '1 day', now() + interval '2 days', 10, 'scheduled', 'EUR', 'override', $3)
		RETURNING id::text`
	want := func(name, got, wantCode string) {
		t.Helper()
		if got != wantCode {
			t.Errorf("%s: SQLSTATE %q, want %q", name, got, wantCode)
		}
	}

	// A control event: control sessions are accepted, a sold one is not.
	control := newEvent("mig 0228 control")
	var c1, c2, s string
	want("control session insert", run(&c1, insertSession, control, venue, "control_only"), "")
	want("second control session insert", run(&c2, insertSession, control, venue, "control_only"), "")
	want("platform session into a control event", run(&s, insertSession, control, venue, "platform"), "23514")
	want("control session with partner fields",
		run(nil, `UPDATE sessions SET external_seller_name = 'Eventim' WHERE id = $1`, c1), "23514")

	// A sold event: a control session is not added, and a session is not
	// flipped to control_only next to another sold session.
	sold := newEvent("mig 0228 sold")
	var p1, p2 string
	want("platform session insert", run(&p1, insertSession, sold, venue, "platform"), "")
	want("platform session insert 2", run(&p2, insertSession, sold, venue, "platform"), "")
	want("control session into a sold event", run(&s, insertSession, sold, venue, "control_only"), "23514")
	want("flip platform to control_only next to a sold session",
		run(nil, `UPDATE sessions SET sales_mode = 'control_only' WHERE id = $1`, p1), "23514")
	want("flip control_only to platform next to a control session",
		run(nil, `UPDATE sessions SET sales_mode = 'platform' WHERE id = $1`, c1), "23514")
	want("move a control session into a sold event",
		run(nil, `UPDATE sessions SET event_id = $2 WHERE id = $1`, c1, sold), "23514")

	// A control event is never published.
	want("publish a control event",
		run(nil, `UPDATE events SET status = 'published' WHERE id = $1`, control), "23514")
	published := newEvent("mig 0228 published")
	want("publish an empty event", run(nil, `UPDATE events SET status = 'published' WHERE id = $1`, published), "")
	want("control session into a published event", run(&s, insertSession, published, venue, "control_only"), "23514")

	// Soft-deleted control sessions no longer block.
	want("soft-delete control sessions",
		run(nil, `UPDATE sessions SET deleted_at = now() WHERE id = ANY($1::uuid[])`, []string{c1, c2}), "")
	want("platform session into the emptied control event", run(&s, insertSession, control, venue, "platform"), "")
	want("publish the emptied control event",
		run(nil, `UPDATE events SET status = 'published' WHERE id = $1`, control), "")
	want("restore a soft-deleted control session next to a sold one",
		run(nil, `UPDATE sessions SET deleted_at = NULL WHERE id = $1`, c1), "23514")

	// Down: the control rows go first, then 0221's constraints are back and
	// control_only is refused; Up again restores 0228.
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE sales_mode = 'control_only'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, section0228(t, "down")); err != nil {
		t.Fatalf("Down: %v", err)
	}
	var triggers int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname IN ('sessions_control_only_guard', 'events_control_only_publish_guard')`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 0 {
		t.Fatalf("after Down %d control_only triggers remain", triggers)
	}
	another := newEvent("mig 0228 after down")
	want("control session after Down", run(&s, insertSession, another, venue, "control_only"), "23514")
	want("platform session after Down", run(&s, insertSession, another, venue, "platform"), "")
	if _, err := tx.Exec(ctx, section0228(t, "up")); err != nil {
		t.Fatalf("Up again: %v", err)
	}
	want("control session after Up again", run(&s, insertSession, newEvent("mig 0228 up again"), venue, "control_only"), "")
}
