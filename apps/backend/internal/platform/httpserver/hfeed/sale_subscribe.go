package hfeed

// sale_subscribe.go — "notify me when sales open" on a "coming soon" session
// (package salesubscribe, migration 0183):
//
//	POST /v1/public/feeds/{feed_token}/sessions/{session_id}/sale-subscriptions
//	GET  /v1/public/sale-subscriptions/confirm?token=...
//
// The POST answers 202 whether the address was new, pending or already
// confirmed, so it never tells anybody which addresses are known. The page
// the buyer subscribed on is kept only when its origin is allowed to call
// this API at all (the return-URL allow-list): the letters link back to it,
// and an arbitrary page would let anybody make arena mail a link to their own
// site. The confirmation link redirects to that page, else the tickets site.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
)

// SaleSubscriptionRequest is the body of the POST.
type SaleSubscriptionRequest struct {
	Email          string `json:"email"`
	Locale         string `json:"locale"`
	Consent        bool   `json:"consent"`
	ConsentVersion string `json:"consent_version"`
	PageURL        string `json:"page_url"`
}

// WithSaleSubscriptions wires the "notify me" service and the salt of the
// client-address hash. Returns the receiver for chaining.
func (h *Handler) WithSaleSubscriptions(svc *salesubscribe.Service, ipSalt string) *Handler {
	h.saleSubscribe = svc
	h.saleSubscribeSalt = ipSalt
	return h
}

func (h *Handler) saleSubscribeIPHash(r *http.Request) string {
	ip := h.clientIP(r)
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(h.saleSubscribeSalt + "|sale-subscribe|" + ip))
	return hex.EncodeToString(sum[:])
}

// SaleSubscriptionError maps a service error to the status and code the
// clients know (the widget and the gateway's SUBSCRIBE_SALE share them).
func SaleSubscriptionError(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, salesubscribe.ErrInvalidEmail):
		return http.StatusBadRequest, "subscription.invalid_email", "Check the e-mail address and send it again."
	case errors.Is(err, salesubscribe.ErrConsentRequired):
		return http.StatusBadRequest, "subscription.consent_required", "Tick the consent box to get the letter."
	case errors.Is(err, salesubscribe.ErrConsentOutdated):
		return http.StatusBadRequest, "subscription.consent_outdated", "The consent text has changed. Reload the page and try again."
	case errors.Is(err, salesubscribe.ErrSalesOpen):
		return http.StatusConflict, "subscription.sales_open", "Sales are open now. Reload the page to buy tickets."
	case errors.Is(err, salesubscribe.ErrNotComingSoon):
		return http.StatusConflict, "subscription.not_coming_soon", "This session is not waiting for sales to open."
	case errors.Is(err, salesubscribe.ErrRateLimited):
		return http.StatusTooManyRequests, "subscription.rate_limited", "Too many attempts. Try again in an hour."
	}
	return http.StatusServiceUnavailable, "subscription.failed", "We could not save the request. Try again in a minute."
}

// HandleCreateSaleSubscription serves the POST.
func (h *Handler) HandleCreateSaleSubscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.saleSubscribe == nil || h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	feedToken := chi.URLParam(r, "feed_token")
	if !h.enforceRateLimit(w, r, "feed.rate_limited", h.rl.CheckFeedToken, feedToken) {
		return
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "session_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"subscription.invalid_session_id", "Send session_id as a UUID.", r,
		))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	var req SaleSubscriptionRequest
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"subscription.invalid_json", "The request body is not valid JSON. Fix it and send it again.", r,
		))
		return
	}
	ctx := r.Context()
	target, err := h.publicFeedQueries.GetPublicCheckoutContext(ctx, feedToken, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"feed.session_not_on_feed", "This session is not on sale here. Check the link.", r,
		))
		return
	}
	if err != nil {
		h.logger.Error("sale_subscribe: context lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"subscription.failed", "We could not save the request. Try again in a minute.", r,
		))
		return
	}
	page := ""
	if h.returnURLPolicy.AllowsOrigin(req.PageURL) {
		page = req.PageURL
	}
	channel := target.SalesChannelID
	err = h.saleSubscribe.Subscribe(ctx, salesubscribe.Input{
		Target: salesubscribe.Target{
			SessionID: target.SessionID, EventID: target.EventID, OrgID: target.OrgID, ChannelID: &channel,
		},
		Email: req.Email, Locale: req.Locale, PageURL: page, Source: salesubscribe.SourceWidget,
		Consent: req.Consent, ConsentVersion: req.ConsentVersion, IPHash: h.saleSubscribeIPHash(r),
	})
	if err != nil {
		status, code, msg := SaleSubscriptionError(err)
		if status >= http.StatusInternalServerError {
			h.logger.Error("sale_subscribe: subscribe failed", slog.String("error", err.Error()))
		}
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "check_email"})
}

// HandleConfirmSaleSubscription serves the link of the confirmation letter:
// it confirms and sends the buyer back with arena_subscription=confirmed, or
// to the tickets site with arena_subscription=invalid.
func (h *Handler) HandleConfirmSaleSubscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fallback := strings.TrimRight(strings.TrimSpace(h.returnURLPolicy.Fallback), "/")
	if h.saleSubscribe == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	c, err := h.saleSubscribe.Confirm(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		if !errors.Is(err, salesubscribe.ErrTokenInvalid) {
			h.logger.Error("sale_subscribe: confirm failed", slog.String("error", err.Error()))
		}
		if fallback == "" {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"subscription.link_invalid", "This link was already used or has expired. Subscribe again on the event page.", r,
			))
			return
		}
		http.Redirect(w, r, withQuery(fallback, "arena_subscription", "invalid"), http.StatusSeeOther)
		return
	}
	target := salesubscribe.CleanPageURL(c.PageURL)
	if target == "" {
		target = fallback
	}
	if target == "" {
		httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
		return
	}
	http.Redirect(w, r, withQuery(target, "arena_subscription", "confirmed"), http.StatusSeeOther)
}

// withQuery sets one query parameter on an absolute URL.
func withQuery(raw, key, value string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}
