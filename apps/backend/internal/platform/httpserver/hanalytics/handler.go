// Package hanalytics serves the two receivers of the product analytics (spec
// 08_architecture/50_product_analytics_ru.md section 4.2):
//
//	POST /v1/t     public, arenasoldout.com (surface site)
//	POST /v1/me/t  signed in, the event center (surface event_center)
//
// The body contract is shared with the scripts that send it (stages A2 and A3)
// and changes only together with them. The receivers tell a client nothing
// about why an event was not stored: the answer is always 204 with no body,
// except 400 for a body that is not readable JSON.
//
//   - the surface comes from the route, never from the body;
//   - for /v1/me/t the user comes from the token, never from the body, and
//     org_id is kept only when the caller belongs to that organization (or is
//     a platform superadmin), otherwise it is stored as NULL;
//   - the server's clock is authoritative: a client time is used only to keep
//     the order of the events inside one batch, and is ignored when it is
//     more than a day away from the server's;
//   - the client's address and User-Agent are read to apply the per-address
//     limit and the suspect flag (pagestats rules) and are stored nowhere.
package hanalytics

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// Limits of the receivers (spec 50 section 4.2).
const (
	MaxBodyBytes    = 16 * 1024
	MaxEvents       = 50
	SiteIPPerMinute = 120
	UserPerMinute   = 300
	maxClientSkew   = 24 * time.Hour
	// MaxBatchSpread is how far apart in time the events of one batch may be.
	MaxBatchSpread   = time.Hour
	codeLookupBudget = 500 * time.Millisecond
	pruneEvery       = 2000
)

var (
	aidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,40}$`)
	pvRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{4,40}$`)
	cidRe = regexp.MustCompile(`^[a-z0-9]{6,20}$`)
)

// CodeLookup says whether a contact code from an e-mail link exists.
type CodeLookup interface {
	ContactCodeExists(ctx context.Context, code string) (bool, error)
}

// OrgAccess says whether the signed-in caller may attach orgID to its events
// (a member of it, or a platform superadmin).
type OrgAccess func(ctx context.Context, orgID uuid.UUID) (bool, error)

// Options are the dependencies of a Handler. Tracker is required.
type Options struct {
	Tracker analytics.Tracker
	// Codes nil means a cid is never confirmed (no email_click is written).
	Codes CodeLookup
	// OrgAccess nil means org_id is always stored as NULL.
	OrgAccess OrgAccess
	Logger    *slog.Logger
	// Now nil means time.Now.
	Now func() time.Time
	// SiteLimit and UserLimit override the per-minute limits (tests).
	SiteLimit, UserLimit int
}

// Handler serves the two receivers.
type Handler struct {
	tracker   analytics.Tracker
	codes     CodeLookup
	orgAccess OrgAccess
	log       *slog.Logger
	now       func() time.Time
	siteRL    *ratelimit.SlidingWindow
	userRL    *ratelimit.SlidingWindow
	calls     atomic.Uint64

	clickMu   sync.Mutex
	clickSeen map[string]clickSeen
}

// clickSeen remembers, per (cid, pv), that an email_click was written and
// whether it already carried the visitor's aid.
type clickSeen struct {
	withAID bool
	at      time.Time
}

const (
	clickSeenTTL = 24 * time.Hour
	clickSeenMax = 50000
)

// New builds a Handler.
func New(o Options) *Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.SiteLimit <= 0 {
		o.SiteLimit = SiteIPPerMinute
	}
	if o.UserLimit <= 0 {
		o.UserLimit = UserPerMinute
	}
	return &Handler{
		tracker:   o.Tracker,
		codes:     o.Codes,
		orgAccess: o.OrgAccess,
		log:       o.Logger,
		now:       o.Now,
		siteRL:    ratelimit.New(ratelimit.Config{MaxAttempts: o.SiteLimit, Window: time.Minute}),
		userRL:    ratelimit.New(ratelimit.Config{MaxAttempts: o.UserLimit, Window: time.Minute}),
		clickSeen: map[string]clickSeen{},
	}
}

type batchEvent struct {
	Name  string         `json:"name"`
	At    string         `json:"at"`
	Props map[string]any `json:"props"`
}

