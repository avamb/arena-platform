package main

// Fixture of the web twin test of spec 63 stage R-3 ("Replace tickets" on the
// order card, apps/event-center/tests/smoke/live/reissue.spec.ts).
//
// One extra event of the fixture organization, "E2E Reissue Show", with its
// own date, category and sales channel, so the places and orders other twins
// use are never touched, plus a channel that a live selling site writes the
// letters of (an active bil24_wp webhook subscriber) and one paid order on it.
//
// A reissue cannot be undone, and the reissue journal is never deleted by
// code outside tests (tests/staticanalysis/ticket_reissues_retention_test.go),
// so the spec does not reset an order: it asks for a FRESH one per test.
//
//	--reissue-op new      creates one fresh paid order of two tickets (the
//	                      second one already used at the door, so the check
//	                      screen warns about it), paid through AllPay (a
//	                      provider arena cannot tell the new address, so the
//	                      check screen shows the hint), and prints
//	                      {order_id, number, email, tickets}. The base rows are
//	                      written idempotently by the same call;
//	--reissue-op site     makes sure the site's order exists and prints its id;
//	--reissue-op inspect  with --order-id prints {buyer_email, reissues, tickets}.
//	--reissue-op invitation creates one fresh invitation issued before spec 46
//	                      (a complimentary issuance with one ticket and no
//	                      order, stage R-5) and prints {invitation_id, email, tickets};
//	--reissue-op inspect-invitation with --order-id = the invitation id prints
//	                      {reissues, tickets}.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	rsiEventID       = "fe0000e2-0000-7000-8000-000000009101"
	rsiSessionID     = "fe0000e2-0000-7000-8000-000000009102"
	rsiTierID        = "fe0000e2-0000-7000-8000-000000009103"
	rsiLedgerID      = "fe0000e2-0000-7000-8000-000000009104"
	rsiChannelID     = "fe0000e2-0000-7000-8000-000000009105"
	rsiSiteChannelID = "fe0000e2-0000-7000-8000-000000009106"
	rsiSiteHookID    = "fe0000e2-0000-7000-8000-000000009107"

	// The site's order; the spec repeats it.
	rsiSiteOrderID     = "fe0000e2-0000-7000-8000-000000009111"
	rsiSiteReservation = "fe0000e2-0000-7000-8000-000000009112"
	rsiSiteCheckout    = "fe0000e2-0000-7000-8000-000000009113"
	rsiSiteTicket      = "fe0000e2-0000-7000-8000-000000009114"

	rsiEventName = "E2E Reissue Show"
	rsiTierName  = "E2E Reissue Standing"
	rsiPrice     = 2500
	// A typo the buyer made at checkout: the order's address.
	rsiWrongEmail = "reissue-buyer@exmaple.test"
)

