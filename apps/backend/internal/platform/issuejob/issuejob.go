// Package issuejob implements the "checkout.issue_tickets" background worker job
// (feature #363, PR2-07 BLOCKER: make webhook processing atomic and durable).
//
// # Problem
//
// The original HandlePaymentIntentWebhook inserted the idempotency event
// (InsertPaymentIntentEvent) in one auto-commit statement, then updated the
// payment intent state (UpdatePaymentIntentState) in a second auto-commit
// statement, and then called issueTickets inline (best-effort).
//
// If the process crashed between the event INSERT and the state UPDATE — or
// between the state UPDATE and the inline issueTickets call — the idempotency
// row was already committed. Provider redelivery hit the ON CONFLICT guard and
// returned 204 without re-processing. Result: the payment intent remained in a
// non-terminal state and tickets were never issued.
//
// # Fix
//
// HandlePaymentIntentWebhook now performs three operations in a single
// PostgreSQL transaction:
//
//  1. InsertPaymentIntentEvent (idempotency guard)
//  2. UpdatePaymentIntentState
//  3. INSERT a checkout.issue_tickets worker_jobs row (this package)
//
// Because all three are committed atomically:
//
//   - A crash before the commit leaves no idempotency row, so provider
//     redelivery processes the event from scratch (step 3 of feature #363).
//   - A crash after the commit leaves the worker job in the database.
//     The job runner picks it up and calls IssueTicketsForCheckout, which
//     is idempotent (feature #366) — if tickets were already partially or
//     fully issued, it returns them without inserting duplicates.
//
// # Delivery
//
// Delivery jobs (email dispatch) are enqueued inside IssueTicketsForCheckout
// / EnqueueDeliveryJobs (feature #367). No separate delivery step is needed in
// this handler.
package issuejob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// JobType is the worker job_type string for checkout ticket issuance.
// It is used both when enqueueing the job (in HandlePaymentIntentWebhook's
// atomic transaction) and when registering the handler in arena-worker.
const JobType = "checkout.issue_tickets"

// Payload is the JSON payload stored in worker_jobs.payload for a
// checkout.issue_tickets job. The checkout_session_id is enough to reconstruct
// the full issuance context at handler time.
type Payload struct {
	CheckoutSessionID string `json:"checkout_session_id"`
}

// HandlerOptions bundles the dependencies required by the checkout.issue_tickets
// worker job handler.
type HandlerOptions struct {
	// Queries provides database access for fetching the checkout session.
	// Must not be nil; the handler returns an error immediately when nil.
	Queries *gen.Queries

	// IssueTickets is the idempotent ticket-issuance function wired by the
	// caller. In production this is htickets.Handler.IssueTicketsForCheckout.
	// When nil, the handler logs a warning and returns nil (no error) so the
	// job is not retried — callers should always wire a real implementation.
	IssueTickets func(ctx context.Context, cs gen.CheckoutSessionRow) ([]gen.TicketRow, error)

	// Logger receives structured log output. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// NewHandler returns a worker handler function for "checkout.issue_tickets" jobs.
//
// The handler:
//  1. Unmarshals the Payload to obtain the checkout_session_id.
//  2. Fetches the checkout session from the database.
//  3. Calls opts.IssueTickets (IssueTicketsForCheckout, feature #366) to issue
//     or re-issue tickets idempotently. Delivery jobs are enqueued inside
//     IssueTicketsForCheckout (feature #367), so no separate delivery step is
//     required.
//
// Errors returned from the handler cause the worker to retry the job up to the
// configured max_attempts. The job is considered permanently failed when
// max_attempts is exhausted and the row is moved to worker_dead_letter.
func NewHandler(opts HandlerOptions) func(ctx context.Context, payload []byte) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return func(ctx context.Context, payload []byte) error {
		var p Payload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("checkout.issue_tickets: unmarshal payload: %w", err)
		}
		csID, err := uuid.Parse(p.CheckoutSessionID)
		if err != nil {
			return fmt.Errorf("checkout.issue_tickets: invalid checkout_session_id %q: %w", p.CheckoutSessionID, err)
		}

		if opts.Queries == nil {
			return fmt.Errorf("checkout.issue_tickets: queries not configured")
		}
		cs, err := opts.Queries.GetCheckoutSessionByID(ctx, csID)
		if err != nil {
			return fmt.Errorf("checkout.issue_tickets: get checkout session %s: %w", csID, err)
		}

		if opts.IssueTickets == nil {
			logger.Warn("checkout.issue_tickets: IssueTickets callback not configured; skipping",
				slog.String("checkout_session_id", csID.String()),
			)
			return nil
		}

		tickets, err := opts.IssueTickets(ctx, cs)
		if err != nil {
			return fmt.Errorf("checkout.issue_tickets: issue tickets for checkout %s: %w", csID, err)
		}

		logger.Info("checkout.issue_tickets: tickets issued",
			slog.String("checkout_session_id", csID.String()),
			slog.Int("count", len(tickets)),
		)
		return nil
	}
}
