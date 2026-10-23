package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The door-scans twin test (apps/event-center/tests/smoke/live/scans.spec.ts,
// task M2) needs a date whose tickets the door treated in different ways, from
// both reporting systems: a ticket MACS let in (gate 2, scanner 7), a ticket
// arena's scanner turned away, a ticket nobody scanned, and an invitation (no
// order). The scans are fixed rows with fixed times in the past; the spec
// looks for the names below.
const (
	scansEventName = "E2E Scans Show"
	scansBuyerName = "Scans Buyer"

	scansChannelID    = "fe0000a2-0000-7000-8000-000000000001"
	scansEventID      = "fe0000a2-0000-7000-8000-000000000002"
	scansSessionID    = "fe0000a2-0000-7000-8000-000000000003"
	scansReservation  = "fe0000a2-0000-7000-8000-000000000004"
	scansCheckoutID   = "fe0000a2-0000-7000-8000-000000000005"
	scansOrderID      = "fe0000a2-0000-7000-8000-000000000006"
	scansSubscriberID = "fe0000a2-0000-7000-8000-000000000007"
)

// scansTicket is one ticket of the fixture.
type scansTicket struct {
	id, itemID string
	ordinal    int
	order      bool // false = an invitation
}

func scansTickets() []scansTicket {
	return []scansTicket{
		{"fe0000a2-0000-7000-8000-0000000000a1", "fe0000a2-0000-7000-8000-0000000000b1", 0, true}, // MACS let it in
		{"fe0000a2-0000-7000-8000-0000000000a2", "fe0000a2-0000-7000-8000-0000000000b2", 1, true}, // turned away by arena's scanner
		{"fe0000a2-0000-7000-8000-0000000000a3", "fe0000a2-0000-7000-8000-0000000000b3", 2, true}, // never scanned
		{"fe0000a2-0000-7000-8000-0000000000a4", "", 3, false},                                    // an invitation, never scanned
	}
}

// applyScansFixture adds the door-scans rows. Idempotent: fixed UUIDs and
// ON CONFLICT DO NOTHING (a scan row is unique by credential code and time).
func applyScansFixture(ctx context.Context, tx pgx.Tx) error {
	exec := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("insert scans fixture %s: %w", what, err)
		}
		return nil
	}
	steps := []struct {
		what, sql string
		args      []any
	}{
		{"sales channel", `INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, 'E2E Scans Channel') ON CONFLICT (id) DO NOTHING`,
			[]any{scansChannelID, orgID}},
		{"event", `INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public') ON CONFLICT (id) DO NOTHING`,
			[]any{scansEventID, orgID, scansEventName}},
		{"session", `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
			VALUES ($1, $2, $3, now() + interval '60 days', now() + interval '60 days 2 hours', 50, 'scheduled', 'EUR', 'override')
			ON CONFLICT (id) DO NOTHING`, []any{scansSessionID, scansEventID, venueID}},
		{"reservation", `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
			VALUES ($1, $2, $3, $4, 3, 'converted', now() + interval '1 hour', now()) ON CONFLICT (id) DO NOTHING`,
			[]any{scansReservation, orgID, scansChannelID, scansSessionID}},
		{"checkout", `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, 'completed') ON CONFLICT (id) DO NOTHING`,
			[]any{scansCheckoutID, orgID, scansChannelID, scansReservation}},
		{"order", `INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
			source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 7500, 0, 0, 7500, $8, 'scans-buyer@example.test', now())
			ON CONFLICT (id) DO NOTHING`,
			[]any{scansOrderID, orgID, scansChannelID, scansEventID, scansSessionID, scansCheckoutID, scansReservation, scansBuyerName}},
		{"macs subscriber", `INSERT INTO webhook_subscribers (id, site_url, callback_url, signing_secret, event_types, active, kind, org_id)
			VALUES ($1, '', 'https://macs.example.test/api/_wh/tickets', 'whsec_e2e_scans', '{}', true, 'macs', $2) ON CONFLICT (id) DO NOTHING`,
			[]any{scansSubscriberID, orgID}},
	}
	for _, s := range steps {
		if err := exec(s.what, s.sql, s.args...); err != nil {
			return err
		}
	}
	for _, tk := range scansTickets() {
		var order any
		if tk.order {
			order = scansOrderID
		}
		if err := exec("ticket", `INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
			VALUES ($1, $2, $3, $4, 'scans-buyer@example.test', $5, $6) ON CONFLICT (id) DO NOTHING`,
			tk.id, scansCheckoutID, scansSessionID, tierID, order, tk.ordinal); err != nil {
			return err
		}
		if tk.order {
			if err := exec("order item", `INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
				VALUES ($1, $2, $3, 'ticket', $4, $5, 2500, 0, 0, 2500) ON CONFLICT (id) DO NOTHING`,
				tk.itemID, scansOrderID, tk.ordinal, tierID, tk.id); err != nil {
				return err
			}
		}
	}
	t := scansTickets()
	// Ticket 1: arena's scanner turned it away first, MACS let it in later.
	// used_at is stamped as the scanner and MACS both do.
	if err := exec("macs scan", `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source, scanner_label, external_scan_id, subscriber_id)
		VALUES ($1, $2, $3, 'e2e-scans-macs-1', '2026-10-01 18:32:00+00', '2', 'admitted', 'macs', 'Scanner 7', 'e2e-scans-1', $4)
		ON CONFLICT DO NOTHING`, orgID, scansSessionID, t[0].id, scansSubscriberID); err != nil {
		return err
	}
	if err := exec("denied scan of the first ticket", `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source)
		VALUES ($1, $2, $3, 'e2e-scans-arena-1', '2026-10-01 18:20:00+00', 'North', 'denied', 'arena_scanner')
		ON CONFLICT DO NOTHING`, orgID, scansSessionID, t[0].id); err != nil {
		return err
	}
	if err := exec("used_at", `UPDATE tickets SET used_at = '2026-10-01 18:32:00+00' WHERE id = $1 AND used_at IS NULL`, t[0].id); err != nil {
		return err
	}
	// Ticket 2: turned away at the gate, never let in.
	return exec("denied scan", `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source)
		VALUES ($1, $2, $3, 'e2e-scans-arena-2', '2026-10-01 18:40:00+00', 'North', 'denied', 'arena_scanner')
		ON CONFLICT DO NOTHING`, orgID, scansSessionID, t[1].id)
}
