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
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
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
// Name and phone are always accepted (the widget offers them as optional);
// the channel's collect_name / collect_phone flag makes them required.
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
	// ReturnURL is the embedding page the buyer must be sent back to after
	// the provider-hosted payment page (widget sends
	// window.location.origin + pathname). Its ORIGIN is validated against
	// CORS_ALLOWED_ORIGINS + PUBLIC_TICKETS_BASE_URL; anything absent or
	// refused falls back to PUBLIC_TICKETS_BASE_URL.
	ReturnURL string `json:"return_url,omitempty"`
	// Locale is the language the buyer is checking out in — the widget's own
	// active locale. It decides which language their ticket e-mail and PDF
	// render in, and nothing else; an unknown or hostile value is silently
	// dropped rather than rejected, because a buyer must never lose a sale
	// over the language tag their browser reported.
	Locale string `json:"locale,omitempty"`
	// Consent is the buyer's tick on "I accept the sale terms" (P2). A channel
	// that asks for it refuses a cart without accepted:true with 422
	// checkout.consent_required before any hold. A ticked consent is recorded
	// in checkout_consents only when the channel asks for it; one sent to a
	// channel that does not is ignored (nothing of the buyer is stored).
	Consent *PublicCheckoutConsent `json:"consent,omitempty"`
}

// normalizeBuyerLocale validates a caller-supplied locale against the
// languages arena actually ships e-mail and PDF templates for, returning ""
// for anything else.
//
// It is deliberately forgiving: the value arrives from an embedded widget on
// someone else's site, and the worst outcome of an unrecognised one is an
// English e-mail — which is exactly what every buyer got before this existed.
// Rejecting the checkout instead would trade a cosmetic miss for a lost sale.
//
// Region subtags are accepted and folded ("cs-CZ" and "CS" both become "cs"),
// because a browser reports the full tag and the templates are per-language.
func normalizeBuyerLocale(raw string) string {
	candidate := strings.ToLower(strings.TrimSpace(raw))
	if candidate == "" {
		return ""
	}
	if idx := strings.IndexAny(candidate, "-_"); idx > 0 {
		candidate = candidate[:idx]
	}
	for _, supported := range templates.SupportedLocales {
		if candidate == supported {
			return candidate
		}
	}
	return ""
}

