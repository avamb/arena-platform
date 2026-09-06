// bil24_shims.go bridges the *Server god-object to the hbil24 sub-package.
// All per-command handler bodies live in hbil24/; this file keeps three
// kinds of surface in package httpserver:
//
//   - thin delegating methods with the ORIGINAL lowercase names
//     (handleBil24Command) so route mounting stays unchanged;
//   - the route mount (mountCompatRoutes) and the BIL24_COMPAT_ENABLED
//     feature-flag accessor (bil24CompatEnabled), which touch *Server state
//     (s.router / s.bil24Enabled) and therefore cannot move;
//   - the Bil24 wire-format aliases / forwarders (bil24Request, bil24Response,
//     bil24OK, bil24Error, writeBil24JSON, ResultCode*, TranslateLegacyID,
//     TranslatePlatformID, ErrLegacyIDNotFound) that bil24_compat_157_test.go
//     references unqualified in package httpserver. The adapter package
//     internal/adapters/bil24compat remains the source of truth.
package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbil24"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// bil24Handler constructs an hbil24.Handler from the server's dependencies.
// A fresh handler per request keeps the wiring uniform with hwordpress /
// hgeo / hfeed and avoids stale captures when test code mutates *Server
// fields between calls.
func (s *Server) bil24Handler() *hbil24.Handler {
	// admissionQ and seatQ (feature #312 Wave SEAT-D1) reuse
	// s.sessionQueries and s.seatingQueries — the same *gen.Queries
	// values wired at Server construction. Passed nil when the Server
	// has not been constructed with a pool (nil-safe: the SEAT-D1
	// GET_SEAT_LIST branch and RESERVATION dispatcher self-gate on
	// h.admissionQ / h.seatQ before dereferencing).
	var admissionQ hbil24.AdmissionQuerier
	if s.sessionQueries != nil {
		admissionQ = s.sessionQueries
	}
	var seatQ hbil24.SeatQuerier
	if s.seatingQueries != nil {
		seatQ = s.seatingQueries
	}
	// schemaQ (feature #313 Wave SEAT-D2) reuses s.seatingQueries: the
	// same *gen.Queries value already satisfies GetPublicSessionSchema
	// (from queries/session_seating_public.sql) and ListSessionSeats.
	// Nil-safe: handleBil24GetSchema self-gates and returns
	// resultCode=-99 when the dependency is unavailable.
	var schemaQ hbil24.SchemaQuerier
	if s.seatingQueries != nil {
		schemaQ = s.seatingQueries
	}
	// Feature #381 / PR2-25: wire credential enforcement from the production
	// config (Options.Bil24RequireToken → s.bil24RequireToken). This ensures
	// that the production composition root (main.go Options) — not a test
	// helper — controls whether fid/token validation is active.
	// BIL24_REQUIRE_TOKEN defaults to true in config; it is false only when
	// explicitly overridden (e.g. in a test that has not set the field).
	h := hbil24.New(
		s.eventQueries,
		s.tierQueries,
		s.checkoutQueries,
		s.ticketQueries,
		s.barcodeQueries,
		admissionQ,
		seatQ,
		schemaQ,
		s.bil24ReservationDeps(),
		s.logger,
	).WithRequireToken(s.bil24RequireToken)
	// Feature #471 (W1-A1b): wire the channel-lookup surface so the auth
	// path can resolve wire `fid` (display_number int64) → sales_channels
	// row → org_id before any read/hold command runs. The org_id gates
	// per-tenant catalog / seat-list / order visibility (spec §5.2, §7).
	if s.channelQueries != nil {
		h = h.WithChannelLookup(s.channelQueries)
	}
	// Feature #476 (W1-A2b): wire the compatibility_id_map DBTX handle so
	// per-command handlers can resolve/mint bigint ids via package compatids
	// (spec §3.1, §4). PoolDB is a superset of gen.DBTX (Exec/Query/QueryRow),
	// so *pgxpool.Pool satisfies it. Nil-safe: a Server built without a pool
	// (many unit tests) leaves the pre-W1 UUID-string wire form in place.
	if s.pool != nil {
		h = h.WithCompatDB(s.pool)
	}
	// Feature #478 (W1-A3b): wire the platform i18n bundle so non-OK
	// descriptions surface in the request's negotiated locale (ru/en/he/
	// cs) per spec §6. A nil bundle preserves the English wire byte
	// surface — unit tests without a wired bundle keep their existing
	// substring expectations.
	if s.bundle != nil {
		h = h.WithBundle(s.bundle)
	}
	// Feature #481 (W1-A4c): wire CREATE_USER's customer resolver and the
	// gateway_sessions surface behind requireGatewaySession (spec §7.3).
	// Both ride the same *gen.Queries over the pool; a Server built without
	// a pool leaves them nil, which makes CREATE_USER self-gate with -99 and
	// turns the session guard into a pass-through.
	if s.pool != nil {
		q := gen.New(s.pool)
		h = h.WithGatewaySessions(q).WithCustomerStore(customers.NewStoreFromQueries(q))
		// Feature #484 (W1-A5b, spec §7.4): wire the session-cart surface so
		// RESERVATION becomes RESERVE / UN_RESERVE / UN_RESERVE_ALL over ONE
		// mutable hold per (gateway session, event session). Without this the
		// handler keeps the pre-#484 immutable-hold behaviour.
		h = h.WithGatewayCart(s.bil24CartDeps(q))
		// Feature #491 (W1-B1a, spec §7.6): wire the promo-code surface so
		// ADD_PROMO_CODES / CHECK_KDP can validate codes against the cart's org
		// and GET_CART can apply the session's accepted code. Without it both
		// commands self-gate with -99 and GET_CART reports discountAmount=0.
		h = h.WithPromoCodes(q)
	}
	// Feature #505 (W1-B7b, spec §7.8/§9.3): wire the neutral order projection
	// so GET_ORDER_INFO answers with the bil24wire order object (36 keys minus
	// ticketList) instead of the hand-built body. The projection speaks raw
	// pgx, so it rides pgxPool rather than the PoolDB interface; without it the
	// handler keeps the pre-#505 fallback body.
	if s.pgxPool != nil {
		pool := s.pgxPool
		h = h.WithOrderExport(func(ctx context.Context, csID uuid.UUID) (*orderexport.Order, error) {
			return orderexport.QueryCheckoutSession(ctx, pool, csID)
		})
	}
	// Feature #509 (W1-B8, spec §7.13): wire REFUND_TICKET onto the platform
	// cancellation transaction. The querier resolves the wire bigint ticketId
	// (tickets.system_ticket_id); the closure runs htickets' CancelTicketTx in
	// manual refund mode with audit actor "gateway:<fid>", records the money on
	// the ticket and projects it onto the order aggregate. Both ride the
	// tickets handler, so the command self-gates with -99 on a pool-less
	// Server, matching every other optional surface.
	if s.ticketQueries != nil && s.pool != nil {
		th := s.ticketsHandler()
		h = h.WithRefundTicket(s.ticketQueries, func(ctx context.Context, in hbil24.GatewayRefundInput) (hbil24.GatewayRefundOutput, error) {
			res, err := th.RefundTicketForGateway(ctx, htickets.GatewayRefundParams{
				TicketID:    in.TicketID,
				OrgID:       in.OrgID,
				Reason:      in.Reason,
				RefundPrice: in.RefundPrice,
				Actor:       in.Actor,
			})
			if err != nil {
				return hbil24.GatewayRefundOutput{}, err
			}
			return hbil24.GatewayRefundOutput{RefundDate: res.RefundDate}, nil
		})
	}
	return h
}

