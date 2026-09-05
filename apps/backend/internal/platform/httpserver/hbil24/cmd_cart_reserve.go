// cmd_cart_reserve.go — the seated (reservationSeated) and general-admission
// (reservationGA) branches of the RESERVATION dispatcher. Extracted from
// cmd_cart.go by feature #476 so no single per-command file exceeds 700
// lines. The dispatcher entry point (handleBil24Reservation), the shared
// tenant/credential context helper (reservationContext), and the small
// pricing/error helpers (bil24FinancialFields, cartTimeoutSeconds,
// writeHoldError) stay in cmd_cart.go alongside handleBil24UnReserve.
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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// reservationSeated is the seated branch of the RESERVATION dispatcher —
// feature #312 second half. It translates the ADR-005 seatList entries
// (session_seats.id AS STRING) into canonical seat_keys, creates a REAL
// hold through the injected hcheckout seated-reservation callback (SEAT-C1
// concurrency contract), prices the held seats through the platform
// pipeline, and responds with the legacy contract fields:
//
//	{
//	  "resultCode": 0, "command": "RESERVATION",
//	  "reservationId":  "<uuid>",                     // real id (string)
//	  "sessionId":      "<uuid>",
//	  "seatList":       ["<session_seat.id>", ...],   // held seats
//	  "seatCount":      N,
//	  "admissionMode":  "assigned_seats" | "hybrid",
//	  "cartTimeout":    <seconds until expiry>,
//	  "sum": <subtotal>, "discount": 0, "charge": <fees>,
//	  "totalSum": <sum - discount + charge>, "currency": "..."
//	}
func (h *Handler) reservationSeated(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID, admissionMode string) {
	// Deduplicate + validate seat identifiers. Spec §4 / §7.4 (feature #476,
	// W1-A2b): the wave-1 wire form is session_seats.system_seat_id (int64)
	// when compatDB is wired — resolveSeatToRow rejects a UUID request field
	// with ErrLegacyIDUUIDRejected via ParseLegacyIntID before any DB
	// round-trip.  The nil-compatDB fallback keeps the ADR-005 UUID
	// passthrough (uuid.Parse + GetSessionSeatByID) so pre-W1 unit tests
	// stay green during the step-by-step migration.
	seen := make(map[string]struct{}, len(req.SeatList))
	rawSeats := make([]string, 0, len(req.SeatList))
	for _, s := range req.SeatList {
		s = strings.TrimSpace(s)
		if s == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				"seatList entries must be non-empty seat identifiers",
			))
			return
		}
		if _, dup := seen[s]; dup {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("seatList contains duplicate seat %q", s),
			))
			return
		}
		if err := h.validateSeatIDFormat(s); err != nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("seatList entry %q is not a valid seat identifier", s),
			))
			return
		}
		seen[s] = struct{}{}
		rawSeats = append(rawSeats, s)
	}

	// Self-gate: the real hold path needs the reservation wiring plus the
	// seat id → seat_key translation surface.
	if h.resDeps.SeatedReserve == nil || h.resDeps.CtxQ == nil || h.seatQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "reservation service unavailable",
		))
		return
	}

	orgID, channelID, expiresAt, ok := h.reservationContext(ctx, w, req, sessionID)
	if !ok {
		return
	}

	// Translate seat wire identifiers → seat_keys (the SEAT-C1 lock path
	// orders and locks by seat_key). resolveSeatToRow enforces the spec §4
	// int64 wire form on the compatDB path and preserves ADR-005 UUID
	// parsing on the fallback path.
	seatKeys := make([]string, 0, len(rawSeats))
	for _, raw := range rawSeats {
		seat, err := h.resolveSeatToRow(ctx, raw, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				resp := bil24Error(req.Command, ResultCodeNotFound, "seat not found in this session")
				resp.Data = map[string]any{"seatId": raw}
				writeBil24JSON(w, http.StatusOK, resp)
				return
			}
			// Parse failure (UUID on compat wire, non-numeric on compat wire,
			// malformed UUID on fallback path) → invalid request.
			if errors.Is(err, ErrSeatIDInvalid) {
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeInvalidRequest,
					fmt.Sprintf("seatList entry %q is not a valid seat identifier", raw),
				))
				return
			}
			h.logger.Error("bil24_compat: RESERVATION: seat lookup failed",
				slog.String("seat_raw", raw),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to resolve seat",
			))
			return
		}
		seatKeys = append(seatKeys, seat.SeatKey)
	}

	result, err := h.resDeps.SeatedReserve(ctx, hcheckout.SeatedHoldInput{
		OrgID:     orgID,
		ChannelID: channelID,
		SessionID: sessionID,
		SeatKeys:  seatKeys,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		h.writeHoldError(ctx, w, req.Command, err)
		return
	}

	// Price the held seats through the platform pipeline (guardrail #15).
	// Tier prices come from the session's tier snapshot; seats without a
	// bound tier price at 0. A missing tier snapshot degrades to zero
	// prices rather than failing the hold (mirrors GET_SEAT_LIST).
	tierPrice := make(map[string]int64)
	currency := ""
	if h.resDeps.TierQ != nil {
		if tiers, terr := h.resDeps.TierQ.ListTicketTiersBySession(ctx, sessionID); terr == nil {
			// AB-48: scheduled prices via the ONE resolver.
			effPrices, effErr := priceresolve.ForTiers(ctx, h.resDeps.TierQ, tiers, time.Now().UTC())
			if effErr != nil {
				h.logger.Warn("bil24_compat: RESERVATION: price window lookup failed; using base prices",
					slog.String("error", effErr.Error()))
				effPrices = nil
			}
			for _, t := range tiers {
				tierPrice[t.ID.String()] = t.PriceAmount
				if eff, ok := effPrices[t.ID]; ok {
					tierPrice[t.ID.String()] = eff.Amount
				}
				if currency == "" {
					currency = t.Currency
				}
			}
		} else {
			h.logger.Warn("bil24_compat: RESERVATION: tier snapshot failed; pricing seats at zero",
				slog.String("session_id", sessionID.String()),
				slog.String("error", terr.Error()),
			)
		}
	}
	bd := hcheckout.ComputePricingLines(
		hcheckout.BuildSeatedPricingLines(result.Seats, tierPrice),
		0, currency, h.resDeps.PricingRules,
	)

	// Spec §4 / §7.4 (W1-A2b feature #476): held seatList entries echo
	// session_seats.system_seat_id (bigint, migration 0088). The legacy
	// ADR-005 UUID projection has been retired in wave-1.
	heldSeatIDs := make([]int64, 0, len(result.Seats))
	for _, s := range result.Seats {
		heldSeatIDs = append(heldSeatIDs, s.SystemSeatID)
	}

	responseAdmission := admissionMode
	if responseAdmission == "" {
		responseAdmission = "assigned_seats"
	}

	h.logger.Info("bil24_compat: RESERVATION: seated hold created",
		slog.String("reservation_id", result.Reservation.ID.String()),
		slog.String("session_id", sessionID.String()),
		slog.Int("seat_count", len(result.Seats)),
		slog.Int64("total_sum", bd.Total),
	)

	extra := map[string]any{
		"reservationId": TranslatePlatformID(result.Reservation.ID),
		// Spec §4 / §7.4 (feature #476): sessionId on the wire is the
		// int64 action_event compat id. Fallback (nil compatDB) returns the
		// legacy UUID string so pre-W1 unit-test Handlers stay green.
		"sessionId":     h.compatActionEventID(ctx, sessionID),
		"seatCount":     len(result.Seats),
		"seatList":      heldSeatIDs,
		"admissionMode": responseAdmission,
		"cartTimeout":   cartTimeoutSeconds(result.Reservation.ExpiresAt),
	}
	for k, v := range bil24FinancialFields(bd) {
		extra[k] = v
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, extra))
}