// localePtr turns a normalized locale into the nullable column value:
// "not stated" must land as NULL, never as an empty string that a later
// reader would have to special-case.
func localePtr(locale string) *string {
	if locale == "" {
		return nil
	}
	return &locale
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
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")

	// ── 1. Rate limiting: per-feed-token (shared with browse endpoints) +
	// per-IP, both always evaluated. ──────────────────────────────────────────
	if !h.enforceRateLimit(w, r, "feed.rate_limited", h.rl.CheckFeedToken, feedToken) {
		return
	}

	// ── 2. Parse + validate body ──────────────────────────────────────────────
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_body", "We could not read the request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.empty_body", "The request has no body. Send the checkout data as JSON.", r,
		))
		return
	}

	var req PublicFeedCheckoutStartRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_json", "The request body is not valid JSON. Fix it and send it again.", r,
		))
		return
	}

	// ── 3. Parse / validate session_id ───────────────────────────────────────
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.invalid_session_id", "Send session_id as a UUID.", r,
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

	// ── 4a. Normalise the buyer's language ────────────────────────────────────
	// Decided here, once, and carried to the checkout session so ticket
	// issuance can render the e-mail in it. An unrecognised value becomes ""
	// and the buyer gets English — the behaviour every buyer got before this
	// existed, and far better than failing a sale over a language tag.
	buyerLocale := normalizeBuyerLocale(req.Locale)

	// ── 4b. Validate holder_email ─────────────────────────────────────────────
	if req.HolderEmail == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_holder_email", "Enter the e-mail address for the tickets (holder_email).", r,
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
					"checkout.invalid_qty", "Choose from 1 to 50 tickets.", r,
					map[string]any{"field": "qty"},
				))
				return
			}
			// tier_id was present but invalid UUID (would have been added to GaItems but Qty was 0).
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_items", "The cart is empty. Send seats[], ga_items[] or tier_id with qty.", r,
			map[string]any{"field": "seats"},
		))
		return
	}

	// Validate legacy tier_id UUID if present (backward-compat; by now it's been
	// merged into GaItems, but we still parse it to give the correct 400).
	if req.TierID != "" && len(req.GaItems) > 0 && req.GaItems[0].TierID == req.TierID {
		if _, err := uuid.Parse(req.TierID); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_tier_id", "Send tier_id as a UUID.", r,
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
				"checkout.invalid_tier_id", fmt.Sprintf("Send ga_items[%d].tier_id as a UUID.", i), r,
				map[string]any{"field": "ga_items", "index": i},
			))
			return
		}
		if item.Quantity <= 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"checkout.invalid_qty", fmt.Sprintf("Send ga_items[%d].quantity as a whole number, 1 or more.", i), r,
				map[string]any{"field": "ga_items", "index": i},
			))
			return
		}
		parsedGATierIDs = append(parsedGATierIDs, tid)
	}

	if code, msg := checkCartCaps(req.Seats, req.GaItems); code != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, msg, r))
		return
	}

	// A spent refused-code budget turns the request away before it costs any
	// database work; otherwise the request holds a slot of it, which only a
	// refused code keeps (see PromoFailureLimiter).
	promoAttempt, promoBlocked := h.reservePromoAttempt(w, r, feedToken, strings.TrimSpace(derefOr(req.PromoCode)) != "")
	if promoBlocked {
		return
	}
	defer promoAttempt.Release()

	ctx := r.Context()

	// ── 6. Validate session belongs to this feed token ────────────────────────
	checkCtx, err := h.publicFeedQueries.GetPublicCheckoutContext(ctx, feedToken, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"feed.session_not_on_feed",
				"This session is not on sale here. Check the link.",
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
			"checkout.context_failed", "We could not check this checkout. Try again in a minute.", r,
		))
		return
	}
	// The service charge is the CHANNEL's fee_percent (the same setting the
	// Bil24 gateway applies), read on the pool before the hold transaction
	// opens — never inside it (AGENTS.md: no pool read while holding the
	// sessions row lock).
	pricingRules := h.channelPricingRules(ctx, checkCtx.OrgID, checkCtx.SalesChannelID)

	// ── 6b. Validate buyer fields against channel flags (WID-0d) ────────────────
	// Fetch the buyer-field flags for this feed token and enforce them.
	// A missing row (token had no linked channel, unlikely but defensive)
	// degrades to name and phone optional and no consent. A failed read is
	// refused (503 checkout.unavailable): it decides whether the buyer must
	// accept the sale terms (P2), so it fails closed rather than sell without
	// a consent the channel asks for.
	var collectName, collectPhone, consentRequired bool
	var consentSeller string
	if h.publicFeedQueries != nil {
		flags, flagsErr := h.publicFeedQueries.GetFeedTokenBuyerFlags(ctx, feedToken)
		if flagsErr == nil {
			collectName = flags.CollectName
			collectPhone = flags.CollectPhone
			consentRequired, consentSeller = consentFromFlags(flags)
		} else if !errors.Is(flagsErr, pgx.ErrNoRows) {
			h.logger.Error("public_feed_checkout: get buyer flags failed",
				slog.String("feed_token", feedToken),
				slog.String("error", flagsErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"checkout.unavailable", "We could not check this checkout. Try again in a minute.", r,
			))
			return
		}
	}

	// Snapshotted onto the order below regardless of the flags: if the buyer
	// volunteered a name or phone we keep it, we just do not demand it.
	var buyerName, buyerPhone *string
	if req.Buyer != nil {
		buyerName = ptrOrNil(strings.TrimSpace(derefOr(req.Buyer.Name)))
		buyerPhone = ptrOrNil(strings.TrimSpace(derefOr(req.Buyer.Phone)))
	}

	// A flag that is off means "optional": an empty value is accepted and a
	// non-empty one is kept (buyerName above). Only a required field refuses
	// an empty value; whitespace alone counts as empty.
	if collectName {
		if buyerName == nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"checkout.buyer_name_required",
				"This sales channel needs the buyer's name (buyer.name). Fill it in and try again.",
				r,
				map[string]any{"field": "buyer.name"},
			))
			return
		}
	}

	if collectPhone {
		if buyerPhone == nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"checkout.buyer_phone_required",
				"This sales channel needs the buyer's phone number (buyer.phone). Fill it in and try again.",
				r,
				map[string]any{"field": "buyer.phone"},
			))
			return
		}
	}

	// ── 6c. Buyer consent (P2) ───────────────────────────────────────────────
	// A channel that asks for the consent refuses a cart without the tick
	// here, before any hold is taken. A ticked consent of such a channel is
	// recorded later, in the checkout-confirmation transaction; everything it
	// needs (address, browser, site, what the checkbox showed, the withdrawal
	// notice the feed showed) is gathered now, on the pool, before any
	// transaction opens. A tick sent to a channel that does not ask for it is
	// ignored: nothing of the buyer is stored without a reason.
	if consentRefused(consentRequired, req.Consent) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"checkout.consent_required",
			"Please accept the sale terms of the organizer and the platform policy, then try again.",
			r,
			map[string]any{"field": "consent.accepted"},
		))
		return
	}
	var consent *pendingConsent
	if consentRecorded(consentRequired, req.Consent) {
		consent = newPendingConsent(r, checkCtx.OrgID, req.Consent.Locale, buyerLocale,
			h.showWithdrawalNotice(ctx, checkCtx.OrgID), time.Now(),
			h.consentBlockFor(ctx, consentRequired, consentSeller, checkCtx.EventID, feedToken))
		// P3a: name the organizer terms rendering the buyer accepts. A
		// failed read refuses the sale, like a failed flags read above.
		if err := h.attachTermsRendering(ctx, consent, checkCtx.OrgID, checkCtx.EventID, feedToken); err != nil {
			h.logger.Error("public_feed_checkout: terms rendering failed",
				slog.String("org_id", checkCtx.OrgID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"checkout.unavailable", "We could not check this checkout. Try again in a minute.", r,
			))
			return
		}
	}

	// ── 7. Price the GA lines ─────────────────────────────────────────────────
	// Shared with checkout/quote (public_feed_pricing.go): one resolution of
	// the tiers, their price windows and the pricing-pipeline input.
	pricedGA, priceErr := h.priceGAItems(ctx, sessionID, req.GaItems, parsedGATierIDs)
	if priceErr != "" {
		h.writePricingError(w, r, priceErr)
		return
	}
	gaLines := gaPricingLines(pricedGA)

	// ── 8. Promo code lookup ───────────────────────────────────────────────────
	// Only the row fetch happens here; validation (min-order-amount etc.) runs
	// once the FULL platform-computed subtotal is known — for seated carts that
	// includes the seat prices resolved inside the hold transaction.
	promoRow, promoLookupErr := h.findPromoCode(ctx, checkCtx.OrgID, derefOr(req.PromoCode))
	if promoLookupErr != nil {
		if errors.Is(promoLookupErr, pgx.ErrNoRows) {
			h.refusePromo(w, r, promoAttempt, "promo.not_found")
			return
		}
		h.logger.Error("public_feed_checkout: promo lookup failed",
			slog.String("error", promoLookupErr.Error()),
		)
		h.refusePromo(w, r, promoAttempt, "checkout.promo_lookup_failed")
		return
	}

	// ── 9. Mint checkout_token ────────────────────────────────────────────────
	checkoutToken, err := mintCheckoutToken()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.token_failed", "We could not start the checkout. Try again in a minute.", r,
		))
		return
	}

	expiresAt := time.Now().UTC().Add(hcheckout.DefaultReservationTTL)

	// ── 9b. Everything the hold does not need the sessions row for ───────────
	// The server load test of 2026-09-22 (docs/loadtest/2026-09-22_server_step2_ru.md
	// §4.4) found the widget's first ceiling here, not in the CPU: this
	// handler used to read the payment configuration, the promo usage
	// counts and the seat tier prices FROM THE POOL while its own
	// transaction already held the sessions row lock. Every such read is a
	// second pool connection taken by a request that is blocking the next
	// hold; under a browsing crowd the pool (20) filled with requests
	// waiting for each other and the whole api stood still until the
	// client's timeout. So: whatever can be known before the lock is
	// gathered here on the pool, and whatever must be read under the lock
	// goes through the transaction's own connection (see 10).
	gaMayBePaid := false
	for _, g := range pricedGA {
		if g.unitPrice > 0 {
			gaMayBePaid = true
			break
		}
	}
	var gaDiscount int64
	var gaPromoCodeID *uuid.UUID
	if !hasSeats {
		// Validate the promo against the platform-computed GA subtotal.
		gaPromoLines := hcheckout.TierLinesForSession(gaLines, sessionID.String(), cartCurrency(pricedGA, ""))
		var promoErrCode string
		gaDiscount, gaPromoCodeID, promoErrCode = h.applyPromoDiscount(ctx, h.promoQueries, promoRow, gaPromoLines, req.HolderEmail)
		if promoErrCode != "" {
			h.refusePromo(w, r, promoAttempt, promoErrCode)
			return
		}
	}
	// A seated cart is priced from the tier bindings of the seats it locks,
	// so it may turn out paid; a GA cart only when a priced line exists.
	preflight := h.preparePaymentPreflight(ctx, checkCtx, req.ReturnURL, hasSeats || gaMayBePaid)

	// ── 10. Begin transaction ─────────────────────────────────────────────────
	if h.inventoryQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	invQ := h.inventoryQueries.WithTx(tx)
	resQ := h.reservationQueries.WithTx(tx)
	// The seat tier prices and the seated cart's promo caps are read after
	// the seats are locked, because the tier bindings come from the locked
	// rows — but through THIS connection, never through the pool (see 9b).
	var tierQ, promoQ *gen.Queries
	if h.tierQueries != nil {
		tierQ = h.tierQueries.WithTx(tx)
	}
	if h.promoQueries != nil {
		promoQ = h.promoQueries.WithTx(tx)
	}

	// ── 11. Seated branch ─────────────────────────────────────────────────────
	if hasSeats {
		// Normalise seat keys: sort + dedup + reject empty.
		normalizedSeats, dupKey, normErr := hcheckout.NormalizeSeatKeys(req.Seats)
		if normErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"reservation.duplicate_seat", "Each seat can appear only once in seats[].", r,
				map[string]any{"seat_key": dupKey},
			))
			return
		}
		if len(normalizedSeats) == 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"reservation.invalid_seats", "Choose at least one seat: send a seat_key in seats[].", r,
				map[string]any{"field": "seats"},
			))
			return
		}

		// Validate admission mode: must be assigned_seats or hybrid.
		mode, err := resQ.GetSessionAdmissionModeByID(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"reservation.session_not_found", "We cannot find this session.", r,
				))
				return
			}
			h.logger.Error("public_feed_checkout: admission mode lookup failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.admission_lookup_failed", "We could not load the session. Try again in a minute.", r,
			))
			return
		}
		if mode.AdmissionMode == hcheckout.AdmissionGeneralAdmission {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"reservation.seats_not_supported",
				"This session has no seat plan. Send ga_items[] instead of seats[].",
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
				"reservation.status_version_failed", "We could not hold the tickets. Try again in a minute.", r,
			))
			return
		}

		// Lock seats FOR UPDATE in seat_key order.
		locked, err := resQ.LockSessionSeatsForHold(ctx, sessionID, normalizedSeats)
		if err != nil {
			h.logger.Error("public_feed_checkout: lock seats failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.lock_seats_failed", "We could not check the seats. Try again in a minute.", r,
			))
			return
		}

		// Check conflicts.
		conflicts := hcheckout.SeatConflicts(normalizedSeats, locked)
		if len(conflicts) > 0 {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"reservation.seats_conflict",
				"Some of the chosen seats are not free. Choose other seats.",
				r,
				map[string]any{"conflicts": conflicts},
			))
			return
		}

		// A closed category, or one outside its sale window, refuses a NEW
		// hold — for the seats' own categories (decision 10) and for every
		// GA line of a mixed cart (plan 08_architecture/23 step 4). Runs
		// under the sessions row lock taken above, before any inventory
		// moves.
		gateNow := time.Now().UTC()
		if gateErr := hcheckout.CheckSeatCategoriesSellable(ctx, resQ, sessionID, locked, gateNow); gateErr != nil {
			hcheckout.WriteCategoryGateError(w, r, gateErr)
			return
		}
		if gateErr := hcheckout.CheckGALinesSellable(ctx, resQ, sessionID, gaGateLines(parsedGATierIDs, req.GaItems), gateNow); gateErr != nil {
			hcheckout.WriteCategoryGateError(w, r, gateErr)
			return
		}

		// Reserve session-level inventory capacity for the WHOLE cart —
		// seats plus GA units (AB-51: GA lines no longer take per-tier
		// ledger reserves; concrete ga_unit rows are the per-tier truth).
		seatQty := int32(len(normalizedSeats)) //nolint:gosec // bounded above by slice len
		totalQty := seatQty
		for _, g := range req.GaItems {
			totalQty += g.Quantity
		}
		if _, err := invQ.ReserveCapacity(ctx, sessionID, nil, totalQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
					"reservation.over_capacity", "There are not enough free places. Choose fewer tickets or another category.", r,
				))
				return
			}
			h.logger.Error("public_feed_checkout: reserve seat capacity failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.capacity_failed", "We could not hold the tickets. Try again in a minute.", r,
			))
			return
		}
		res, err := resQ.InsertReservation(
			ctx, checkCtx.OrgID, checkCtx.SalesChannelID, sessionID,
			nil, nil, totalQty, expiresAt,
		)
		if err != nil {
			h.logger.Error("public_feed_checkout: insert reservation failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.insert_failed", "We could not hold the tickets. Try again in a minute.", r,
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
						"Seat "+s.SeatKey+" is no longer free. Choose another seat.",
						r,
						map[string]any{"conflicts": []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}},
					))
					return
				}
				h.logger.Error("public_feed_checkout: hold seat failed",
					slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.hold_failed", "We could not hold the seat. Try again in a minute.", r,
				))
				return
			}
			_ = held

			if err := resQ.InsertReservationSeat(ctx, res.ID, s.ID); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
					httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
						"reservation.seats_conflict",
						"Seat "+s.SeatKey+" is held for another buyer. Choose another seat.",
						r,
						map[string]any{"conflicts": []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}},
					))
					return
				}
				h.logger.Error("public_feed_checkout: reservation_seats insert failed",
					slog.String("seat_key", s.SeatKey), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.seats_link_failed", "We could not hold the seat. Try again in a minute.", r,
				))
				return
			}
		}

		// Allocate concrete GA units (AB-51) + persist the per-tier GA
		// lines (migration 0063) for each ga_item (if any in mixed mode).
		// Both happen in the SAME transaction as the seat holds so the
		// order-status and recovery endpoints can reconstruct the full
		// cart later (WID-0b / WID-0c). Mixed carts run on hybrid
		// sessions, which are plan-bound by construction.
		for i, item := range req.GaItems {
			tierID := parsedGATierIDs[i]
			if _, err := hcheckout.AllocateGAUnitsTx(
				ctx, resQ, sessionID, res.ID, newVersion,
				[]hcheckout.GAUnitLine{{TierID: &tierID, Quantity: item.Quantity}},
			); err != nil {
				var capErr *hcheckout.CapacityError
				if errors.As(err, &capErr) {
					httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
						"reservation.over_capacity", "There are not enough free places. Choose fewer tickets or another category.", r,
						map[string]any{"tier_id": tierID.String(), "requested": item.Quantity},
					))
					return
				}
				h.logger.Error("public_feed_checkout: GA unit allocation failed",
					slog.String("tier_id", tierID.String()), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.capacity_failed", "We could not hold the tickets. Try again in a minute.", r,
				))
				return
			}
			if err := resQ.InsertReservationGAItem(ctx, res.ID, tierID, item.Quantity, pricedGA[i].unitPrice); err != nil {
				h.logger.Error("public_feed_checkout: insert GA line failed",
					slog.String("tier_id", tierID.String()), slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"reservation.insert_failed", "We could not hold the tickets. Try again in a minute.", r,
				))
				return
			}
		}

		// Price the held seats through the platform pipeline (guardrail #15):
		// one line per (tier, unit_price) group resolved from the seats'
		// session_seats.tier_id bindings, plus the GA lines. Seats without a
		// bound tier price at 0. Runs inside the hold transaction so a pricing
		// rejection (e.g. pwyw tier) rolls the holds back cleanly.
		seatLines, seatCurrency, errCode := h.seatPricingLines(ctx, tierQ, sessionID, locked)
		if errCode != "" {
			h.writePricingError(w, r, errCode)
			return
		}
		lines := append(seatLines, gaLines...)

		currency := cartCurrency(pricedGA, seatCurrency)
		if currency == "" {
			currency = "EUR" // defensive default: no tier bound anywhere
		}

		// Validate the promo against the FULL subtotal (seats + GA) now that
		// every line is priced. A rejection rolls back the holds via the
		// deferred tx.Rollback.
		promoLines := hcheckout.TierLinesForSession(lines, sessionID.String(), currency)
		discount, promoCodeID, promoErrCode := h.applyPromoDiscount(ctx, promoQ, promoRow, promoLines, req.HolderEmail)
		if promoErrCode != "" {
			// Same neutral verdicts as the quote and the general-admission
			// branch, and the refusal is counted against the refused-code
			// budget. The deferred rollback releases the holds.
			h.refusePromo(w, r, promoAttempt, promoErrCode)
			return
		}

		// Priced BEFORE the commit on purpose: ComputePricingLines is pure, and
		// the total it yields is what decides whether this cart needs a payment
		// provider at all.
		bd := hcheckout.ComputePricingLines(lines, discount, currency, pricingRules)

		// A paid cart that could never have been charged must not survive this
		// request. Returning here lets the deferred tx.Rollback release the
		// seats, the GA units and the reserved capacity that would otherwise
		// sit held until the TTL sweep (~31 min) — which is how three failed
		// attempts "sold out" a 15-seat master class on 2026-09-20. The
		// answer was gathered before the transaction opened (9b); only the
		// decision is taken here, once the total is known.
		if !h.applyPaymentPreflight(ctx, w, r, checkCtx, bd.Total, preflight) {
			return
		}

		// Commit the reservation transaction before creating the checkout session
		// (matches the original #153 pattern: reservation tx committed first so that
		// a checkout-session-insert failure does not roll back the seat holds).
		if err := tx.Commit(ctx); err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.commit_failed", "We could not hold the tickets. Try again in a minute.", r,
			))
			return
		}

		h.logger.Info("public_feed_checkout: seated session priced",
			slog.String("feed_token", feedToken),
			slog.String("session_id", sessionID.String()),
			slog.String("holder_email", req.HolderEmail),
			slog.Int("seat_count", len(normalizedSeats)),
			slog.Int64("total", bd.Total),
			slog.String("currency", bd.Currency),
		)

		// Seated units are priced from the per-tier map: reservation_ga_items
		// carries the price lock, but the seats are the units (see
		// ordering.collectUnits), so the order needs tier → unit price here.
		seatTierPrices := make(map[uuid.UUID]int64, len(lines))
		for _, l := range lines {
			tid, parseErr := uuid.Parse(l.TierID)
			if parseErr != nil {
				continue
			}
			seatTierPrices[tid] = l.UnitPrice
		}
		h.confirmPublicCheckout(ctx, w, r, checkCtx, res.ID, checkoutToken, bd, promoCodeID, expiresAt,
			publicOrderBuyer{Email: req.HolderEmail, Name: buyerName, Phone: buyerPhone, Locale: buyerLocale}, seatTierPrices, req.ReturnURL, buyerLocale, consent)
		return
	}

	// ── 12. Pure GA branch ────────────────────────────────────────────────────
	// hasGA is true here (we checked both are false above and returned 400).

	// Determine total quantity.
	var totalQty int32
	for _, g := range req.GaItems {
		totalQty += g.Quantity
	}

	// The promo was validated against the GA subtotal before the
	// transaction opened (9b): nothing here depends on the hold.
	discount, promoCodeID := gaDiscount, gaPromoCodeID

	// Lock order (AGENTS.md, hcheckout.createGAHoldTx): bump
	// sessions.seat_status_version FIRST, then inventory_ledger, then the
	// places. This branch used to reserve capacity before the bump, the
	// same inversion that deadlocked (40P01) the GA hold path.
	gaVersion, err := resQ.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		h.logger.Error("public_feed_checkout: bump seat_status_version failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.insert_failed", "We could not hold the tickets. Try again in a minute.", r,
		))
		return
	}

	// A closed category, or one outside its sale window, takes no NEW hold
	// (plan 08_architecture/23 step 4).
	if gateErr := hcheckout.CheckGALinesSellable(ctx, resQ, sessionID, gaGateLines(parsedGATierIDs, req.GaItems), time.Now().UTC()); gateErr != nil {
		hcheckout.WriteCategoryGateError(w, r, gateErr)
		return
	}

	// Reserve session-level capacity for the whole cart (AB-51: per-tier
	// truth lives in the ga_unit rows allocated below).
	if _, err := invQ.ReserveCapacity(ctx, sessionID, nil, totalQty); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"reservation.over_capacity", "There are not enough free places. Choose fewer tickets or another category.", r,
			))
			return
		}
		h.logger.Error("public_feed_checkout: reserve capacity failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.capacity_failed", "We could not hold the tickets. Try again in a minute.", r,
		))
		return
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
			"reservation.insert_failed", "We could not hold the tickets. Try again in a minute.", r,
		))
		return
	}

	// Allocate concrete GA places (AB-51) and persist the per-tier GA
	// lines (migration 0063) in the same transaction so the order-status
	// and recovery endpoints can reconstruct the cart.
	for i, item := range req.GaItems {
		tierID := parsedGATierIDs[i]
		if _, err := hcheckout.AllocateGAUnitsTx(
			ctx, resQ, sessionID, reservation.ID, gaVersion,
			[]hcheckout.GAUnitLine{{TierID: &tierID, Quantity: item.Quantity}},
		); err != nil {
			var capErr *hcheckout.CapacityError
			if errors.As(err, &capErr) {
				httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
					"reservation.over_capacity", "There are not enough free places. Choose fewer tickets or another category.", r,
					map[string]any{"tier_id": tierID.String(), "requested": item.Quantity},
				))
				return
			}
			h.logger.Error("public_feed_checkout: GA unit allocation failed",
				slog.String("tier_id", tierID.String()), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.capacity_failed", "We could not hold the tickets. Try again in a minute.", r,
			))
			return
		}
		if err := resQ.InsertReservationGAItem(ctx, reservation.ID, tierID, item.Quantity, pricedGA[i].unitPrice); err != nil {
			h.logger.Error("public_feed_checkout: insert GA line failed",
				slog.String("tier_id", tierID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reservation.insert_failed", "We could not hold the tickets. Try again in a minute.", r,
			))
			return
		}
	}

	// Pricing for GA items — always through the multi-line pipeline. A single
	// line produces totals identical to the original feature #153 single-tier
	// path (see TestSeatC2_ComputePricingLines_SingleLine_MatchesSingleTier).
	// Computed BEFORE the commit so the pre-flight below can still roll the
	// hold back.
	currency := cartCurrency(pricedGA, "")
	bd := hcheckout.ComputePricingLines(gaLines, discount, currency, pricingRules)

	// A paid cart that could never have been charged must not survive this
	// request: the deferred tx.Rollback releases the GA units and the
	// reserved capacity instead of leaving them held until the TTL sweep.
	// The configuration was read before the transaction opened (9b).
	if !h.applyPaymentPreflight(ctx, w, r, checkCtx, bd.Total, preflight) {
		return
	}

	// Commit the reservation transaction before creating the checkout session
	// (matches the original #153 pattern: reservation tx committed first).
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.commit_failed", "We could not hold the tickets. Try again in a minute.", r,
		))
		return
	}

	h.logger.Info("public_feed_checkout: GA session priced",
		slog.String("feed_token", feedToken),
		slog.String("session_id", sessionID.String()),
		slog.String("holder_email", req.HolderEmail),
		slog.Int64("total", bd.Total),
		slog.String("currency", bd.Currency),
	)

	// Pure GA: reservation_ga_items.unit_price is authoritative for every unit,
	// so no tier price map is needed.
	h.confirmPublicCheckout(ctx, w, r, checkCtx, reservation.ID, checkoutToken, bd, promoCodeID, expiresAt,
		publicOrderBuyer{Email: req.HolderEmail, Name: buyerName, Phone: buyerPhone, Locale: buyerLocale}, nil, req.ReturnURL, buyerLocale, consent)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pricing + confirmation helpers (shared by the seated and GA branches)
