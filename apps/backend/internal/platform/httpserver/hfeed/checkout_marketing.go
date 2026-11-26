// checkout_marketing.go — "subscribe the order's buyer to the organizer's
// mailings" from the thank-you page of a static site, and the read side of it
// (marketing_offer on the order status).
//
// POST /v1/public/checkout/{checkout_token}/marketing
//
// The checkout token is the credential and the address is the order's own
// buyer e-mail, never one in the body. The write is the site subscription's
// (hsite/subscribe.go): the same tables, marketing.RecordGrantTx with source
// site, the same advisory lock and the same hourly limits (10 per client IP
// answers 429, 3 per address are absorbed as a quiet success). No Turnstile:
// the token already proves a paid order. The organizer must offer mailings
// (marketing.LoadOffer Enabled, else 409 marketing.not_offered; a deleted
// organization is 404) and the body names marketing.ThankYouTextVersion only.
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsite"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

const (
	checkoutMarketingMaxBody      = 8 << 10
	checkoutMarketingPageURLMax   = 2048
	checkoutMarketingPerIPPerHour = 10
	checkoutMarketingPerMailPerHr = 3
	checkoutMarketingPending      = "pending"
	checkoutMarketingAlreadyDone  = "already_confirmed"
)

// checkoutMarketingRequest is the body of the route. Unknown fields are refused.
type checkoutMarketingRequest struct {
	// TextVersion names the consent text the page showed, the evidence of what
	// was agreed to. Only marketing.ThankYouTextVersion is accepted.
	TextVersion string `json:"text_version"`
	// PageURL is the page the buyer was on (https, at most 2048 characters, no
	// user info); the confirmation sends the buyer back to it when it lies on
	// one of the sales channel's site origins, else to the hosted tickets page.
	PageURL string `json:"page_url"`
}

// cleanCheckoutPageURL reports whether raw is an acceptable page_url: empty,
// or a plain https link of at most 2048 characters without user info.
func cleanCheckoutPageURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	if len(raw) > checkoutMarketingPageURLMax {
		return "", false
	}
	p, err := url.Parse(raw)
	if err != nil || p.Scheme != "https" || p.Host == "" || p.User != nil {
		return "", false
	}
	return raw, true
}

func checkoutMarketingBad(w http.ResponseWriter, r *http.Request, code, msg, field string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(code, msg, r, map[string]any{"field": field}))
}

