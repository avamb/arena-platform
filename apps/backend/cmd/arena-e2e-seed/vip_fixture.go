package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Task V1 (the organizer's private VIP mark of a ticket): one paid order of two
// tickets in the resend channel. The twin test
// apps/event-center/tests/smoke/live/vip.spec.ts repeats the ids and puts both
// tickets back to "not VIP" through the API before it starts, so it does not
// matter how many times the fixture is reused.
const (
	vipOrderID       = "fe0000e2-0000-7000-8000-0000000001a1"
	vipReservationID = "fe0000e2-0000-7000-8000-0000000001a2"
	vipCheckoutID    = "fe0000e2-0000-7000-8000-0000000001a3"
	vipBuyerEmail    = "vip-buyer@example.test"
)

// vipTickets are the two tickets (and their order lines) of the order.
var vipTickets = []struct{ ticketID, itemID string }{
	{"fe0000e2-0000-7000-8000-0000000001b1", "fe0000e2-0000-7000-8000-0000000001c1"},
	{"fe0000e2-0000-7000-8000-0000000001b2", "fe0000e2-0000-7000-8000-0000000001c2"},
}

// applyVIPOrder adds the paid two-ticket order. Idempotent: fixed ids, ON
// CONFLICT DO NOTHING. It runs after applyResendOrders (the channel).
func applyVIPOrder(ctx context.Context, tx pgx.Tx) error {
	step := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("insert %s: %w", what, err)
		}
		return nil
	}
	if err := step("vip reservation", `
		INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())
		ON CONFLICT (id) DO NOTHING`, vipReservationID, orgID, resendChannelID, sessionID); err != nil {
		return err
	}
	if err := step("vip checkout", `
		INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, 'completed')
		ON CONFLICT (id) DO NOTHING`, vipCheckoutID, orgID, resendChannelID, vipReservationID); err != nil {
		return err
	}
	if err := step("vip order", `
		INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 5000, 0, 0, 5000, 'Vip Buyer', $8, now())
		ON CONFLICT (id) DO NOTHING`,
		vipOrderID, orgID, resendChannelID, eventID, sessionID, vipCheckoutID, vipReservationID, vipBuyerEmail); err != nil {
		return err
	}
	for i, tk := range vipTickets {
		if err := step("vip ticket", `
			INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO NOTHING`, tk.ticketID, vipCheckoutID, sessionID, tierID, vipBuyerEmail, vipOrderID, i); err != nil {
			return err
		}
		if err := step("vip order item", `
			INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
			VALUES ($1, $2, $3, 'ticket', $4, $5, 2500, 0, 0, 2500)
			ON CONFLICT (id) DO NOTHING`, tk.itemID, vipOrderID, i, tierID, tk.ticketID); err != nil {
			return err
		}
	}
	return nil
}