// ─────────────────────────────────────────────────────────────────────────────

// resolvePublicTierUnitPrice maps a tier's pricing mode onto a unit price for
// the public (anonymous) checkout. Returns ("" errCode) on success. pwyw is
// rejected — it requires the authenticated flow (matching the GA validation
// in step 7).
func resolvePublicTierUnitPrice(tier gen.TicketTierRow) (int64, string) {
	switch tier.PricingMode {
	case "free":
		return 0, ""
	case "fixed":
		return tier.PriceAmount, ""
	case "pwyw":
		return 0, "checkout.pwyw_not_supported"
	default:
		return 0, "checkout.unknown_pricing_mode"
	}
}

// publicTierUnitPrice is the AB-48-aware variant of
// resolvePublicTierUnitPrice: fixed-mode tiers resolve their scheduled
// price window at "now" through the ONE resolver (priceresolve →
// domain/pricing). The mode gating stays identical.
//
// q is the handle the price windows are read through: the pool before a
// hold transaction opens, the transaction's own Queries (WithTx) inside
// one — a pool read from inside an open transaction is a second
// connection held while the first one holds the sessions row lock.
func (h *Handler) publicTierUnitPrice(ctx context.Context, q *gen.Queries, tier gen.TicketTierRow) (int64, string) {
	unit, errCode := resolvePublicTierUnitPrice(tier)
	if errCode != "" || tier.PricingMode != "fixed" {
		return unit, errCode
	}
	if q == nil {
		return 0, "dependency.tier_unavailable"
	}
	eff, err := priceresolve.ForTier(ctx, q, tier, time.Now().UTC())
	if err != nil {
		h.logger.Error("public_feed_checkout: price window lookup failed",
			slog.String("tier_id", tier.ID.String()), slog.String("error", err.Error()))
		return 0, "checkout.tier_lookup_failed"
	}
	return eff.Amount, ""
}