// HandlePublicCheckoutMarketing serves POST /v1/public/checkout/{checkout_token}/marketing.
func (h *Handler) HandlePublicCheckoutMarketing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.checkoutQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return
	}
	checkoutToken := chi.URLParam(r, "checkout_token")
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, checkoutMarketingMaxBody+1))
	if err != nil || len(raw) > checkoutMarketingMaxBody {
		checkoutMarketingBad(w, r, "checkout.invalid_body", "We could not read the request. Reload the page and try again.", "body")
		return
	}
	var req checkoutMarketingRequest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		checkoutMarketingBad(w, r, "checkout.invalid_body", "We could not read the request. Reload the page and try again.", "body")
		return
	}
	if req.TextVersion != marketing.ThankYouTextVersion {
		checkoutMarketingBad(w, r, "checkout.invalid_text_version", "Send the consent text version the order status offers (marketing_offer.text_version).", "text_version")
		return
	}
	page, ok := cleanCheckoutPageURL(req.PageURL)
	if !ok {
		checkoutMarketingBad(w, r, "checkout.invalid_page_url", "Send page_url as an https link of at most 2048 characters without a user name.", "page_url")
		return
	}

	ctx := r.Context()
	fail := func(op string, err error) {
		h.logger.Error("public_checkout_marketing: "+op+" failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"subscription.failed", "We could not save the subscription. Try again in a minute.", r))
	}
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r))
		return
	}
	if err != nil {
		fail("session lookup", err)
		return
	}
	offerState, _, err := loadCheckoutOffer(ctx, h.checkoutQueries.DB(), cs.OrgID)
	if err != nil {
		fail("offer lookup", err)
		return
	}
	switch offerState {
	case checkoutOfferNoOrg:
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r))
		return
	case checkoutOfferNone:
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"marketing.not_offered", "This organizer does not offer mailings.", r))
		return
	}
	ord, err := h.checkoutQueries.GetOrderByCheckoutSession(ctx, cs.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fail("order lookup", err)
		return
	}
	if err != nil || (ord.Status != "paid" && ord.Status != "partially_refunded") {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"checkout.not_paid", "The order is not paid yet. Subscribe after the payment.", r))
		return
	}
	buyerEmail := ""
	if ord.BuyerEmail != nil {
		buyerEmail = *ord.BuyerEmail
	}
	email, err := marketing.NormalizeEmail(buyerEmail)
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"subscription.invalid_email", "The e-mail of this order cannot receive mailings.", r))
		return
	}
	// The page the confirmation links back to: the page_url when it lies on the
	// channel's own site origins, else the hosted tickets page.
	returnBase := h.checkoutReturnPage(ctx, h.checkoutQueries.DB(), cs.ChannelID, page)
	locale := ""
	if cs.BuyerLocale != nil {
		locale = *cs.BuyerLocale
	}
	ip := httputil.TrustedClientIP(r, h.trustedProxies)
	now := time.Now().UTC()

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		fail("begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The lock hsite's subscription takes: one per organization and address.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('site-subscribe:' || $1::text || ':' || $2, 0))`,
		cs.OrgID, email); err != nil {
		fail("lock", err)
		return
	}
	status, err := subscriptionStatus(ctx, tx, cs.OrgID, email)
	if err != nil {
		fail("status", err)
		return
	}
	if status == marketing.StatusConfirmed {
		httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": checkoutMarketingAlreadyDone})
		return
	}
	var already bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM marketing_consents c
		               JOIN marketing_subscriptions s ON s.id = c.subscription_id
		               WHERE c.checkout_session_id = $1 AND c.action = 'granted' AND s.status = 'pending')`,
		cs.ID).Scan(&already); err != nil {
		fail("pending lookup", err)
		return
	}
	if already {
		httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": checkoutMarketingPending})
		return
	}
	var byIP, byEmail int
	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM marketing_consents WHERE action = 'granted' AND source = 'site'
		       AND $1 <> '' AND client_ip = $1 AND created_at > $3),
		  (SELECT count(*) FROM marketing_consents c JOIN marketing_subscriptions s ON s.id = c.subscription_id
		       WHERE c.action = 'granted' AND s.org_id = $2 AND s.email_normalized = $4 AND c.created_at > $3)`,
		ip, cs.OrgID, now.Add(-time.Hour), email).Scan(&byIP, &byEmail); err != nil {
		fail("count", err)
		return
	}
	if byIP >= checkoutMarketingPerIPPerHour {
		w.Header().Set("Retry-After", strconv.Itoa(3600))
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"subscription.rate_limited", "Too many subscriptions from this address. Try again in an hour.", r))
		return
	}
	if byEmail >= checkoutMarketingPerMailPerHr {
		httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": checkoutMarketingPending})
		return
	}
	name := ""
	if ord.BuyerName != nil {
		name = *ord.BuyerName
	}
	csID := cs.ID
	if _, err := marketing.RecordGrantTx(ctx, tx, marketing.Grant{
		OrgID: cs.OrgID, Email: buyerEmail, Name: name, Locale: locale,
		TextVersion: req.TextVersion, Source: marketing.SourceSite, CheckoutSessionID: &csID,
		PageURL: returnBase, ClientIP: ip, UserAgent: r.UserAgent(), Now: now,
	}); err != nil {
		fail("grant", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail("commit", err)
		return
	}
	// Neither the address nor the name is logged.
	h.logger.Info("checkout marketing: grant recorded", slog.String("org_id", cs.OrgID.String()))
	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": checkoutMarketingPending})
}

