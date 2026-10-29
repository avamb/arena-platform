package main

// The cleanup both the seats fixture (seats_fixture.go) and the invitations
// fixture (invitations_fixture.go) run before they put their date back.
//
// A staff invitation (spec 46) leaves a reservation, a checkout session, an
// order, its tickets and worker jobs on the places of the date, and two
// foreign keys point at session_seats without a cascade (reservation_seats
// and order_items), so those rows must go before the places do. Only rows of
// ONE session are touched, children first.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// clearSessionSales removes the sales and invitation rows of the session down to
// (and including) reservation_seats. The caller then deletes its own places,
// reservations, ledger and categories, in that order.
//
// checkout_consents are never deleted (dispute evidence, retention guard in
// tests/staticanalysis). Staff invitations write none, and neither fixture is
// bought through the public checkout. email_letters stay for the same reason
// (tests/staticanalysis/email_letters_retention_test.go): they hold no foreign
// key to the tickets.
func clearSessionSales(ctx context.Context, tx pgx.Tx, who, sessionID string) error {
	const (
		sessionReservations = `SELECT id FROM reservations WHERE session_id = $1`
		sessionCheckouts    = `SELECT id FROM checkout_sessions WHERE reservation_id IN (` + sessionReservations + `)`
		sessionOrders       = `SELECT id FROM orders WHERE session_id = $1`
		sessionTickets      = `SELECT id FROM tickets WHERE session_id = $1`
	)
	for _, step := range []struct{ what, sql string }{
		{"jobs of checkouts", `DELETE FROM worker_jobs WHERE payload->>'checkout_session_id' IN (SELECT id::text FROM (` + sessionCheckouts + `) c)`},
		{"jobs of reservations", `DELETE FROM worker_jobs WHERE payload->>'reservation_id' IN (SELECT id::text FROM (` + sessionReservations + `) r)`},
		{"jobs of tickets", `DELETE FROM worker_jobs WHERE payload->>'ticket_id' IN (SELECT id::text FROM (` + sessionTickets + `) t)`},
		// order_items.ticket_id and tickets.order_id point at each other, as do tickets.refund_id and refunds.ticket_id.
		{"item tickets", `UPDATE order_items SET ticket_id = NULL WHERE order_id IN (` + sessionOrders + `)`},
		{"ticket refunds", `UPDATE tickets SET refund_id = NULL WHERE session_id = $1`},
		{"refunds", `DELETE FROM refunds WHERE order_id IN (` + sessionOrders + `)`},
		{"delivery jobs", `DELETE FROM delivery_jobs WHERE ticket_id IN (` + sessionTickets + `)`},
		{"barcodes", `DELETE FROM barcodes WHERE ticket_id IN (` + sessionTickets + `)`},
		{"credentials", `DELETE FROM ticket_credentials WHERE ticket_id IN (` + sessionTickets + `)`},
		{"scans", `DELETE FROM scan_events WHERE ticket_id IN (` + sessionTickets + `)`},
		{"tickets", `DELETE FROM tickets WHERE session_id = $1`},
		{"payments", `DELETE FROM payment_intents WHERE checkout_session_id IN (` + sessionCheckouts + `)`},
		{"orders", `DELETE FROM orders WHERE session_id = $1`},
		{"checkout sessions", `DELETE FROM checkout_sessions WHERE reservation_id IN (` + sessionReservations + `)`},
		{"held places", `DELETE FROM reservation_seats WHERE reservation_id IN (` + sessionReservations + `)`},
	} {
		if _, err := tx.Exec(ctx, step.sql, sessionID); err != nil {
			return fmt.Errorf("%s fixture %s: %w", who, step.what, err)
		}
	}
	return nil
}
