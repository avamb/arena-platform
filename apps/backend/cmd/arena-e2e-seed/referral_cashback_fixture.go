package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// "Refer a friend" cashback in the sales reports (spec 52, owner decision
// 2026-11-03): an event of its own with one date and the stand's case. The
// referrer paid 454,50; the friend paid 252,50 with the referrer's personal
// code; the friend got 207,05 back; the referrer got 45,45 cashback. The
// organizer kept 454,50. The twin test
// apps/event-center/tests/smoke/live/referral-cashback.spec.ts repeats the
// ids. Read-only for the spec, so it needs no reset: fixed ids, ON CONFLICT
// DO NOTHING.
const (
	rcEventID     = "fe0000e2-0000-7000-8000-00000000c501"
	rcSessionID   = "fe0000e2-0000-7000-8000-00000000c502"
	rcTierID      = "fe0000e2-0000-7000-8000-00000000c503"
	rcProgramID   = "fe0000e2-0000-7000-8000-00000000c504"
	rcCodeID      = "fe0000e2-0000-7000-8000-00000000c505"
	rcRewardID    = "fe0000e2-0000-7000-8000-00000000c506"
	rcRefundID    = "fe0000e2-0000-7000-8000-00000000c507"
	rcCashbackID  = "fe0000e2-0000-7000-8000-00000000c508"
	rcCode        = "E2E-ANNA-7K2Q"
	rcEventName   = "E2E Cashback Show"
	rcBuyerDomain = "@example.test"
)

// rcOrder is one paid one-ticket order of the fixture.
type rcOrder struct {
	orderID, reservationID, checkoutID, ticketID, itemID, intentID, status, buyer string
	total                                                                         int64
}

var (
	rcReferrer = rcOrder{
		orderID: "fe0000e2-0000-7000-8000-00000000c511", reservationID: "fe0000e2-0000-7000-8000-00000000c512",
		checkoutID: "fe0000e2-0000-7000-8000-00000000c513", ticketID: "fe0000e2-0000-7000-8000-00000000c514",
		itemID: "fe0000e2-0000-7000-8000-00000000c515", intentID: "fe0000e2-0000-7000-8000-00000000c516",
		status: "paid", buyer: "Anna Referrer", total: 45450,
	}
	rcFriend = rcOrder{
		orderID: "fe0000e2-0000-7000-8000-00000000c521", reservationID: "fe0000e2-0000-7000-8000-00000000c522",
		checkoutID: "fe0000e2-0000-7000-8000-00000000c523", ticketID: "fe0000e2-0000-7000-8000-00000000c524",
		itemID: "fe0000e2-0000-7000-8000-00000000c525", intentID: "fe0000e2-0000-7000-8000-00000000c526",
		status: "partially_refunded", buyer: "Boris Friend", total: 25250,
	}
)

