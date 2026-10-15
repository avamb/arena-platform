package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The promo-code twin test (apps/event-center/tests/smoke/live/promo.spec.ts,
// task 4-3) needs what the bot's own e2e seeds (seedPromoOrg): a second
// running event with two EUR dates for the per-event merge of a code's
// sessions, a running event sold in another currency (a fixed EUR code must
// refuse it), and a code that was already used in two currencies by two named
// buyers, so the card can show the usage report and say "several currencies"
// instead of one sum. The names below are what the spec looks for.
const (
	promoSwanEventName  = "E2E Promo Swan Show"
	promoCzechEventName = "E2E Promo Czech Show"
	// PromoUsedCode is the code the fixture used twice (EUR and CZK).
	promoUsedCode = "E2EUSED"
	// The buyers are named on the report. Their e-mails must never reach a screen.
	promoBuyerOne = "Alice Promotest"
	promoBuyerTwo = "Boris Promotest"

	promoChannelID   = "fe0000e5-0000-7000-8000-000000000001"
	promoSwanEventID = "fe0000e5-0000-7000-8000-000000000002"
	promoSwanSess1   = "fe0000e5-0000-7000-8000-000000000003"
	promoSwanSess2   = "fe0000e5-0000-7000-8000-000000000004"
	promoCzechEvent  = "fe0000e5-0000-7000-8000-000000000005"
	promoCzechSess   = "fe0000e5-0000-7000-8000-000000000006"
	promoUsedCodeID  = "fe0000e5-0000-7000-8000-000000000007"
)

// applyPromoFixture adds the promo-code twin test's rows to the fixture
// organization. It is idempotent like the rest of the seed: fixed UUIDs and
// ON CONFLICT DO NOTHING.
func applyPromoFixture(ctx context.Context, tx pgx.Tx) error {
	exec := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("insert promo fixture %s: %w", what, err)
		}
		return nil
	}
	if err := exec("sales channel", `
		INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, 'E2E Promo Channel')
		ON CONFLICT (id) DO NOTHING`, promoChannelID, orgID); err != nil {
		return err
	}
	type ev struct {
		id, name, cur string
		firstDay      int
		sessions      []string
	}
	events := []ev{
		{promoSwanEventID, promoSwanEventName, "EUR", 40, []string{promoSwanSess1, promoSwanSess2}},
		{promoCzechEvent, promoCzechEventName, "CZK", 50, []string{promoCzechSess}},
	}
	for _, e := range events {
		if err := exec("event "+e.name, `
			INSERT INTO events (id, org_id, name, status, visibility)
			VALUES ($1, $2, $3, 'published', 'public')
			ON CONFLICT (id) DO NOTHING`, e.id, orgID, e.name); err != nil {
			return err
		}
		for i, sid := range e.sessions {
			start := time.Now().UTC().AddDate(0, 0, e.firstDay+i).Truncate(time.Hour)
			if err := exec("session of "+e.name, `
				INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
				VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled', $5, 'override')
				ON CONFLICT (id) DO NOTHING`, sid, e.id, venueID, start, e.cur); err != nil {
				return err
			}
		}
	}

	// A code used twice: once in EUR, once in CZK, by two named buyers.
	if err := exec("used promo code", `
		INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value)
		VALUES ($1, $2, $3, 'percent', 10)
		ON CONFLICT (id) DO NOTHING`, promoUsedCodeID, orgID, promoUsedCode); err != nil {
		return err
	}
	type use struct {
		n                 string
		session, event    string
		cur               string
		total, discount   int64
		buyer, buyerEmail string
	}
	uses := []use{
		{"1", sessionID, eventID, "EUR", 4500, 500, promoBuyerOne, "alice.promotest@example.test"},
		{"2", promoCzechSess, promoCzechEvent, "CZK", 90000, 10000, promoBuyerTwo, "boris.promotest@example.test"},
	}
	for _, u := range uses {
		res := "fe0000e6-0000-7000-8000-00000000000" + u.n
		cs := "fe0000e7-0000-7000-8000-00000000000" + u.n
		order := "fe0000e8-0000-7000-8000-00000000000" + u.n
		if err := exec("reservation", `
			INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
			VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())
			ON CONFLICT (id) DO NOTHING`, res, orgID, promoChannelID, u.session); err != nil {
			return err
		}
		if err := exec("checkout session", `
			INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, 'completed')
			ON CONFLICT (id) DO NOTHING`, cs, orgID, promoChannelID, res); err != nil {
			return err
		}
		if err := exec("order", `
			INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
			                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', $8, $9, 0, 0, $9, $10, $11, now())
			ON CONFLICT (id) DO NOTHING`,
			order, orgID, promoChannelID, u.event, u.session, cs, res, u.cur, u.total, u.buyer, u.buyerEmail); err != nil {
			return err
		}
		if err := exec("redemption", `
			INSERT INTO promo_code_redemptions (promo_code_id, discount_amount, order_amount, order_id, channel_id)
			SELECT $1, $2, $3, $4, $5
			WHERE NOT EXISTS (SELECT 1 FROM promo_code_redemptions WHERE order_id = $4)`,
			promoUsedCodeID, u.discount, u.total, order, promoChannelID); err != nil {
			return err
		}
	}
	return nil
}