// seatPricingLines resolves the unit price of every tier bound to the given
// (locked) seats and groups the seats into pricing lines — one line per
// (tier_id, unit_price) group, via the shared SEAT-C2 helper. Seats without
// a bound tier price at 0. Also returns the currency of the first priced
// tier ("" when no seat has a tier) and an error code ("" on success).
//
// q must be the hold transaction's own Queries (tierQueries.WithTx): the
// seats are already locked when this runs, and a pool read here is exactly
// the second-connection-under-the-lock pattern the 2026-09-22 load test
// traced the widget's first ceiling to.
func (h *Handler) seatPricingLines(
	ctx context.Context,
	q *gen.Queries,
	sessionID uuid.UUID,
	seats []gen.SessionSeatRow,
) ([]hcheckout.PricingLineInput, string, string) {
	tierPrice := make(map[string]int64)
	currency := ""
	for _, s := range seats {
		if s.TierID == nil {
			continue
		}
		key := s.TierID.String()
		if _, done := tierPrice[key]; done {
			continue
		}
		if q == nil {
			return nil, "", "dependency.tier_unavailable"
		}
		tier, err := q.GetTicketTierByID(ctx, *s.TierID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "", "checkout.tier_not_found"
			}
			h.logger.Error("public_feed_checkout: seat tier lookup failed",
				slog.String("tier_id", key),
				slog.String("error", err.Error()),
			)
			return nil, "", "checkout.tier_lookup_failed"
		}
		unit, errCode := h.publicTierUnitPrice(ctx, q, tier)
		if errCode != "" {
			return nil, "", errCode
		}
		tierPrice[key] = unit
		if currency == "" {
			currency = tier.Currency
		}
	}
	return hcheckout.BuildSeatedPricingLines(seats, tierPrice), currency, ""
}

