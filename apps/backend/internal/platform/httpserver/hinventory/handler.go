// Package hinventory implements HTTP handlers for the inventory domain:
// the session capacity ledger (feature #130 — GA-first reserve / release /
// confirm operations) and the external allocation quota model (feature #145 —
// partner quota blocks carved out of platform inventory).
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin inventory_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation / hbilling / hgeo / hgdpr / hfeed.
package hinventory

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TxStarter is the narrow subset of PoolDB that hinventory requires. PoolDB
// satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all inventory-domain HTTP
// handlers. inventoryQueries serves the inventory_ledger.sql methods;
// allocationQueries serves the external_allocations.sql methods. Both sit on
// the same *gen.Queries type and are wired from the corresponding *Server
// fields by inventory_shims.go.
type Handler struct {
	inventoryQueries  *gen.Queries
	allocationQueries *gen.Queries
	pool              TxStarter
	logger            *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	inventoryQ *gen.Queries,
	allocationQ *gen.Queries,
	pool TxStarter,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		inventoryQueries:  inventoryQ,
		allocationQueries: allocationQ,
		pool:              pool,
		logger:            logger,
	}
}