// bil24CartDeps wires the feature-#483 hold-mutation primitives (ExtendHold /
// ShrinkHold / RefreshHoldExpiry) plus the gateway_cart query surface into
// hbil24 as callbacks, following the same cross-domain closure precedent as
// bil24ReservationDeps — hbil24 never imports package httpserver.
func (s *Server) bil24CartDeps(q *gen.Queries) hbil24.CartDeps {
	pool := s.pool
	return hbil24.CartDeps{
		Q: q,
		Extend: func(ctx context.Context, in hcheckout.HoldMutationInput) (hcheckout.HoldMutationResult, error) {
			return hcheckout.ExtendHold(ctx, pool, q, in)
		},
		Shrink: func(ctx context.Context, in hcheckout.HoldMutationInput) (hcheckout.HoldMutationResult, error) {
			return hcheckout.ShrinkHold(ctx, pool, q, in)
		},
		Refresh: func(ctx context.Context, ids []uuid.UUID, ttl time.Duration) ([]gen.ReservationRow, error) {
			return hcheckout.RefreshHoldExpiry(ctx, pool, q, ids, ttl)
		},
	}
}

// bil24ReservationDeps wires the REAL RESERVATION / UN_RESERVE machinery
// (feature #312 second half) into hbil24 as callbacks over the hcheckout
// hold API, following the cross-domain callback precedent of
// feed_shims.go (PromoValidator). hbil24 never imports package httpserver;
// the closures below capture the *Server query handles instead.
//
// Every dependency is nil-safe: when the reservation / inventory queries
// or the pool are not wired the callbacks stay nil and the commands
// self-gate with resultCode=-99.
func (s *Server) bil24ReservationDeps() hbil24.ReservationDeps {
	deps := hbil24.ReservationDeps{
		PricingRules: hcheckout.PricingRules(s.pricingRules),
	}
	if s.sessionQueries != nil {
		deps.CtxQ = s.sessionQueries
	}
	if s.tierQueries != nil {
		deps.TierQ = s.tierQueries
	}
	if s.reservationQueries == nil || s.inventoryQueries == nil || s.pool == nil {
		return deps
	}
	resQ := s.reservationQueries
	pool := s.pool
	deps.SeatedReserve = func(ctx context.Context, in hcheckout.SeatedHoldInput) (hcheckout.SeatedHoldResult, error) {
		return hcheckout.CreateSeatedHold(ctx, pool, resQ, in)
	}
	deps.GAReserve = func(ctx context.Context, in hcheckout.GAHoldInput) (gen.ReservationRow, error) {
		return hcheckout.CreateGAHold(ctx, pool, resQ, in)
	}
	deps.Release = func(ctx context.Context, reservationID uuid.UUID) (gen.ReservationRow, error) {
		return hcheckout.ReleaseHold(ctx, pool, resQ, reservationID)
	}
	return deps
}

