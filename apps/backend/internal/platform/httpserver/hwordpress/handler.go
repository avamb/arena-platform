// Package hwordpress implements the WordPress webhook subscriber registry
// (feature #156): registration, listing, updating and deactivation of
// webhook subscribers plus the recent-deliveries admin panel read view.
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin wordpress_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation / hbilling / hgeo / hgdpr / hfeed.
//
// Cross-domain note: unique-violation detection reuses the barcode domain's
// exported hbarcode.IsUniqueViolation via a direct sub-package →
// sub-package import (the same helper the parent package forwards in
// barcode_shims.go).
package hwordpress

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// RowQuerier is the narrow subset of PoolDB that hwordpress requires: the
// recent-deliveries panel reads the outbox table with raw SQL. PoolDB
// satisfies this by structural typing.
type RowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Handler holds the shared dependencies for all WordPress-webhook-domain
// HTTP handlers.
type Handler struct {
	queries *gen.Queries
	pool    RowQuerier
	logger  *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// service_unavailable envelope, matching the *Server route-mount precedent.
func New(
	queries *gen.Queries,
	pool RowQuerier,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		queries: queries,
		pool:    pool,
		logger:  logger,
	}
}
