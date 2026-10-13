// Package hcontact serves the public "Write to the organizer" form of the
// hosted sales page (HP contact form, owner decision 2026-10-13):
//
//	POST /v1/public/organizer-contact/{org_slug}
//
// The page never publishes the organizer's e-mail. A buyer sends a name, an
// e-mail, a message and an explicit consent, protected by Cloudflare
// Turnstile (the onboarding verifier: production without a secret answers
// 503, never runs unprotected), a per-IP and a per-channel limit counted in
// the database (so every arena-api replica shares them) and a size cap. The
// message is stored and one worker job (contactform.JobType) is queued in the
// same transaction; the job mails it to the channel's public e-mail and,
// when the organization keeps the switch on, posts it to its sales chat.
//
// The answer never carries the organizer's address: 202 {"status":"queued"}.
// An unknown slug, a channel without a public e-mail and a feed token of
// another organization all answer the same 404.
package hcontact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/contactform"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// Limits of one message and of the senders.
const (
	MaxBodyBytes    = 16 << 10
	MaxNameRunes    = 100
	MinMessageRunes = 10
	MaxMessageRunes = 2000
	// PerIPPerHour: messages one IP may send in an hour, to any page.
	PerIPPerHour = 5
	// PerChannelPerHour: messages one sales page may receive in an hour.
	PerChannelPerHour = 30
)

// TurnstileAction is the widget action the contact form renders with
// (tickets-page lib/contact.ts). The verifier refuses a token of any other
// action, so a token solved on the onboarding form, which may share the
// secret, cannot send a message.
const TurnstileAction = "contact"

// Handler serves the route.
type Handler struct {
	pool           *pgxpool.Pool
	verifier       honboarding.Verifier
	ipSalt         string
	trustedProxies int
	logger         *slog.Logger
}

// New builds the handler. verifier nil disables the captcha (tests only).
func New(pool *pgxpool.Pool, verifier honboarding.Verifier, ipSalt string, trustedProxies int, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{pool: pool, verifier: verifier, ipSalt: ipSalt, trustedProxies: trustedProxies, logger: logger}
}

// Request is the body of the route.
type Request struct {
	Name           string `json:"name"`
	Email          string `json:"email"`
	Message        string `json:"message"`
	Consent        bool   `json:"consent"`
	TurnstileToken string `json:"turnstile_token"`
	// FeedToken names the event page's channel; empty = the organization's
	// newest sales-page channel (the promoter landing page).
	FeedToken string `json:"feed_token"`
	Locale    string `json:"locale"`
}

func (h *Handler) bad(w http.ResponseWriter, r *http.Request, code, msg, field string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(code, msg, r, map[string]any{"field": field}))
}

