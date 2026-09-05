// cmd_cart.go — Bil24-compatible cart commands: RESERVATION dispatcher
// (handleBil24Reservation), shared reservationContext + credential helper,
// UN_RESERVE (handleBil24UnReserve), and the small pricing/error helpers
// only the reservation flow needs (bil24FinancialFields,
// cartTimeoutSeconds, writeHoldError). The heavy per-mode reservation
// bodies (reservationSeated, reservationGA) live in cmd_cart_reserve.go
// so no single file exceeds 700 lines (feature #476 split rule).
//
// The token validators (validateGatewayToken, validateUnReserveToken) live
// in auth.go alongside the rest of the credential surface.
package hbil24

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// ─────────────────────────────────────────────────────────────────────────────
// RESERVATION — create a reservation (seated: seatList; GA: categoryList)
// Feature #312 Wave SEAT-D1.
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24Reservation dispatches the Bil24 RESERVATION command to
// either the SEAT-C1 seated reservation contract (assigned_seats /
// hybrid, seatList payload) or the pre-existing tier-facade
// (general_admission, categoryList payload).
//
// Wire contract (both modes require actionEventId):
//
//	seated mode:
//	  { "command": "RESERVATION",
//	    "actionEventId": "<session-uuid>",
//	    "seatList": ["<session_seat.id>", ...] }
//
//	GA mode:
//	  { "command": "RESERVATION",
//	    "actionEventId": "<session-uuid>",
//	    "categoryList": [{"categoryPriceId":"<tier-uuid>","quantity":N}, ...] }
//
// seatList and categoryList are mutually exclusive; supplying both — or
// neither — returns resultCode=-2 (invalid request). The admission_mode
// of the target session is enforced when the seating dependency is
// wired:
//
//   - assigned_seats session + categoryList  → -2 seats.required
//   - general_admission session + seatList   → -2 quantity.required
//   - hybrid session                         → either mode is accepted
//
// Once the wire contract passes, both branches create a REAL hold via
// the hcheckout hold API (injected as callbacks by bil24_shims.go — the
// gateway never imports package httpserver). The tenant context is
// resolved from the request itself: the owning organization via the
// session (sessions → events join) and the sales channel via the fid
// credential (fid → sales_channel per the gateway ID mapping; until the
// compatibility_id_map lands, fid must be the platform sales_channel
// UUID). The response carries the real reservationId, cartTimeout
// (whole seconds until the hold expires — legacy contract §5.1) and the
// platform-computed financial fields (sum / discount / charge /
// totalSum; totalSum = sum - discount + charge, guardrail #15).
func (h *Handler) handleBil24Reservation(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.ActionEventID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId is required",
		))
		return
	}
	// Spec §4 / §7.4 (feature #476, W1-A2b): actionEventId is int64 on the
	// wire when compatDB is wired — resolveActionEventID rejects UUID input
	// with -2 before touching downstream queries. The nil-compatDB fallback
	// keeps the pre-W1 UUID passthrough so existing unit-test constructors
	// (seat_d1_312, bil24_374, ...) that omit the pool stay green during the
	// step-by-step migration.
	sessionID, err := h.resolveActionEventID(r.Context(), req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}

	// Feature #381 / PR2-25 — early auth pre-check: reject obviously
	// unauthenticated requests before any DB round-trips.
	// The full bcrypt validation (including gateway_token_hash lookup) happens
	// inside reservationContext() once the channel row is loaded.  This early
	// gate surfaces a clear -4 Unauthorized instead of an opaque -99 when the
	// operator omits the token field entirely.
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		h.logger.Warn("bil24_compat: RESERVATION: token missing in request; rejecting",
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for RESERVATION",
		))
		return
	}

	// Feature #481 / spec §7.4: the (userId, sessionId) pair the site got
	// from CREATE_USER must still be alive and belong to this channel's
	// organization, otherwise the hold is refused with resultCode=1 so the
	// plugin re-runs CREATE_USER and retries. resolveChannelByFID is the
	// silent variant — an unresolvable fid was already handled (or
	// deliberately tolerated) by the requireToken gate above, and the
	// session guard degrades to skipping only the cross-org comparison.
	sessChannel, _ := h.resolveChannelByFID(r.Context(), req)
	if !h.requireGatewaySession(r.Context(), w, req, sessChannel,
		parseGatewaySettings(sessChannel.Settings).DefaultLocale) {
		return
	}

	hasSeats := len(req.SeatList) > 0
	hasCats := len(req.CategoryList) > 0

	if hasSeats && hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"seatList and categoryList are mutually exclusive",
		))
		return
	}
	if !hasSeats && !hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"either seatList or categoryList must be provided",
		))
		return
	}

	// Resolve admission_mode when the seating dependency is wired so we
	// can enforce the SEAT-D1 branch table. Missing dependencies fall
	// back to accepting whichever payload the caller supplied — matches
	// GET_SEAT_LIST fallback behavior during the SEAT-D rollout.
	admissionMode := ""
	if h.admissionQ != nil {
		row, aerr := h.admissionQ.GetSessionAdmissionModeByID(r.Context(), sessionID)
		if aerr != nil {
			if errors.Is(aerr, pgx.ErrNoRows) {
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeNotFound, "session not found",
				))
				return
			}
			h.logger.Error("bil24_compat: RESERVATION: session admission lookup failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", aerr.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError,
				"failed to resolve session",
			))
			return
		}
		admissionMode = row.AdmissionMode
	}

	if admissionMode == "general_admission" && hasSeats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"seatList is not supported on general_admission sessions; use categoryList",
		))
		return
	}
	if admissionMode == "assigned_seats" && hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"categoryList is not supported on assigned_seats sessions; use seatList",
		))
		return
	}

	if hasSeats {
		h.reservationSeated(w, r.Context(), req, sessionID, admissionMode)
		return
	}
	h.reservationGA(w, r.Context(), req, sessionID, admissionMode)
}

