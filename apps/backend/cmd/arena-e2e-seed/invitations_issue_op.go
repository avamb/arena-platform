package main

// --invitations-op issue: runs, for the staff invitations of the invitations
// fixture date, what arena-worker's checkout.issue_tickets job would run.
//
// The live harness starts arena-api only. A staff invitation (spec 46) creates
// its order and a checkout.issue_tickets job in one transaction, and the tickets
// are minted by the worker, so without a worker every invitation of the twin
// stays "issuing" for ever and can be neither canceled nor resent. This op calls
// the same function the job calls (htickets.Handler.IssueTicketsForCheckout, wired
// as cmd/arena-worker/main.go wires it) for every checkout session of the date.
// It is idempotent: a checkout session that already has its tickets gets the same
// ones back.

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// issueInvitationTickets mints the tickets of every staff invitation of the date and
// prints how many checkout sessions it handled.
func issueInvitationTickets(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT cs.id FROM checkout_sessions cs
		  JOIN reservations r ON r.id = cs.reservation_id
		 WHERE r.session_id = $1
		 ORDER BY cs.created_at`, invSessionID)
	if err != nil {
		return fmt.Errorf("list the checkout sessions of the date: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	queries := gen.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuer := htickets.New(
		queries, queries, nil, nil, queries, nil, queries, nil,
		pool, pool, nil, logger, nil, nil, nil,
	).WithOutboxWriter(outbox.NewPGEventsWriter(pool))

	tickets := 0
	for _, id := range ids {
		cs, err := queries.GetCheckoutSessionByID(ctx, id)
		if err != nil {
			return fmt.Errorf("read checkout session %s: %w", id, err)
		}
		issued, err := issuer.IssueTicketsForCheckout(ctx, cs)
		if err != nil {
			return fmt.Errorf("issue the tickets of checkout session %s: %w", id, err)
		}
		tickets += len(issued)
	}
	return printCancelOrderJSON(map[string]int{"checkout_sessions": len(ids), "tickets": tickets})
}
