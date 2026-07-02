// Package hgdpr implements the GDPR data subject request domain
// (feature #164): the HTTP endpoints that queue export/delete requests and
// record consent, plus the GDPRProcessor background worker that processes
// pending data_subject_requests rows.
//
// The HTTP handlers live behind a small Handler struct so *Server can wire
// them via a thin gdpr_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation / hbilling.
package hgdpr

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TxStarter is the narrow subset of PoolDB that hgdpr requires. PoolDB
// satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all GDPR-domain HTTP handlers.
// Request-scoped logging uses logging.FromContext, so no logger field is
// needed here (the background GDPRProcessor carries its own logger).
type Handler struct {
	queries *gen.Queries
	pool    TxStarter
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	queries *gen.Queries,
	pool TxStarter,
) *Handler {
	return &Handler{
		queries: queries,
		pool:    pool,
	}
}
