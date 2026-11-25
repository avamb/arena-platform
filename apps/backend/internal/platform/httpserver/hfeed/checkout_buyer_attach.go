// checkout_buyer_attach.go — POST /v1/public/checkout/{checkout_token}/buyer
// (spec 65 early order, risk 4): the buyer of a prepared order.
//
// checkout/start with "prepare": true (checkout_prepare.go) creates the hold,
// the order and the embedded Stripe session when the order sheet opens,
// without the buyer. At "Pay" the widget sends the buyer here, then hands the
// e-mail to Stripe (updateEmail) and confirms. This route does not call
// Stripe.
//
// The checks are those of checkout/start: a valid e-mail address, the name
// and phone the channel requires, the sale-terms consent the channel asks
// for. Then, in ONE transaction (checkout row, then order row locked, the
// order a cancel locks): the order's buyer columns, the customer resolution
// behind a SAVEPOINT, the reservation's customer, the buyer's previous open
// order on the session superseded, the consent recorded.
//
// Idempotent for the same e-mail (200, nothing written); another e-mail is
// 409 checkout.buyer_already_set. An order that can no longer be paid is 409
// checkout.not_payable.
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailaddr"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// ErrCodeBuyerAlreadySet refuses an attach that names another e-mail than
// the one the order already has.
const ErrCodeBuyerAlreadySet = "checkout.buyer_already_set"

// checkoutBuyerMaxBody is how much of an attach body is read.
const checkoutBuyerMaxBody = 16 * 1024

// PublicCheckoutBuyerRequest is the body of
// POST /v1/public/checkout/{checkout_token}/buyer.
type PublicCheckoutBuyerRequest struct {
	Buyer *PublicBuyerInfo `json:"buyer"`
	// Consent is the tick on the sale terms, as in checkout/start.
	Consent *PublicCheckoutConsent `json:"consent,omitempty"`
	// Locale is the buyer's language, as in checkout/start; an unknown
	// value is dropped.
	Locale string `json:"locale,omitempty"`
}

