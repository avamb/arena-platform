// Package hscanner implements HTTP handlers for the scanner domain:
// outbox event publishing for scanner-relevant ticket lifecycle events
// (feature #143), offline-friendly snapshot + online validate endpoints
// (feature #144), and the external-scanner scan-events ingest callback
// (feature #293 / S-2).
package hscanner

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// TxStarter is the narrow subset of PoolDB that hscanner requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// RateLimiter is the narrow surface hscanner needs from an in-memory rate
// limiter. The concrete scannerRateLimiter type lives in package httpserver
// (so unit tests can swap the package-level serverScannerRL var and inspect
// the ipLimit / sessionLimit fields directly) and satisfies this interface
// through wrapper methods.
type RateLimiter interface {
	CheckIP(ip string) bool
	CheckSession(sessionID string) bool
}

// OutboxWriter narrows outbox.Writer to what hscanner actually calls. It is
// satisfied structurally by the concrete outbox.Writer used by *Server.
type OutboxWriter interface {
	Append(ctx context.Context, tx pgx.Tx, event outbox.Event) error
}

// Handler holds the shared dependencies for all scanner-domain HTTP handlers
// and outbox publishers. Nil dependencies are tolerated by individual handlers:
// snapshot / validate self-gate with 503 dependency.database_unavailable when
// barcodeQueries is nil, the scan-events ingest self-gates on feedTokenQueries,
// and every publish helper silently no-ops when pool or outboxWriter is nil
// so unit-test servers do not have to wire the outbox pipeline.
type Handler struct {
	barcodeQueries   *gen.Queries
	feedTokenQueries *gen.Queries
	pool             TxStarter
	outboxWriter     OutboxWriter
	rateLimiter      RateLimiter
	logger           *slog.Logger
}

// New constructs a Handler from the caller's dependencies.
func New(
	barcodeQ *gen.Queries,
	feedTokenQ *gen.Queries,
	pool TxStarter,
	outboxWriter OutboxWriter,
	rateLimiter RateLimiter,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		barcodeQueries:   barcodeQ,
		feedTokenQueries: feedTokenQ,
		pool:             pool,
		outboxWriter:     outboxWriter,
		rateLimiter:      rateLimiter,
		logger:           logger,
	}
}