// reservationContext resolves the tenant context of a RESERVATION request:
// the owning organization via the session (sessions → events join) and the
// sales channel addressed by the fid credential (fid → sales_channel per
// the gateway ID mapping; until the compatibility_id_map lands, fid must be
// the platform sales_channel UUID). The hold TTL honours the channel's
// reservation_ttl_override and falls back to the platform default.
//
// When h.requireToken is true (feature #374), the token field from the
// request is validated against the bcrypt hash stored in the channel's
// settings JSON under "gateway_token_hash". Missing or invalid tokens cause
// a resultCode=-4 (Unauthorized) response. Channels without a stored hash
// are rejected when requireToken=true (channel must be configured before
// gateway access is allowed).
//
// On failure the Bil24 error envelope has already been written and
// ok=false is returned.
func (h *Handler) reservationContext(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	sessionID uuid.UUID,
) (orgID, channelID uuid.UUID, expiresAt time.Time, ok bool) {
	orgCtx, err := h.resDeps.CtxQ.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "session not found",
			))
			return uuid.Nil, uuid.Nil, time.Time{}, false
		}
		h.logger.Error("bil24_compat: RESERVATION: session org lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve session",
		))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}

	if strings.TrimSpace(req.FID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"fid is required for RESERVATION (sales channel credential)",
		))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	// Feature #471 (W1-A1b, spec §5.2): prefer the display_number path.
	// A legacy UUID fid still resolves via GetSalesChannelByID scoped to
	// the session's org — the pre-W1 fixtures depend on this and #476 will
	// finish removing the UUID form once every deployed channel migrates.
	var (
		channel gen.SalesChannelRow
		chID    uuid.UUID
	)
	if dn, dnOK := parseFIDInt64(req.FID); dnOK && h.channelQ != nil {
		ch, lookupErr := h.channelQ.GetSalesChannelByDisplayNumber(ctx, dn)
		if lookupErr == nil {
			channel = ch
			chID = ch.ID
			// Cross-tenant guard: the channel must belong to the session's
			// org (spec §5.3 / §7.4). A crafted display_number cannot be
			// used to hold seats in a different tenant.
			if channel.OrgID != orgCtx.OrgID {
				h.logger.Warn("bil24_compat: RESERVATION: cross-tenant channel access rejected",
					slog.String("channel_id", channel.ID.String()),
					slog.String("channel_org", channel.OrgID.String()),
					slog.String("session_org", orgCtx.OrgID.String()),
				)
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeNotFound,
					"sales channel not found in this session's organization",
				))
				return uuid.Nil, uuid.Nil, time.Time{}, false
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			h.logger.Error("bil24_compat: RESERVATION: display_number lookup failed",
				slog.String("fid", req.FID),
				slog.Int64("display_number", dn),
				slog.String("error", lookupErr.Error()),
			)
		}
	}
	if chID == uuid.Nil {
		legacyID, terr := TranslateLegacyID(req.FID)
		if terr != nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				"fid must be a valid sales channel identifier",
			))
			return uuid.Nil, uuid.Nil, time.Time{}, false
		}
		chID = legacyID
	}
	if channel.ID == uuid.Nil {
		var lookupErr error
		channel, lookupErr = h.resDeps.CtxQ.GetSalesChannelByID(ctx, chID, orgCtx.OrgID)
		err = lookupErr
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				"sales channel not found for fid in this session's organization",
			))
			return uuid.Nil, uuid.Nil, time.Time{}, false
		}
		h.logger.Error("bil24_compat: RESERVATION: sales channel lookup failed",
			slog.String("fid", req.FID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve sales channel",
		))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}

	// Feature #374: validate fid/token when requireToken is enabled.
	// The gateway_token_hash is stored in the channel's settings JSONB as:
	//   { "gateway_token_hash": "<bcrypt hash of the secret token>" }
	// Channels without a stored hash are rejected when requireToken=true.
	if h.requireToken {
		if !h.validateGatewayToken(w, req, channel.Settings) {
			return uuid.Nil, uuid.Nil, time.Time{}, false
		}
	}

	ttl := hcheckout.DefaultReservationTTL
	if channel.ReservationTTLOverride != nil && *channel.ReservationTTLOverride > 0 {
		ttl = time.Duration(*channel.ReservationTTLOverride) * time.Second
	}
	return orgCtx.OrgID, channel.ID, time.Now().UTC().Add(ttl), true
}

