package hsite

// track.go serves POST /v1/public/feeds/{feed_token}/track, the server-side
// tracking of a static site (spec 63 section 19, task L-10). The site's nginx
// passes each browser beacon here with two headers it adds itself:
// X-Site-Key (the channel's secret, compared with settings.site.track_key_hash)
// and X-Site-Client-IP (the visitor's address). The route:
//
//   - always counts an allowed event in the anonymous funnel (a click on a
//     partner box office is the funnel step outbound_click and one row of
//     outbound_clicks_daily); no visitor data is stored;
//   - queues the event for Meta and GA4 (conversions.EnqueueEvent) only when the
//     request carried a valid site key AND the visitor did not refuse tracking.
//     Without the key the client address is the connection's, not a header's,
//     and nothing goes to an ad platform, so a forged header buys nothing;
//   - counts a bot nowhere;
//   - answers 202 {"status":"accepted"} without detail for everything that is
//     not counted (bad event, page of another site, unknown event of the
//     channel). Only an unreadable JSON body is 400, an oversized one 413, a
//     flood 429, an unknown feed token 404.
//
// The model is "tracking is on until the visitor refuses": the site sends
// tracking_declined=true after a refusal. The IP address and the User-Agent
// are read to decide and are never stored in the funnel, never logged and never
// put in a metric label.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesfunnel"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

// Events of a site (the "event" field of the body).
const (
	TrackPageView       = "page_view"
	TrackViewEvent      = "view_event"
	TrackOutboundClick  = "outbound_ticket_click"
	TrackCheckoutOpen   = "checkout_open"
	TrackLead           = "lead"
	trackMaxBodyBytes   = 8 << 10
	trackMaxToken       = 256
	trackMaxPageURL     = 2048
	trackMaxUA          = 512
	trackMaxID          = 64
	trackMaxSeller      = 64
	trackMaxCity        = 100
	trackMaxKeyHeader   = 256
	trackLimitKeyPrefix = "site-track/"
)

var trackEventIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// TrackLimiter is the part of the public rate limiter the route uses: the
// bucket of one key (here the feed token) and the bucket of the client IP, both
// counted on every call, before any database read.
type TrackLimiter interface {
	CheckPageTrack(key, ip string) (allowed bool, retryAfterSeconds int)
}

// EventSink queues an event for the ad platforms (conversions.PGStore).
type EventSink interface {
	EnqueueEvent(ctx context.Context, orgID uuid.UUID, ev conversions.SiteEvent) error
}

// TrackHandler serves the route.
type TrackHandler struct {
	Pool           *pgxpool.Pool
	Limiter        TrackLimiter
	Deduper        *salesfunnel.Deduper
	Events         EventSink
	TrustedProxies int
	Logger         *slog.Logger
}

// TrackRequest is the body of the route. Unknown fields are refused.
type TrackRequest struct {
	Event            string `json:"event"`
	EventID          string `json:"event_id"`
	PageURL          string `json:"page_url"`
	Referrer         string `json:"referrer"` // accepted and ignored
	UTMSource        string `json:"utm_source"`
	UTMCampaign      string `json:"utm_campaign"`
	FBCLID           string `json:"fbclid"`
	GCLID            string `json:"gclid"`
	FBP              string `json:"fbp"`
	FBC              string `json:"fbc"`
	GAClientID       string `json:"ga_client_id"`
	GASessionID      string `json:"ga_session_id"`
	VID              string `json:"vid"`
	TrackingDeclined bool   `json:"tracking_declined"`
	Data             struct {
		EventID      string `json:"event_id"`
		SessionID    string `json:"session_id"`
		Seller       string `json:"seller"`
		City         string `json:"city"`
		Value        any    `json:"value"`
		Currency     string `json:"currency"`
		CheckoutType string `json:"checkout_type"`
		Email        string `json:"email"`
		Phone        string `json:"phone"`
	} `json:"data"`
}

// ParseTrackBody reads the body. An error means unreadable JSON (400).
func ParseTrackBody(raw []byte) (TrackRequest, error) {
	var req TrackRequest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return req, errors.New("trailing data")
	}
	return req, nil
}

