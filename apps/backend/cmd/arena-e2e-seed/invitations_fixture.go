package main

// Fixture of the web twin test of task 4-4 (invitations: issue, list with the
// state of the letter, annul; apps/event-center/tests/smoke/live/invitations.spec.ts).
//
// One extra event of the fixture organization, "E2E Invitations Show", with
// ONE general-admission date and ONE category of six real places (a category
// owns its places since migration 0101; a ticket_tiers row alone sells nothing
// and the issue call would answer 409 tier.sold_out). It has its own event so
// the places other twins use are never touched.
//
// The spec calls four operations through the seed binary, because the test
// process has no SQL driver:
//
//	--invitations-op reset    removes every invitation of the date (tickets,
//	                          delivery jobs, issuances) and puts the
//	                          six places back, so a repeated run starts clean;
//	--invitations-op inspect  prints {issuances, revoked, tickets, free_places};
//	--invitations-op deliver  --invitation-email X: the mail relay takes the
//	                          letter of the guest X and Brevo reports it
//	                          delivered (the harness runs no worker, so no
//	                          letter is ever sent by itself);
//	--invitations-op fail     --invitation-email X: the guest X's delivery job
//	                          ends failed (the relay refused it).
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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

const (
	invEventID   = "fe0000e2-0000-7000-8000-00000000d401"
	invSessionID = "fe0000e2-0000-7000-8000-00000000d402"
	invTierID    = "fe0000e2-0000-7000-8000-00000000d403"
	invLedgerID  = "fe0000e2-0000-7000-8000-00000000d404"

	invEventName = "E2E Invitations Show"
	invTierName  = "E2E Invitations GA"
	invPlaces    = 6
)

// applyInvitationsFixture is idempotent AND resetting: the date returns to
// six free places and no invitations.
func applyInvitationsFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("invitations fixture %s: %w", what, err)
		}
		return nil
	}
	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, invEventID, orgID, invEventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 35).Truncate(time.Hour)
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'general_admission', 'EUR', 'override')
		ON CONFLICT (id) DO NOTHING`, invSessionID, invEventID, venueID, start, invPlaces); err != nil {
		return err
	}
	// Everything the invitations of an earlier run left, children first.
	for _, step := range []struct{ what, sql string }{
		// email_letters rows stay: letters are dispute evidence and no code deletes
		// them (tests/staticanalysis/email_letters_retention_test.go). They hold no
		// foreign key to the tickets, so a run's letters just stay in the scratch base.
		{"worker jobs", `DELETE FROM worker_jobs WHERE payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE session_id = $1)`},
		{"delivery jobs", `DELETE FROM delivery_jobs WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`},
		{"barcodes", `DELETE FROM barcodes WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`},
		{"credentials", `DELETE FROM ticket_credentials WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`},
		{"tickets", `DELETE FROM tickets WHERE session_id = $1`},
		{"issuances", `DELETE FROM complimentary_issuances WHERE session_id = $1`},
		{"places", `DELETE FROM session_seats WHERE session_id = $1`},
		{"ledger", `DELETE FROM inventory_ledger WHERE session_id = $1`},
		{"category", `DELETE FROM ticket_tiers WHERE session_id = $1`},
	} {
		if err := run(step.what, step.sql, invSessionID); err != nil {
			return err
		}
	}
	if err := run("ledger row", `
		INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total)
		VALUES ($1, $2, NULL, $3)`, invLedgerID, invSessionID, invPlaces); err != nil {
		return err
	}
	if err := run("category row", `
		INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
		VALUES ($1, $2, $3, 'fixed', 1500, 'EUR', $4, 0)`, invTierID, invSessionID, invTierName, invPlaces); err != nil {
		return err
	}
	sid, _ := uuid.Parse(invSessionID)
	tid, _ := uuid.Parse(invTierID)
	if err := gaquota.CreateCategory(ctx, gen.New(tx), sid, tid, invPlaces); err != nil {
		return fmt.Errorf("invitations fixture places: %w", err)
	}
	return nil
}

// runInvitationsOp serves --invitations-op. It prints one JSON line.
func runInvitationsOp(op, email string) error {
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
		if err := applyInvitationsFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]string{"ok": "reset"})
	case "inspect":
		var issuances, revoked, tickets, free int
		q := func(dst *int, sql string) error {
			if err := pool.QueryRow(ctx, sql, invSessionID).Scan(dst); err != nil {
				return fmt.Errorf("inspect %q: %w", sql, err)
			}
			return nil
		}
		for _, s := range []struct {
			dst *int
			sql string
		}{
			{&issuances, `SELECT count(*) FROM complimentary_issuances WHERE session_id = $1`},
			{&revoked, `SELECT count(*) FROM complimentary_issuances WHERE session_id = $1 AND status = 'revoked'`},
			{&tickets, `SELECT count(*) FROM tickets WHERE session_id = $1 AND status = 'active'`},
			{&free, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND kind = 'ga_unit' AND status = 'available'`},
		} {
			if err := q(s.dst, s.sql); err != nil {
				return err
			}
		}
		return printCancelOrderJSON(map[string]int{"issuances": issuances, "revoked": revoked, "tickets": tickets, "free_places": free})
	case "deliver", "fail":
		var jobID, ticketID uuid.UUID
		err := pool.QueryRow(ctx, `
			SELECT dj.id, t.id FROM tickets t JOIN delivery_jobs dj ON dj.ticket_id = t.id
			WHERE t.session_id = $1 AND lower(t.holder_email) = lower($2)`, invSessionID, email).Scan(&jobID, &ticketID)
		if err != nil {
			return fmt.Errorf("find the delivery job of that guest: %w", err)
		}
		if op == "fail" {
			if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'failed', last_error = 'smtp refused the recipient' WHERE id = $1`, jobID); err != nil {
				return err
			}
			return printCancelOrderJSON(map[string]string{"ok": "failed"})
		}
		letter, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{
			OrgID: uuid.MustParse(orgID), Kind: emailstatus.KindInvitation, Recipient: email,
			DeliveryJobIDs: []uuid.UUID{jobID},
		})
		if err != nil {
			return fmt.Errorf("begin letter: %w", err)
		}
		if err := emailstatus.Accepted(ctx, pool, letter, "e2e-"+uuid.NewString()+"@relay", []uuid.UUID{jobID}); err != nil {
			return fmt.Errorf("accept letter: %w", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE id = $1`, jobID); err != nil {
			return err
		}
		name, class := emailstatus.Normalize("delivered")
		if _, err := emailstatus.Apply(ctx, pool, emailstatus.ProviderEvent{
			Event: name, RawEvent: "delivered", Class: class, LetterID: letter, At: time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("apply delivered: %w", err)
		}
		return printCancelOrderJSON(map[string]string{"ok": "delivered"})
	default:
		return fmt.Errorf("unknown --invitations-op %q (reset, inspect, deliver, fail)", op)
	}
}
