// Package hreports implements HTTP handlers for the post-event reporting
// domain: report reads and on-demand generation triggers (feature #159) plus
// the report-delivery worker-job enqueue hook (feature #160).
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin reports_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation / hbilling / hgeo / hgdpr / hfeed / hinventory.
package hreports

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Handler holds the shared dependencies for all report-domain HTTP handlers.
// reportQueries serves the event_reports.sql methods; workerPool is the raw
// pgx pool used to enqueue report.deliver worker jobs (nil when the delivery
// infrastructure is not configured).
type Handler struct {
	reportQueries *gen.Queries
	workerPool    *pgxpool.Pool
	logger        *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil worker pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope (and the enqueue hook no-ops),
// matching the *Server route-mount precedent.
func New(
	reportQ *gen.Queries,
	workerPool *pgxpool.Pool,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		reportQueries: reportQ,
		workerPool:    workerPool,
		logger:        logger,
	}
}