type batch struct {
	AID    string       `json:"aid"`
	CID    string       `json:"cid"`
	PV     string       `json:"pv"`
	OrgID  string       `json:"org_id"`
	Events []batchEvent `json:"events"`
}

// HandleSite serves POST /v1/t.
func (h *Handler) HandleSite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// The per-address limit comes first: it needs neither a body nor a
	// database. trustedRealIP has already rewritten RemoteAddr when a trusted
	// proxy stands in front, so the hop count is 0 here.
	ip := httputil.TrustedClientIP(r, 0)
	h.maybePrune()
	if !h.siteRL.Take(httputil.ClientKey(ip)) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b, ok := h.decode(w, r)
	if !ok {
		return
	}
	idKind, idValue := "", ""
	switch {
	case aidRe.MatchString(b.AID):
		idKind, idValue = analytics.IDKindAID, b.AID
	case pvRe.MatchString(b.PV):
		idKind, idValue = analytics.IDKindAnon, b.PV
	}
	if idKind != "" {
		suspect := pagestats.IsSuspectAgent(r.UserAgent())
		h.trackBatch(r.Context(), b, analytics.SurfaceSite, idKind, idValue, nil, suspect)
		h.trackEmailClick(r.Context(), b, idKind, idValue, suspect)
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleMe serves POST /v1/me/t. The route is mounted behind the JWT
// middleware; the user is read from the actor, never from the body.
func (h *Handler) HandleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := auth.ActorFromContext(r.Context())
	userID, err := uuid.Parse(actor.ID)
	if !ok || err != nil {
		// Not a platform user (an anonymous or foreign principal): nothing to
		// attribute events to.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.maybePrune()
	if !h.userRL.Take(userID.String()) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b, ok := h.decode(w, r)
	if !ok {
		return
	}
	org := h.allowedOrg(r.Context(), b.OrgID)
	suspect := pagestats.IsSuspectAgent(r.UserAgent())
	h.trackBatch(r.Context(), b, analytics.SurfaceEventCenter, analytics.IDKindUser, userID.String(), org, suspect)
	w.WriteHeader(http.StatusNoContent)
}

// decode reads the body. It writes 400 and returns false for a body that is
// too large or not one JSON object.
func (h *Handler) decode(w http.ResponseWriter, r *http.Request) (batch, bool) {
	var b batch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err := dec.Decode(&b); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"track.invalid_body", "Send a JSON object with an events list.", r))
		return batch{}, false
	}
	if len(b.Events) > MaxEvents {
		b.Events = b.Events[:MaxEvents]
	}
	return b, true
}

// allowedOrg returns the organization to store, or nil.
func (h *Handler) allowedOrg(ctx context.Context, raw string) *uuid.UUID {
	if raw == "" || h.orgAccess == nil {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return nil
	}
	allowed, err := h.orgAccess(ctx, id)
	if err != nil {
		h.log.Warn("analytics: org check failed", slog.String("error", err.Error()))
		return nil
	}
	if !allowed {
		return nil
	}
	return &id
}

// trackBatch hands every event of the batch to the tracker with a time that
// keeps the batch's order.
func (h *Handler) trackBatch(ctx context.Context, b batch, surface, idKind, idValue string, org *uuid.UUID, suspect bool) {
	now := h.now()
	times := batchTimes(b.Events, now)
	for i, e := range b.Events {
		h.tracker.Track(ctx, analytics.Event{
			At: times[i], Surface: surface, Name: e.Name, IDKind: idKind, IDValue: idValue,
			OrgID: org, Props: e.Props, Suspect: suspect,
		})
	}
}