// TrackEvent is a cleaned request: every field is bounded, and the event is
// one the route knows.
type TrackEvent struct {
	Kind        string
	EventID     string // the browser's id of this event
	PageURL     string
	UTMSource   string
	UTMCampaign string
	FBCLID      string
	GCLID       string
	FBP         string
	FBC         string
	GAClientID  string
	GASessionID string
	VisitorID   string
	Declined    bool

	PlatformEventID *uuid.UUID // data.event_id
	SessionID       *uuid.UUID // data.session_id
	Seller          string
	City            string
	Value           float64
	Currency        string
	CheckoutType    string
	Email           string
	Phone           string
}

// NeedsPlatformEvent reports whether the kind is about one event of the
// platform (the funnel step and the content id need it).
func (e TrackEvent) NeedsPlatformEvent() bool {
	return e.Kind == TrackViewEvent || e.Kind == TrackOutboundClick || e.Kind == TrackCheckoutOpen
}

// CleanTrack bounds and validates a request. ok is false for an event that is
// not counted at all (unknown kind, bad id, a page outside the channel's
// origins, an event kind without a platform event id).
func CleanTrack(req TrackRequest, ch Channel) (TrackEvent, bool) {
	var e TrackEvent
	switch req.Event {
	case TrackPageView, TrackViewEvent, TrackOutboundClick, TrackCheckoutOpen, TrackLead:
		e.Kind = req.Event
	default:
		return e, false
	}
	if !trackEventIDRe.MatchString(req.EventID) {
		return e, false
	}
	e.EventID = req.EventID
	page := strings.TrimSpace(req.PageURL)
	if utf8.RuneCountInString(page) > trackMaxPageURL || !ch.AllowsPage(page) {
		return e, false
	}
	e.PageURL = cleanTrackText(page, trackMaxPageURL)
	if e.PageURL == "" {
		return e, false
	}
	e.UTMSource = pagestats.CleanUTM(req.UTMSource)
	e.UTMCampaign = pagestats.CleanUTM(req.UTMCampaign)
	e.FBCLID = cleanTrackText(req.FBCLID, trackMaxToken)
	e.GCLID = cleanTrackText(req.GCLID, trackMaxToken)
	e.FBP = cleanTrackText(req.FBP, trackMaxToken)
	e.FBC = cleanTrackText(req.FBC, trackMaxToken)
	e.GAClientID = cleanTrackText(req.GAClientID, trackMaxToken)
	e.GASessionID = cleanTrackText(req.GASessionID, trackMaxID)
	e.VisitorID = cleanTrackText(req.VID, trackMaxID)
	e.Declined = req.TrackingDeclined

	d := req.Data
	if id, err := uuid.Parse(strings.TrimSpace(d.EventID)); err == nil && d.EventID != "" {
		e.PlatformEventID = &id
	}
	if e.NeedsPlatformEvent() && e.PlatformEventID == nil {
		return e, false
	}
	if id, err := uuid.Parse(strings.TrimSpace(d.SessionID)); err == nil && d.SessionID != "" {
		e.SessionID = &id
	}
	e.Seller = cleanTrackText(d.Seller, trackMaxSeller)
	e.City = cleanTrackText(d.City, trackMaxCity)
	if v, ok := d.Value.(float64); ok && v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) && v < 1e9 {
		cur := strings.ToUpper(strings.TrimSpace(d.Currency))
		if len(cur) == 3 && isLetters(cur) {
			e.Value, e.Currency = v, cur
		}
	}
	switch e.Kind {
	case TrackOutboundClick:
		e.CheckoutType = "external"
	case TrackCheckoutOpen:
		e.CheckoutType = "own"
	}
	if e.Kind == TrackLead {
		if email, ok := sellerinfo.NormalizeEmail(d.Email); ok {
			e.Email = email
		}
		if phone := strings.Join(strings.Fields(d.Phone), " "); ValidPhone(phone) {
			e.Phone = phone
		}
	}
	return e, true
}

