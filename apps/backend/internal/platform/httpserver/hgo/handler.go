// Package hgo serves the public short-link redirect GET /go/{code} of the
// organizer sales links (spec 43 section 4.1, task QL-3).
//
// The route is public and writes without a login, so it is narrow by
// construction:
//
//   - a code that is malformed or unknown goes to one fixed fallback page and
//     is never counted, so a scan of random addresses writes nothing;
//   - the per-IP bucket of the public routes runs before any database read; a
//     refused scan is sent to the same fallback page (decision R-QL17: a person
//     who scanned a poster lands on a page, never on a JSON error) uncounted;
//   - at most countSlots scans are counted at once, one statement each; a scan
//     that finds every slot taken is not counted (outcome dropped), so a burst
//     on one code cannot queue on its row lock and drain the database pool;
//   - the 302 is written and flushed BEFORE the scan is counted, and a counting
//     failure is logged and changes nothing for the client;
//   - nothing here logs or stores the client's address, its User-Agent or a
//     cookie (stop condition ST-QL1). The only value derived from them is the
//     in-memory de-duplication hash of salelinks.Dedup; the database receives
//     a day, a two-letter country and a bot flag.
//
// The country is the CF-IPCountry header, trusted whenever TRUSTED_PROXY_COUNT
// is above zero. An origin reachable without Cloudflare therefore lets a client
// choose the country it is counted under; the damage is bounded (at most 676
// rows per link and day) and the ops runbook (QL-14) locks the origin to
// Cloudflare.
//
// Server failures answer 503, never 502: Cloudflare replaces an origin 502
// with its own page.
package hgo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

// DefaultFallbackURL is where a malformed or unknown code goes when the
// configuration names no fallback (SHORT_LINK_FALLBACK_URL, then
// PUBLIC_TICKETS_BASE_URL, then APP_PUBLIC_URL are all empty).
const DefaultFallbackURL = "https://tickets.arenasoldout.com/"

// countTimeout bounds the counting statement. They run on a context
// detached from the request, after the response has been flushed.
const countTimeout = 3 * time.Second

// countSlots is how many scans may be counted at the same time per process.
const countSlots = 4

// Outcomes of one request, the closed label set of arena_short_link_total.
const (
	// OutcomeRedirect: 302 to the link's destination; for GET the scan was
	// added to hits, HEAD counts nothing.
	OutcomeRedirect = "redirect"
	// OutcomeSuspect: 302 to the destination, the scan went to suspect_hits.
	OutcomeSuspect = "suspect"
	// OutcomeDedup: 302 to the destination, the same client was counted for
	// this code today already.
	OutcomeDedup = "dedup"
	// OutcomeFallback: 302 to the fallback page (malformed or unknown code, or
	// no page address could be built). Nothing is counted.
	OutcomeFallback = "fallback"
	// OutcomeRateLimited: the client was over the per-IP limit; 302 to the
	// fallback page, nothing was read or written.
	OutcomeRateLimited = "rate_limited"
	// OutcomeDropped: 302 to the destination, the scan was not counted because
	// every counting slot was taken.
	OutcomeDropped = "dropped"
	// OutcomeError: 503, or a 302 whose scan could not be counted.
	OutcomeError = "error"
)

// archivedStatus is the sales_links.status of an archived link.
const archivedStatus = "archived"

// countryRe is the shape of a usable CF-IPCountry value after upper-casing.
var countryRe = regexp.MustCompile("^[A-Z]{2}$")

// Queries is what the handler reads and writes. *gen.Queries implements it.
type Queries interface {
	salelinks.FactsQuerier
	GetSalesLinkByCode(ctx context.Context, code string) (gen.SalesLinkRow, error)
	CountSalesLinkScan(ctx context.Context, id uuid.UUID, day time.Time, country string, suspect bool) error
}

// RateLimiter is the per-IP bucket of the public routes. The server's public
// feed limiter implements it (PUBLIC_API_IP_RATE_LIMIT).
type RateLimiter interface {
	CheckIP(ip string) (allowed bool, retryAfterSeconds int)
}