// ─── result codes (re-exported from the adapter package) ─────────────────────

// Bil24 wire result codes — re-exported from internal/adapters/bil24compat so
// existing in-package references and the #157 test suite continue to compile
// without churn. The adapter package is the source of truth.
const (
	// ResultCodeOK signals a successful command execution (Bil24 wire: 0).
	ResultCodeOK = bil24compat.ResultCodeOK
	// ResultCodeSessionExpired signals expired gateway session (feature #477).
	ResultCodeSessionExpired = bil24compat.ResultCodeSessionExpired
	// ResultCodeUserVisible signals a user-visible business failure
	// whose description is shown to the buyer verbatim (feature #477).
	ResultCodeUserVisible = bil24compat.ResultCodeUserVisible
	// ResultCodeTransient signals a transient/retry-able failure — DB/pool
	// errors, deadlocks, timeouts (Bil24 wire: -1, feature #477).
	ResultCodeTransient = bil24compat.ResultCodeTransient
	// ResultCodeUnknownCommand is a deprecated alias for
	// ResultCodeInvalidRequest kept for backward compatibility with the
	// #157 tests; its value moved from -1 to -2 in feature #477.
	//
	// Deprecated: use ResultCodeInvalidRequest.
	ResultCodeUnknownCommand = bil24compat.ResultCodeUnknownCommand //nolint:staticcheck // intentional re-export of deprecated alias
	// ResultCodeInvalidRequest is returned when the request is malformed
	// (missing/malformed field, unknown command name) (Bil24 wire: -2).
	ResultCodeInvalidRequest = bil24compat.ResultCodeInvalidRequest
	// ResultCodeNotFound is returned when the requested resource does not
	// exist in the platform (Bil24 wire: -3).
	ResultCodeNotFound = bil24compat.ResultCodeNotFound
	// ResultCodeUnauthorized is returned when the fid/token credential pair
	// is invalid or missing. Platform extension (feature #374).
	ResultCodeUnauthorized = bil24compat.ResultCodeUnauthorized
	// ResultCodeNotImplemented is returned for commands recognized by the
	// gateway but not yet wired to platform functionality (feature #374).
	ResultCodeNotImplemented = bil24compat.ResultCodeNotImplemented
	// ResultCodeInternalError is returned when an unexpected error prevents
	// command execution (Bil24 wire: -99). Reserved for panic-recovery.
	ResultCodeInternalError = bil24compat.ResultCodeInternalError
)

// ─── request / response envelope (aliased from the adapter package) ──────────

// bil24Request is the top-level request envelope for POST /compat/bil24/json.
// Aliased to the adapter package so the wire format has exactly one
// definition.
//
//nolint:unused // source-grep witness: alias surface kept for test #157
type bil24Request = bil24compat.Request

// bil24Response is the Bil24-compatible response envelope, aliased to the
// adapter package.
type bil24Response = bil24compat.Response

// bil24OK constructs a success response for the given command with optional
// extra payload fields. Forwarder to bil24compat.OK.
func bil24OK(command string, extra map[string]any) bil24Response {
	return bil24compat.OK(command, extra)
}

// bil24Error constructs an error response for the given command. Forwarder
// to bil24compat.Error.
func bil24Error(command string, code int, description string) bil24Response {
	return bil24compat.Error(command, code, description)
}

// writeBil24JSON writes a Bil24-envelope response with Content-Type
// application/json. Forwarder to bil24compat.WriteJSON.
func writeBil24JSON(w http.ResponseWriter, status int, resp bil24Response) {
	bil24compat.WriteJSON(w, status, resp)
}