// HandlePublicCheckoutBuyer serves POST /v1/public/checkout/{checkout_token}/buyer.
func (h *Handler) HandlePublicCheckoutBuyer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.checkoutQueries == nil || h.publicFeedQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	checkoutToken := chi.URLParam(r, "checkout_token")
	// The same limiter pair as checkout/start: one key per credential in the
	// path (here the checkout token) plus the per-IP limit.
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, checkoutBuyerMaxBody))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_body", "Send the buyer as JSON.", r,
		))
		return
	}
	var req PublicCheckoutBuyerRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_json", "The request body is not valid JSON. Fix it and send it again.", r,
		))
		return
	}
	email := ""
	if req.Buyer != nil {
		email = strings.TrimSpace(req.Buyer.Email)
	}
	if email == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"checkout.missing_holder_email", "Enter the e-mail address for the tickets (buyer.email).", r,
			map[string]any{"field": "buyer.email"},
		))
		return
	}
	if !emailaddr.Valid(email) {
		writeInvalidEmail(w, r)
		return
	}
	buyerName := ptrOrNil(strings.TrimSpace(derefOr(req.Buyer.Name)))
	buyerPhone := ptrOrNil(strings.TrimSpace(derefOr(req.Buyer.Phone)))
	buyerLocale := normalizeBuyerLocale(req.Locale)

	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r,
			))
			return
		}
		h.logger.Error("public_checkout_buyer: session lookup failed", slog.String("error", err.Error()))
		h.writeAttachUnavailable(w, r)
		return
	}
	pc, err := h.checkoutQueries.GetCheckoutPaymentContext(ctx, cs.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.writeNotPayable(w, r, checkoutStatusToPublic(cs.State))
		return
	}
	if err != nil {
		h.logger.Error("public_checkout_buyer: checkout lookup failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		h.writeAttachUnavailable(w, r)
		return
	}
	if now := time.Now().UTC(); !fallbackPayable(pc, now) || !paymentWindowOpen(pc.HoldExpiresAt, h.paymentGrace, now) {
		// The hold alone is not enough: the Stripe session dies at hold expiry
		// minus the grace (payment_expires_at), so a buyer attached after it
		// could never pay.
		h.writeNotPayable(w, r, h.currentPublicStatus(ctx, cs))
		return
	}
	if pc.BuyerEmail != nil && strings.TrimSpace(*pc.BuyerEmail) != "" {
		h.answerExistingBuyer(w, r, checkoutToken, pc, *pc.BuyerEmail, email)
		return
	}

	// ── The channel's rules, read on the pool before the transaction ───────
	// Fails closed: without an active feed token that publishes the event
	// nobody can say which name, phone and consent the channel requires, and
	// an attach with every flag false would skip them. Such an order (its
	// feed was switched off or unpublished after prepare) is not payable.
	fc, err := h.checkoutQueries.GetCheckoutFeedContext(ctx, cs.ID)
	switch attachLookupOutcome(err, fc.FeedToken) {
	case lookupMissing:
		h.logger.Warn("public_checkout_buyer: no active feed publishes the order's event; refusing the attach",
			slog.String("checkout_session_id", cs.ID.String()))
		h.writeNotPayable(w, r, publicStatusPending)
		return
	case lookupFailed:
		h.logger.Error("public_checkout_buyer: feed lookup failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		h.writeAttachUnavailable(w, r)
		return
	}
	flags, flagsErr := h.publicFeedQueries.GetFeedTokenBuyerFlags(ctx, fc.FeedToken)
	switch attachLookupOutcome(flagsErr, fc.FeedToken) {
	case lookupMissing:
		h.logger.Warn("public_checkout_buyer: the feed token has no buyer flags; refusing the attach",
			slog.String("checkout_session_id", cs.ID.String()))
		h.writeNotPayable(w, r, publicStatusPending)
		return
	case lookupFailed:
		// Fails closed, like checkout/start: the flags decide whether the
		// buyer must accept the sale terms.
		h.logger.Error("public_checkout_buyer: get buyer flags failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", flagsErr.Error()))
		h.writeAttachUnavailable(w, r)
		return
	}
	collectName := flags.CollectName
	collectPhone := flags.CollectPhone
	consentRequired, consentSeller := consentFromFlags(flags)
	if collectName && buyerName == nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"checkout.buyer_name_required",
			"This sales channel needs the buyer's name (buyer.name). Fill it in and try again.", r,
			map[string]any{"field": "buyer.name"},
		))
		return
	}
	if collectPhone && buyerPhone == nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"checkout.buyer_phone_required",
			"This sales channel needs the buyer's phone number (buyer.phone). Fill it in and try again.", r,
			map[string]any{"field": "buyer.phone"},
		))
		return
	}
	if consentRefused(consentRequired, req.Consent) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"checkout.consent_required",
			"Please accept the sale terms of the organizer and the platform policy, then try again.", r,
			map[string]any{"field": "consent.accepted"},
		))
		return
	}
	var consent *pendingConsent
	if consentRecorded(consentRequired, req.Consent) {
		consent = newPendingConsent(r, pc.OrgID, req.Consent.Locale, buyerLocale,
			h.showWithdrawalNotice(ctx, pc.OrgID), time.Now(),
			h.consentBlockFor(ctx, consentRequired, consentSeller, fc.EventID, fc.FeedToken))
		if err := h.attachTermsRendering(ctx, consent, pc.OrgID, fc.EventID, fc.FeedToken); err != nil {
			h.logger.Error("public_checkout_buyer: terms rendering failed",
				slog.String("org_id", pc.OrgID.String()), slog.String("error", err.Error()))
			h.writeAttachUnavailable(w, r)
			return
		}
	}

	buyer := publicOrderBuyer{Email: email, Name: buyerName, Phone: buyerPhone, Locale: buyerLocale}
	outcome, existing, err := h.attachBuyerTx(r, cs, buyer, consent)
	if err != nil {
		h.logger.Error("public_checkout_buyer: attach failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		h.writeAttachUnavailable(w, r)
		return
	}
	switch outcome {
	case attachNotPayable:
		h.writeNotPayable(w, r, h.currentPublicStatus(ctx, cs))
	case attachAlreadySet:
		h.answerExistingBuyer(w, r, checkoutToken, pc, existing, email)
	default:
		h.logger.Info("public_checkout_buyer: the buyer was attached to a prepared order",
			slog.String("checkout_session_id", cs.ID.String()))
		h.writeAttachAnswer(w, checkoutToken, pc)
	}
}

type attachOutcome int

const (
	attachDone attachOutcome = iota
	attachNotPayable
	attachAlreadySet
)

