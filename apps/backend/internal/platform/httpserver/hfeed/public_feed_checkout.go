// public_feed_checkout.go implements the unauthenticated public checkout start
// endpoint (features #153 and #318 WID-0a).
//
// This allows external consumers browsing via a feed token to initiate a
// checkout without a JWT session. The feed token acts as the credential
// (ADR-013 federated feeds).
//
// Endpoint:
//
//	POST /v1/public/feeds/{feed_token}/checkout/start
//
// Request body (WID-0a extended — all modes supported):
//
//	{
//	  "session_id":   "<uuid>",
//	  "holder_email": "buyer@example.com",
//	  "promo_code":   "SAVE10",           // optional
//	  // Seated / hybrid:
//	  "seats":        ["Main Hall-A-1", "Main Hall-A-2"],
//	  // GA / hybrid:
//	  "ga_items":     [{"tier_id": "<uuid>", "quantity": 2}],
//	  // Legacy GA (backward-compat, feature #153):
//	  "tier_id":      "<uuid>",
//	  "qty":          2
//	}
//
// Response (201):
//
//	{
//	  "checkout_session": { ... },
//	  "redirect_url":     "/checkout/<id>",
//	  "checkout_token":   "<64-char hex>",
//	  "expires_at":       "2024-06-01T15:04:05Z"
//	}
//
// Error codes:
//
//	400 — missing or malformed fields
//	403 — session does not belong to this feed token (ADR-013 mismatch)
//	404 — tier not found in this session
//	409 — insufficient capacity or seat conflict
//	422 — admission mode mismatch or PWYW not supported
//	429 — rate limited (shared publicFeedRL limiter)
//	503 — database not available
package hfeed

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Request / response types
// ─────────────────────────────────────────────────────────────────────────────

// PublicGAItem is a GA ticket item in the WID-0a mixed-cart checkout.
type PublicGAItem struct {
	TierID   string `json:"tier_id"`
	Quantity int32  `json:"quantity"`
}

// PublicBuyerInfo carries the buyer's contact details for the checkout form
// (feature #321 WID-0d).  Email supersedes holder_email when both are present.
// Name and phone are collected only when the sales channel has the
// corresponding collect_name / collect_phone flag enabled.
type PublicBuyerInfo struct {
	Email string  `json:"email"`
	Name  *string `json:"name,omitempty"`
	Phone *string `json:"phone,omitempty"`
}

// PublicFeedCheckoutStartRequest is the JSON body for
// POST /v1/public/feeds/{feed_token}/checkout/start.
type PublicFeedCheckoutStartRequest struct {
	// Existing backward-compat GA fields (feature #153):
	TierID      string  `json:"tier_id"`
	SessionID   string  `json:"session_id"`
	Qty         int32   `json:"qty"`
	HolderEmail string  `json:"holder_email"`
	PromoCode   *string `json:"promo_code"`
	// New WID-0a fields (feature #318):
	Seats   []string       `json:"seats,omitempty"`
	GaItems []PublicGAItem `json:"ga_items,omitempty"`
	// New WID-0d field (feature #321): structured buyer info.
	// When present, Buyer.Email supersedes HolderEmail.
	Buyer *PublicBuyerInfo `json:"buyer,omitempty"`
}

// mintCheckoutToken generates a 32-byte crypto-random hex string (64 chars).
func mintCheckoutToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/public/feeds/{feed_token}/checkout/start
// ─────────────────────────────────────────────────────────────────────────────

