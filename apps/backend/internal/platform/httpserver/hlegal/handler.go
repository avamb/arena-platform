// Package hlegal serves the organizer's terms of sale (line P, task P3a):
// the public page GET /v1/public/pages/{org_slug}/terms and the platform
// superadmin's minimal template routes under /v1/admin/legal-documents.
//
// The rendering itself lives in internal/platform/legaldocs; this package
// resolves the page slug the way the hosted page does (an organization
// first, else a promoter), rate-limits like the other public page reads and
// answers JSON, or a self-contained HTML page with format=html.
package hlegal

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/legaldocs"
)

// IPLimiter is the per-client-IP bucket of the public reads (the server's
// public feed limiter satisfies it).
type IPLimiter interface {
	CheckIP(ip string) (allowed bool, retryAfterSeconds int)
}

// Handler serves the terms routes.
type Handler struct {
	pool           *pgxpool.Pool
	queries        *gen.Queries
	audit          audit.Writer
	logger         *slog.Logger
	rl             IPLimiter
	trustedProxies int
}

// New builds the handler. A nil pool makes every route answer 503; a nil
// limiter skips rate limiting (tests).
func New(pool *pgxpool.Pool, auditW audit.Writer, logger *slog.Logger, rl IPLimiter, trustedProxies int) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{pool: pool, audit: auditW, logger: logger, rl: rl, trustedProxies: trustedProxies}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

func (h *Handler) unavailable(w http.ResponseWriter, r *http.Request) bool {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return true
	}
	return false
}

// termsResponse is the JSON answer of the public terms route.
type termsResponse struct {
	DocumentVersion int    `json:"document_version"`
	RenderingID     string `json:"rendering_id"`
	Locale          string `json:"locale"`
	HTML            string `json:"html"`
	SHA256          string `json:"sha256"`
	RefundMode      string `json:"refund_mode"`
	SellerLegalName string `json:"seller_legal_name"`
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"page.not_found", "We cannot find this page. Check the address.", r))
}

// requestLocale is the language asked for: ?locale=, else the first tag of
// Accept-Language. legaldocs.NormalizeLocale folds it to a template language.
func requestLocale(r *http.Request) (string, bool) {
	if l := strings.TrimSpace(r.URL.Query().Get("locale")); l != "" {
		return l, false
	}
	first := strings.Split(r.Header.Get("Accept-Language"), ",")[0]
	if i := strings.Index(first, ";"); i >= 0 {
		first = first[:i]
	}
	return strings.TrimSpace(first), true
}

// HandlePublicTerms serves GET /v1/public/pages/{org_slug}/terms.
//
// Query: locale (else Accept-Language, else English), event_id or
// event_slug (the terms of one published event: its refund policy and
// promoter; event_id wins), feed_token (the channel the
// terms are read for; else the organization's newest hosted-page channel),
// format=html (a self-contained page instead of JSON).
//
// Every miss (unknown slug, no channel, an event that is not published or
// not the promoter's, a feed token of another organization) answers the same
// 404 page.not_found as the hosted page. The rendering is stored once per
// document and data (legal_renderings), so the answer names a stable
// rendering_id and sha256.
func (h *Handler) HandlePublicTerms(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w, r) {
		return
	}
	if h.rl != nil {
		if ok, retry := h.rl.CheckIP(httputil.TrustedClientIP(r, h.trustedProxies)); !ok {
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", fmt.Sprint(retry))
			httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
				"page.rate_limited", "Too many requests. Wait one minute and try again.", r))
			return
		}
	}

	slug := strings.TrimSpace(chi.URLParam(r, "org_slug"))
	if slug == "" {
		notFound(w, r)
		return
	}
	ctx := r.Context()
	owner, err := h.queries.ResolveLegalTermsSlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "resolve", err)
		return
	}

	q := r.URL.Query()
	var eventID *uuid.UUID
	if raw := strings.TrimSpace(q.Get("event_id")); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			notFound(w, r)
			return
		}
		eventID = &id
	}
	locale, fromHeader := requestLocale(r)
	rendering, err := legaldocs.RenderTerms(ctx, h.queries, legaldocs.TermsRequest{
		OrgID:         owner.OrgID,
		FeedToken:     q.Get("feed_token"),
		PublicEventID: eventID,
		EventSlug:     q.Get("event_slug"),
		PromoterID:    owner.PromoterID,
		Locale:        locale,
	})
	switch {
	case errors.Is(err, legaldocs.ErrNotFound):
		notFound(w, r)
		return
	case errors.Is(err, legaldocs.ErrNoDocument):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"terms.not_published", "The terms of sale are not available yet. Try again later.", r))
		return
	case err != nil:
		h.failed(w, r, "render", err)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=30, stale-while-revalidate=15")
	if fromHeader {
		// Add, never Set: the CORS middleware's Vary: Origin must stay.
		w.Header().Add("Vary", "Accept-Language")
	}
	if strings.EqualFold(strings.TrimSpace(q.Get("format")), "html") {
		writeTermsPage(w, rendering)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, termsResponse{
		DocumentVersion: rendering.DocumentVersion,
		RenderingID:     rendering.ID.String(),
		Locale:          rendering.Locale,
		HTML:            rendering.HTML,
		SHA256:          rendering.SHA256,
		RefundMode:      rendering.RefundMode,
		SellerLegalName: rendering.SellerLegalName,
	})
}

func (h *Handler) failed(w http.ResponseWriter, r *http.Request, step string, err error) {
	h.logger.Error("legal terms: "+step+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"terms.render_failed", "We could not load the terms of sale. Try again in a minute.", r))
}

// writeTermsPage answers the rendering as a complete HTML page a buyer can
// open from the checkout link (the tickets page renders the JSON from P3c
// on). No script runs on it: the CSP allows inline styles only.
func writeTermsPage(w http.ResponseWriter, rd legaldocs.Rendering) {
	dir := "ltr"
	if rd.Locale == "he" {
		dir = "rtl"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="%s" dir="%s">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Terms of sale</title>
<style>body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;line-height:1.55;color:#1d1d1f;background:#fff}h2{margin-top:2rem;font-size:1.3rem}h3{font-size:1.1rem}footer{margin-top:3rem;color:#6b6b70;font-size:.8rem;word-break:break-all}</style>
</head>
<body>
%s<footer>Version %d · SHA-256 %s</footer>
</body>
</html>
`, html.EscapeString(rd.Locale), dir, rd.HTML, rd.DocumentVersion, html.EscapeString(rd.SHA256))
}
