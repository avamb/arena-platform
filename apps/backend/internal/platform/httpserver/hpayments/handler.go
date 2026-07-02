// Package hpayments implements HTTP handlers for the payment-provider-config
// domain (feature #237): the CRUD surface an org admin uses to wire Stripe,
// AllPay and friends into the platform.
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin payments_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation / hbilling / hgeo / hgdpr / hfeed / hinventory.
package hpayments

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

const pgUniqueViolation = "23505"

// TxStarter is the narrow subset of PoolDB that hpayments requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all payment-config HTTP handlers.
type Handler struct {
	paymentConfigQueries *gen.Queries
	pool                 TxStarter
	audit                audit.Writer
	logger               *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	paymentConfigQ *gen.Queries,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		paymentConfigQueries: paymentConfigQ,
		pool:                 pool,
		audit:                auditWriter,
		logger:               logger,
	}
}