// bil24FinancialFields projects a platform pricing breakdown onto the
// legacy Bil24 financial fields (08_architecture/01_api_compatibility_
// gateway_ru.md): sum = subtotal, discount = discount, charge = service
// charge, and the invariant totalSum = sum - discount + charge is
// preserved by deriving charge from the pipeline total.
func bil24FinancialFields(bd hcheckout.PricingBreakdown) map[string]any {
	charge := bd.Total - (bd.Subtotal - bd.Discount)
	fields := map[string]any{
		"sum":      bd.Subtotal,
		"discount": bd.Discount,
		"charge":   charge,
		"totalSum": bd.Total,
	}
	if bd.Currency != "" {
		fields["currency"] = bd.Currency
	}
	return fields
}

// cartTimeoutSeconds converts an absolute hold deadline into the legacy
// cartTimeout wire field (whole seconds remaining, clamped at zero).
func cartTimeoutSeconds(expiresAt time.Time) int64 {
	secs := int64(time.Until(expiresAt).Seconds())
	if secs < 0 {
		secs = 0
	}
	return secs
}

// writeHoldError translates the typed errors of the hcheckout hold API into
// Bil24 envelopes. Seat conflicts and over-capacity carry structured detail
// alongside the description so migrated clients can highlight the exact
// seats / zones.
//
// The categoryPriceId carried on capacity errors follows the spec §4
// int64-wire contract (feature #476) when h.compatDB is wired; unit tests
// that omit the pool fall back to the pre-W1 UUID-string form via
// compatCategoryPriceID.
func (h *Handler) writeHoldError(ctx context.Context, w http.ResponseWriter, command string, err error) {
	var conflicts *hcheckout.SeatConflictsError
	var capErr *hcheckout.CapacityError
	switch {
	case errors.Is(err, hcheckout.ErrHoldSessionNotFound):
		writeBil24JSON(w, http.StatusOK, bil24Error(command, ResultCodeNotFound, "session not found"))
	case errors.Is(err, hcheckout.ErrHoldSeatsNotSupported):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			command, ResultCodeInvalidRequest,
			"seatList is not supported on general_admission sessions; use categoryList",
		))
	case errors.Is(err, hcheckout.ErrHoldQuantityNotSupported):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			command, ResultCodeInvalidRequest,
			"categoryList is not supported on assigned_seats sessions; use seatList",
		))
	case errors.Is(err, hcheckout.ErrHoldInvalidInput):
		writeBil24JSON(w, http.StatusOK, bil24Error(command, ResultCodeInvalidRequest, "invalid reservation payload"))
	case errors.As(err, &conflicts):
		resp := bil24Error(command, ResultCodeInvalidRequest, "one or more requested seats are not available")
		resp.Data = map[string]any{"conflicts": conflicts.Conflicts}
		writeBil24JSON(w, http.StatusOK, resp)
	case errors.As(err, &capErr):
		resp := bil24Error(command, ResultCodeInvalidRequest, "insufficient capacity for this reservation")
		detail := map[string]any{"requested": capErr.Requested}
		if capErr.TierID != nil {
			detail["categoryPriceId"] = h.compatCategoryPriceID(ctx, *capErr.TierID)
		}
		resp.Data = map[string]any{"capacity": detail}
		writeBil24JSON(w, http.StatusOK, resp)
	default:
		h.logger.Error("bil24_compat: RESERVATION: hold failed",
			slog.String("command", command),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(command, ResultCodeInternalError, "failed to create reservation"))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UN_RESERVE — release a hold created by RESERVATION
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24UnReserve maps the legacy cancel semantics of the RESERVATION
// flow (§5.1 of the ticket-agent notes) onto the platform hold release:
// held seats flip back to 'available' (with a seat_status_version bump),
// reserved capacity is returned (session-level for seats, per-tier for GA
// lines), and the reservation transitions to 'cancelled'.
//
// Bil24 request fields used:
//   - reservationId: the id returned by a successful RESERVATION
//
// Response:
//
//	{ "resultCode": 0, "command": "UN_RESERVE",
//	  "reservationId": "<uuid>", "status": "cancelled" }
func (h *Handler) handleBil24UnReserve(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.ReservationID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "reservationId is required",
		))
		return
	}
	reservationID, err := TranslateLegacyID(req.ReservationID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"reservationId must be a valid reservation identifier",
		))
		return
	}

	// Feature #381 / PR2-25: credential enforcement for UN_RESERVE.
	// Quick-reject when token is absent — no DB round-trip needed.
	if h.requireToken && strings.TrimSpace(req.Token) == "" {
		h.logger.Warn("bil24_compat: UN_RESERVE: token missing in request; rejecting",
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is required for UN_RESERVE",
		))
		return
	}
	// Full credential validation: look up the reservation's owning channel and
	// verify the token against its stored gateway_token_hash.
	if h.requireToken {
		if !h.validateUnReserveToken(r.Context(), w, req, reservationID) {
			return
		}
	}

	// Feature #481 / spec §7.4: UN_RESERVE carries the same (userId,
	// sessionId) pair as RESERVATION and is guarded identically — a stale
	// session releases nothing and answers resultCode=1.
	unresChannel, _ := h.resolveChannelByFID(r.Context(), req)
	if !h.requireGatewaySession(r.Context(), w, req, unresChannel,
		parseGatewaySettings(unresChannel.Settings).DefaultLocale) {
		return
	}

	if h.resDeps.Release == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "reservation service unavailable",
		))
		return
	}

	cancelled, err := h.resDeps.Release(r.Context(), reservationID)
	if err != nil {
		var notReleasable *hcheckout.NotReleasableError
		switch {
		case errors.Is(err, hcheckout.ErrHoldNotFound):
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "reservation not found",
			))
		case errors.As(err, &notReleasable):
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("reservation cannot be released from state %q", notReleasable.State),
			))
		default:
			h.logger.Error("bil24_compat: UN_RESERVE: release failed",
				slog.String("reservation_id", reservationID.String()),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to release reservation",
			))
		}
		return
	}

	h.logger.Info("bil24_compat: UN_RESERVE: hold released",
		slog.String("reservation_id", cancelled.ID.String()),
	)

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"reservationId": TranslatePlatformID(cancelled.ID),
		"status":        "cancelled",
	}))
}
