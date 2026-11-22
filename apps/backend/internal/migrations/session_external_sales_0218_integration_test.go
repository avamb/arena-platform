//go:build integration

// session_external_sales_0218_integration_test.go — migration 0218 (spec 63
// §14.1) on a migrated database: sessions.sales_mode defaults to
// 'platform', an external session needs an https URL and a seller name, a
// platform session carries none of the partner fields, and the display
// price goes with its currency. Everything runs in a transaction that is
// rolled back.
package migrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMigration0218_SessionSalesModeChecks(t *testing.T) {
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
	var event, venue, session, mode string
	if err := tx.QueryRow(ctx, `INSERT INTO events (org_id, name) VALUES ($1, 'mig 0218') RETURNING id::text`, org).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO venues (org_id, name, timezone) VALUES ($1, 'mig 0218', 'UTC') RETURNING id::text`, org).Scan(&venue); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO sessions (event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		VALUES ($1, $2, now() + interval '1 day', now() + interval '2 days', 10, 'scheduled', 'EUR', 'override')
		RETURNING id::text, sales_mode`, event, venue).Scan(&session, &mode); err != nil {
		t.Fatal(err)
	}
	if mode != "platform" {
		t.Fatalf("default sales_mode = %q, want platform", mode)
	}

	// update runs one SET on the session under a savepoint and reports the
	// SQLSTATE of a failure ("" on success).
	update := func(set string) string {
		t.Helper()
		if _, err := tx.Exec(ctx, "SAVEPOINT sp"); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, "UPDATE sessions SET "+set+" WHERE id = $1", session)
		if err != nil {
			if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT sp"); rerr != nil {
				t.Fatal(rerr)
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				return pgErr.Code
			}
			t.Fatalf("UPDATE %s: %v", set, err)
		}
		if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT sp"); err != nil {
			t.Fatal(err)
		}
		return ""
	}

	rejected := []struct{ name, set string }{
		{"external without URL", `sales_mode = 'external', external_seller_name = 'Eventim'`},
		{"external without seller", `sales_mode = 'external', external_ticket_url = 'https://www.eventim.de/x'`},
		{"external with http URL", `sales_mode = 'external', external_ticket_url = 'http://www.eventim.de/x', external_seller_name = 'Eventim'`},
		{"external with empty seller", `sales_mode = 'external', external_ticket_url = 'https://www.eventim.de/x', external_seller_name = ''`},
		{"external with a too long seller", `sales_mode = 'external', external_ticket_url = 'https://www.eventim.de/x', external_seller_name = repeat('a', 81)`},
		{"external with price but no currency", `sales_mode = 'external', external_ticket_url = 'https://www.eventim.de/x', external_seller_name = 'Eventim', display_price_from_minor = 3900`},
		{"external with negative price", `sales_mode = 'external', external_ticket_url = 'https://www.eventim.de/x', external_seller_name = 'Eventim', display_price_from_minor = -1, display_currency = 'EUR'`},
		{"platform with URL", `external_ticket_url = 'https://www.eventim.de/x'`},
		{"platform with seller", `external_seller_name = 'Eventim'`},
		{"platform with display price", `display_price_from_minor = 3900, display_currency = 'EUR'`},
		{"unknown mode", `sales_mode = 'partner'`},
	}
	for _, c := range rejected {
		if code := update(c.set); code != "23514" {
			t.Errorf("%s: SQLSTATE %q, want 23514 (check_violation)", c.name, code)
		}
	}

	accepted := []struct{ name, set string }{
		{"external with URL and seller", `sales_mode = 'external', external_ticket_url = 'https://www.eventim.de/x', external_seller_name = 'Eventim'`},
		{"external with price and currency", `display_price_from_minor = 3900, display_currency = 'EUR'`},
		{"back to platform", `sales_mode = 'platform', external_ticket_url = NULL, external_seller_name = NULL, display_price_from_minor = NULL, display_currency = NULL`},
	}
	for _, c := range accepted {
		if code := update(c.set); code != "" {
			t.Errorf("%s: refused with SQLSTATE %s", c.name, code)
		}
	}
}
