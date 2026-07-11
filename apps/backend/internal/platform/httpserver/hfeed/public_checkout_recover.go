// public_checkout_recover.go — WID-0c hold-expiry recovery endpoint (feature #320).
//
// POST /v1/public/checkout/{checkout_token}/recover
//
// No JWT required.  The checkout_token in the path is the credential.
// Rate-limited via the shared publicFeedRL limiter.
//
// The endpoint re-captures the SAME seats/zones that were originally reserved
// under the given checkout session.  This is intended to recover a dead-end
// widget state where the buyer's hold expired while they were filling in
// payment details (design note §4.4).
//
// On success (200):
//
//	{
//	  "checkout_session": { ... },    // updated session with fresh reservation
//	  "checkout_token":   "<64-char hex>",
//	  "expires_at":       "2024-06-01T15:04:05Z"
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
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandlePublicCheckoutRecover serves
// POST /v1/public/checkout/{checkout_token}/recover.
//
// It attempts to re-hold the same seats (or re-reserve GA capacity) that were
// originally captured under the checkout session identified by checkout_token.
// If all seats are still available a new reservation is created, the checkout
// session is reset to 'created' state with the new reservation ID, and the
// session is immediately re-confirmed with the original pricing.  A fresh
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
	if h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "reservation service is not available", r,
		))
		return
	}

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

	// ── 6. Determine recovery mode: seated vs GA ──────────────────────────────
	if h.inventoryQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	// Fetch original reservation seats.
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

	isSeated := len(origSeats) > 0
	expiresAt := time.Now().UTC().Add(hcheckout.DefaultReservationTTL)

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

	if isSeated {
		// ── 7a. Seated recovery ───────────────────────────────────────────────

		// Collect original seat keys.
		seatKeys := make([]string, len(origSeats))
		for i, s := range origSeats {
			seatKeys[i] = s.SeatKey
		}

		// Normalise (sort, dedup, strip empty).
		normalizedSeats, _, normErr := hcheckout.NormalizeSeatKeys(seatKeys)
		if normErr != nil || len(normalizedSeats) == 0 {
			// Seat keys came from the DB, so this should never fire in practice.
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.reservation_failed", "original seat keys are invalid", r,
			))
			return
		}

		// Bump seat_status_version.
		newVersion, err := resQ.IncrementSessionSeatStatusVersion(ctx, origRes.SessionID)
		if err != nil {
			h.logger.Error("public_checkout_recover: increment seat_status_version failed",
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.status_version_failed", "failed to bump seat_status_version", r,
			))
			return
		}

		// Lock seats FOR UPDATE in seat_key order.
		locked, err := resQ.LockSessionSeatsForHold(ctx, origRes.SessionID, normalizedSeats)
		if err != nil {
			h.logger.Error("public_checkout_recover: lock seats failed",
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.lock_seats_failed", "failed to lock target seats", r,
			))
			return
		}

		// Check conflicts.
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

		// Reserve capacity (nil tier = session-level for seated).
		seatQty := int32(len(normalizedSeats)) //nolint:gosec
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

		// Insert new reservation.
		newRes, err := resQ.InsertReservation(
			ctx, origRes.OrgID, origRes.ChannelID, origRes.SessionID,
			nil, nil, seatQty, expiresAt,
		)
		if err != nil {
			h.logger.Error("public_checkout_recover: insert reservation failed",
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.insert_failed", "failed to create new reservation", r,
			))
			return
		}

		// Hold + link each seat.
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
				h.logger.Error("public_checkout_recover: reservation_seats insert failed",
					slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.seats_link_failed", "failed to link seat to reservation", r,
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

		// Reset checkout session to point to new reservation.
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

		// Re-confirm pricing using original snapshot values (seats are free; preserve
		// any GA pricing carried over from the original confirmation).
		updatedCS, err = h.checkoutQueries.ConfirmCheckoutSession(
			ctx, updatedCS.ID,
			derefInt64(cs.Subtotal), derefInt64(cs.Discount),
			derefInt64(cs.PlatformFee), derefInt64(cs.ProviderFee),
			derefInt64(cs.Tax), derefInt64(cs.Total),
			derefStringOrDefault(cs.Currency, "EUR"),
			cs.PromoCodeID,
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

		h.logger.Info("public_checkout_recover: seated reservation recovered",
			slog.String("checkout_token", checkoutToken),
			slog.String("old_reservation_id", origRes.ID.String()),
			slog.String("new_reservation_id", newRes.ID.String()),
			slog.Int("seat_count", len(normalizedSeats)),
		)

		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"checkout_session": hcheckout.CheckoutSessionFromRow(updatedCS),
			"checkout_token":   checkoutToken,
			"expires_at":       expiresAt.Format(time.RFC3339),
		})
		return
	}

	// ── 7b. GA recovery ───────────────────────────────────────────────────────

	gaQty := origRes.Quantity
	if gaQty <= 0 {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.reservation_failed", "original reservation has no quantity", r,
		))
		return
	}

	if _, err := invQ.ReserveCapacity(ctx, origRes.SessionID, origRes.TierID, gaQty); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"reservation.over_capacity", "insufficient capacity to recover GA reservation", r,
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

	// Insert new GA reservation.
	newRes, err := resQ.InsertReservation(
		ctx, origRes.OrgID, origRes.ChannelID, origRes.SessionID,
		origRes.TierID, nil, gaQty, expiresAt,
	)
	if err != nil {
		h.logger.Error("public_checkout_recover: insert GA reservation failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.insert_failed", "failed to create new GA reservation", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	// Reset checkout session.
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

	// Re-confirm with original pricing snapshot.
	updatedCS, err = h.checkoutQueries.ConfirmCheckoutSession(
		ctx, updatedCS.ID,
		derefInt64(cs.Subtotal), derefInt64(cs.Discount),
		derefInt64(cs.PlatformFee), derefInt64(cs.ProviderFee),
		derefInt64(cs.Tax), derefInt64(cs.Total),
		derefStringOrDefault(cs.Currency, "EUR"),
		cs.PromoCodeID,
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

	h.logger.Info("public_checkout_recover: GA reservation recovered",
		slog.String("checkout_token", checkoutToken),
		slog.String("old_reservation_id", origRes.ID.String()),
		slog.String("new_reservation_id", newRes.ID.String()),
		slog.Int("quantity", int(gaQty)),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_session": hcheckout.CheckoutSessionFromRow(updatedCS),
		"checkout_token":   checkoutToken,
		"expires_at":       expiresAt.Format(time.RFC3339),
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
