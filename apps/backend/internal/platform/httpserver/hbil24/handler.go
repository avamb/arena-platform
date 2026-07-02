// Package hbil24 implements the HTTP-layer entry point of the Bil24-compatible
// API gateway (feature #157, refined for feature #188): the single-command
// dispatcher behind POST /compat/bil24/json and the per-command handlers that
// orchestrate platform queries.
//
// The wire format itself (request/response envelope, result codes, ID
// translation helpers) lives in the dedicated adapter package
// internal/adapters/bil24compat; this package re-exports the aliases its
// handler bodies use so the moved code stays byte-comparable with its
// pre-refactor form.
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin bil24_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation / hbilling / hgeo / hgdpr / hfeed / hwordpress.
// Route mounting (mountCompatRoutes) and the BIL24_COMPAT_ENABLED feature
// flag stay in the parent package because they touch *Server state
// (s.router / s.bil24Enabled).
package hbil24

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Handler holds the shared dependencies for all Bil24-gateway command
// handlers. Every query handle is nilable; individual commands self-gate
// with a Bil24 envelope resultCode=-99 ("service unavailable") response,
// matching the *Server route-mount precedent.
type Handler struct {
	eventQueries    *gen.Queries
	tierQueries     *gen.Queries
	checkoutQueries *gen.Queries
	ticketQueries   *gen.Queries
	barcodeQueries  *gen.Queries
	logger          *slog.Logger
}

// New constructs a Handler from the caller's dependencies.
func New(
	eventQ *gen.Queries,
	tierQ *gen.Queries,
	checkoutQ *gen.Queries,
	ticketQ *gen.Queries,
	barcodeQ *gen.Queries,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		eventQueries:    eventQ,
		tierQueries:     tierQ,
		checkoutQueries: checkoutQ,
		ticketQueries:   ticketQ,
		barcodeQueries:  barcodeQ,
		logger:          logger,
	}
}
