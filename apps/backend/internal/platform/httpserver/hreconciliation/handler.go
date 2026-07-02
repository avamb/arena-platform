// Package hreconciliation implements HTTP handlers for the external
// reconciliation domain (feature #147): partner-submitted sales/return
// reports, the auto-match algorithm, the exception queue, and operator
// review endpoints.
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin reconciliation_shims.go bridge in the parent package, matching
// the pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner.
package hreconciliation

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ConfidenceThreshold is the minimum confidence score for a reconciliation
// line to be auto-matched. Lines below this threshold become exceptions.
// Exported so the *Server-side shim can re-export the original unexported
// name (reconciliationConfidenceThreshold) that reconciliation_147_test.go
// references at compile time.
const ConfidenceThreshold = 80

// TxStarter is the narrow subset of PoolDB that hreconciliation requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all reconciliation-domain HTTP
// handlers. The queries object exposes both the reconciliation.sql methods
// (Insert*/Get*/List*/Update*/Count*) and the external_allocations.sql
// GetExternalAllocationByID method used by the submit flow, since both
// query groups sit on the same *gen.Queries type.
type Handler struct {
	queries *gen.Queries
	pool    TxStarter
	logger  *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries are
// allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	queries *gen.Queries,
	pool TxStarter,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		queries: queries,
		pool:    pool,
		logger:  logger,
	}
}