// attachBuyerTx writes the buyer in one transaction. It locks the checkout
// row, then the order row — the order of the buyer cancel — and re-checks
// both, so a cancel, an expiry or a payment that won the race is answered,
// not overwritten. existing is the order's e-mail when it already had one.
func (h *Handler) attachBuyerTx(r *http.Request, cs gen.CheckoutSessionRow, buyer publicOrderBuyer, consent *pendingConsent) (attachOutcome, string, error) {
	// Detached and bounded: a buyer whose connection drops mid-write still
	// gets the attach committed or not at all, never cut off between the
	// customer and the order writes.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txq := gen.New(tx)

	// The checkout row is locked first (the order a cancel and the payment
	// webhook take); the hold's expiry is read with it so the payment window
	// is checked again under the lock.
	var state string
	var holdExpiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT cs.state, r.expires_at
		FROM   checkout_sessions cs
		JOIN   reservations r ON r.id = cs.reservation_id
		WHERE  cs.id = $1
		FOR UPDATE OF cs`, cs.ID).Scan(&state, &holdExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachNotPayable, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	if !paymentWindowOpen(holdExpiresAt, h.paymentGrace, time.Now().UTC()) {
		return attachNotPayable, "", nil
	}
	o, err := txq.GetOrderByCheckoutSession(ctx, cs.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachNotPayable, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	ord, err := txq.LockOrderForUpdate(ctx, o.ID, o.OrgID)
	if err != nil {
		return 0, "", err
	}
	if state != "pricing_confirmed" || ord.Status != ordering.StatusPendingPayment {
		return attachNotPayable, "", nil
	}
	if ord.BuyerEmail != nil && strings.TrimSpace(*ord.BuyerEmail) != "" {
		return attachAlreadySet, *ord.BuyerEmail, nil
	}

	checkCtx := gen.PublicCheckoutContextRow{
		SessionID:      ord.SessionID,
		EventID:        ord.EventID,
		OrgID:          ord.OrgID,
		SalesChannelID: ord.ChannelID,
	}
	customerID, err := h.resolvePublicCustomer(ctx, tx, txq, checkCtx, cs, buyer)
	if err != nil {
		return 0, "", err
	}
	// The buyer is written without the customer link: two attaches with the
	// same e-mail on the same session can both pass the supersede step, which
	// reads committed rows only, and the second customer_id write then hits
	// orders_one_pending_per_customer_session_uq (23505). The link sits in its
	// own SAVEPOINT; when it fails, the order keeps customer_id NULL and the
	// buyer can still pay.
	changed, err := txq.AttachOrderBuyer(ctx, ord.ID, buyer.Email, buyer.Name, buyer.Phone, nil)
	if err != nil {
		return 0, "", err
	}
	if !changed {
		return attachNotPayable, "", nil
	}
	if customerID != nil {
		h.bestEffort(ctx, tx, "order customer link", cs.ID, func(sp pgx.Tx) error {
			_, err := sp.Exec(ctx, `UPDATE orders SET customer_id = $2, updated_at = now() WHERE id = $1`, ord.ID, *customerID)
			return err
		})
	}
	if buyer.Locale != "" {
		if err := txq.SetCheckoutBuyerLocale(ctx, cs.ID, buyer.Locale); err != nil {
			return 0, "", err
		}
	}
	if consent != nil {
		if _, err := txq.InsertCheckoutConsent(ctx, consent.params(ord.OrgID, cs.ID)); err != nil {
			return 0, "", err
		}
	}
	// The moment the order got its buyer: the unpaid-order reminder counts
	// from it, not from the prepare (cartreminder.SweepOrgs).
	if _, err := txq.InsertOrderEvent(ctx, ord.ID, ordering.EventBuyerAttached, ordering.ActorSystem, json.RawMessage(`{}`)); err != nil {
		return 0, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", err
	}
	return attachDone, "", nil
}

// answerExistingBuyer answers an order that already has a buyer: the same
// address is a repeat (200, nothing written), another one is refused.
func (h *Handler) answerExistingBuyer(w http.ResponseWriter, r *http.Request, token string, pc gen.CheckoutPaymentContextRow, existing, email string) {
	if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(email)) {
		h.writeAttachAnswer(w, token, pc)
		return
	}
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
		ErrCodeBuyerAlreadySet,
		"This order already has another e-mail address. Start a new order to use this one.", r,
	))
}

func (h *Handler) writeAttachAnswer(w http.ResponseWriter, token string, pc gen.CheckoutPaymentContextRow) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_token":     token,
		"status":             publicStatusPending,
		"expires_at":         pc.HoldExpiresAt.UTC().Format(time.RFC3339),
		"payment_expires_at": pc.HoldExpiresAt.Add(-h.paymentGrace).UTC().Format(time.RFC3339),
	})
}

// paymentWindowOpen reports whether a buyer can still pay: the provider
// session dies at the hold's expiry minus the grace (payment_expires_at),
// which is earlier than the hold itself.
func paymentWindowOpen(holdExpiresAt time.Time, grace time.Duration, now time.Time) bool {
	return now.Before(holdExpiresAt.Add(-grace))
}

type lookupOutcome int

const (
	lookupFound lookupOutcome = iota
	lookupMissing
	lookupFailed
)

// attachLookupOutcome classifies a lookup of the channel's rules: no row (or
// no feed token to read the flags through) is "missing", which the attach
// refuses; any other error is "failed".
func attachLookupOutcome(err error, feedToken string) lookupOutcome {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return lookupMissing
	case err != nil:
		return lookupFailed
	case strings.TrimSpace(feedToken) == "":
		return lookupMissing
	}
	return lookupFound
}

func (h *Handler) writeAttachUnavailable(w http.ResponseWriter, r *http.Request) {
	// 503, never 502, on this Cloudflare-fronted public route.
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"checkout.unavailable", "We could not save the buyer. Try again in a minute.", r,
	))
}
