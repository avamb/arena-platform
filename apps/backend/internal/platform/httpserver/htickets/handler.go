// Package htickets implements HTTP handlers for the tickets domain:
// ticket issuance and read, ticket credentials (QR + PDF), complimentary
// ticket issuance and revocation, post-issuance delivery enqueueing, and
// the support-console admin endpoints for ticket delivery and scan history.
package htickets

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// OutboxWriter is the narrow append contract htickets needs. Unlike the
// ticket.issued publishers — which are injected callbacks that open their own
// short-lived transaction after the fact — v1.order.paid is written on the
// ISSUANCE transaction itself (W1-A6c, feature #488). That is what makes it
// exactly-once: the advisory lock plus UNIQUE (checkout_session_id, ordinal)
// already guarantee a single caller completes the last ordinal, and putting
// the outbox row in the same commit means the event cannot be emitted for
// tickets that were rolled back, nor lost for tickets that were kept.
type OutboxWriter interface {
	Append(ctx context.Context, tx pgx.Tx, event outbox.Event) error
}

// TxStarter is the narrow subset of PoolDB that htickets requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all ticket-domain HTTP handlers.
type Handler struct {
	ticketQueries        *gen.Queries
	credentialQueries    *gen.Queries
	complimentaryQueries *gen.Queries
	inventoryQueries     *gen.Queries
	reservationQueries   *gen.Queries
	barcodeQueries       *gen.Queries
	deliveryJobQueries   *gen.Queries
	feedTokenQueries     *gen.Queries
	workerPool           *pgxpool.Pool
	pool                 TxStarter
	audit                audit.Writer
	logger               *slog.Logger

	// Cross-domain callbacks. tickets.IssueTicketsForCheckout publishes
	// ticket.issued events; complimentary revocation publishes per-ticket
	// ticket.revoked events; AB-49 operator cancellation publishes
	// v1.ticket.cancelled. Injecting them keeps htickets free of the
	// scanner-events writer wiring.
	publishTicketIssuedEvents    func(ctx context.Context, tickets []gen.TicketRow)
	publishTicketRevokedV1Events func(ctx context.Context, ticketIDs []string, complimentaryIssuanceID, reason string)
	publishTicketCancelledEvent  func(ctx context.Context, ticket gen.TicketRow, reason, refundMode string)

	// outboxWriter is optional: when nil, issuance still links tickets to
	// their order but publishes no v1.order.paid. Attached via
	// WithOutboxWriter rather than a New parameter so the existing callers
	// (tickets_shims.go, arena-worker, the MACS integration test) opt in
	// individually.
	outboxWriter OutboxWriter
}

// WithOutboxWriter attaches the transactional outbox writer used to publish
// v1.order.paid from IssueTicketsForCheckout. Returns h so it can be chained
// onto a New(...) call.
func (h *Handler) WithOutboxWriter(w OutboxWriter) *Handler {
	h.outboxWriter = w
	return h
}

// New constructs a Handler from the caller's dependencies. Nil queries are
// allowed; individual handlers self-gate with a 503 dependency.database_unavailable
// envelope, matching the *Server route-mount precedent.
func New(
	ticketQ *gen.Queries,
	credentialQ *gen.Queries,
	complimentaryQ *gen.Queries,
	inventoryQ *gen.Queries,
	reservationQ *gen.Queries,
	barcodeQ *gen.Queries,
	deliveryJobQ *gen.Queries,
	feedTokenQ *gen.Queries,
	workerPool *pgxpool.Pool,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
	publishTicketIssuedEvents func(ctx context.Context, tickets []gen.TicketRow),
	publishTicketRevokedV1Events func(ctx context.Context, ticketIDs []string, complimentaryIssuanceID, reason string),
	publishTicketCancelledEvent func(ctx context.Context, ticket gen.TicketRow, reason, refundMode string),
) *Handler {
	return &Handler{
		ticketQueries:                ticketQ,
		credentialQueries:            credentialQ,
		complimentaryQueries:         complimentaryQ,
		inventoryQueries:             inventoryQ,
		reservationQueries:           reservationQ,
		barcodeQueries:               barcodeQ,
		deliveryJobQueries:           deliveryJobQ,
		feedTokenQueries:             feedTokenQ,
		workerPool:                   workerPool,
		pool:                         pool,
		audit:                        auditWriter,
		logger:                       logger,
		publishTicketIssuedEvents:    publishTicketIssuedEvents,
		publishTicketRevokedV1Events: publishTicketRevokedV1Events,
		publishTicketCancelledEvent:  publishTicketCancelledEvent,
	}
}