// applyReissueBase writes the event, its date, category, ledger and the two
// channels. Idempotent.
func applyReissueBase(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("reissue fixture %s: %w", what, err)
		}
		return nil
	}
	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, rsiEventID, orgID, rsiEventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 45).Truncate(time.Hour)
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 500, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO NOTHING`, rsiSessionID, rsiEventID, venueID, start); err != nil {
		return err
	}
	if err := run("category", `
		INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
		VALUES ($1, $2, $3, 'fixed', $4, 'EUR', 500, 0)
		ON CONFLICT (id) DO NOTHING`, rsiTierID, rsiSessionID, rsiTierName, rsiPrice); err != nil {
		return err
	}
	if err := run("ledger", `
		INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total)
		VALUES ($1, $2, $3, 500)
		ON CONFLICT (id) DO NOTHING`, rsiLedgerID, rsiSessionID, rsiTierID); err != nil {
		return err
	}
	if err := run("channel", `
		INSERT INTO sales_channels (id, org_id, name, settings)
		VALUES ($1, $2, 'E2E Reissue Channel', '{"hosted_page": {"enabled": true}}'::jsonb)
		ON CONFLICT (id) DO NOTHING`, rsiChannelID, orgID); err != nil {
		return err
	}
	if err := run("site channel", `
		INSERT INTO sales_channels (id, org_id, name, settings)
		VALUES ($1, $2, 'E2E Reissue Site', '{}'::jsonb)
		ON CONFLICT (id) DO NOTHING`, rsiSiteChannelID, orgID); err != nil {
		return err
	}
	// A live selling site: it writes this channel's letters (spec 63 R-8).
	return run("site subscriber", `
		INSERT INTO webhook_subscribers (id, site_url, callback_url, signing_secret, event_types, active, kind, channel_id)
		VALUES ($1, 'https://reissue-site.example.test', 'https://reissue-site.example.test/hook', 'whsec_e2e_reissue_site', '{}', true, 'bil24_wp', $2)
		ON CONFLICT (id) DO UPDATE SET active = true`, rsiSiteHookID, rsiSiteChannelID)
}

// insertReissueOrder writes one paid order with its tickets and lines.
// scanned names the ticket indexes already used at the door.
func insertReissueOrder(ctx context.Context, tx pgx.Tx, channel, orderID, reservation, checkout string,
	tickets []string, email, buyer, phone string, scanned map[int]bool, payment bool) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("reissue fixture %s: %w", what, err)
		}
		return nil
	}
	total := rsiPrice * len(tickets)
	if err := run("reservation", `
		INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		VALUES ($1, $2, $3, $4, $5, 'converted', now() + interval '1 hour', now())
		ON CONFLICT (id) DO NOTHING`, reservation, orgID, channel, rsiSessionID, len(tickets)); err != nil {
		return err
	}
	if err := run("checkout", `
		INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, 'completed')
		ON CONFLICT (id) DO NOTHING`, checkout, orgID, channel, reservation); err != nil {
		return err
	}
	if err := run("order", `
		INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone, paid_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', $8, 0, 0, $8, $9, $10, NULLIF($11, ''), now())
		ON CONFLICT (id) DO NOTHING`,
		orderID, orgID, channel, rsiEventID, rsiSessionID, checkout, reservation, total, buyer, email, phone); err != nil {
		return err
	}
	if payment {
		if err := run("payment", `
			INSERT INTO payment_intents (id, checkout_session_id, org_id, provider, provider_payment_id, provider_charge_ref,
			                             amount, currency, state, succeeded_at)
			VALUES ($1, $2, $3, 'allpay', $4, $4, $5, 'EUR', 'succeeded', now())
			ON CONFLICT (id) DO NOTHING`,
			uuid.NewString(), checkout, orgID, "e2e-reissue-"+orderID, total); err != nil {
			return err
		}
	}
	for i, tk := range tickets {
		var usedAt any
		if scanned[i] {
			usedAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
		}
		if err := run("ticket", `
			INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal, used_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO NOTHING`,
			tk, checkout, rsiSessionID, rsiTierID, email, orderID, i, usedAt); err != nil {
			return err
		}
		if err := run("order item", `
			INSERT INTO order_items (order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
			SELECT $1, $2, 'ticket', $3, $4, $5, 0, 0, $5
			WHERE NOT EXISTS (SELECT 1 FROM order_items WHERE order_id = $1 AND ordinal = $2)`,
			orderID, i, rsiTierID, tk, rsiPrice); err != nil {
			return err
		}
		// The first letter went to the wrong address already.
		if err := run("delivery job", `
			INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at)
			VALUES ($1, $2, 'sent', now())
			ON CONFLICT (ticket_id) DO NOTHING`, tk, email); err != nil {
			return err
		}
	}
	return nil
}

// runReissueOp serves --reissue-op. It prints one JSON line.
func runReissueOp(op, orderID string) error {
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()

	inTx := func(fn func(pgx.Tx) error) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := applyReissueBase(ctx, tx); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	switch op {
	case "new":
		id, reservation, checkout := uuid.NewString(), uuid.NewString(), uuid.NewString()
		tickets := []string{uuid.NewString(), uuid.NewString()}
		if err := inTx(func(tx pgx.Tx) error {
			return insertReissueOrder(ctx, tx, rsiChannelID, id, reservation, checkout, tickets,
				rsiWrongEmail, "Reissue Buyer", "+34600111222", map[int]bool{1: true}, true)
		}); err != nil {
			return err
		}
		var number int64
		if err := pool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id = $1`, id).Scan(&number); err != nil {
			return fmt.Errorf("read order number: %w", err)
		}
		numbers, err := reissueTicketNumbers(ctx, pool, id)
		if err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]any{"order_id": id, "number": number, "email": rsiWrongEmail, "tickets": numbers})
	case "site":
		if err := inTx(func(tx pgx.Tx) error {
			return insertReissueOrder(ctx, tx, rsiSiteChannelID, rsiSiteOrderID, rsiSiteReservation, rsiSiteCheckout,
				[]string{rsiSiteTicket}, "reissue-site-buyer@example.test", "Site Buyer", "", nil, false)
		}); err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]string{"order_id": rsiSiteOrderID})
	case "inspect":
		if _, err := uuid.Parse(orderID); err != nil {
			return fmt.Errorf("--reissue-op inspect needs --order-id")
		}
		var email string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(buyer_email, '') FROM orders WHERE id = $1`, orderID).Scan(&email); err != nil {
			return fmt.Errorf("inspect order: %w", err)
		}
		type reissue struct {
			State    string `json:"state"`
			NewEmail string `json:"new_email"`
			Reason   string `json:"reason"`
			Note     string `json:"note"`
			Items    int    `json:"items"`
		}
		rows, err := pool.Query(ctx, `
			SELECT r.state, r.new_email, r.reason, COALESCE(r.note, ''),
			       (SELECT count(*) FROM ticket_reissue_items i WHERE i.reissue_id = r.id)
			FROM ticket_reissues r WHERE r.order_id = $1 ORDER BY r.created_at`, orderID)
		if err != nil {
			return fmt.Errorf("inspect reissues: %w", err)
		}
		reissues := []reissue{}
		for rows.Next() {
			var r reissue
			if err := rows.Scan(&r.State, &r.NewEmail, &r.Reason, &r.Note, &r.Items); err != nil {
				rows.Close()
				return err
			}
			reissues = append(reissues, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		numbers, err := reissueTicketNumbers(ctx, pool, orderID)
		if err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]any{"buyer_email": email, "reissues": reissues, "tickets": numbers})
	case "invitation":
		id, ticket := uuid.NewString(), uuid.NewString()
		guest := "reissue-guest@exmaple.test"
		if err := inTx(func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO complimentary_issuances (id, org_id, session_id, tier_id, qty, recipients, batch_id, status)
				VALUES ($1, $2, $3, $4, 1, ARRAY[$5::text], $6, 'issued')`,
				id, orgID, rsiSessionID, rsiTierID, guest, "e2e-reissue-"+id); err != nil {
				return fmt.Errorf("reissue fixture invitation: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO tickets (id, session_id, tier_id, holder_email, holder_name, complimentary_issuance_id, ordinal)
				VALUES ($1, $2, $3, $4, 'Reissue Guest', $5, 0)`, ticket, rsiSessionID, rsiTierID, guest, id); err != nil {
				return fmt.Errorf("reissue fixture invitation ticket: %w", err)
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at)
				VALUES ($1, $2, 'sent', now())`, ticket, guest)
			return err
		}); err != nil {
			return err
		}
		numbers, err := reissueInvitationTickets(ctx, pool, id)
		if err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]any{"invitation_id": id, "email": guest, "tickets": numbers})
	case "inspect-invitation":
		if _, err := uuid.Parse(orderID); err != nil {
			return fmt.Errorf("--reissue-op inspect-invitation needs --order-id (the invitation id)")
		}
		var reissues []string
		if err := pool.QueryRow(ctx, `
			SELECT COALESCE(array_agg(state || ':' || new_email ORDER BY created_at), '{}')
			FROM ticket_reissues WHERE complimentary_issuance_id = $1`, orderID).Scan(&reissues); err != nil {
			return fmt.Errorf("inspect invitation reissues: %w", err)
		}
		numbers, err := reissueInvitationTickets(ctx, pool, orderID)
		if err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]any{"reissues": reissues, "tickets": numbers})
	default:
		return fmt.Errorf("unknown --reissue-op %q (new, site, inspect, invitation, inspect-invitation)", op)
	}
}

type reissueTicket struct {
	Number int64  `json:"number"`
	Status string `json:"status"`
}

func reissueTicketNumbers(ctx context.Context, pool *pgxpool.Pool, orderID string) ([]reissueTicket, error) {
	rows, err := pool.Query(ctx, `SELECT system_ticket_id, status FROM tickets WHERE order_id = $1 ORDER BY system_ticket_id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("inspect tickets: %w", err)
	}
	defer rows.Close()
	out := []reissueTicket{}
	for rows.Next() {
		var t reissueTicket
		if err := rows.Scan(&t.Number, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func reissueInvitationTickets(ctx context.Context, pool *pgxpool.Pool, issuanceID string) ([]reissueTicket, error) {
	rows, err := pool.Query(ctx, `SELECT system_ticket_id, status FROM tickets
		WHERE complimentary_issuance_id = $1 ORDER BY system_ticket_id`, issuanceID)
	if err != nil {
		return nil, fmt.Errorf("inspect invitation tickets: %w", err)
	}
	defer rows.Close()
	out := []reissueTicket{}
	for rows.Next() {
		var t reissueTicket
		if err := rows.Scan(&t.Number, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
