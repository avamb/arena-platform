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
	// GetSessionSeatBySystemSeatID resolves the wave-1 wire seatId
	// (session_seats.system_seat_id — bigint, migration 0088) to a seat
	// row. Feature #476 (W1-A2b) spec §4/§7.4: the RESERVATION seated
	// branch calls this variant when compatDB is wired so the wire stays
	// int64 end-to-end; the nil-compatDB fallback keeps calling
	// GetSessionSeatByID with the ADR-005 UUID passthrough.
	GetSessionSeatBySystemSeatID(ctx context.Context, sessionID uuid.UUID, systemSeatID int64) (gen.SessionSeatRow, error)
}

// ReservationContextQuerier resolves the tenant context a Bil24 RESERVATION
// needs: the session's owning organization (sessions → events join) and the
// sales channel addressed by the request's fid credential. *gen.Queries
// satisfies this interface.
//
// GetReservationByID (feature #381) is required for UN_RESERVE credential
// enforcement: UN_RESERVE supplies a reservationId but not a fid, so the
// handler looks up the reservation to find its owning channel, then validates
// the token against that channel's gateway_token_hash.
type ReservationContextQuerier interface {
	GetSessionOrgContext(ctx context.Context, sessionID uuid.UUID) (gen.SessionOrgContextRow, error)
	GetSalesChannelByID(ctx context.Context, id, orgID uuid.UUID) (gen.SalesChannelRow, error)
	// GetReservationByID fetches a reservation by ID so UN_RESERVE can resolve
	// the owning sales channel for fid/token credential validation.
	// Feature #381, PR2-25 variant A.
	GetReservationByID(ctx context.Context, id uuid.UUID) (gen.ReservationRow, error)
	// GetSalesChannelByIDGlobal fetches a sales channel by primary key without
	// an org filter so SCAN_TICKET — which carries a fid but no session or
	// reservation to derive the org from — can validate its fid/token
	// credential. Feature #390, PR2-32.
	GetSalesChannelByIDGlobal(ctx context.Context, id uuid.UUID) (gen.SalesChannelRow, error)
}

// TierPriceQuerier resolves ticket-tier unit prices for the RESERVATION
// totalSum computation (guardrail #15 — the gateway never trusts client
// prices). *gen.Queries satisfies this interface.
type TierPriceQuerier interface {
	GetTicketTierByID(ctx context.Context, id, sessionID uuid.UUID) (gen.TicketTierRow, error)
	ListTicketTiersBySession(ctx context.Context, sessionID uuid.UUID) ([]gen.TicketTierRow, error)
	// ListTierPriceWindows feeds the AB-48 resolver (priceresolve).
	ListTierPriceWindows(ctx context.Context, tierIDs []uuid.UUID) ([]gen.TicketTierPriceRow, error)
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
//
// requireToken (feature #374, extended by #390): when true, every
// state-mutating command (RESERVATION, UN_RESERVE, SCAN_TICKET) validates
// the request's fid/token pair against the gateway_token_hash stored in
// the sales channel's settings JSON before performing any DB writes.
// Channels without a stored hash are rejected when requireToken=true.
// Default false preserves backward compatibility with existing deployments.
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
	requireToken    bool // feature #374: enforce fid/token auth on mutating commands

	// channelQ (feature #471, W1-A1b) is the read surface for resolving the
	// wire `fid` to a sales_channels row. When non-nil, per-command auth
	// (spec §5) runs against it: fid → display_number → channel → org_id.
	// A nil channelQ falls through to the pre-W1 unit-test behaviour where
	// individual commands self-gate. Production wiring passes *gen.Queries.
	channelQ ChannelLookupQuerier

	// compatDB (feature #476, W1-A2b) is the DBTX handle the per-command
	// handlers use to resolve/mint bigint compatibility ids via package
	// compatids (spec §3.1, §4). Production wiring passes the *pgxpool.Pool
	// so lazy mint-on-read runs as a single ON CONFLICT DO NOTHING round-trip
	// without opening an ambient transaction. A nil compatDB preserves the
	// pre-W1 wire behaviour (UUID strings via TranslatePlatformID) so unit
	// tests that build a Handler without a pool keep passing; the wire-fixture
	// guardrail (tests/compat/bil24/no_uuid_in_wire_test.go) ensures the
	// production path never regresses.
	compatDB gen.DBTX
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
//
// Token authentication is OFF by default; call WithRequireToken(true) on
// the returned handler to enforce fid/token validation for mutating
// commands (feature #374).
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

// WithRequireToken enables or disables fid/token authentication for
// state-mutating commands (RESERVATION, UN_RESERVE, SCAN_TICKET). When enabled, every
// such command must supply a token that matches the bcrypt hash stored in
// the target sales channel's settings JSON under the key
// "gateway_token_hash". Channels without a stored hash are rejected when
// requireToken=true. Returns the receiver for chaining.
//
// Production deployments SHOULD set this to true by enabling the
// BIL24_REQUIRE_TOKEN environment variable (feature #374).
func (h *Handler) WithRequireToken(v bool) *Handler {
	h.requireToken = v
	return h
}

// WithChannelLookup wires the ChannelLookupQuerier used by the W1 auth path
// (feature #471) to resolve the wire `fid` (int64 display_number) to a
// sales_channels row before any read/hold command runs. Callers that omit
// this call retain the pre-W1 unit-test behaviour where individual commands
// self-gate on the ReservationContextQuerier. Returns the receiver for
// chaining.
func (h *Handler) WithChannelLookup(q ChannelLookupQuerier) *Handler {
	h.channelQ = q
	return h
}

// WithCompatDB wires the DBTX handle used by per-command handlers to
// resolve/mint bigint compatibility ids via package compatids (feature #476,
// W1-A2b, spec §3.1 / §4). Production wiring passes the *pgxpool.Pool so the
// mint-on-read call (ON CONFLICT DO NOTHING) runs as a single round-trip
// without an ambient transaction. Callers that omit this setter retain the
// pre-W1 wire behaviour where wire IDs are emitted as UUID strings via
// TranslatePlatformID (safe for unit tests that build a Handler without a
// pool; the wire-fixture guardrail catches any regression on the production
// path). Returns the receiver for chaining.
func (h *Handler) WithCompatDB(db gen.DBTX) *Handler {
	h.compatDB = db
	return h
}
