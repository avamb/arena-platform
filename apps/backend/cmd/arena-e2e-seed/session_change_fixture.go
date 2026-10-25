package main

// Fixture of the web twin test of task 5-2 (move or cancel a session,
// apps/event-center/tests/smoke/live/session-change.spec.ts).
//
// ONE event of the fixture organization ("E2E Session Change Show") with five
// sessions, so every scenario owns its own and a change in one never moves
// another:
//
//	move     one paid order of the platform's own sales page -> a move, letters queued
//	cancel   one paid order of the same page                  -> a cancellation
//	manager  one paid order                                   -> the manager's move to another venue
//	site     one paid order of a channel with an active bil24_wp subscriber
//	         -> the change is BLOCKED ("the site writes its own letter")
//	empty    no orders at all                                 -> nobody to write to
//
// A second venue ("E2E Session Change Annex") lets a move change the venue.
//
// Operations the spec calls through the seed binary, because the test process
// has no SQL driver:
//
//	--session-change-op reset    rebuilds the five sessions as they were created
//	                             (their rows are DELETED and inserted again: a raw
//	                             UPDATE of a session's date is exactly what the
//	                             session-change guard forbids outside the API),
//	                             drops the journal and the queued letters, and
//	                             clears the organization's contact e-mail, so the
//	                             dry run says contact_missing;
//	--session-change-op inspect  prints what the database holds for --session-id.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it. Nothing here touches the three people of the main fixture.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	scEventID      = "fe0000e2-0000-7000-8000-000000000501"
	scAnnexVenueID = "fe0000e2-0000-7000-8000-000000000502"
	scChannelID    = "fe0000e2-0000-7000-8000-000000000503"
	scSiteChannel  = "fe0000e2-0000-7000-8000-000000000504"

	scSessionMove    = "fe0000e2-0000-7000-8000-000000000511"
	scSessionCancel  = "fe0000e2-0000-7000-8000-000000000512"
	scSessionManager = "fe0000e2-0000-7000-8000-000000000513"
	scSessionSite    = "fe0000e2-0000-7000-8000-000000000514"
	scSessionEmpty   = "fe0000e2-0000-7000-8000-000000000515"

	// Every session starts on this day (19:00 in Madrid, summer time), far
	// enough away that no date the spec types is ever in the past.
	scStartAt = "2090-06-15T17:00:00Z"

	scBuyerEmail = "change-buyer@example.test"
)

type scOrder struct {
	session, orderID, reservationID, checkoutID, ticketID, channel string
}

func scOrders() []scOrder {
	return []scOrder{
		{scSessionMove, "fe0000e2-0000-7000-8000-000000000521", "fe0000e2-0000-7000-8000-000000000531", "fe0000e2-0000-7000-8000-000000000541", "fe0000e2-0000-7000-8000-000000000551", scChannelID},
		{scSessionCancel, "fe0000e2-0000-7000-8000-000000000522", "fe0000e2-0000-7000-8000-000000000532", "fe0000e2-0000-7000-8000-000000000542", "fe0000e2-0000-7000-8000-000000000552", scChannelID},
		{scSessionManager, "fe0000e2-0000-7000-8000-000000000523", "fe0000e2-0000-7000-8000-000000000533", "fe0000e2-0000-7000-8000-000000000543", "fe0000e2-0000-7000-8000-000000000553", scChannelID},
		{scSessionSite, "fe0000e2-0000-7000-8000-000000000524", "fe0000e2-0000-7000-8000-000000000534", "fe0000e2-0000-7000-8000-000000000544", "fe0000e2-0000-7000-8000-000000000554", scSiteChannel},
	}
}

func scSessions() []string {
	return []string{scSessionMove, scSessionCancel, scSessionManager, scSessionSite, scSessionEmpty}
}

