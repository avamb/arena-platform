package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Fixed ids of the orders the "Resend tickets" twin test (task 4-1) uses. The
// spec file apps/event-center/tests/smoke/live/resend.spec.ts repeats them.
const (
	resendChannelID  = "fe0000e2-0000-7000-8000-000000000101"
	resendOrderID    = "fe0000e2-0000-7000-8000-000000000111" // a paid order of the platform's own sales page
	resendSiteOrder  = "fe0000e2-0000-7000-8000-000000000112" // the same, sold through a seller's own site
	resendBuyerEmail = "resend-buyer@example.test"
)

// resendOrder describes one paid one-ticket order of the fixture.
type resendOrder struct {
	orderID, reservationID, checkoutID, ticketID, itemID, source, buyerName string
}

func resendOrders() []resendOrder {
	return []resendOrder{
		{
			orderID: resendOrderID, reservationID: "fe0000e2-0000-7000-8000-000000000121", checkoutID: "fe0000e2-0000-7000-8000-000000000131",
			ticketID: "fe0000e2-0000-7000-8000-000000000141", itemID: "fe0000e2-0000-7000-8000-000000000151", source: "public_feed", buyerName: "Resend Buyer",
		},
		{
			orderID: resendSiteOrder, reservationID: "fe0000e2-0000-7000-8000-000000000122", checkoutID: "fe0000e2-0000-7000-8000-000000000132",
			ticketID: "fe0000e2-0000-7000-8000-000000000142", itemID: "fe0000e2-0000-7000-8000-000000000152", source: "bil24_gateway", buyerName: "Site Buyer",
		},
	}
}

// applyResendOrders adds a sales channel and two paid orders (one ticket each)
// whose ticket letter was already sent, so the order card has something to
// resend and a status line to show. Idempotent: fixed ids, ON CONFLICT DO
// NOTHING, except the delivery job, which every run puts back to 'sent' so a
// spec starts from the same state however many times the fixture is reused.
func applyResendOrders(ctx context.Context, tx pgx.Tx) error {
	step := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("insert %s: %w", what, err)
		}
		return nil
	}
	if err := step("resend channel", `
		INSERT INTO sales_channels (id, org_id, name, settings)
		VALUES ($1, $2, 'E2E Resend Channel', '{"hosted_page": {"enabled": true}}'::jsonb)
		ON CONFLICT (id) DO NOTHING`, resendChannelID, orgID); err != nil {
		return err
	}
	for _, o := range resendOrders() {
		if err := step("resend reservation", `
			INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
			VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())
			ON CONFLICT (id) DO NOTHING`, o.reservationID, orgID, resendChannelID, sessionID); err != nil {
			return err
		}
		if err := step("resend checkout", `
			INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, 'completed')
			ON CONFLICT (id) DO NOTHING`, o.checkoutID, orgID, resendChannelID, o.reservationID); err != nil {
			return err
		}
		if err := step("resend order", `
			INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
			                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'paid', 'EUR', 2500, 0, 0, 2500, $9, $10, now())
			ON CONFLICT (id) DO NOTHING`,
			o.orderID, orgID, resendChannelID, eventID, sessionID, o.checkoutID, o.reservationID, o.source, o.buyerName, resendBuyerEmail); err != nil {
			return err
		}
		if err := step("resend ticket", `
			INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
			VALUES ($1, $2, $3, $4, $5, $6, 0)
			ON CONFLICT (id) DO NOTHING`, o.ticketID, o.checkoutID, sessionID, tierID, resendBuyerEmail, o.orderID); err != nil {
			return err
		}
		if err := step("resend order item", `
			INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
			VALUES ($1, $2, 0, 'ticket', $3, $4, 2500, 0, 0, 2500)
			ON CONFLICT (id) DO NOTHING`, o.itemID, o.orderID, tierID, o.ticketID); err != nil {
			return err
		}
		// The letter went out already: 'sent', and nothing queued for the worker.
		if err := step("resend delivery job", `
			INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at)
			VALUES ($1, $2, 'sent', now())
			ON CONFLICT (ticket_id) DO UPDATE
			   SET recipient_email = EXCLUDED.recipient_email, status = 'sent', sent_at = now(),
			       attempts = 0, last_error = NULL, processing_at = NULL`, o.ticketID, resendBuyerEmail); err != nil {
			return err
		}
	}
	return nil
}