// applyReferralCashbackFixture runs after applyResendOrders (its channel).
func applyReferralCashbackFixture(ctx context.Context, tx pgx.Tx) error {
	step := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("referral cashback fixture %s: %w", what, err)
		}
		return nil
	}
	if err := step("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, rcEventID, orgID, rcEventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 45).Truncate(time.Hour)
	if err := step("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 10, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO NOTHING`, rcSessionID, rcEventID, venueID, start); err != nil {
		return err
	}
	if err := step("tier", `
		INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
		VALUES ($1, $2, 'Standing', 'fixed', 25250, 'EUR', 10, 1, true)
		ON CONFLICT (id) DO NOTHING`, rcTierID, rcSessionID); err != nil {
		return err
	}
	for _, o := range []rcOrder{rcReferrer, rcFriend} {
		if err := step("reservation", `
			INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
			VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())
			ON CONFLICT (id) DO NOTHING`, o.reservationID, orgID, resendChannelID, rcSessionID); err != nil {
			return err
		}
		if err := step("checkout", `
			INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, 'completed')
			ON CONFLICT (id) DO NOTHING`, o.checkoutID, orgID, resendChannelID, o.reservationID); err != nil {
			return err
		}
		if err := step("order", `
			INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
			                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', $8, 'EUR', $9, 0, 0, $9, $10, $11, now())
			ON CONFLICT (id) DO NOTHING`,
			o.orderID, orgID, resendChannelID, rcEventID, rcSessionID, o.checkoutID, o.reservationID, o.status, o.total,
			o.buyer, "rc-"+o.orderID[len(o.orderID)-3:]+rcBuyerDomain); err != nil {
			return err
		}
		if err := step("ticket", `
			INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
			VALUES ($1, $2, $3, $4, $5, $6, 0)
			ON CONFLICT (id) DO NOTHING`, o.ticketID, o.checkoutID, rcSessionID, rcTierID,
			"rc-"+o.orderID[len(o.orderID)-3:]+rcBuyerDomain, o.orderID); err != nil {
			return err
		}
		if err := step("order item", `
			INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
			VALUES ($1, $2, 0, 'ticket', $3, $4, $5, 0, 0, $5)
			ON CONFLICT (id) DO NOTHING`, o.itemID, o.orderID, rcTierID, o.ticketID, o.total); err != nil {
			return err
		}
		if err := step("payment intent", `
			INSERT INTO payment_intents (id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state)
			VALUES ($1, $2, $3, 'stripe', $4, $5, 'EUR', 'succeeded')
			ON CONFLICT (id) DO NOTHING`, o.intentID, o.checkoutID, orgID, "cs_test_e2e_rc_"+o.intentID, o.total); err != nil {
			return err
		}
	}
	if err := step("programme", `
		INSERT INTO referral_programs (id, org_id, scope, event_id, friend_discount_pct, cashback_pct, terms_accepted_at)
		VALUES ($1, $2, 'event', $3, 10, 18, now())
		ON CONFLICT (id) DO NOTHING`, rcProgramID, orgID, rcEventID); err != nil {
		return err
	}
	if err := step("personal code", `
		INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value, origin, referral_program_id, referrer_order_id)
		VALUES ($1, $2, $3, 'percent', 10, 'referral', $4, $5)
		ON CONFLICT (id) DO NOTHING`, rcCodeID, orgID, rcCode, rcProgramID, rcReferrer.orderID); err != nil {
		return err
	}
	if err := step("friend's code", `UPDATE orders SET promo_code_id = $1 WHERE id = $2 AND promo_code_id IS NULL`,
		rcCodeID, rcFriend.orderID); err != nil {
		return err
	}
	if err := step("friend's refund", `
		INSERT INTO refunds (id, payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement, succeeded_at)
		VALUES ($1, $2, $3, $4, 20705, 'EUR', 'requested_by_customer', 'succeeded', 'provider', now())
		ON CONFLICT (id) DO NOTHING`, rcRefundID, rcFriend.intentID, orgID, rcFriend.orderID); err != nil {
		return err
	}
	if err := step("cashback refund", `
		INSERT INTO refunds (id, payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement, origin, succeeded_at)
		VALUES ($1, $2, $3, $4, 4545, 'EUR', 'referral_cashback', 'succeeded', 'provider', 'referral_cashback', now())
		ON CONFLICT (id) DO NOTHING`, rcCashbackID, rcReferrer.intentID, orgID, rcReferrer.orderID); err != nil {
		return err
	}
	return step("reward", `
		INSERT INTO referral_rewards (id, org_id, program_id, promo_code_id, referrer_order_id, friend_order_id,
		                              amount_minor, currency, cashback_pct, cap_pct, state, refund_id)
		VALUES ($1, $2, $3, $4, $5, $6, 4545, 'EUR', 18, 100, 'paid', $7)
		ON CONFLICT (id) DO NOTHING`, rcRewardID, orgID, rcProgramID, rcCodeID, rcReferrer.orderID, rcFriend.orderID, rcCashbackID)
}
