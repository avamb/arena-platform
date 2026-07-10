// Package hbil24 implements the HTTP-layer entry point of the Bil24-compatible
// API gateway (feature #157, refined for feature #188, and extended for
// feature #312 Wave SEAT-D1 with real assigned-seat GET_SEAT_LIST +
// RESERVATION): the single-command dispatcher behind POST /compat/bil24/json
// and the per-command handlers that orchestrate platform queries.
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
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// AdmissionQuerier is the narrow contract handleBil24GetSeatList and
// handleBil24Reservation use to resolve a session's admission_mode. Kept
// as an interface so unit tests can substitute an in-memory fake for the
// (assigned_seats | general_admission | hybrid) branch table without a
// live PostgreSQL pool. *gen.Queries satisfies this interface.
type AdmissionQuerier interface {
	GetSessionAdmissionModeByID(ctx context.Context, sessionID uuid.UUID) (gen.SessionAdmissionRow, error)
}

// SeatQuerier is the narrow contract handleBil24GetSeatList uses to load
// the real assigned-seat rows for a session (branch when admission_mode
// != general_admission). Kept behind an interface so unit tests can
// substitute an in-memory fake. *gen.Queries satisfies this interface.
type SeatQuerier interface {
	ListSessionSeats(ctx context.Context, sessionID uuid.UUID) ([]gen.SessionSeatRow, error)
}

// SchemaQuerier is the narrow contract handleBil24GetSchema uses to load
// the bound seating-plan geometry payload plus the session's seat rows
// (feature #313, Wave SEAT-D2). Kept behind an interface so unit tests
// can substitute an in-memory fake without a live PostgreSQL pool.
// *gen.Queries satisfies this interface.
type SchemaQuerier interface {
	GetPublicSessionSchema(ctx context.Context, sessionID uuid.UUID) (gen.PublicSessionSchemaRow, error)
	ListSessionSeats(ctx context.Context, sessionID uuid.UUID) ([]gen.SessionSeatRow, error)
}

// Handler holds the shared dependencies for all Bil24-gateway command
// handlers. Every query handle is nilable; individual commands self-gate
// with a Bil24 envelope resultCode=-99 ("service unavailable") response,
// matching the *Server route-mount precedent.
//
// admissionQ and seatQ back the SEAT-D1 assigned-seat GET_SEAT_LIST /
// RESERVATION branches. Both are typed as interfaces so tests can inject
// deterministic fakes; production wiring passes *gen.Queries values.
type Handler struct {
	eventQueries    *gen.Queries
	tierQueries     *gen.Queries
	checkoutQueries *gen.Queries
	ticketQueries   *gen.Queries
	barcodeQueries  *gen.Queries
	admissionQ      AdmissionQuerier
	seatQ           SeatQuerier
	schemaQ         SchemaQuerier
	logger          *slog.Logger
}

// New constructs a Handler from the caller's dependencies.
//
// admissionQ, seatQ, and schemaQ are optional (may be nil): when omitted,
// GET_SEAT_LIST silently falls back to the pre-#312 tier-facade behavior
// for every session, RESERVATION reports the seating service as
// unavailable (resultCode=-99), and GET_SCHEMA (§SEAT-D2, feature #313)
// returns resultCode=-99 ("schema service unavailable"). Production
// wiring passes *gen.Queries values that satisfy the interfaces.
func New(
	eventQ *gen.Queries,
	tierQ *gen.Queries,
	checkoutQ *gen.Queries,
	ticketQ *gen.Queries,
	barcodeQ *gen.Queries,
	admissionQ AdmissionQuerier,
	seatQ SeatQuerier,
	schemaQ SchemaQuerier,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		eventQueries:    eventQ,
		tierQueries:     tierQ,
		checkoutQueries: checkoutQ,
		ticketQueries:   ticketQ,
		barcodeQueries:  barcodeQ,
		admissionQ:      admissionQ,
		seatQ:           seatQ,
		schemaQ:         schemaQ,
		logger:          logger,
	}
}