// Options are the dependencies of a Handler. Queries is required; every other
// field has a safe default.
type Options struct {
	Queries Queries
	// Limiter nil means no per-IP limit (tests only).
	Limiter RateLimiter
	// Dedup nil means a fresh salelinks.NewDedup(0, 0). One Dedup must live
	// for the whole process: a Handler built per request forgets every scan.
	Dedup *salelinks.Dedup
	// Base is PUBLIC_TICKETS_BASE_URL, the base of every page address.
	Base string
	// Fallback is config.ShortLinkFallback(); empty, or not an absolute
	// http(s) address without credentials, means DefaultFallbackURL.
	Fallback string
	// TrustedProxies is TRUSTED_PROXY_COUNT. The CF-IPCountry header is read
	// only when it is above zero: without a proxy in front a client could set
	// any country.
	TrustedProxies int
	// Now nil means time.Now.
	Now    func() time.Time
	Logger *slog.Logger
	// Observe receives one outcome per request (metrics). Nil means none.
	Observe func(outcome string)
}

// Handler serves GET and HEAD /go/{code}.
type Handler struct {
	q        Queries
	limiter  RateLimiter
	dedup    *salelinks.Dedup
	base     string
	fallback string
	trusted  int
	now      func() time.Time
	logger   *slog.Logger
	observe  func(string)
	slots    chan struct{}
}

// New builds a Handler from o.
func New(o Options) *Handler {
	h := &Handler{
		q:        o.Queries,
		limiter:  o.Limiter,
		dedup:    o.Dedup,
		base:     o.Base,
		fallback: strings.TrimSpace(o.Fallback),
		trusted:  o.TrustedProxies,
		now:      o.Now,
		logger:   o.Logger,
		observe:  o.Observe,
		slots:    make(chan struct{}, countSlots),
	}
	if h.dedup == nil {
		h.dedup = salelinks.NewDedup(0, 0)
	}
	if !safeDestination(h.fallback) {
		h.fallback = DefaultFallbackURL
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	if h.observe == nil {
		h.observe = func(string) {}
	}
	return h
}

// ServeHTTP answers one scan. HEAD is answered exactly like GET but never
// counted.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	code, ok := salelinks.NormalizeCode(chi.URLParam(r, "code"))
	if !ok {
		h.redirectFallback(w)
		return
	}

	ip := httputil.TrustedClientIP(r, 0)
	if h.limiter != nil {
		if allowed, _ := h.limiter.CheckIP(ip); !allowed {
			// A person who scanned a poster lands on a page, never on an
			// error body (R-QL17), so no 429 and no Retry-After.
			writeRedirect(w, h.fallback)
			h.observe(OutcomeRateLimited)
			return
		}
	}
	if h.q == nil {
		h.unavailable(w, r)
		return
	}

	ctx := r.Context()
	row, err := h.q.GetSalesLinkByCode(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		h.redirectFallback(w)
		return
	}
	if err != nil {
		h.logger.Error("short_link: lookup failed", slog.String("code", code), slog.String("error", err.Error()))
		h.unavailable(w, r)
		return
	}

	utm := h.decodeUTM(row)
	link := salelinks.Link{
		OrgID:    row.OrgID,
		Code:     row.Code,
		Archived: row.Status == archivedStatus,
		UTM:      utm,
		Target: salelinks.Target{
			Kind:       row.TargetKind,
			EventID:    row.EventID,
			SessionID:  row.SessionID,
			PromoterID: row.PromoterID,
		},
	}
	if row.URL != nil {
		link.Target.URL = *row.URL
	}
	dest, _, err := salelinks.Resolve(ctx, h.q, h.base, link)
	if errors.Is(err, salelinks.ErrOrgNotFound) {
		// No organization, no page to fall to: the platform's own page.
		h.redirectFallback(w)
		return
	}
	if err != nil {
		h.logger.Error("short_link: resolve failed",
			slog.String("code", code), slog.String("sales_link_id", row.ID.String()), slog.String("error", err.Error()))
		h.unavailable(w, r)
		return
	}
	if !safeDestination(dest) {
		h.redirectFallback(w)
		return
	}

	writeRedirect(w, salelinks.BuildDestination(dest, utm, row.Code))
	if r.Method != http.MethodGet {
		h.observe(OutcomeRedirect)
		return
	}
	// The client has its answer before the scan is counted; a slow or failing
	// database only delays this goroutine, never the redirect.
	_ = http.NewResponseController(w).Flush()
	h.observe(h.count(r, row.ID, code, ip))
}

