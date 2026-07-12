// public_checkout_recover.go — WID-0c hold-expiry recovery endpoint (feature #320).
//
// POST /v1/public/checkout/{checkout_token}/recover
//
// No JWT required.  The checkout_token in the path is the credential.
// Rate-limited via the shared publicFeedRL limiter.
//
// The endpoint re-captures the SAME cart that was originally reserved under
// the given checkout session — the seats (reservation_seats) AND the
// general-admission lines (reservation_ga_items, migration 0063) — in one
// transaction with one shared TTL.  This is intended to recover a dead-end
// widget state where the buyer's hold expired while they were filling in
// payment details (design note §4.4).
//
// Pricing: the recovered checkout is RE-PRICED through the platform pricing
// pipeline (ComputePricingLines) using the current tier prices — the stale
// snapshot from the original confirmation is never reused.  A promo code
// stamped on the original session is re-validated against the fresh
// subtotal and silently dropped when no longer applicable.
//
// On success (200):
//
//	{
//	  "checkout_session": { ... },    // updated session with fresh reservation
//	  "checkout_token":   "<64-char hex>",
//	  "expires_at":       "2024-06-01T15:04:05Z",
//	  "pricing":          { ... }     // fresh platform-computed breakdown
//	}
//
// On seat conflict (409):
//
//	{
//	  "error":   "reservation.seats_conflict",
//	  "message": "...",
//	  "details": {"conflicts": [{"seat_key":"...", "status":"..."}]}
//	}
//
// On GA over-capacity (409):
//
//	{
//	  "error":   "reservation.over_capacity",
//	  "message": "...",
//	  "details": {"tier_id": "...", "tier_name": "...", "requested": N}
//	}
//
// Error codes:
//
//	400 — checkout is in a terminal/non-recoverable state (completed/abandoned)
//	404 — checkout_token not found
//	409 — one or more original seats/zones no longer available
//	429 — rate limited
//	500 — internal error
//	503 — database not available
package hfeed

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandlePublicCheckoutRecover serves
// POST /v1/public/checkout/{checkout_token}/recover.
//
// It attempts to re-hold the same seats AND re-reserve the same GA capacity
// that were originally captured under the checkout session identified by
// checkout_token.  If the whole cart is still available a new reservation is
// created (one transaction, one TTL), the checkout session is reset to
// 'created' state with the new reservation ID, and the session is
// immediately re-confirmed with a FRESH pricing-pipeline snapshot.  A fresh
// expires_at is returned so the widget can restart its countdown timer.
func (h *Handler) HandlePublicCheckoutRecover(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	checkoutToken := chi.URLParam(r, "checkout_token")
	clientIP := httputil.ExtractClientIP(r)

	// ── 1. Rate limiting ──────────────────────────────────────────────────────
	if !h.rl.CheckToken(checkoutToken) || !h.rl.CheckIP(clientIP) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"checkout.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	ctx := r.Context()

	// ── 2. Load checkout session by token ─────────────────────────────────────
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "checkout session not found", r,
			))
			return
		}
		h.logger.Error("public_checkout_recover: checkout lookup failed",
			slog.String("checkout_token", checkoutToken),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.lookup_failed", "failed to retrieve checkout session", r,
		))
		return
	}

	// ── 3. Guard: non-recoverable terminal states ─────────────────────────────
	switch cs.State {
	case "completed":
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.already_completed", "this checkout has been paid and cannot be recovered", r,
		))
		return
	case "abandoned":
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.abandoned", "this checkout was abandoned and cannot be recovered", r,
		))
		return
	}

	// ── 4. Load original reservation ─────────────────────────────────────────
	origRes, err := h.reservationQueries.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.reservation_failed", "original reservation not found", r,
			))
			return
		}
		h.logger.Error("public_checkout_recover: reservation lookup failed",
			slog.String("reservation_id", cs.ReservationID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.reservation_failed", "failed to retrieve original reservation", r,
		))
		return
	}

	// ── 5. Idempotency: if the original reservation is still active, return it ─
	if origRes.State == "active" && origRes.ExpiresAt.After(time.Now().UTC()) {
		updatedCS, err := h.checkoutQueries.ConfirmCheckoutSession(
			ctx, cs.ID,
			derefInt64(cs.Subtotal), derefInt64(cs.Discount),
			derefInt64(cs.PlatformFee), derefInt64(cs.ProviderFee),
			derefInt64(cs.Tax), derefInt64(cs.Total),
			derefString(cs.Currency), cs.PromoCodeID,
		)
		if err != nil {
			// ConfirmCheckoutSession fails when state != 'created' — in that case
			// cs is already confirmed, so just return the existing session.
			updatedCS = cs
		}
		h.logger.Info("public_checkout_recover: idempotent — reservation still active",
			slog.String("checkout_token", checkoutToken),
			slog.String("reservation_id", origRes.ID.String()),
		)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"checkout_session": hcheckout.CheckoutSessionFromRow(updatedCS),
			"checkout_token":   checkoutToken,
			"expires_at":       origRes.ExpiresAt.Format(time.RFC3339),
		})
		return
	}

	// ── 6. Load the original cart: seats + GA lines ───────────────────────────
	if h.inventoryQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	origSeats, err := h.reservationQueries.ListReservationSeats(ctx, origRes.ID)
	if err != nil {
		h.logger.Error("public_checkout_recover: list reservation seats failed",
			slog.String("reservation_id", origRes.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.reservation_failed", "failed to retrieve original reservation seats", r,
		))
		return
	}

	origGA, err := h.reservationQueries.ListReservationGAItems(ctx, origRes.ID)
	if err != nil {
		h.logger.Error("public_checkout_recover: list reservation GA lines failed",
			slog.String("reservation_id", origRes.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.reservation_failed", "failed to retrieve original reservation GA lines", r,
		))
		return
	}

	hasSeats := len(origSeats) > 0
	hasGA := len(origGA) > 0
	expiresAt := time.Now().UTC().Add(hcheckout.DefaultReservationTTL)

	// Legacy fallback: reservations created before migration 0063 have no GA
	// lines. A GA-only legacy reservation is re-captured from origRes.TierID +
	// origRes.Quantity like the original WID-0c implementation did.
	legacyGA := !hasSeats && !hasGA

	// ── 7. Re-capture the WHOLE cart in ONE transaction (one TTL) ─────────────
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	invQ := h.inventoryQueries.WithTx(tx)
	resQ := h.reservationQueries.WithTx(tx)

	var (
		locked     []gen.SessionSeatRow
		newVersion int64
		seatQty    int32
	)

	if hasSeats {
		// ── 7a. Seated portion: lock + conflict-check the original seats ──────
		seatKeys := make([]string, len(origSeats))
		for i, s := range origSeats {
			seatKeys[i] = s.SeatKey
		}
		normalizedSeats, _, normErr := hcheckout.NormalizeSeatKeys(seatKeys)
		if normErr != nil || len(normalizedSeats) == 0 {
			// Seat keys came from the DB, so this should never fire in practice.
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.reservation_failed", "original seat keys are invalid", r,
			))
			return
		}
		seatQty = int32(len(normalizedSeats)) //nolint:gosec

		// Bump seat_status_version.
		newVersion, err = resQ.IncrementSessionSeatStatusVersion(ctx, origRes.SessionID)
		if err != nil {
			h.logger.Error("public_checkout_recover: increment seat_status_version failed",
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.status_version_failed", "failed to bump seat_status_version", r,
			))
			return
		}

		// Lock seats FOR UPDATE in seat_key order.
		locked, err = resQ.LockSessionSeatsForHold(ctx, origRes.SessionID, normalizedSeats)
		if err != nil {
			h.logger.Error("public_checkout_recover: lock seats failed",
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.lock_seats_failed", "failed to lock target seats", r,
			))
			return
		}

		// Check conflicts (per-seat availability map on failure).
		conflicts := hcheckout.SeatConflicts(normalizedSeats, locked)
		if len(conflicts) > 0 {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"reservation.seats_conflict",
				"one or more original seats are no longer available",
				r,
				map[string]any{"conflicts": conflicts},
			))
			return
		}

		// Reserve session-level capacity for the seats (nil tier).
		if _, err := invQ.ReserveCapacity(ctx, origRes.SessionID, nil, seatQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"reservation.over_capacity", "insufficient capacity to recover reservation", r,
				))
				return
			}
			h.logger.Error("public_checkout_recover: reserve seat capacity failed",
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.capacity_failed", "failed to reserve capacity", r,
			))
			return
		}
	}

	// ── 7b. GA portion: re-reserve per-tier capacity ──────────────────────────
	var gaQty int32
	for i := range origGA {
		g := origGA[i]
		tierID := g.TierID
		gaQty += g.Quantity
		if _, err := invQ.ReserveCapacity(ctx, origRes.SessionID, &tierID, g.Quantity); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Per-tier availability detail so the widget can name the zone.
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
					"reservation.over_capacity",
					"insufficient capacity to recover GA reservation",
					r,
					map[string]any{
						"tier_id":   tierID.String(),
						"tier_name": g.TierName,
						"requested": g.Quantity,
					},
				))
				return
			}
			h.logger.Error("public_checkout_recover: reserve GA capacity failed",
				slog.String("tier_id", tierID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.capacity_failed", "failed to reserve capacity", r,
			))
			return
		}
	}

	// ── 7c. Legacy GA fallback (pre-0063 reservation without GA lines) ────────
	if legacyGA {
		gaQty = origRes.Quantity
		if gaQty <= 0 {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.reservation_failed", "original reservation has no quantity", r,
			))
			return
		}
		if _, err := invQ.ReserveCapacity(ctx, origRes.SessionID, origRes.TierID, gaQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				details := map[string]any{"requested": gaQty}
				if origRes.TierID != nil {
					details["tier_id"] = origRes.TierID.String()
				}
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
					"reservation.over_capacity",
					"insufficient capacity to recover GA reservation",
					r,
					details,
				))
				return
			}
			h.logger.Error("public_checkout_recover: reserve GA capacity failed",
				slog.String("session_id", origRes.SessionID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.capacity_failed", "failed to reserve capacity", r,
			))
			return
		}
	}

	// ── 7d. Insert the replacement reservation ────────────────────────────────
	// Tier pointer convention mirrors checkout/start: single-tier pure-GA holds
	// keep the reservation.tier_id column populated; mixed and multi-tier holds
	// leave it NULL and rely on the seat links / GA lines.
	var tierIDPtr *uuid.UUID
	switch {
	case legacyGA:
		tierIDPtr = origRes.TierID
	case !hasSeats && len(origGA) == 1:
		tid := origGA[0].TierID
		tierIDPtr = &tid
	}

	newRes, err := resQ.InsertReservation(
		ctx, origRes.OrgID, origRes.ChannelID, origRes.SessionID,
		tierIDPtr, nil, seatQty+gaQty, expiresAt,
	)
	if err != nil {
		h.logger.Error("public_checkout_recover: insert reservation failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.insert_failed", "failed to create new reservation", r,
		))
		return
	}

	// ── 7e. Hold + link each seat ─────────────────────────────────────────────
	for _, s := range locked {
		if _, err := resQ.HoldSessionSeat(ctx, s.ID, newRes.ID, newVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
					"reservation.seats_conflict",
					"seat "+s.SeatKey+" is no longer available",
					r,
					map[string]any{"conflicts": []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}},
				))
				return
			}
			h.logger.Error("public_checkout_recover: hold seat failed",
				slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.hold_failed", "failed to hold seat", r,
			))
			return
		}
		if err := resQ.InsertReservationSeat(ctx, newRes.ID, s.ID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
					"reservation.seats_conflict",
					"seat "+s.SeatKey+" is already linked to another reservation",
					r,
					map[string]any{"conflicts": []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}},
				))
				return
			}
			h.logger.Error("public_checkout_recover: reservation_seats insert failed",
				slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.seats_link_failed", "failed to link seat to reservation", r,
			))
			return
		}
	}

	// ── 8. Re-price the whole cart through the platform pipeline ──────────────
	// Fresh tier prices — the stale snapshot on the checkout session is never
	// reused. Runs inside the tx so a pricing failure rolls the holds back.
	var lines []hcheckout.PricingLineInput
	currency := ""

	if hasSeats {
		seatLines, seatCurrency, errCode := h.seatPricingLines(ctx, origRes.SessionID, locked)
		if errCode != "" {
			h.writePricingError(w, r, errCode)
			return
		}
		lines = append(lines, seatLines...)
		currency = seatCurrency
	}

	// GA lines: fresh price per tier, falling back to the stored snapshot when
	// the tier cannot be re-priced (defensive — e.g. pricing mode changed).
	gaUnitPrices := make([]int64, len(origGA))
	for i := range origGA {
		g := origGA[i]
		unit := g.UnitPrice
		if h.tierQueries != nil {
			if tier, terr := h.tierQueries.GetTicketTierByID(ctx, g.TierID, origRes.SessionID); terr == nil {
				if fresh, errCode := resolvePublicTierUnitPrice(tier); errCode == "" {
					unit = fresh
				}
				if currency == "" {
					currency = tier.Currency
				}
			}
		}
		if currency == "" {
			currency = g.Currency
		}
		gaUnitPrices[i] = unit
		lines = append(lines, hcheckout.PricingLineInput{
			TierID:    g.TierID.String(),
			Quantity:  g.Quantity,
			UnitPrice: unit,
		})
	}

	if legacyGA {
		// Reprice from the tier when known; otherwise fall back to the original
		// per-unit snapshot derived from the confirmed subtotal.
		unit := int64(0)
		if cs.Subtotal != nil && origRes.Quantity > 0 {
			unit = *cs.Subtotal / int64(origRes.Quantity)
		}
		tierIDStr := ""
		if origRes.TierID != nil {
			tierIDStr = origRes.TierID.String()
			if h.tierQueries != nil {
				if tier, terr := h.tierQueries.GetTicketTierByID(ctx, *origRes.TierID, origRes.SessionID); terr == nil {
					if fresh, errCode := resolvePublicTierUnitPrice(tier); errCode == "" {
						unit = fresh
					}
					if currency == "" {
						currency = tier.Currency
					}
				}
			}
		}
		lines = append(lines, hcheckout.PricingLineInput{
			TierID:    tierIDStr,
			Quantity:  origRes.Quantity,
			UnitPrice: unit,
		})
	}

	if currency == "" {
		currency = derefStringOrDefault(cs.Currency, "EUR")
	}

	// Re-validate the original promo against the fresh subtotal; drop it
	// silently when it no longer applies (expired, min-amount, exhausted).
	var subtotal int64
	for _, l := range lines {
		subtotal += l.UnitPrice * int64(l.Quantity)
	}
	var discount int64
	promoCodeID := (*uuid.UUID)(nil)
	if cs.PromoCodeID != nil && h.promoQueries != nil && h.validatePromo != nil {
		if promo, perr := h.promoQueries.GetPromoCodeByID(ctx, *cs.PromoCodeID, cs.OrgID); perr == nil {
			if d, errCode := h.validatePromo(promo, subtotal, time.Now().UTC()); errCode == "" {
				discount = d
				promoCodeID = cs.PromoCodeID
			} else {
				h.logger.Info("public_checkout_recover: promo no longer applicable — dropped",
					slog.String("promo_code_id", cs.PromoCodeID.String()),
					slog.String("reason", errCode),
				)
			}
		}
	}

	bd := hcheckout.ComputePricingLines(lines, discount, currency, h.pricingRules)

	// ── 9. Persist the fresh GA lines for the replacement reservation ─────────
	for i := range origGA {
		g := origGA[i]
		if err := resQ.InsertReservationGAItem(ctx, newRes.ID, g.TierID, g.Quantity, gaUnitPrices[i]); err != nil {
			h.logger.Error("public_checkout_recover: insert GA line failed",
				slog.String("tier_id", g.TierID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.insert_failed", "failed to record GA line", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	// ── 10. Reset checkout session to point to the new reservation ────────────
	updatedCS, err := h.checkoutQueries.UpdateCheckoutSessionReservationAndReset(
		ctx, cs.ID, newRes.ID,
	)
	if err != nil {
		h.logger.Error("public_checkout_recover: update checkout session failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.update_failed", "failed to update checkout session", r,
		))
		return
	}

	// ── 11. Re-confirm with the FRESH pricing-pipeline snapshot ───────────────
	updatedCS, err = h.checkoutQueries.ConfirmCheckoutSession(
		ctx, updatedCS.ID,
		bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total,
		bd.Currency, promoCodeID,
	)
	if err != nil {
		h.logger.Error("public_checkout_recover: confirm checkout session failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.confirm_failed", "failed to confirm checkout session", r,
		))
		return
	}

	h.logger.Info("public_checkout_recover: reservation recovered",
		slog.String("checkout_token", checkoutToken),
		slog.String("old_reservation_id", origRes.ID.String()),
		slog.String("new_reservation_id", newRes.ID.String()),
		slog.Int("seat_count", int(seatQty)),
		slog.Int("ga_quantity", int(gaQty)),
		slog.Int64("total", bd.Total),
		slog.String("currency", bd.Currency),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_session": hcheckout.CheckoutSessionFromRow(updatedCS),
		"checkout_token":   checkoutToken,
		"expires_at":       expiresAt.Format(time.RFC3339),
		"pricing":          bd,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// derefInt64 dereferences an *int64, returning 0 if nil.
func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// derefString dereferences a *string, returning "" if nil.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// derefStringOrDefault returns the dereferenced value or fallback if nil/empty.
func derefStringOrDefault(p *string, fallback string) string {
	if p == nil || *p == "" {
		return fallback
	}
	return *p
}
