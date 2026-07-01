// Package hbarcode implements HTTP handlers for the barcode-authority
// federation domain: authority CRUD, individual barcode register / get /
// revoke, the authority-aware POST /v1/scan endpoint, and the external
// barcode-batch import + operator approval flow.
package hbarcode

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TxStarter is the narrow subset of PoolDB that hbarcode requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all barcode-domain HTTP handlers.
type Handler struct {
	barcodeQueries      *gen.Queries
	barcodeBatchQueries *gen.Queries
	pool                TxStarter
	logger              *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries are
// allowed; individual handlers self-gate with a 503 dependency.database_unavailable
// envelope, matching the *Server route-mount precedent.
func New(
	barcodeQ *gen.Queries,
	barcodeBatchQ *gen.Queries,
	pool TxStarter,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		barcodeQueries:      barcodeQ,
		barcodeBatchQueries: barcodeBatchQ,
		pool:                pool,
		logger:              logger,
	}
}
