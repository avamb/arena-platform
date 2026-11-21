// checkout_tracking_consent.go — the buyer's cookie (tracking) consent on a
// purchase (spec 62 §8.1, П-20).
//
// checkout/start takes it as tracking_consent {granted, version, at}; when the
// buyer changes the choice in the site's banner while the order is still in
// the tab, the site sends PATCH /v1/public/checkout/{checkout_token}/consent
// with the same object. The conversion sender reads it from the order at the
// moment it sends (package trackingconsent), so a "no" given before that
// moment stops the conversion; one already sent is not recalled.
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// trackingConsentFromStart normalizes the consent sent with checkout/start.
// An unreadable one is dropped with a log line: a cookie banner's payload
// must never cost a sale.
func (h *Handler) trackingConsentFromStart(in *trackingconsent.Input) *trackingconsent.Consent {
	if in == nil {
		return nil
	}
	c, err := trackingconsent.Normalize(*in, time.Now().UTC())
	if err != nil {
		h.logger.Info("public_feed_checkout: tracking consent ignored", slog.String("reason", err.Error()))
		return nil
	}
	return &c
}

// HandlePatchCheckoutTrackingConsent serves
// PATCH /v1/public/checkout/{checkout_token}/consent.
func (h *Handler) HandlePatchCheckoutTrackingConsent(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	checkoutToken := chi.URLParam(r, "checkout_token")
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_consent", "Send the consent as {granted, version, at}.", r,
		))
		return
	}
	var in trackingconsent.Input
	if err := json.Unmarshal(body, &in); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_consent", "Send the consent as {granted, version, at}.", r,
		))
		return
	}
	now := time.Now().UTC()
	c, err := trackingconsent.Normalize(in, now)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"checkout.invalid_consent", err.Error(), r,
		))
		return
	}

	ctx := r.Context()
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r,
			))
			return
		}
		h.logger.Error("public_checkout_consent: session lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.lookup_failed", "We could not load this checkout. Try again in a minute.", r,
		))
		return
	}
	stored, applied, err := h.storeTrackingConsent(ctx, cs.ID, c)
	if err != nil {
		h.logger.Error("public_checkout_consent: store failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.consent_failed", "We could not save your choice. Try again in a minute.", r,
		))
		return
	}
	resp := map[string]any{
		"checkout_token": checkoutToken,
		"applied":        applied,
		"tracking_consent": map[string]any{
			"granted": stored.Granted,
			"version": stored.Version,
			"at":      formatTimePtr(stored.At),
		},
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// storeTrackingConsent writes c and reads back what the checkout carries,
// on one short transaction. A newer choice already stored (two tabs, a
// request overtaken) is kept and answered.
func (h *Handler) storeTrackingConsent(ctx context.Context, csID uuid.UUID, c trackingconsent.Consent) (trackingconsent.Stored, bool, error) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return trackingconsent.Stored{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	applied, err := trackingconsent.Store(ctx, tx, csID, c)
	if err != nil {
		return trackingconsent.Stored{}, false, err
	}
	cur, err := trackingconsent.ForCheckout(ctx, tx, csID)
	if err != nil {
		return trackingconsent.Stored{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return trackingconsent.Stored{}, false, err
	}
	return cur, applied, nil
}

func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