// reservationGA is the general-admission branch of the RESERVATION
// dispatcher. It validates the categoryList shape, prices every tier
// platform-side (guardrail #15 — pwyw tiers are rejected because the
// legacy wire has no chosen-price field), creates a REAL hold through the
// injected hcheckout GA callback (per-tier capacity + reservation_ga_items
// lines), and responds with the same financial contract as the seated
// branch.
func (h *Handler) reservationGA(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID, admissionMode string) {
	// Validate + aggregate the categoryList (duplicate tiers are summed so
	// the per-tier hold lines stay unique).
	type gaLine struct {
		tierID uuid.UUID
		qty    int32
	}
	order := make([]uuid.UUID, 0, len(req.CategoryList))
	byTier := make(map[uuid.UUID]*gaLine, len(req.CategoryList))
	for i, c := range req.CategoryList {
		if strings.TrimSpace(c.CategoryPriceID) == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].categoryPriceId is required", i),
			))
			return
		}
		// Spec §4 / §7.4 (feature #476, W1-A2b): categoryPriceId is int64 on
		// the wire when compatDB is wired — resolveCategoryPriceID rejects a
		// UUID request field with -2 via ParseLegacyIntID before compatids.
		// Resolve touches the pool.  The nil-compatDB fallback preserves the
		// pre-W1 UUID passthrough so seat_d1_312 / seat_d2_313 / bil24_374
		// unit-test constructors that omit the pool stay green during the
		// step-by-step migration.
		tierID, err := h.resolveCategoryPriceID(ctx, c.CategoryPriceID)
		if err != nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].categoryPriceId must be a valid tier identifier", i),
			))
			return
		}
		if c.Quantity <= 0 {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].quantity must be >= 1", i),
			))
			return
		}
		if line, exists := byTier[tierID]; exists {
			line.qty += int32(c.Quantity) //nolint:gosec // validated > 0 above
		} else {
			byTier[tierID] = &gaLine{tierID: tierID, qty: int32(c.Quantity)} //nolint:gosec // validated > 0
			order = append(order, tierID)
		}
	}

	// Self-gate: the real hold path needs the reservation wiring plus the
	// tier pricing surface.
	if h.resDeps.GAReserve == nil || h.resDeps.CtxQ == nil || h.resDeps.TierQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "reservation service unavailable",
		))
		return
	}

	orgID, channelID, expiresAt, ok := h.reservationContext(ctx, w, req, sessionID)
	if !ok {
		return
	}

	// Price every tier platform-side.
	items := make([]hcheckout.GAHoldItem, 0, len(order))
	lines := make([]hcheckout.PricingLineInput, 0, len(order))
	currency := ""
	for _, tierID := range order {
		line := byTier[tierID]
		tier, err := h.resDeps.TierQ.GetTicketTierByID(ctx, tierID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				resp := bil24Error(req.Command, ResultCodeNotFound, "categoryPriceId not found in this session")
				// Feature #476 (W1-A2b) spec §4: emit int64 wire form when
				// compatDB is wired; fall back to UUID string for unit tests
				// that build the Handler without a pool.
				resp.Data = map[string]any{"categoryPriceId": h.compatCategoryPriceID(ctx, tierID)}
				writeBil24JSON(w, http.StatusOK, resp)
				return
			}
			h.logger.Error("bil24_compat: RESERVATION: tier lookup failed",
				slog.String("tier_id", tierID.String()),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to resolve ticket tier",
			))
			return
		}
		var unitPrice int64
		switch tier.PricingMode {
		case "free":
			unitPrice = 0
		case "fixed":
			// AB-48: scheduled price via the ONE resolver.
			eff, effErr := priceresolve.ForTier(ctx, h.resDeps.TierQ, tier, time.Now().UTC())
			if effErr != nil {
				h.logger.Error("bil24_compat: RESERVATION: price resolution failed",
					slog.String("error", effErr.Error()))
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeInternalError, "failed to resolve ticket tier price",
				))
				return
			}
			unitPrice = eff.Amount
		default:
			// pwyw (no chosen-price field on the legacy wire) and unknown
			// modes cannot be priced by the gateway.
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("tier %s pricing mode %q is not supported via the compatibility gateway", tierID, tier.PricingMode),
			))
			return
		}
		if currency == "" {
			currency = tier.Currency
		}
		items = append(items, hcheckout.GAHoldItem{TierID: tierID, Quantity: line.qty, UnitPrice: unitPrice})
		lines = append(lines, hcheckout.PricingLineInput{TierID: tierID.String(), Quantity: line.qty, UnitPrice: unitPrice})
	}

	res, err := h.resDeps.GAReserve(ctx, hcheckout.GAHoldInput{
		OrgID:     orgID,
		ChannelID: channelID,
		SessionID: sessionID,
		Items:     items,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		h.writeHoldError(ctx, w, req.Command, err)
		return
	}

	bd := hcheckout.ComputePricingLines(lines, 0, currency, h.resDeps.PricingRules)

	echoed := make([]map[string]any, 0, len(items))
	var total int32
	for _, it := range items {
		total += it.Quantity
		echoed = append(echoed, map[string]any{
			// Spec §4 / §7.4 (feature #476): int64 wire form via compat map.
			// Fallback (nil compatDB) returns the legacy UUID string.
			"categoryPriceId": h.compatCategoryPriceID(ctx, it.TierID),
			"quantity":        it.Quantity,
		})
	}

	responseAdmission := admissionMode
	if responseAdmission == "" {
		responseAdmission = "general_admission"
	}

	h.logger.Info("bil24_compat: RESERVATION: GA hold created",
		slog.String("reservation_id", res.ID.String()),
		slog.String("session_id", sessionID.String()),
		slog.Int("total_quantity", int(total)),
		slog.Int64("total_sum", bd.Total),
	)

	extra := map[string]any{
		"reservationId": TranslatePlatformID(res.ID),
		// Spec §4 / §7.4 (feature #476): sessionId on the wire is the
		// int64 action_event compat id. Fallback (nil compatDB) returns the
		// legacy UUID string so pre-W1 unit-test Handlers stay green.
		"sessionId":     h.compatActionEventID(ctx, sessionID),
		"categoryList":  echoed,
		"totalQuantity": total,
		"admissionMode": responseAdmission,
		"cartTimeout":   cartTimeoutSeconds(res.ExpiresAt),
	}
	for k, v := range bil24FinancialFields(bd) {
		extra[k] = v
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, extra))
}