// writePricingError translates a pricing-resolution error code from
// seatPricingLines into the canonical HTTP envelope.
func (h *Handler) writePricingError(w http.ResponseWriter, r *http.Request, code string) {
	switch code {
	case "dependency.tier_unavailable":
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			code, "The service is temporarily unavailable. Try again in a minute.", r,
		))
	case "checkout.tier_lookup_failed":
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			code, "We could not load the ticket category. Try again in a minute.", r,
		))
	case "checkout.tier_not_found":
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			code, "This session has no such ticket category.", r,
		))
	case "checkout.pwyw_not_supported":
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			code, "This page cannot sell a pay-what-you-want category.", r,
		))
	default:
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			code, "We could not calculate the price. Try again in a minute.", r,
		))
	}
}

// applyPromoDiscount validates the pre-fetched promo row against the
// platform-computed order lines. Returns (discount, promoCodeID, errCode);
// errCode is "" when no promo was supplied or the promo is applicable.
//
// q is where the usage caps are counted: the pool when this runs before the
// hold transaction (GA carts), the transaction's own Queries inside it
// (seated carts, whose subtotal is only known once the seats are locked).
func (h *Handler) applyPromoDiscount(ctx context.Context, q *gen.Queries, promoRow *gen.PromoCodeRow, lines []hcheckout.TierLine, buyerEmail string) (int64, *uuid.UUID, string) {
	if promoRow == nil {
		return 0, nil, ""
	}
	d, errCode := hcheckout.ValidatePromoForLines(*promoRow, lines, time.Now().UTC())
	if errCode != "" {
		return 0, nil, errCode
	}
	// The usage caps, for the buyer this cart names. A counting failure is
	// logged and the code is refused — a cap the platform cannot verify is
	// not one it may wave through.
	// A nil *gen.Queries must stay a nil interface, or CheckPromoLimits
	// would call through a typed nil.
	var limits hcheckout.PromoLimitQuerier
	if q != nil {
		limits = q
	}
	limitErr, err := hcheckout.CheckPromoLimits(ctx, limits, *promoRow, nil, buyerEmail)
	if err != nil {
		h.logger.Error("public_feed_checkout: promo usage count failed",
			slog.String("promo_code_id", promoRow.ID.String()), slog.String("error", err.Error()))
		return 0, nil, "checkout.promo_lookup_failed"
	}
	if limitErr != "" {
		return 0, nil, limitErr
	}
	return d, &promoRow.ID, ""
}

