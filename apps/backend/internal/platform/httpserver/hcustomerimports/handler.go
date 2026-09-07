// Package hcustomerimports implements the platform.superadmin
// customer-imports admin HTTP surface (feature #520, W1-C7b, epic #468,
// spec §12.4): create an import record, run it in dry_run/apply mode, and
// inspect the resulting report/rows. Every mutation runs
// customerimport.RunImport (feature #519) synchronously in-process — there
// is no worker_jobs enqueue on this path.
package hcustomerimports

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// TxStarter is the narrow subset of PoolDB that hcustomerimports requires
// for INSERT of the customer_imports row. PoolDB satisfies this by
// structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for the customer-imports HTTP
// handlers.
type Handler struct {
	queries *gen.Queries
	pool    TxStarter
	// pgxPool is the raw pool RunImport needs (it opens its own
	// transactions internally rather than accepting a pgx.Tx).
	pgxPool *pgxpool.Pool
	media   *mediastore.Repo
	audit   audit.Writer
	logger  *slog.Logger
}

// New constructs a Handler from the caller's dependencies.
func New(queries *gen.Queries, pool TxStarter, pgxPool *pgxpool.Pool, mediaRepo *mediastore.Repo, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	return &Handler{
		queries: queries,
		pool:    pool,
		pgxPool: pgxPool,
		media:   mediaRepo,
		audit:   auditWriter,
		logger:  logger,
	}
}
