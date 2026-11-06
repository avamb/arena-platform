package hsite

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

// Limits of the public subscription (П-10).
const (
	subscribePerIPPerHour    = 10
	subscribePerEmailPerHour = 3
	subscribeMaxBody         = 8 << 10
)

// SubscribeHandler serves POST /v1/public/feeds/{feed_token}/marketing-subscriptions:
// a subscription to the organization's mailings without a purchase, with
// double opt-in. It stores a pending marketing_subscriptions row (one per
// organization and address), journals the grant in marketing_consents and
// queues the confirmation letter in the same transaction; the link in the
// letter is the existing /v1/public/marketing/confirm page (a button, so a mail
// scanner's GET never confirms) and every letter carries the withdraw link
// (/v1/public/marketing/withdraw).
type SubscribeHandler struct {
	Pool           *pgxpool.Pool
	Guard          Guard
	TrustedProxies int
	Logger         *slog.Logger
	Now            func() time.Time
}

// SubscribeRequest is the body of the route. Unknown fields are refused.
type SubscribeRequest struct {
	Email string `json:"email"`
	// Name is optional; Phone is optional and kept with the subscription.
	Name   string `json:"name"`
	Phone  string `json:"phone"`
	Locale string `json:"locale"`
	// Consent must be true: the person ticked the box next to the text.
	Consent bool `json:"consent"`
	// TextVersion names the consent text the site showed (3-64 characters of
	// letters, digits and . _ : -). It is stored with the grant and the
	// confirmation as the evidence of what was agreed to.
	TextVersion string `json:"text_version"`
	// PageURL is the site page the form was on; the confirmation sends the
	// person back there. It must lie on one of the channel's site origins,
	// else it is dropped.
	PageURL        string `json:"page_url"`
	TurnstileToken string `json:"turnstile_token"`
}

func (h *SubscribeHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func subBad(w http.ResponseWriter, r *http.Request, code, msg, field string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(code, msg, r, map[string]any{"field": field}))
}

// HandleSubscribe serves the route. Every well-formed request answers 202
// {"status":"check_email"}: whether the address is new, pending, confirmed or
// withdrawn is never revealed, and the same address asking again within an
// hour is quietly absorbed.
func (h *SubscribeHandler) HandleSubscribe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.Pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, subscribeMaxBody+1))
	if err != nil || len(raw) > subscribeMaxBody {
		subBad(w, r, "subscription.invalid_body", "We could not read the form. Reload the page and try again.", "body")
		return
	}
	var req SubscribeRequest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		subBad(w, r, "subscription.invalid_body", "We could not read the form. Reload the page and try again.", "body")
		return
	}
	email, err := marketing.NormalizeEmail(req.Email)
	if err != nil {
		subBad(w, r, "subscription.invalid_email", "Enter a valid e-mail address, for example anna@example.com.", "email")
		return
	}
	name := strings.Join(strings.Fields(req.Name), " ")
	if utf8.RuneCountInString(name) > MaxNameRunes {
		subBad(w, r, "subscription.invalid_name", "Enter your name, up to 100 characters.", "name")
		return
	}
	phone := strings.Join(strings.Fields(req.Phone), " ")
	if phone != "" && !ValidPhone(phone) {
		subBad(w, r, "subscription.invalid_phone", "Enter a phone number with digits, for example +972 50 123 4567.", "phone")
		return
	}
	if !req.Consent {
		subBad(w, r, "subscription.consent_required", "Tick the box to agree to receive the mailings.", "consent")
		return
	}
	if !marketing.ValidSiteTextVersion(req.TextVersion) {
		subBad(w, r, "subscription.invalid_text_version", "Send the version of the consent text the form showed, 3 to 64 characters.", "text_version")
		return
	}

	ctx := r.Context()
	ch, err := ChannelByFeedToken(ctx, h.Pool, chi.URLParam(r, "feed_token"))
	if err != nil {
		if errors.Is(err, ErrNoChannel) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"subscription.not_available", "This site has no subscription form. Check the address of the page.", r))
			return
		}
		h.fail(w, r, "channel", err)
		return
	}
	ip := httputil.TrustedClientIP(r, h.TrustedProxies)
	if !WriteCaptchaOutcome(w, r, "subscription", h.Guard.Captcha(ctx, ch, req.TurnstileToken, ip, ActionClub)) {
		return
	}

	page := ""
	if ch.AllowsPage(req.PageURL) {
		page = req.PageURL
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}

	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One lock per organization and address makes the count below exact.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('site-subscribe:' || $1::text || ':' || $2, 0))`,
		ch.OrgID, email); err != nil {
		h.fail(w, r, "lock", err)
		return
	}
	var byIP, byEmail int
	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM marketing_consents WHERE action = 'granted' AND source = 'site'
		       AND $1 <> '' AND client_ip = $1 AND created_at > $3),
		  (SELECT count(*) FROM marketing_consents c JOIN marketing_subscriptions s ON s.id = c.subscription_id
		       WHERE c.action = 'granted' AND s.org_id = $2 AND s.email_normalized = $4 AND c.created_at > $3)`,
		ip, ch.OrgID, now.Add(-time.Hour), email).Scan(&byIP, &byEmail); err != nil {
		h.fail(w, r, "count", err)
		return
	}
	if byIP >= subscribePerIPPerHour {
		w.Header().Set("Retry-After", strconv.Itoa(3600))
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"subscription.rate_limited", "Too many subscriptions from this address. Try again in an hour.", r))
		return
	}
	if byEmail >= subscribePerEmailPerHour {
		// The same address asked again and again: the letter is already on its
		// way. Answered like a success so the reply reveals nothing.
		httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "check_email"})
		return
	}
	if _, err := marketing.RecordGrantTx(ctx, tx, marketing.Grant{
		OrgID: ch.OrgID, Email: req.Email, Name: name, Phone: phone, Locale: req.Locale,
		TextVersion: req.TextVersion, Source: marketing.SourceSite, PageURL: page,
		ClientIP: ip, UserAgent: r.UserAgent(), Now: now,
	}); err != nil {
		h.fail(w, r, "grant", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	// Neither the address nor the name is logged.
	h.logger().Info("site subscription: grant recorded", slog.String("org_id", ch.OrgID.String()))
	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "check_email"})
}

func (h *SubscribeHandler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.logger().Error("site subscription: "+op+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"subscription.failed", "We could not save the subscription. Try again in a minute.", r))
}