// trackEmailClick writes the server-side email_click when the batch carries a
// contact code that exists. The visit's own identifier (aid) is kept on the
// event when there is one: it is the pair the rollup glues. Only the first
// page of the batch gives the path and the campaign.
func (h *Handler) trackEmailClick(ctx context.Context, b batch, idKind, idValue string, suspect bool) {
	if h.codes == nil || !cidRe.MatchString(b.CID) {
		return
	}
	withAID := idKind == analytics.IDKindAID
	// Already written for this (cid, pv)? Only a verified code is ever
	// remembered, so this needs no lookup.
	if h.clickDone(b.CID, b.PV, withAID) {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, codeLookupBudget)
	defer cancel()
	ok, err := h.codes.ContactCodeExists(lctx, b.CID)
	if err != nil {
		// Not remembered: a database hiccup must not block the click for a day.
		h.log.Warn("analytics: contact code lookup failed", slog.String("error", err.Error()))
		return
	}
	if !ok {
		return
	}
	// One email_click per (cid, pv): the site resends the cid together with
	// the aid right after the visitor accepts consent on the same page. That
	// second send is the one that carries the pair, so it is written once more
	// (an aid-less click never blocks it, an aid-bearing one is never repeated).
	if !h.rememberClick(b.CID, b.PV, withAID) {
		return
	}
	props := map[string]any{}
	for _, e := range b.Events {
		if e.Name == "page_view" {
			if v, has := e.Props["path"]; has {
				props["path"] = v
			}
			if v, has := e.Props["utm_campaign"]; has {
				props["utm_campaign"] = v
			}
			break
		}
	}
	if idKind == analytics.IDKindAID {
		props["aid"] = idValue
	}
	h.tracker.Track(ctx, analytics.Event{
		At: h.now(), Surface: analytics.SurfaceSite, Name: "email_click",
		IDKind: analytics.IDKindCID, IDValue: b.CID, Props: props, Suspect: suspect,
		ServerOrigin: true,
	})
}

// batchTimes gives every event the server time minus how much older it is
// than the newest client time of the batch. A client time more than a day
// from the server's, unparsable, or more than MaxBatchSpread older than the
// batch's newest event is discarded and the event gets the server time. The
// result is always inside [now - 24 h, now], so it falls in a partition that
// exists whatever the client's clock says.
func batchTimes(events []batchEvent, now time.Time) []time.Time {
	client := make([]time.Time, len(events))
	var newest time.Time
	for i, e := range events {
		t, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			continue
		}
		if d := t.Sub(now); d > maxClientSkew || d < -maxClientSkew {
			continue
		}
		client[i] = t
		if t.After(newest) {
			newest = t
		}
	}
	out := make([]time.Time, len(events))
	for i := range events {
		out[i] = now
		if client[i].IsZero() {
			continue
		}
		offset := newest.Sub(client[i])
		if offset > MaxBatchSpread {
			continue
		}
		out[i] = clampTime(now.Add(-offset), now.Add(-maxClientSkew), now)
	}
	return out
}

func clampTime(t, lo, hi time.Time) time.Time {
	if t.Before(lo) {
		return lo
	}
	if t.After(hi) {
		return hi
	}
	return t
}

// clickDone reports whether an email_click that covers this send was already
// written: the same (cid, pv) seen within the TTL, with an aid when this send
// has one.
func (h *Handler) clickDone(cid, pv string, withAID bool) bool {
	h.clickMu.Lock()
	defer h.clickMu.Unlock()
	prev, ok := h.clickSeen[cid+"|"+pv]
	return ok && h.now().Sub(prev.at) < clickSeenTTL && (prev.withAID || !withAID)
}

// rememberClick records a click about to be written for a VERIFIED code and
// reports whether to write it (false: a concurrent request just did). The
// memory is per process and bounded: when it is full of live entries a new
// pair is written without being remembered, the map is never flushed.
func (h *Handler) rememberClick(cid, pv string, withAID bool) bool {
	key := cid + "|" + pv
	now := h.now()
	h.clickMu.Lock()
	defer h.clickMu.Unlock()
	if prev, ok := h.clickSeen[key]; ok && now.Sub(prev.at) < clickSeenTTL && (prev.withAID || !withAID) {
		return false
	}
	if len(h.clickSeen) >= clickSeenMax {
		for k, v := range h.clickSeen {
			if now.Sub(v.at) >= clickSeenTTL {
				delete(h.clickSeen, k)
			}
		}
		if len(h.clickSeen) >= clickSeenMax {
			return true
		}
	}
	h.clickSeen[key] = clickSeen{withAID: withAID, at: now}
	return true
}

func (h *Handler) maybePrune() {
	if h.calls.Add(1)%pruneEvery == 0 {
		h.siteRL.Prune()
		h.userRL.Prune()
	}
}