// HandleSend serves POST /v1/public/organizer-contact/{org_slug}.
func (h *Handler) HandleSend(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "org_slug"))
	if slug == "" || utf8.RuneCountInString(slug) > 100 {
		h.notFound(w, r)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil || len(raw) > MaxBodyBytes {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelope(
			"contact.too_large", "Your message is too long. Shorten it to 2000 characters and send it again.", r))
		return
	}
	var req Request
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		h.bad(w, r, "contact.invalid_body", "We could not read the form. Reload the page and try again.", "body")
		return
	}
	name := strings.Join(strings.Fields(req.Name), " ")
	if name == "" || utf8.RuneCountInString(name) > MaxNameRunes {
		h.bad(w, r, "contact.invalid_name", "Enter your name, up to 100 characters.", "name")
		return
	}
	email, ok := sellerinfo.NormalizeEmail(req.Email)
	if !ok {
		h.bad(w, r, "contact.invalid_email", "Enter your e-mail address so the organizer can answer you, for example anna@example.com.", "email")
		return
	}
	msg := strings.TrimSpace(strings.ReplaceAll(req.Message, "\r\n", "\n"))
	if n := utf8.RuneCountInString(msg); n < MinMessageRunes || n > MaxMessageRunes {
		h.bad(w, r, "contact.invalid_message", "Write a message of 10 to 2000 characters.", "message")
		return
	}
	if !req.Consent {
		h.bad(w, r, "contact.consent_required", "Tick the box to agree that we pass your message to the organizer.", "consent")
		return
	}

	ip := httputil.TrustedClientIP(r, h.trustedProxies)
	if !h.checkCaptcha(w, r, req.TurnstileToken, ip) {
		return
	}

	ctx := r.Context()
	q := gen.New(h.pool)
	orgID, pageSlug, err := h.resolveOrg(ctx, q, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.notFound(w, r)
			return
		}
		h.fail(w, r, "resolve", err)
		return
	}
	target, err := q.GetContactFormTarget(ctx, orgID, strings.TrimSpace(req.FeedToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.notFound(w, r)
			return
		}
		h.fail(w, r, "target", err)
		return
	}
	if !target.HasEmail {
		h.notFound(w, r)
		return
	}

	locale := strings.ToLower(strings.TrimSpace(req.Locale))
	if utf8.RuneCountInString(locale) < 2 || utf8.RuneCountInString(locale) > 10 {
		locale = "en"
	}
	ipHash := h.hashIP(ip)

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One lock per channel makes the two counts below exact even when
	// several replicas take messages for the same page at once.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('organizer-contact:' || $1::text, 0))`, target.ChannelID); err != nil {
		h.fail(w, r, "lock", err)
		return
	}
	var perIP, perChannel int
	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM organizer_contact_messages WHERE ip_hash = $1 AND $1 <> '' AND created_at > now() - interval '1 hour'),
		  (SELECT count(*) FROM organizer_contact_messages WHERE channel_id = $2 AND created_at > now() - interval '1 hour')`,
		ipHash, target.ChannelID).Scan(&perIP, &perChannel); err != nil {
		h.fail(w, r, "count", err)
		return
	}
	if perIP >= PerIPPerHour || perChannel >= PerChannelPerHour {
		w.Header().Set("Retry-After", strconv.Itoa(3600))
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"contact.rate_limited", "Too many messages were sent from here. Try again in an hour.", r))
		return
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO organizer_contact_messages (org_id, channel_id, page_slug, sender_name, sender_email, message, locale, ip_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		orgID, target.ChannelID, pageSlug, name, email, msg, locale, ipHash).Scan(&id); err != nil {
		h.fail(w, r, "insert", err)
		return
	}
	if _, err := worker.EnqueueInTx(ctx, tx, contactform.JobType, contactform.Payload{MessageID: id.String()}, 8); err != nil {
		h.fail(w, r, "enqueue", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	// Neither the address nor the message is logged.
	h.logger.Info("contact: message queued", slog.String("org_id", orgID.String()), slog.String("message_id", id.String()))
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

// resolveOrg finds the organization behind a page slug: an organization's own
// slug, else a promoter's (migration 0117). The slug answered is the page's.
func (h *Handler) resolveOrg(ctx context.Context, q *gen.Queries, slug string) (uuid.UUID, string, error) {
	org, err := q.GetHostedPromoterPageOrg(ctx, slug)
	if err == nil {
		if !org.HasHostedChannel {
			return uuid.Nil, "", pgx.ErrNoRows
		}
		return org.OrgID, org.OrgSlug, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", err
	}
	p, err := q.GetHostedPromoterPageByPromoter(ctx, slug)
	if err != nil {
		return uuid.Nil, "", err
	}
	if !p.HasHostedChannel {
		return uuid.Nil, "", pgx.ErrNoRows
	}
	return p.OrgID, p.Slug, nil
}

func (h *Handler) checkCaptcha(w http.ResponseWriter, r *http.Request, token, ip string) bool {
	if h.verifier == nil {
		return true
	}
	passed, err := h.verifier.Verify(r.Context(), token, ip)
	switch {
	case errors.Is(err, honboarding.ErrCaptchaNotConfigured):
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"contact.captcha_not_configured", "The contact form is not available yet. Try again later.", r))
		return false
	case err != nil:
		h.logger.Warn("contact: captcha check failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"contact.captcha_unavailable", "We could not check the anti-robot test. Try again in a minute.", r))
		return false
	case !passed:
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"contact.captcha_failed", "The anti-robot test did not pass. Try it again, then send the message.", r))
		return false
	}
	return true
}

func (h *Handler) hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(h.ipSalt + "|contact|" + ip))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"contact.not_available", "This page has no contact form. Check the address of the page.", r))
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.logger.Error("contact: "+op+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"contact.send_failed", "We could not send your message. Try again in a minute.", r))
}