func isLetters(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// cleanTrackText trims s and drops it whole when it is too long or holds a
// control character (the same rule as the buyer context of a checkout).
func cleanTrackText(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > max {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

// SiteKeyValid compares the X-Site-Key header with the channel's stored
// SHA-256 hex hash in constant time. An empty hash or key never matches.
func SiteKeyValid(storedHash, header string) bool {
	storedHash = strings.ToLower(strings.TrimSpace(storedHash))
	header = strings.TrimSpace(header)
	if storedHash == "" || header == "" || len(header) > trackMaxKeyHeader {
		return false
	}
	sum := sha256.Sum256([]byte(header))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

// clientIPFromHeader validates X-Site-Client-IP; "" when it is not an address.
func clientIPFromHeader(v string) string {
	ip := net.ParseIP(strings.TrimSpace(v))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// ToSiteEvent builds the ad-platform event. ip and ua are the trusted ones
// (the route calls it only for a valid site key).
func (e TrackEvent) ToSiteEvent(ip, ua string) (conversions.SiteEvent, bool) {
	kind := ""
	switch e.Kind {
	case TrackPageView:
		kind = conversions.KindPageView
	case TrackViewEvent:
		kind = conversions.KindViewContent
	case TrackOutboundClick, TrackCheckoutOpen:
		kind = conversions.KindInitiateCheckout
	case TrackLead:
		kind = conversions.KindLead
	default:
		return conversions.SiteEvent{}, false
	}
	ev := conversions.SiteEvent{
		Kind:    kind,
		EventID: e.EventID,
		Context: conversions.BuyerContext{
			FBCLID: e.FBCLID, GCLID: e.GCLID, FBP: e.FBP, FBC: e.FBC, GAClientID: e.GAClientID,
			PageURL:   e.PageURL,
			ClientIP:  cleanTrackText(ip, 64),
			UserAgent: cleanTrackText(ua, trackMaxUA),
		},
		VisitorID:    e.VisitorID,
		GASessionID:  e.GASessionID,
		Email:        e.Email,
		Phone:        e.Phone,
		Seller:       e.Seller,
		City:         e.City,
		Value:        e.Value,
		Currency:     e.Currency,
		CheckoutType: e.CheckoutType,
	}
	if e.PlatformEventID != nil {
		ev.ContentID = e.PlatformEventID.String()
	}
	return ev, true
}

func (h *TrackHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func accepted(w http.ResponseWriter) {
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}

// HandleTrack serves the route.
func (h *TrackHandler) HandleTrack(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.Pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return
	}
	token := strings.TrimSpace(chi.URLParam(r, "feed_token"))
	if len(token) > 200 {
		token = token[:200]
	}

	// The limits run before any database read. The address is the connection's
	// unless the request names itself a site (a key header and an address
	// header): the key is checked only after the channel is read, so this
	// choice is only a bucket key, and the feed token's own bucket bounds a
	// forger anyway.
	trusted := httputil.TrustedClientIP(r, h.TrustedProxies)
	limitIP := trusted
	if r.Header.Get("X-Site-Key") != "" {
		if hip := clientIPFromHeader(r.Header.Get("X-Site-Client-IP")); hip != "" {
			limitIP = hip
		}
	}
	if h.Limiter != nil {
		if ok, retry := h.Limiter.CheckPageTrack(trackLimitKeyPrefix+token, limitIP); !ok {
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
				"track.rate_limited", "Too many requests. Wait one minute and try again.", r))
			return
		}
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, trackMaxBodyBytes+1))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("track.invalid_body", "Send a JSON object.", r))
		return
	}
	if len(raw) > trackMaxBodyBytes {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelope("track.too_large", "The event is too large.", r))
		return
	}
	req, err := ParseTrackBody(raw)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("track.invalid_body", "Send a JSON object.", r))
		return
	}

	ctx := r.Context()
	ch, err := ChannelByFeedToken(ctx, h.Pool, token)
	if err != nil {
		if errors.Is(err, ErrNoChannel) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"track.not_available", "This site has no tracking. Check the address of the page.", r))
			return
		}
		h.logger().Error("site track: channel lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"track.unavailable", "We could not take the event. Try again in a minute.", r))
		return
	}
	ev, ok := CleanTrack(req, ch)
	if !ok {
		accepted(w)
		return
	}

	keyValid := SiteKeyValid(ch.TrackKeyHash, r.Header.Get("X-Site-Key"))
	if !keyValid && limitIP != trusted && h.Limiter != nil {
		// A forged key and address header only chose the first bucket: the
		// real connection address pays for the request now.
		if ok, retry := h.Limiter.CheckPageTrack(trackLimitKeyPrefix+token, trusted); !ok {
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
				"track.rate_limited", "Too many requests. Wait one minute and try again.", r))
			return
		}
	}
	ip, ua := trusted, r.UserAgent()
	if keyValid {
		if hip := clientIPFromHeader(r.Header.Get("X-Site-Client-IP")); hip != "" {
			ip = hip
		}
		if v := cleanTrackText(r.Header.Get("X-Site-User-Agent"), trackMaxUA); v != "" {
			ua = v
		}
	}
	if pagestats.IsSuspectAgent(ua) {
		accepted(w)
		return
	}

	var target gen.PageTrackTarget
	if ev.NeedsPlatformEvent() {
		q := gen.New(h.Pool)
		t, err := q.ResolveFunnelTargetByFeedToken(ctx, token, *ev.PlatformEventID)
		if errors.Is(err, pgx.ErrNoRows) {
			accepted(w)
			return
		}
		if err != nil {
			h.logger().Error("site track: event lookup failed", slog.String("error", err.Error()))
			accepted(w)
			return
		}
		target = t
		if ev.SessionID != nil {
			if belongs, err := q.FunnelSessionBelongsToEvent(ctx, target.EventID, *ev.SessionID); err != nil || !belongs {
				ev.SessionID = nil
			}
		}
		h.count(ctx, ev, target, ip, ua)
	}

	if keyValid && !ev.Declined && h.Events != nil {
		if se, ok := ev.ToSiteEvent(ip, ua); ok {
			if err := h.Events.EnqueueEvent(ctx, ch.OrgID, se); err != nil {
				h.logger().Error("site track: enqueue failed", slog.String("org_id", ch.OrgID.String()),
					slog.String("kind", se.Kind), slog.String("error", err.Error()))
			}
		}
	}
	accepted(w)
}