// publicOrderBuyer is the buyer identity the public feed collected on this
// request. Email is always present (the endpoint rejects an empty one at step
// 4); name and phone are filled when the buyer typed them, and are demanded
// only when the channel's collect_name / collect_phone flags are on.
type publicOrderBuyer struct {
	Email string
	Name  *string
	Phone *string
	// Locale is the already-normalized buyer language ("" when not stated).
	// It reaches customers.Resolve, which applies it only when it creates a
	// brand-new customer row.
	Locale string
}

// ptrOrNil turns a possibly-empty string into the *string the order columns
// want: an empty buyer field must land as NULL, not as ”.
func ptrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// derefOr reads an optional JSON string field, yielding "" for absent.
func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// confirmPublicCheckout inserts the checkout session with the pre-minted
// token, stores the platform-computed pricing snapshot (created →
// pricing_confirmed), mints the order aggregate, and writes the 201 response.
// The response includes the full pricing breakdown (subtotal / fees / total /
// currency plus the per-line tier groups) so the widget can display totals
// without doing any money arithmetic client-side (guardrail #15).
//
// W1-A6c (feature #488; spec §14.1) made this ONE transaction. Before, the
// insert and the confirm were two independent statements, so a crash between
// them left a 'created' session nobody would ever confirm. Now the insert, the
// confirm, the §12.2 customer resolution and the orders/order_items/
// order_events triple either all land or none do — an order without its
// checkout snapshot is a reconciliation bug no later step can repair.
//
// Customer resolution is best-effort: a public buyer who cannot be resolved
// into a customer row still gets their tickets, with orders.customer_id NULL
// and the buyer columns carrying the identity. Losing a sale over a CRM
// rollup would be the wrong trade.
func (h *Handler) confirmPublicCheckout(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	checkCtx gen.PublicCheckoutContextRow,
	reservationID uuid.UUID,
	checkoutToken string,
	bd hcheckout.PricingBreakdown,
	promoCodeID *uuid.UUID,
	expiresAt time.Time,
	buyer publicOrderBuyer,
	tierUnitPrices map[uuid.UUID]int64,
	returnURL string,
	buyerLocale string,
	consent *pendingConsent,
) {
	// ── 0. A paid cart needs a place to send the buyer BACK to, and a
	// payment window, before anything is written. Resolving the return URL
	// first means a misconfigured embed fails before it has an order and a
	// confirmed checkout to abandon.
	paid := bd.Total > 0
	var returnBase string
	if paid {
		base, rErr := h.returnURLPolicy.Resolve(returnURL)
		if rErr != nil {
			pse, _ := AsPaymentStartError(rErr)
			h.logger.Warn("public_feed_checkout: return_url rejected and no fallback configured",
				slog.String("return_url", returnURL),
				slog.String("reservation_id", reservationID.String()),
			)
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				pse.Code, pse.Message, r,
			))
			return
		}
		returnBase = base
	}

	// The hold, the order and the checkout must all outlive the hosted
	// payment page, or the buyer can pay for seats arena has already
	// released. The payment window is the hosted session's own expiry; the
	// grace on top is what arena waits before releasing. Mirrors the Bil24
	// gateway's CREATE_ORDER_EXT contract (hbil24/cmd_order_create.go):
	// move reservations.expires_at inside the SAME transaction that mints
	// the order, and ordering.CreateOrderFromCheckout copies orders.expires_at
	// straight off the reservation it re-reads — so the two cannot disagree.
	holdExpiresAt := expiresAt
	if paid && h.paymentWindow > 0 {
		holdExpiresAt = time.Now().UTC().Add(h.paymentWindow + h.paymentGrace)
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.logger.Error("public_feed_checkout: begin checkout tx failed",
			slog.String("reservation_id", reservationID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.start_failed", "We could not start the checkout. Try again in a minute.", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txq := gen.New(tx)

	// Touches only the reservations row — no inventory, no session lock — so
	// it observes no lock order of its own (AGENTS.md hold-mutation rule).
	if !holdExpiresAt.Equal(expiresAt) {
		if _, err := hcheckout.SetHoldExpiryTx(ctx, txq, reservationID, holdExpiresAt); err != nil {
			h.logger.Error("public_feed_checkout: set payment-window hold expiry failed",
				slog.String("reservation_id", reservationID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.start_failed", "We could not start the checkout. Try again in a minute.", r,
			))
			return
		}
		expiresAt = holdExpiresAt
	}

	cs, err := txq.InsertCheckoutSessionWithToken(
		ctx, checkCtx.OrgID, checkCtx.SalesChannelID, reservationID, nil, checkoutToken,
		localePtr(buyerLocale),
	)
	if err != nil {
		h.logger.Error("public_feed_checkout: insert checkout session failed",
			slog.String("reservation_id", reservationID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.start_failed", "We could not start the checkout. Try again in a minute.", r,
		))
		return
	}

	// The buyer's consent (P2) is evidence for the sale it belongs to. It is
	// written here, in the checkout-confirmation transaction, because it needs
	// the checkout session id; the hold transaction has already committed. A
	// failed insert fails the request like any other confirmation failure:
	// this transaction rolls back and the hold stays until its TTL sweep.
	// Neither this code nor anything else of P2 logs the address or browser
	// (the router's request log still records remote_addr and user_agent of
	// every request; this row is the only stored copy with a retention rule).
	if consent != nil {
		if _, err := txq.InsertCheckoutConsent(ctx, consent.params(checkCtx.OrgID, cs.ID)); err != nil {
			h.logger.Error("public_feed_checkout: insert checkout consent failed",
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.start_failed", "We could not start the checkout. Try again in a minute.", r,
			))
			return
		}
	}

	cs, err = txq.ConfirmCheckoutSession(
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
			"checkout.confirm_failed", "We could not confirm the checkout. Try again in a minute.", r,
		))
		return
	}

	orderID, err := h.createPublicOrder(ctx, tx, txq, checkCtx, cs, buyer, tierUnitPrices)
	if err != nil {
		h.logger.Error("public_feed_checkout: create order failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.confirm_failed", "We could not confirm the checkout. Try again in a minute.", r,
		))
		return
	}

	// A zero-total cart has nothing to pay, so it is finished right here —
	// completed, tickets enqueued, order paid, hold queued for conversion,
	// all in THIS transaction through the same helper the payment webhook
	// uses. Before this, checkout/start answered a "/checkout/<id>/complete"
	// URL that no route has ever served: a free order simply never arrived.
	if !paid {
		if _, err := hcheckout.CompleteFreeCheckoutTx(ctx, tx, txq, cs.ID); err != nil {
			h.logger.Error("public_feed_checkout: complete free checkout failed",
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.confirm_failed", "We could not complete the free order. Try again in a minute.", r,
			))
			return
		}
		cs, err = txq.GetCheckoutSessionByID(ctx, cs.ID)
		if err != nil {
			h.logger.Error("public_feed_checkout: reload completed free checkout failed",
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"checkout.confirm_failed", "We could not complete the free order. Try again in a minute.", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("public_feed_checkout: commit checkout tx failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.confirm_failed", "We could not confirm the checkout. Try again in a minute.", r,
		))
		return
	}

	// ── Free order: the buyer goes straight back to the embedding page,
	// where the widget resumes on ?checkout_token= and shows the tickets.
	// The return URL is best-effort here: with none configured a free order
	// still succeeded, and the widget already holds the token it needs.
	if !paid {
		redirectURL := ""
		if base, rErr := h.returnURLPolicy.Resolve(returnURL); rErr == nil {
			redirectURL = ReturnURLWithToken(base, checkoutToken)
		}
		httputil.WriteJSON(w, http.StatusCreated, map[string]any{
			"checkout_session": hcheckout.CheckoutSessionFromRow(cs),
			"redirect_url":     redirectURL,
			"checkout_token":   checkoutToken,
			"expires_at":       expiresAt.Format(time.RFC3339),
			"pricing":          bd,
		})
		return
	}

	// ── Paid order: take the money. This runs AFTER the commit on purpose —
	// a provider call can hang for seconds and must never hold a transaction
	// that has seats locked behind it.
	redirectURL, ok := h.startHostedPayment(ctx, w, r, hostedPaymentInput{
		CheckCtx:      checkCtx,
		Session:       cs,
		CheckoutToken: checkoutToken,
		OrderID:       orderID,
		Breakdown:     bd,
		Buyer:         buyer,
		ReturnBase:    returnBase,
		ExpiresAt:     expiresAt,
	})
	if !ok {
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"checkout_session": hcheckout.CheckoutSessionFromRow(cs),
		"redirect_url":     redirectURL,
		"checkout_token":   checkoutToken,
		"expires_at":       expiresAt.Format(time.RFC3339),
		"pricing":          bd,
	})
}

