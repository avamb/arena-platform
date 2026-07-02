// Package hgeo implements HTTP handlers for the geo reference domain
// (feature #123): public country/city read endpoints and the admin
// create/update endpoints with i18n_text name upserts.
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin geo_shims.go bridge in the parent package, matching the pattern
// established by hcatalog / hcheckout / htickets / hbarcode / hscanner /
// hreconciliation / hbilling.
package hgeo

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// pgUniqueViolation is the PostgreSQL error code for unique-constraint violations.
const pgUniqueViolation = "23505"

// TxStarter is the narrow subset of PoolDB that hgeo requires. PoolDB
// satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all geo-domain HTTP handlers.
// The cfg pointer is read by GeoLocale for the default/active locale set;
// a nil cfg falls back to "en" exactly as the original *Server method did.
type Handler struct {
	queries *gen.Queries
	pool    TxStarter
	cfg     *config.Config
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	queries *gen.Queries,
	pool TxStarter,
	cfg *config.Config,
) *Handler {
	return &Handler{
		queries: queries,
		pool:    pool,
		cfg:     cfg,
	}
}