// count writes the anonymous funnel hit and, for a click on a box office, the
// per-seller day counter. A failure is logged and never reaches the visitor.
func (h *TrackHandler) count(ctx context.Context, ev TrackEvent, target gen.PageTrackTarget, ip, ua string) {
	step := ""
	switch ev.Kind {
	case TrackViewEvent:
		step = salesfunnel.StepView
	case TrackCheckoutOpen:
		step = salesfunnel.StepSessionOpen
	case TrackOutboundClick:
		step = salesfunnel.StepOutboundClick
	default:
		return
	}
	session := ""
	if ev.SessionID != nil {
		session = ev.SessionID.String()
	}
	parts := []string{target.EventID.String(), session, step, salesfunnel.SurfaceExternalSite, ev.UTMSource, ev.UTMCampaign, ev.Seller}
	if h.Deduper != nil && h.Deduper.SeenToday(ip, ua, parts...) {
		return
	}
	tz := ""
	if target.Timezone != nil {
		tz = *target.Timezone
	}
	day := pagestats.Day(time.Now(), tz)
	q := gen.New(h.Pool)
	err := q.RecordSalesFunnelHit(ctx, gen.RecordSalesFunnelHitParams{
		EventID: target.EventID, OrgID: target.OrgID, SessionID: ev.SessionID, Day: day,
		Step: step, Surface: salesfunnel.SurfaceExternalSite,
		UTMSource: ev.UTMSource, UTMCampaign: ev.UTMCampaign,
		MaxUTMPairs: pagestats.MaxUTMPairsPerDay,
	})
	if err == nil && ev.Kind == TrackOutboundClick {
		err = q.RecordOutboundClick(ctx, target.OrgID, target.EventID, day, ev.Seller)
	}
	if err != nil {
		if h.Deduper != nil {
			h.Deduper.Forget(ip, ua, parts...)
		}
		h.logger().Error("site track: count failed", slog.String("event_id", target.EventID.String()),
			slog.String("error", err.Error()))
	}
}