// subscriptionStatus is the status of the organization's subscription of an
// address ("" when there is none).
func subscriptionStatus(ctx context.Context, db marketing.DB, orgID uuid.UUID, emailNormalized string) (string, error) {
	var status string
	err := db.QueryRow(ctx, `
		SELECT status FROM marketing_subscriptions WHERE org_id = $1 AND email_normalized = $2`,
		orgID, emailNormalized).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return status, err
}

// checkoutOfferState is what the organizer offers at the thank-you page.
type checkoutOfferState int

const (
	checkoutOfferNone    checkoutOfferState = iota // not offered (the tick is off)
	checkoutOfferOn                                // offered
	checkoutOfferNoOrg                             // the organization is deleted or missing
	checkoutOfferUnknown                           // unreadable
)

// loadCheckoutOffer reads whether the organization offers mailings (the same
// marketing.LoadOffer rule as the checkout tick) and the name the consent text
// shows. A deleted organization is checkoutOfferNoOrg.
func loadCheckoutOffer(ctx context.Context, db marketing.DB, orgID uuid.UUID) (checkoutOfferState, string, error) {
	var name string
	err := db.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(btrim(legal_name), ''), name) FROM organizations
		WHERE id = $1 AND deleted_at IS NULL`, orgID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return checkoutOfferNoOrg, "", nil
	}
	if err != nil {
		return checkoutOfferUnknown, "", err
	}
	offer, err := marketing.LoadOffer(ctx, db, orgID)
	if err != nil {
		return checkoutOfferUnknown, "", err
	}
	if !offer.Enabled {
		return checkoutOfferNone, "", nil
	}
	return checkoutOfferOn, name, nil
}

// checkoutMarketingOffer is the marketing_offer of the order status: nil when
// the organizer does not offer mailings, the organization is deleted, the
// buyer e-mail already has a pending or confirmed subscription, or anything
// cannot be read (best effort: no offer then).
func (h *Handler) checkoutMarketingOffer(ctx context.Context, orgID uuid.UUID, buyerEmail *string) *checkoutMarketingOffer {
	if h.checkoutQueries == nil || buyerEmail == nil {
		return nil
	}
	email, err := marketing.NormalizeEmail(*buyerEmail)
	if err != nil {
		return nil
	}
	db := h.checkoutQueries.DB()
	state, name, err := loadCheckoutOffer(ctx, db, orgID)
	if err != nil {
		h.logger.Warn("public_checkout_status: marketing offer lookup failed", slog.String("error", err.Error()))
		return nil
	}
	if state != checkoutOfferOn {
		return nil
	}
	status, err := subscriptionStatus(ctx, db, orgID, email)
	if err != nil {
		h.logger.Warn("public_checkout_status: subscription lookup failed", slog.String("error", err.Error()))
		return nil
	}
	if status == marketing.StatusPending || status == marketing.StatusConfirmed {
		return nil
	}
	return &checkoutMarketingOffer{OrgName: name, TextVersion: marketing.ThankYouTextVersion}
}

// checkoutReturnPage is the address the confirmation letter sends the buyer
// back to: the page_url when it lies on one of the CHANNEL's site origins
// (settings.site.origins, exactly as hsite's subscription form decides), with
// its query and fragment dropped; otherwise the hosted tickets page. The
// platform-wide return-URL allow-list is never used here.
func (h *Handler) checkoutReturnPage(ctx context.Context, db marketing.DB, channelID uuid.UUID, page string) string {
	fallback := strings.TrimRight(strings.TrimSpace(h.returnURLPolicy.Fallback), "/")
	if page == "" {
		return fallback
	}
	var raw []byte
	err := db.QueryRow(ctx, `
		SELECT COALESCE(settings #> '{site,origins}', '[]'::jsonb) FROM sales_channels
		WHERE id = $1 AND deleted_at IS NULL`, channelID).Scan(&raw)
	if err != nil {
		return fallback
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return fallback
	}
	ch := hsite.Channel{}
	for _, o := range list {
		if n := hsite.OriginOf(o); n != "" {
			ch.Origins = append(ch.Origins, n)
		}
	}
	if !ch.AllowsPage(page) {
		return fallback
	}
	u, err := url.Parse(page)
	if err != nil {
		return fallback
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}