// applySessionChangeFixture is idempotent AND resetting.
func applySessionChangeFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("session-change fixture %s: %w", what, err)
		}
		return nil
	}
	orders := scOrders()
	orderIDs := make([]string, 0, len(orders))
	for _, o := range orders {
		orderIDs = append(orderIDs, o.orderID)
	}
	// Children first. The journal cascades from the sessions.
	if err := run("letters", `DELETE FROM worker_jobs WHERE job_type = 'session.change_email' AND payload->>'order_id' = ANY($1::text[])`, orderIDs); err != nil {
		return err
	}
	if err := run("order events", `DELETE FROM order_events WHERE order_id = ANY($1::uuid[])`, orderIDs); err != nil {
		return err
	}
	for _, q := range []struct{ what, sql string }{
		{"journal", `DELETE FROM session_changes WHERE session_id = ANY($1::uuid[])`},
		{"tickets", `DELETE FROM tickets WHERE session_id = ANY($1::uuid[])`},
		{"orders", `DELETE FROM orders WHERE session_id = ANY($1::uuid[])`},
		{"checkout sessions", `DELETE FROM checkout_sessions WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = ANY($1::uuid[]))`},
		{"reservations", `DELETE FROM reservations WHERE session_id = ANY($1::uuid[])`},
		{"sessions", `DELETE FROM sessions WHERE id = ANY($1::uuid[])`},
	} {
		if err := run(q.what, q.sql, scSessions()); err != nil {
			return err
		}
	}
	// Nobody has an address buyers can write to: the dry run says contact_missing.
	if err := run("contact", `UPDATE organizations SET contact_email = NULL, contact_phone = NULL, contact_phone_hidden = false WHERE id = $1`, orgID); err != nil {
		return err
	}
	if err := run("annex venue", `
		INSERT INTO venues (id, org_id, name, timezone)
		VALUES ($1, $2, 'E2E Session Change Annex', 'Europe/Madrid')
		ON CONFLICT (id) DO NOTHING`, scAnnexVenueID, orgID); err != nil {
		return err
	}
	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, 'E2E Session Change Show', 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, scEventID, orgID); err != nil {
		return err
	}
	for _, c := range []struct{ id, name string }{{scChannelID, "E2E Change Channel"}, {scSiteChannel, "E2E Change Site Channel"}} {
		if err := run("channel", `
			INSERT INTO sales_channels (id, org_id, name, settings)
			VALUES ($1, $2, $3, '{}'::jsonb)
			ON CONFLICT (id) DO NOTHING`, c.id, orgID, c.name); err != nil {
			return err
		}
	}
	// The site channel is a selling WordPress site: it writes its own letters.
	if err := run("site subscriber", `
		INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, channel_id)
		SELECT '', 'https://site.e2e.test/hook', 'e2e-site-secret', '{}', TRUE, 'bil24_wp', $1::uuid
		WHERE NOT EXISTS (SELECT 1 FROM webhook_subscribers WHERE channel_id = $1::uuid AND kind = 'bil24_wp' AND active)`, scSiteChannel); err != nil {
		return err
	}
	start, err := time.Parse(time.RFC3339, scStartAt)
	if err != nil {
		return err
	}
	for _, s := range scSessions() {
		if err := run("session", `
			INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
			VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 30, 'scheduled', 'EUR', 'override')`,
			s, scEventID, venueID, start); err != nil {
			return err
		}
	}
	for _, o := range orders {
		if err := run("reservation", `
			INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
			VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`,
			o.reservationID, orgID, o.channel, o.session); err != nil {
			return err
		}
		if err := run("checkout", `
			INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, 'completed')`, o.checkoutID, orgID, o.channel, o.reservationID); err != nil {
			return err
		}
		if err := run("order", `
			INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
			                    source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'Change Buyer', $8, now())`,
			o.orderID, orgID, o.channel, scEventID, o.session, o.checkoutID, o.reservationID, scBuyerEmail); err != nil {
			return err
		}
		if err := run("ticket", `
			INSERT INTO tickets (id, checkout_session_id, session_id, holder_email, order_id, ordinal)
			VALUES ($1, $2, $3, $4, $5, 0)`, o.ticketID, o.checkoutID, o.session, scBuyerEmail, o.orderID); err != nil {
			return err
		}
	}
	return nil
}

// runSessionChangeOp serves --session-change-op. It prints one JSON line.
func runSessionChangeOp(op, sessionID string) error {
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

	switch op {
	case "reset":
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := applySessionChangeFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printSessionChangeJSON(map[string]string{"ok": "reset"})
	case "inspect":
		if sessionID == "" {
			return fmt.Errorf("--session-change-op inspect needs --session-id")
		}
		var (
			status, venue, start string
			changes, notices     int
			paidOrders           int
			queued               int
			orgEmail             string
			lastMessage          string
			lastFormat           string
		)
		err := pool.QueryRow(ctx, `
			SELECT s.status, s.venue_id::text, to_char(s.start_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
			       (SELECT count(*) FROM session_changes c WHERE c.session_id = s.id),
			       (SELECT count(*) FROM session_change_notices n JOIN session_changes c ON c.id = n.change_id WHERE c.session_id = s.id),
			       (SELECT count(*) FROM worker_jobs j WHERE j.job_type = 'session.change_email'
			          AND j.payload->>'order_id' IN (SELECT o.id::text FROM orders o WHERE o.session_id = s.id)),
			       COALESCE((SELECT btrim(o.contact_email) FROM organizations o WHERE o.id = $2), ''),
			       COALESCE((SELECT c.message FROM session_changes c WHERE c.session_id = s.id ORDER BY c.created_at DESC LIMIT 1), ''),
			       COALESCE((SELECT c.message_format FROM session_changes c WHERE c.session_id = s.id ORDER BY c.created_at DESC LIMIT 1), ''),
			       (SELECT count(*) FROM orders o2 WHERE o2.session_id = s.id AND o2.status = 'paid')
			FROM sessions s WHERE s.id = $1`, sessionID, orgID).
			Scan(&status, &venue, &start, &changes, &notices, &queued, &orgEmail, &lastMessage, &lastFormat, &paidOrders)
		if err != nil {
			return fmt.Errorf("inspect: %w", err)
		}
		return printSessionChangeJSON(map[string]any{
			"status": status, "venue_id": venue, "start_at": start, "changes": changes,
			"notices": notices, "queued_letters": queued, "org_contact_email": orgEmail, "last_message": lastMessage, "last_message_format": lastFormat,
			"paid_orders": paidOrders,
		})
	default:
		return fmt.Errorf("unknown --session-change-op %q (reset or inspect)", op)
	}
}

func printSessionChangeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(b, '\n'))
	return err
}