// bestEffort runs a write set that must never cost a buyer their paid-for
// cart. Merely logging and swallowing the error is NOT enough: Postgres
// marks the whole enclosing transaction aborted after any failed
// statement, so every later statement in that transaction dies with
// 25P02 and the eventual COMMIT silently rolls the order back too. A
// SAVEPOINT (pgx nested transaction) confines the damage to this section,
// which is then discarded and logged. Mirrors
// hbil24.Handler.payBestEffort (AGENTS.md "best-effort writes... MUST sit
// behind a SAVEPOINT"); returns whether the section committed, so callers
// that need to know what was actually written (e.g. a customer id to
// carry forward) can act on it instead of assuming success.
func (h *Handler) bestEffort(
	ctx context.Context,
	tx pgx.Tx,
	label string,
	checkoutSessionID uuid.UUID,
	fn func(pgx.Tx) error,
) bool {
	sp, err := tx.Begin(ctx)
	if err != nil {
		h.logger.Error("public_feed_checkout: could not open a savepoint",
			slog.String("section", label),
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()),
		)
		return false
	}
	if err := fn(sp); err != nil {
		_ = sp.Rollback(ctx)
		h.logger.Warn("public_feed_checkout: bookkeeping section failed and was discarded (non-fatal)",
			slog.String("section", label),
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()),
		)
		return false
	}
	if err := sp.Commit(ctx); err != nil {
		_ = sp.Rollback(ctx)
		h.logger.Warn("public_feed_checkout: bookkeeping section could not be released (non-fatal)",
			slog.String("section", label),
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("error", err.Error()),
		)
		return false
	}
	return true
}

