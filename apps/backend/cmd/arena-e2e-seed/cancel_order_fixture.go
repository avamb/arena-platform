package main

// Fixture of the web twin test of task 4-2 (cancel an unpaid order with the
// typed word, apps/event-center/tests/smoke/live/cancel-order.spec.ts).
//
// Five orders of the fixture organization with FIXED ids, one channel, and
// three small operations the spec calls through the seed binary because the
// test process has no SQL driver:
//
//	--cancel-order-op reset    puts the five orders back (pending / paid) and
//	                           drops the 'cancelled' events, so a retried or
//	                           repeated run starts clean;
//	--cancel-order-op inspect  prints {status, cancelled_events} of --order-id;
//	--cancel-order-op pay      marks --order-id paid, the way a payment landing
//	                           while the card is open would (the 409 case).
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

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
	cancelChannelID = "fe0000e2-0000-7000-8000-0000000000c0"

	// Orders of the 4-2 twin: one per person who cancels, one for the agent
	// (who must not), one already paid, one that gets paid under the card.
	cancelOrderOwner   = "fe0000e2-0000-7000-8000-0000000000c1"
	cancelOrderManager = "fe0000e2-0000-7000-8000-0000000000c2"
	cancelOrderAgent   = "fe0000e2-0000-7000-8000-0000000000c3"
	cancelOrderPaid    = "fe0000e2-0000-7000-8000-0000000000c4"
	cancelOrderRace    = "fe0000e2-0000-7000-8000-0000000000c5"
)

type cancelOrderSpec struct {
	id, status, buyer, email string
}

func cancelOrderSpecs() []cancelOrderSpec {
	return []cancelOrderSpec{
		{cancelOrderOwner, "pending_payment", "Cancel Owner Buyer", "cancel-owner@example.test"},
		{cancelOrderManager, "pending_payment", "Cancel Manager Buyer", "cancel-manager@example.test"},
		{cancelOrderAgent, "pending_payment", "Cancel Agent Buyer", "cancel-agent@example.test"},
		{cancelOrderPaid, "paid", "Cancel Paid Buyer", "cancel-paid@example.test"},
		{cancelOrderRace, "pending_payment", "Cancel Race Buyer", "cancel-race@example.test"},
	}
}

// applyCancelOrderFixture is idempotent AND resetting: every order returns to
// its starting status, with an active hold and no 'cancelled' event.
func applyCancelOrderFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("cancel-order fixture %s: %w", what, err)
		}
		return nil
	}
	if err := run("channel", `
		INSERT INTO sales_channels (id, org_id, name, settings)
		VALUES ($1, $2, 'E2E Cancel Channel', '{"hosted_page": {"enabled": true}}'::jsonb)
		ON CONFLICT (id) DO NOTHING`, cancelChannelID, orgID); err != nil {
		return err
	}
	for i, o := range cancelOrderSpecs() {
		// Ids of the hold rows follow the order's index, so a rerun finds them again.
		res := fmt.Sprintf("fe0000e2-0000-7000-8000-0000000001%02d", i)
		cs := fmt.Sprintf("fe0000e2-0000-7000-8000-0000000002%02d", i)
		resState, csState := "active", "pricing_confirmed"
		if o.status == "paid" {
			resState, csState = "converted", "completed"
		}
		if err := run("reservation", `
			INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
			VALUES ($1, $2, $3, $4, 1, $5, now() + interval '1 day')
			ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, expires_at = EXCLUDED.expires_at`,
			res, orgID, cancelChannelID, sessionID, resState); err != nil {
			return err
		}
		if err := run("checkout session", `
			INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state`,
			cs, orgID, cancelChannelID, res, csState); err != nil {
			return err
		}
		if err := run("order", `
			INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
			                    source, status, currency, subtotal, discount, charge, total,
			                    buyer_name, buyer_email, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', $8, 'EUR', 2500, 0, 0, 2500, $9, $10, now() - interval '10 minutes')
			ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, cancelled_at = NULL,
			                               paid_at = CASE WHEN EXCLUDED.status = 'paid' THEN now() ELSE NULL END`,
			o.id, orgID, cancelChannelID, eventID, sessionID, cs, res, o.status, o.buyer, o.email); err != nil {
			return err
		}
		if err := run("order events", `DELETE FROM order_events WHERE order_id = $1 AND type = 'cancelled'`, o.id); err != nil {
			return err
		}
	}
	return nil
}

// runCancelOrderOp serves --cancel-order-op. It prints one JSON line.
func runCancelOrderOp(op, orderID string) error {
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
		if err := applyCancelOrderFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]string{"ok": "reset"})
	case "inspect":
		var status string
		var events int
		if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status); err != nil {
			return fmt.Errorf("inspect order: %w", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'cancelled'`, orderID).Scan(&events); err != nil {
			return fmt.Errorf("inspect order events: %w", err)
		}
		return printCancelOrderJSON(map[string]any{"status": status, "cancelled_events": events})
	case "pay":
		if _, err := pool.Exec(ctx, `UPDATE orders SET status = 'paid', paid_at = now() WHERE id = $1 AND status = 'pending_payment'`, orderID); err != nil {
			return fmt.Errorf("pay order: %w", err)
		}
		return printCancelOrderJSON(map[string]string{"ok": "paid"})
	default:
		return fmt.Errorf("unknown --cancel-order-op %q (reset, inspect, pay)", op)
	}
}

func printCancelOrderJSON(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(body, '\n'))
	return err
}
