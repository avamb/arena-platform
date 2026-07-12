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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
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
// != general_admission) and the RESERVATION seated branch uses to
// translate ADR-005 seatId values (session_seats.id strings) into the
// canonical seat_keys the SEAT-C1 lock path consumes. Kept behind an
// interface so unit tests can substitute an in-memory fake. *gen.Queries
// satisfies this interface.
type SeatQuerier interface {
	ListSessionSeats(ctx context.Context, sessionID uuid.UUID) ([]gen.SessionSeatRow, error)
	GetSessionSeatByID(ctx context.Context, id, sessionID uuid.UUID) (gen.SessionSeatRow, error)
}

// ReservationContextQuerier resolves the tenant context a Bil24 RESERVATION
// needs: the session's owning organization (sessions → events join) and the
// sales channel addressed by the request's fid credential. *gen.Queries
// satisfies this interface.
type ReservationContextQuerier interface {
	GetSessionOrgContext(ctx context.Context, sessionID uuid.UUID) (gen.SessionOrgContextRow, error)
	GetSalesChannelByID(ctx context.Context, id, orgID uuid.UUID) (gen.SalesChannelRow, error)
}

// TierPriceQuerier resolves ticket-tier unit prices for the RESERVATION
// totalSum computation (guardrail #15 — the gateway never trusts client
// prices). *gen.Queries satisfies this interface.
type TierPriceQuerier interface {
	GetTicketTierByID(ctx context.Context, id, sessionID uuid.UUID) (gen.TicketTierRow, error)
	ListTicketTiersBySession(ctx context.Context, sessionID uuid.UUID) ([]gen.TicketTierRow, error)
}

// SeatedReserveFunc creates a real seated hold. Production wiring
// (bil24_shims.go) injects a closure over hcheckout.CreateSeatedHold; tests
// inject in-memory fakes. Never import package httpserver from here — the
// callback direction follows the PromoValidator precedent in feed_shims.go.
type SeatedReserveFunc func(ctx context.Context, in hcheckout.SeatedHoldInput) (hcheckout.SeatedHoldResult, error)

// GAReserveFunc creates a real general-admission hold (per-tier capacity +
// reservation_ga_items lines). Production wiring injects a closure over
// hcheckout.CreateGAHold.
type GAReserveFunc func(ctx context.Context, in hcheckout.GAHoldInput) (gen.ReservationRow, error)

// ReleaseHoldFunc releases a hold created by the RESERVATION command
// (UN_RESERVE). Production wiring injects a closure over
// hcheckout.ReleaseHold.
type ReleaseHoldFunc func(ctx context.Context, reservationID uuid.UUID) (gen.ReservationRow, error)

// ReservationDeps bundles the dependencies of the real RESERVATION /
// UN_RESERVE wiring (feature #312 second half). Every field is optional:
// when the reserve callbacks are missing the commands self-gate with a
// Bil24 envelope resultCode=-99 ("reservation service unavailable"),
// matching the nil-query precedent of the other commands.
type ReservationDeps struct {
	CtxQ          ReservationContextQuerier
	TierQ         TierPriceQuerier
	SeatedReserve SeatedReserveFunc
	GAReserve     GAReserveFunc
	Release       ReleaseHoldFunc
	PricingRules  hcheckout.PricingRules
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
	resDeps         ReservationDeps
	logger          *slog.Logger
}

// New constructs a Handler from the caller's dependencies.
//
// admissionQ, seatQ, and schemaQ are optional (may be nil): when omitted,
// GET_SEAT_LIST silently falls back to the pre-#312 tier-facade behavior
// for every session and GET_SCHEMA (§SEAT-D2, feature #313) returns
// resultCode=-99 ("schema service unavailable"). resDeps carries the real
// RESERVATION / UN_RESERVE wiring (feature #312 second half); when its
// callbacks are nil those commands self-gate with resultCode=-99
// ("reservation service unavailable"). Production wiring passes
// *gen.Queries values that satisfy the interfaces plus closures over the
// hcheckout hold API.
func New(
	eventQ *gen.Queries,
	tierQ *gen.Queries,
	checkoutQ *gen.Queries,
	ticketQ *gen.Queries,
	barcodeQ *gen.Queries,
	admissionQ AdmissionQuerier,
	seatQ SeatQuerier,
	schemaQ SchemaQuerier,
	resDeps ReservationDeps,
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
		resDeps:         resDeps,
		logger:          logger,
	}
}