// HandlePublicFeedCheckoutStart serves
// POST /v1/public/feeds/{feed_token}/checkout/start.
//
// No JWT required — the feed token in the path is the credential.
//
// Supports three modes (feature #318 WID-0a):
//  1. Legacy GA (feature #153): tier_id + qty → normalised to ga_items
//  2. Pure GA: ga_items[] only
//  3. Pure seated: seats[] only
//  4. Mixed (hybrid): seats[] + ga_items[]
func (h *Handler) HandlePublicFeedCheckoutStart(w http.ResponseWriter, r *http.Request) {
	if h.publicFeedQueries == nil || h.checkoutQueries == nil || h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")
	clientIP := httputil.ExtractClientIP(r)

	// ── 1. Rate limiting (shared with browse endpoints) ──────────────────────
	if !h.rl.CheckToken(feedToken) || !h.rl.CheckIP(clientIP) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"feed.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// ── 2. Parse + validate body ──────────────────────────────────────────────
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_body", "cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.empty_body", "request body is required", r,
		))
		return
	}

	var req PublicFeedCheckoutStartRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	// ── 3. Parse / validate session_id ───────────────────────────────────────
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_session_id", "session_id must be a valid UUID", r,
			map[string]any{"field": "session_id"},
		))
		return
	}

	// ── 4. Merge buyer.email into holder_email (WID-0d, feature #321) ──────────
	// Do this before the holder_email empty-check so that callers using the new
	// buyer object don't need to repeat the email in the top-level field.
	if req.Buyer != nil && req.Buyer.Email != "" {
		req.HolderEmail = req.Buyer.Email
	}

	// ── 4b. Validate holder_email ─────────────────────────────────────────────
	if req.HolderEmail == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_holder_email", "holder_email is required", r,
			map[string]any{"field": "holder_email"},
		))
		return
	}

	// ── 5. Normalise legacy GA format → ga_items ──────────────────────────────
	// If the caller supplied the legacy tier_id + qty (feature #153) and did NOT
	// supply the new seats / ga_items fields, convert to a single GaItems entry
	// so the rest of the handler only deals with the unified format.
	if req.TierID != "" && req.Qty > 0 && len(req.Seats) == 0 && len(req.GaItems) == 0 {
		req.GaItems = []PublicGAItem{{TierID: req.TierID, Quantity: req.Qty}}
	}

	hasSeats := len(req.Seats) > 0
	hasGA := len(req.GaItems) > 0

	// Legacy GA path: validate qty range for backward-compat (feature #153).
	// This only fires when the caller used the old tier_id+qty format (no seats, no ga_items yet).
	if !hasSeats && !hasGA {
		// Neither seats nor ga_items provided.
		// Check if the old tier_id+qty was invalid:
		if req.TierID != "" {
			if req.Qty <= 0 || req.Qty > 50 {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"checkout.invalid_qty", "qty must be between 1 and 50", r,
					map[string]any{"field": "qty"},
				))
				return
			}
			// tier_id was present but invalid UUID (would have been added to GaItems but Qty was 0).
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_items", "provide seats[], ga_items[], or legacy tier_id+qty", r,
			map[string]any{"field": "seats"},
		))
		return
	}

	// Validate legacy tier_id UUID if present (backward-compat; by now it's been
	// merged into GaItems, but we still parse it to give the correct 400).
	if req.TierID != "" && len(req.GaItems) > 0 && req.GaItems[0].TierID == req.TierID {
		if _, err := uuid.Parse(req.TierID); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_tier_id", "tier_id must be a valid UUID", r,
				map[string]any{"field": "tier_id"},
			))
			return
		}
	}

	// Validate each ga_item.tier_id.
	parsedGATierIDs := make([]uuid.UUID, 0, len(req.GaItems))
	for i, item := range req.GaItems {
		tid, err := uuid.Parse(item.TierID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_tier_id", fmt.Sprintf("ga_items[%d].tier_id must be a valid UUID", i), r,
				map[string]any{"field": "ga_items", "index": i},
			))
			return
		}
		if item.Quantity <= 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_qty", fmt.Sprintf("ga_items[%d].quantity must be >= 1", i), r,
				map[string]any{"field": "ga_items", "index": i},
			))
			return
		}
		parsedGATierIDs = append(parsedGATierIDs, tid)
	}

	ctx := r.Context()

	// ── 6. Validate session belongs to this feed token ────────────────────────
	checkCtx, err := h.publicFeedQueries.GetPublicCheckoutContext(ctx, feedToken, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"feed.session_not_on_feed",
				"session is not published to this feed token",
				r,
			))
			return
		}
		h.logger.Error("public_feed_checkout: context lookup failed",
			slog.String("feed_token", feedToken),
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.context_failed", "failed to validate checkout context", r,
		))
		return
	}

	// ── 6b. Validate buyer fields against channel flags (WID-0d) ────────────────
	// Fetch the buyer-field flags for this feed token and enforce them.
	// A flag-lookup failure is treated as "flags all off" (non-blocking)
	// so that a missing row (token had no linked channel, unlikely but
	// defensive) degrades to email-only rather than hard-failing all
	// checkouts.
	var collectName, collectPhone bool
	if h.publicFeedQueries != nil {
		flags, flagsErr := h.publicFeedQueries.GetFeedTokenBuyerFlags(ctx, feedToken)
		if flagsErr == nil {
			collectName = flags.CollectName
			collectPhone = flags.CollectPhone
		} else if !errors.Is(flagsErr, pgx.ErrNoRows) {
			h.logger.Error("public_feed_checkout: get buyer flags failed",
				slog.String("feed_token", feedToken),
				slog.String("error", flagsErr.Error()),
			)
		}
	}

	if collectName {
		var name string
		if req.Buyer != nil && req.Buyer.Name != nil {
			name = *req.Buyer.Name
		}
		if name == "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"checkout.buyer_name_required",
				"buyer.name is required by this sales channel",
				r,
				map[string]any{"field": "buyer.name"},
			))
			return
		}
	}

	if collectPhone {
		var phone string
		if req.Buyer != nil && req.Buyer.Phone != nil {
			phone = *req.Buyer.Phone
		}
		if phone == "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"checkout.buyer_phone_required",
				"buyer.phone is required by this sales channel",
				r,
				map[string]any{"field": "buyer.phone"},
			))
			return
		}
	}

	// ── 7. Validate GA tier(s) exist in this session ──────────────────────────
	if h.tierQueries == nil && hasGA {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.tier_unavailable", "tier service is not available", r,
		))
		return
	}

	// Look up first GA tier (for pricing; we'll compute per-item below).
	// For a pure GA single-tier request, this follows the original #153 flow.
	type gaItemPriced struct {
		tierID    uuid.UUID
		qty       int32
		unitPrice int64
		currency  string
	}
	pricedGA := make([]gaItemPriced, 0, len(req.GaItems))

	for i, item := range req.GaItems {
		tierID := parsedGATierIDs[i]
		tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"checkout.tier_not_found", "ticket tier not found in this session", r,
				))
				return
			}
			h.logger.Error("public_feed_checkout: tier lookup failed",
				slog.String("tier_id", tierID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.tier_lookup_failed", "failed to retrieve ticket tier", r,
			))
			return
		}
		var unitPrice int64
		switch tier.PricingMode {
		case "free":
			unitPrice = 0
		case "fixed":
			unitPrice = tier.PriceAmount
		case "pwyw":
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"checkout.pwyw_not_supported",
				"pay-what-you-want tiers require the authenticated checkout flow",
				r,
			))
			return
		default:
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.unknown_pricing_mode", "ticket tier has an unsupported pricing mode", r,
			))
			return
		}
		pricedGA = append(pricedGA, gaItemPriced{
			tierID:    tierID,
			qty:       item.Quantity,
			unitPrice: unitPrice,
			currency:  tier.Currency,
		})
	}

	// ── 8. Promo code lookup ───────────────────────────────────────────────────
	// Compute subtotal from GA items (seated items are free for promo purposes
	// in this initial implementation).
	var gaSubtotal int64
	for _, g := range pricedGA {
		gaSubtotal += g.unitPrice * int64(g.qty)
	}

	var discount int64
	var promoCodeID *uuid.UUID

	if req.PromoCode != nil && *req.PromoCode != "" && h.promoQueries != nil {
		promoRow, err := h.promoQueries.GetPromoCodeByCode(ctx, checkCtx.OrgID, *req.PromoCode)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
					"promo.not_found", "promo code not found", r,
				))
				return
			}
			h.logger.Error("public_feed_checkout: promo lookup failed",
				slog.String("promo_code", *req.PromoCode),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.promo_lookup_failed", "failed to retrieve promo code", r,
			))
			return
		}
		d, errCode := h.validatePromo(promoRow, gaSubtotal, time.Now().UTC())
		if errCode != "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, "promo code is not applicable", r))
			return
		}
		discount = d
		promoCodeID = &promoRow.ID
	}

	// ── 9. Mint checkout_token ────────────────────────────────────────────────
	checkoutToken, err := mintCheckoutToken()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.token_failed", "failed to mint checkout token", r,
		))
		return
	}

	expiresAt := time.Now().UTC().Add(hcheckout.DefaultReservationTTL)

	// ── 10. Begin transaction ─────────────────────────────────────────────────
	if h.inventoryQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

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

	// ── 11. Seated branch ─────────────────────────────────────────────────────
	if hasSeats {
		// Normalise seat keys: sort + dedup + reject empty.
		normalizedSeats, dupKey, normErr := hcheckout.NormalizeSeatKeys(req.Seats)
		if normErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"reservation.duplicate_seat", "seats[] must not contain duplicate keys", r,
				map[string]any{"seat_key": dupKey},
			))
			return
		}
		if len(normalizedSeats) == 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"reservation.invalid_seats", "seats[] must contain at least one non-empty seat_key", r,
				map[string]any{"field": "seats"},
			))
			return
		}

		// Validate admission mode: must be assigned_seats or hybrid.
		mode, err := resQ.GetSessionAdmissionModeByID(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"reservation.session_not_found", "session not found", r,
				))
				return
			}
			h.logger.Error("public_feed_checkout: admission mode lookup failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.admission_lookup_failed", "failed to resolve session admission_mode", r,
			))
			return
		}
		if mode.AdmissionMode == hcheckout.AdmissionGeneralAdmission {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"reservation.seats_not_supported",
				"session is general_admission — pass ga_items[] instead of seats[]",
				r,
				map[string]any{"admission_mode": mode.AdmissionMode},
			))
			return
		}

		// Bump seat_status_version.
		newVersion, err := resQ.IncrementSessionSeatStatusVersion(ctx, sessionID)
		if err != nil {
			h.logger.Error("public_feed_checkout: increment seat_status_version failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.status_version_failed", "failed to bump seat_status_version", r,
			))
			return
		}

		// Lock seats FOR UPDATE in seat_key order.
		locked, err := resQ.LockSessionSeatsForHold(ctx, sessionID, normalizedSeats)
		if err != nil {
			h.logger.Error("public_feed_checkout: lock seats failed", slog.String("error", err.Error()))
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
				"one or more requested seats are not available",
				r,
				map[string]any{"conflicts": conflicts},
			))
			return
		}

		// Reserve inventory capacity for seats (nil tier = session-level).
		seatQty := int32(len(normalizedSeats)) //nolint:gosec // bounded above by slice len
		if _, err := invQ.ReserveCapacity(ctx, sessionID, nil, seatQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"reservation.over_capacity", "insufficient capacity for this reservation", r,
				))
				return
			}
			h.logger.Error("public_feed_checkout: reserve seat capacity failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.capacity_failed", "failed to reserve capacity", r,
			))
			return
		}

		// Insert reservation row.
		totalQty := seatQty
		for _, g := range req.GaItems {
			totalQty += g.Quantity
		}
		res, err := resQ.InsertReservation(
			ctx, checkCtx.OrgID, checkCtx.SalesChannelID, sessionID,
			nil, nil, totalQty, expiresAt,
		)
		if err != nil {
			h.logger.Error("public_feed_checkout: insert reservation failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.insert_failed", "failed to create reservation", r,
			))
			return
		}

		// Hold + link each seat.
		for _, s := range locked {
			held, err := resQ.HoldSessionSeat(ctx, s.ID, res.ID, newVersion)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
						"reservation.seats_conflict",
						"seat "+s.SeatKey+" is no longer available",
						r,
						map[string]any{"conflicts": []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}},
					))
					return
				}
				h.logger.Error("public_feed_checkout: hold seat failed",
					slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.hold_failed", "failed to hold seat", r,
				))
				return
			}
			_ = held

			if err := resQ.InsertReservationSeat(ctx, res.ID, s.ID); err != nil {
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
				h.logger.Error("public_feed_checkout: reservation_seats insert failed",
					slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.seats_link_failed", "failed to link seat to reservation", r,
				))
				return
			}
		}

		// Reserve GA capacity for each ga_item (if any in mixed mode).
		for i, item := range req.GaItems {
			tierID := parsedGATierIDs[i]
			tierIDPtr := &tierID
			if _, err := invQ.ReserveCapacity(ctx, sessionID, tierIDPtr, item.Quantity); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
						"reservation.over_capacity", "insufficient capacity for this reservation", r,
					))
					return
				}
				h.logger.Error("public_feed_checkout: reserve GA capacity failed",
					slog.String("tier_id", tierID.String()), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.capacity_failed", "failed to reserve GA capacity", r,
				))
				return
			}
		}

		// Commit the reservation transaction before creating the checkout session
		// (matches the original #153 pattern: reservation tx committed first so that
		// a checkout-session-insert failure does not roll back the seat holds).
		if err := tx.Commit(ctx); err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.commit_failed", "failed to commit transaction", r,
			))
			return
		}

		// Insert checkout session with pre-minted token (outside tx).
		cs, err := h.checkoutQueries.InsertCheckoutSessionWithToken(
			ctx, checkCtx.OrgID, checkCtx.SalesChannelID, res.ID, nil, checkoutToken,
		)
		if err != nil {
			h.logger.Error("public_feed_checkout: insert checkout session failed",
				slog.String("reservation_id", res.ID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.start_failed", "failed to create checkout session", r,
			))
			return
		}

		// Pricing for seated/mixed: seats are free in this initial implementation;
		// GA items contribute their unit prices.
		currency := "EUR" // default for seated-only; override if GA item present
		if len(pricedGA) > 0 {
			currency = pricedGA[0].currency
		}
		var bd hcheckout.PricingBreakdown
		if len(pricedGA) == 0 {
			// Pure seated: free
			bd = hcheckout.ComputePricing(0, totalQty, discount, currency, h.pricingRules)
		} else if len(pricedGA) == 1 {
			bd = hcheckout.ComputePricing(pricedGA[0].unitPrice, pricedGA[0].qty, discount, currency, h.pricingRules)
		} else {
			lines := make([]hcheckout.PricingLineInput, len(pricedGA))
			for i, g := range pricedGA {
				lines[i] = hcheckout.PricingLineInput{UnitPrice: g.unitPrice, Quantity: g.qty}
			}
			bd = hcheckout.ComputePricingLines(lines, discount, currency, h.pricingRules)
		}

		cs, err = h.checkoutQueries.ConfirmCheckoutSession(
			ctx, cs.ID,
			bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total,
			bd.Currency, promoCodeID,
		)
		if err != nil {
			h.logger.Error("public_feed_checkout: confirm checkout session failed",
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.confirm_failed", "failed to confirm checkout session", r,
			))
			return
		}

		redirectURL := fmt.Sprintf("/checkout/%s", cs.ID.String())
		if bd.Total == 0 {
			redirectURL = fmt.Sprintf("/checkout/%s/complete", cs.ID.String())
		}

		h.logger.Info("public_feed_checkout: seated session created",
			slog.String("feed_token", feedToken),
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("session_id", sessionID.String()),
			slog.String("holder_email", req.HolderEmail),
			slog.Int("seat_count", len(normalizedSeats)),
			slog.Int64("total", bd.Total),
		)

		httputil.WriteJSON(w, http.StatusCreated, map[string]any{
			"checkout_session": hcheckout.CheckoutSessionFromRow(cs),
			"redirect_url":     redirectURL,
			"checkout_token":   checkoutToken,
			"expires_at":       expiresAt.Format(time.RFC3339),
		})
		return
	}

	// ── 12. Pure GA branch ────────────────────────────────────────────────────
	// hasGA is true here (we checked both are false above and returned 400).

	// Determine total quantity.
	var totalQty int32
	for _, g := range req.GaItems {
		totalQty += g.Quantity
	}

	// Reserve capacity for each GA item.
	for i, item := range req.GaItems {
		tierID := parsedGATierIDs[i]
		tierIDPtr := &tierID
		if _, err := invQ.ReserveCapacity(ctx, sessionID, tierIDPtr, item.Quantity); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"reservation.over_capacity", "insufficient capacity for this reservation", r,
				))
				return
			}
			h.logger.Error("public_feed_checkout: reserve capacity failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.capacity_failed", "failed to reserve capacity", r,
			))
			return
		}
	}

	// Insert reservation (nil tier for multi-tier; single-tier uses first tier).
	var tierIDPtr *uuid.UUID
	if len(parsedGATierIDs) == 1 {
		tid := parsedGATierIDs[0]
		tierIDPtr = &tid
	}
	reservation, err := resQ.InsertReservation(
		ctx,
		checkCtx.OrgID,
		checkCtx.SalesChannelID,
		sessionID,
		tierIDPtr,
		nil, // userID — anonymous public checkout
		totalQty,
		expiresAt,
	)
	if err != nil {
		h.logger.Error("public_feed_checkout: insert reservation failed",
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.insert_failed", "failed to create reservation", r,
		))
		return
	}

	// Commit the reservation transaction before creating the checkout session
	// (matches the original #153 pattern: reservation tx committed first).
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	// Insert checkout session with pre-minted token (outside tx).
	cs, err := h.checkoutQueries.InsertCheckoutSessionWithToken(
		ctx, checkCtx.OrgID, checkCtx.SalesChannelID, reservation.ID, nil, checkoutToken,
	)
	if err != nil {
		h.logger.Error("public_feed_checkout: insert checkout session failed",
			slog.String("reservation_id", reservation.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.start_failed", "failed to create checkout session", r,
		))
		return
	}

	// Pricing for GA items.
	var currency string
	if len(pricedGA) > 0 {
		currency = pricedGA[0].currency
	}
	// For single GA item (including legacy tier_id+qty), use ComputePricing which
	// matches the original feature #153 behaviour. For multiple GA tiers, use
	// ComputePricingLines to preserve per-line breakdown accuracy.
	var bd hcheckout.PricingBreakdown
	if len(pricedGA) == 1 {
		bd = hcheckout.ComputePricing(pricedGA[0].unitPrice, pricedGA[0].qty, discount, currency, h.pricingRules)
	} else {
		lines := make([]hcheckout.PricingLineInput, len(pricedGA))
		for i, g := range pricedGA {
			lines[i] = hcheckout.PricingLineInput{UnitPrice: g.unitPrice, Quantity: g.qty}
		}
		bd = hcheckout.ComputePricingLines(lines, discount, currency, h.pricingRules)
	}

	cs, err = h.checkoutQueries.ConfirmCheckoutSession(
		ctx,
		cs.ID,
		bd.Subtotal,
		bd.Discount,
		bd.PlatformFee,
		bd.ProviderFee,
		bd.Tax,
		bd.Total,
		bd.Currency,
		promoCodeID,
	)
	if err != nil {
		h.logger.Error("public_feed_checkout: confirm checkout session failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.confirm_failed", "failed to confirm checkout session", r,
		))
		return
	}

	// Construct redirect URL.
	redirectURL := fmt.Sprintf("/checkout/%s", cs.ID.String())
	if bd.Total == 0 {
		redirectURL = fmt.Sprintf("/checkout/%s/complete", cs.ID.String())
	}

	h.logger.Info("public_feed_checkout: GA session created",
		slog.String("feed_token", feedToken),
		slog.String("checkout_session_id", cs.ID.String()),
		slog.String("session_id", sessionID.String()),
		slog.String("holder_email", req.HolderEmail),
		slog.Int64("total", bd.Total),
		slog.String("currency", bd.Currency),
	)

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"checkout_session": hcheckout.CheckoutSessionFromRow(cs),
		"redirect_url":     redirectURL,
		"checkout_token":   checkoutToken,
		"expires_at":       expiresAt.Format(time.RFC3339),
	})
}