// createPublicOrder mints the order aggregate for a just-confirmed public
// checkout session, on the caller's transaction.
//
// It resolves (or creates) the customer behind the buyer's email first, so
// the org's CRM rollup sees the sale, then hands the money snapshot to
// ordering.CreateOrderFromCheckout. Only a genuine order-write failure is
// returned: identity resolution and the fee-percent audit snapshot both
// degrade quietly, because neither is worth failing a paid-for cart over.
//
// Returns the new order's id so the caller can stamp it onto the payment
// provider's metadata — a Stripe dashboard row that cannot be traced back to
// an arena order is useless during a dispute.
func (h *Handler) createPublicOrder(
	ctx context.Context,
	tx pgx.Tx,
	txq *gen.Queries,
	checkCtx gen.PublicCheckoutContextRow,
	cs gen.CheckoutSessionRow,
	buyer publicOrderBuyer,
	tierUnitPrices map[uuid.UUID]int64,
) (*uuid.UUID, error) {
	var customerID *uuid.UUID
	if buyer.Email != "" {
		// Best-effort by design (see the doc comment above), but "log and
		// swallow" is NOT enough on its own: Postgres marks the whole
		// checkout transaction aborted after any failed statement in this
		// block (e.g. customers.Resolve exhausting its own internal race
		// retries, or a genuine constraint error), and the later order
		// write / COMMIT would then die with 25P02, silently losing an
		// already-paid-for cart. A SAVEPOINT confines the damage to this
		// section — AGENTS.md "best-effort writes... MUST sit behind a
		// SAVEPOINT", mirroring hbil24.Handler.payBestEffort. resolvedID is
		// only promoted to the outer customerID once the WHOLE section
		// (resolve + org link + reservation link) has actually committed —
		// otherwise a later statement in this same section failing after
		// Resolve succeeded would leave customerID pointing at a customer
		// row the savepoint rollback just undid.
		var resolvedID uuid.UUID
		committed := h.bestEffort(ctx, tx, "customer resolution", cs.ID, func(sp pgx.Tx) error {
			spq := gen.New(sp)
			store := customers.NewStoreFromQueries(spq)
			name := ""
			if buyer.Name != nil {
				name = *buyer.Name
			}
			phone := ""
			if buyer.Phone != nil {
				phone = *buyer.Phone
			}
			res, resErr := customers.Resolve(ctx, store, customers.ResolveInput{
				Email:     buyer.Email,
				Phone:     phone,
				Name:      name,
				ChannelID: checkCtx.SalesChannelID,
				// Applied only if this CREATES the customer; an existing
				// row keeps the locale it already has.
				Locale: buyer.Locale,
				Now:    time.Now().UTC(),
			})
			if resErr != nil {
				return fmt.Errorf("resolve customer: %w", resErr)
			}
			resolvedID = res.Customer.ID
			if linkErr := customers.LinkOrg(ctx, store, resolvedID, checkCtx.OrgID, "order"); linkErr != nil {
				return fmt.Errorf("link customer to org %s: %w", resolvedID.String(), linkErr)
			}
			// spec §12.2 call sites: attach the resolved buyer to the
			// reservation itself, not just the order, so a cancelled/
			// expired cart that never reaches an order still records who
			// held it.
			if resvErr := spq.UpdateReservationCustomer(ctx, cs.ReservationID, resolvedID); resvErr != nil {
				return fmt.Errorf("link customer to reservation %s: %w", cs.ReservationID.String(), resvErr)
			}
			return nil
		})
		if committed {
			customerID = &resolvedID
		}
	}

	if customerID != nil {
		if err := h.supersedeOpenOrder(ctx, txq, checkCtx, cs, *customerID); err != nil {
			return nil, err
		}
	}

	// The channel fee percentage is an audit snapshot only; the exact charge
	// travels in orders.charge, so a lookup failure degrades to 0.
	var chargePercentBP int32
	if ch, chErr := txq.GetSalesChannelByID(ctx, checkCtx.SalesChannelID, checkCtx.OrgID); chErr == nil {
		chargePercentBP = ordering.ChargePercentBP(ch.FeePercent)
	}

	res, err := ordering.CreateOrderFromCheckout(ctx, txq, ordering.CreateInput{
		CheckoutSessionID: cs.ID,
		EventID:           checkCtx.EventID,
		CustomerID:        customerID,
		Source:            ordering.SourcePublicFeed,
		Actor:             ordering.ActorSystem,
		ChargePercentBP:   chargePercentBP,
		BuyerName:         buyer.Name,
		BuyerEmail:        ptrOrNil(buyer.Email),
		BuyerPhone:        buyer.Phone,
		TierUnitPrices:    tierUnitPrices,
	})
	if err != nil {
		return nil, err
	}
	orderID := res.Order.ID
	return &orderID, nil
}

// supersedeOpenOrder closes the customer's previous pending_payment order on
// this event session before a new one is minted, so the partial unique index
// orders_one_pending_per_customer_session_uq (spec §14.3) admits the insert.
//
// A widget buyer who abandons one cart and starts another with the same email
// legitimately arrives here; before W1-A6c that was two independent holds and
// nothing else. The earlier hold still keeps its seats until it expires on its
// own (unchanged behaviour), only its order aggregate is cancelled with a
// "superseded_by_new_checkout" event so the admin list shows what happened.
// Without this the second checkout/start died on the index with a 500.
func (h *Handler) supersedeOpenOrder(
	ctx context.Context,
	txq *gen.Queries,
	checkCtx gen.PublicCheckoutContextRow,
	cs gen.CheckoutSessionRow,
	customerID uuid.UUID,
) error {
	open, err := ordering.FindOpenOrder(ctx, txq, customerID, checkCtx.SessionID)
	if errors.Is(err, ordering.ErrNoOpenOrder) {
		return nil
	}
	if err != nil {
		return err
	}
	if open.CheckoutSessionID == cs.ID {
		return nil
	}
	if _, err := ordering.Cancel(ctx, txq, ordering.CancelInput{
		OrderID: open.ID,
		OrgID:   open.OrgID,
		Actor:   ordering.ActorSystem,
		Reason:  "superseded_by_new_checkout",
	}); err != nil {
		return fmt.Errorf("supersede open order %s: %w", open.ID, err)
	}
	h.logger.Info("public_feed_checkout: superseded open order",
		slog.String("order_id", open.ID.String()),
		slog.String("customer_id", customerID.String()),
		slog.String("session_id", checkCtx.SessionID.String()),
		slog.String("checkout_session_id", cs.ID.String()),
	)
	return nil
}

// gaGateLines pairs the parsed GA tier ids with their requested quantities
// for hcheckout.CheckGALinesSellable (the chain-aware category gate).
func gaGateLines(ids []uuid.UUID, items []PublicGAItem) []hcheckout.GALine {
	out := make([]hcheckout.GALine, 0, len(ids))
	for i, id := range ids {
		out = append(out, hcheckout.GALine{TierID: id, Quantity: items[i].Quantity})
	}
	return out
}

// channelPricingRules is the pricing rule set for one sales channel: the
// process-wide defaults with the platform service charge replaced by the
// channel's own fee_percent when one is configured. Until 2026-09-29 the
// widget priced every cart with the process-wide rate alone — which nothing
// ever set — so a channel's fee_percent (the "service charge paid by the
// buyer" an organizer asks for) reached the Bil24 gateway's carts but never
// a widget checkout: a 5 % channel sold 50 EUR tickets for 50.00. The read
// runs on the pool; callers must take it BEFORE opening the hold transaction.
// A lookup failure keeps the defaults so a transient error cannot silently
// widen or drop the charge on a live cart — it is logged.
func (h *Handler) channelPricingRules(ctx context.Context, orgID, channelID uuid.UUID) hcheckout.PricingRules {
	rules := h.pricingRules
	if h.publicFeedQueries == nil {
		return rules
	}
	ch, err := h.publicFeedQueries.GetSalesChannelByID(ctx, channelID, orgID)
	if err != nil {
		h.logger.Warn("public_feed_checkout: channel fee lookup failed, pricing with the defaults",
			slog.String("channel_id", channelID.String()), slog.String("error", err.Error()))
		return rules
	}
	if bp := ordering.ChargePercentBP(ch.FeePercent); bp > 0 {
		rules.PlatformFeeRate = int64(bp)
	}
	return rules
}
