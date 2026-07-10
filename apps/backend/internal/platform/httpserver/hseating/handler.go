// Package hseating implements HTTP handlers for the seating-plan CRUD /
// fork / version surface (feature #304, Wave SEAT-A3). It follows the
// hbankaccounts / hpayments sub-package pattern: a small Handler struct
// receives every request, the parent *Server exposes thin delegating
// shims via seating_shims.go, and mount_seating.go wires the chi routes.
//
// The endpoints served here are (all under the existing auth middleware
// with RBAC per §5.1 of 09_autoforge/seating_backlog.md):
//
//	GET    /v1/venues/{venue_id}/seating-plans       — list by venue
//	POST   /v1/venues/{venue_id}/seating-plans       — create draft
//	GET    /v1/seating-plans/{id}                    — read one
//	PATCH  /v1/seating-plans/{id}                    — mutate metadata
//	POST   /v1/seating-plans/{id}/fork               — fork (copy latest)
//	POST   /v1/seating-plans/{id}/versions           — new version (svg | geometry)
//	GET    /v1/seating-plans/{id}/versions/{n}       — read a single version
//
// The geometry importer / canonicaliser lives under internal/domain/seating
// (SEAT-A2) and is called inline by version create. Sensitive geometry /
// SVG parsing is stdlib-only and pool-independent so import-only routes
// keep working even when the transactional pool is unavailable.
package hseating

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// TxStarter is the narrow subset of PoolDB that hseating requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for every hseating HTTP handler.
type Handler struct {
	queries *gen.Queries
	pool    TxStarter
	audit   audit.Writer
	logger  *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	seatingQ *gen.Queries,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		queries: seatingQ,
		pool:    pool,
		audit:   auditWriter,
		logger:  logger,
	}
}