// ─── ID translation layer (re-exported from the adapter package) ─────────────

// ErrLegacyIDNotFound is returned by TranslateLegacyID when the provided
// legacy identifier cannot be resolved to a platform UUID. Re-exported from
// the adapter package so existing references resolve to the same sentinel
// value (errors.Is still works because it is the very same variable).
var ErrLegacyIDNotFound = bil24compat.ErrLegacyIDNotFound

// TranslateLegacyID converts a legacy Bil24 identifier (actionId,
// actionEventId, orderId, ticketId, …) to the platform's UUIDv7.
// Forwarder to bil24compat.TranslateLegacyID.
func TranslateLegacyID(raw string) (uuid.UUID, error) {
	return bil24compat.TranslateLegacyID(raw)
}

// TranslatePlatformID converts a platform UUID to the Bil24 legacy ID
// format. Forwarder to bil24compat.TranslatePlatformID.
func TranslatePlatformID(id uuid.UUID) string {
	return bil24compat.TranslatePlatformID(id)
}

// ─── gateway feature-flag guard ───────────────────────────────────────────────

// bil24CompatEnabled returns true when the Bil24 compatibility gateway has
// been enabled at server construction time. When false, the
// /compat/bil24/* subtree is not mounted and requests to those paths get a
// chi 404 via handleNotFound. Individual commands may still return 503 if a
// specific query subset is missing.
//
//nolint:unused // referenced by test #157 as identifier surface check
func (s *Server) bil24CompatEnabled() bool {
	return s.bil24Enabled
}

// ─── command gateway handler shim ─────────────────────────────────────────────

// handleBil24Command delegates to hbil24.(*Handler).HandleBil24Command, the
// single-entry-point dispatcher for POST /compat/bil24/json.
func (s *Server) handleBil24Command(w http.ResponseWriter, r *http.Request) {
	s.bil24Handler().HandleBil24Command(w, r)
}

// handleBil24Image delegates to hbil24.(*Handler).HandleBil24Image, the
// sbt/1.0 seating-plan export (feature #501, spec §8).
func (s *Server) handleBil24Image(w http.ResponseWriter, r *http.Request) {
	s.bil24Handler().HandleBil24Image(w, r)
}

// ─── router mounting ──────────────────────────────────────────────────────────

// mountCompatRoutes mounts the Bil24-compatible API gateway under /compat/bil24/*.
//
// The subtree is only mounted when bil24Enabled is true (env: BIL24_COMPAT_ENABLED).
// When disabled the paths do not exist in the router; chi returns 404 via
// handleNotFound — NOT 401/403. This removes the attack surface entirely
// (feature #385, PR2-25B).
//
// VARIANT-A ENFORCEMENT CONTRACT (feature #385, PR2-25B):
// If BIL24_COMPAT_ENABLED is ever set TRUE in a future release, the PR2-25
// variant-A credential enforcement MUST be activated before accepting real
// traffic. Specifically:
//   - Call h.WithRequireToken(true) when constructing the hbil24.Handler
//     (see bil24Handler() above).
//   - Ensure every sales_channels row has gateway_token_hash set in its
//     settings JSONB column.
//   - All state-mutating commands (RESERVATION, UN_RESERVE, CREATE_ORDER_EXT,
//     CANCEL_ORDER) must be validated via bcrypt.CompareHashAndPassword against
//     the stored hash. See feature #374 (hbil24.WithRequireToken) for the
//     implementation.
//   - Do NOT enable the gateway without WithRequireToken(true) — an uncredentialled
//     gateway allows unauthenticated inventory mutation (ticket minting, etc.).
//
// Follow-up: feature #381 (PR2-25 variant A) documents the full credential
// enforcement checklist; it was superseded by this variant-B flag approach for
// the first release but should be revisited if the gateway is ever enabled.
//
// Feature #157.
func (s *Server) mountCompatRoutes() {
	if !s.bil24Enabled {
		return
	}
	s.router.Route("/compat/bil24", func(r chi.Router) {
		// POST /compat/bil24/json — Bil24 command gateway.
		// Accepts { "command": "...", "fid": "...", "token": "...", ... }
		// and dispatches to the appropriate domain adapter.
		// No JWT auth — the gateway uses fid/token credentials from the request body.
		r.Post("/json", s.handleBil24Command)
		// GET /compat/bil24/image?type=seatingPlan&actionEventId=…&fid=…
		// — the sbt/1.0 seating plan (spec §8). GET, not a command, because
		// the WP picker fetches it as a plain cacheable asset; auth is
		// fid → channel → org only (no token: it rides a browser URL).
		r.Get("/image", s.handleBil24Image)
	})
}