// count records one GET scan and returns its outcome. It never writes to the
// response.
func (h *Handler) count(r *http.Request, id uuid.UUID, code, ip string) string {
	ua := r.UserAgent()
	suspect := pagestats.IsSuspectAgent(ua)
	now := h.now()
	// Never wait for a slot: a scan that finds them all taken is not counted
	// and not remembered by the de-duplication either.
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return OutcomeDropped
	}
	// One IPv6 subscriber (a /64) is one client, like for the rate limit.
	if !suspect && h.dedup.Seen(code, httputil.ClientKey(ip), ua, now) {
		return OutcomeDedup
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), countTimeout)
	defer cancel()
	utc := now.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	// The log line names the code and the link only: never the address or the agent.
	if err := h.q.CountSalesLinkScan(ctx, id, day, h.country(r), suspect); err != nil {
		h.logger.Warn("short_link: count scan failed",
			slog.String("code", code), slog.String("sales_link_id", id.String()), slog.String("error", err.Error()))
		return OutcomeError
	}
	if suspect {
		return OutcomeSuspect
	}
	return OutcomeRedirect
}

// country is the two-letter CF-IPCountry value, or "" when no trusted proxy
// stands in front (the client could set the header itself), when the header is
// missing or malformed, or when Cloudflare says it does not know (XX).
func (h *Handler) country(r *http.Request) string {
	if h.trusted <= 0 {
		return ""
	}
	c := strings.ToUpper(strings.TrimSpace(r.Header.Get("CF-IPCountry")))
	if !countryRe.MatchString(c) || c == "XX" {
		return ""
	}
	return c
}

// decodeUTM reads the link's utm labels. A document that is not an object of
// strings is ignored (the redirect still carries ref).
func (h *Handler) decodeUTM(row gen.SalesLinkRow) map[string]string {
	if len(row.UTM) == 0 {
		return nil
	}
	var utm map[string]string
	if err := json.Unmarshal(row.UTM, &utm); err != nil {
		h.logger.Warn("short_link: utm is not an object of strings",
			slog.String("code", row.Code), slog.String("sales_link_id", row.ID.String()))
		return nil
	}
	return utm
}

// safeDestination accepts only an absolute http(s) address with a host and no
// user information. A url link is checked when it is created (QL-4); this is
// the second line, so a stored value of another scheme, or one that disguises
// its host behind credentials (https://trusted.example@evil.example/), can
// never become a Location.
func safeDestination(dest string) bool {
	if dest == "" {
		return false
	}
	u, err := url.Parse(dest)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

func (h *Handler) redirectFallback(w http.ResponseWriter) {
	writeRedirect(w, h.fallback)
	h.observe(OutcomeFallback)
}

func (h *Handler) unavailable(w http.ResponseWriter, r *http.Request) {
	h.writeError(w, r, http.StatusServiceUnavailable, "short_link.unavailable",
		"This link cannot be opened right now. Try again in a minute.")
	h.observe(OutcomeError)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

// writeRedirect writes a complete 302 with an empty body. Content-Length 0
// makes the response whole the moment the headers are flushed, so the client
// follows it while the scan is still being counted.
func writeRedirect(w http.ResponseWriter, location string) {
	hd := w.Header()
	hd.Set("Location", location)
	hd.Set("Cache-Control", "no-store")
	hd.Set("Referrer-Policy", "no-referrer-when-downgrade")
	hd.Set("Content-Length", "0")
	w.WriteHeader(http.StatusFound)
}
