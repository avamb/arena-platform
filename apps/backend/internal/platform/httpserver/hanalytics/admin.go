package hanalytics

// admin.go — the platform side of the CRM link (spec 50 section 9, stage A6):
//
//	POST /v1/admin/analytics/contact-codes   superadmin.read + X-Admin-Reason, audited
//	GET  /v1/admin/analytics/profiles        superadmin.read
//
// n8n asks for a `cid` code per CRM contact before a mailing goes out, and
// pulls the profiles that changed since its cursor to write them into the
// contact's card. Both routes are platform-wide and superadmin-only; the
// e-mail of a profile is resolved at query time and never stored in the
// analytics schema.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// AuditContactCodesIssue is the audit action of POST .../contact-codes.
const AuditContactCodesIssue = "v1.admin.analytics.contact_codes.issue"

// maxAdminBody is far above 500 ids of 64 characters.
const maxAdminBody = 128 * 1024

// AdminOptions are the dependencies of an AdminHandler.
type AdminOptions struct {
	Pool  *pgxpool.Pool
	Audit audit.Writer
	// TimelineBaseURL is the origin of the admin console (APP_PUBLIC_URL);
	// empty leaves timeline_url out.
	TimelineBaseURL string
	Weights         analytics.ScoreWeights
	Logger          *slog.Logger
	// Now nil means time.Now.
	Now func() time.Time
}

// AdminHandler serves the two superadmin routes.
type AdminHandler struct {
	pool     *pgxpool.Pool
	audit    audit.Writer
	timeline string
	weights  analytics.ScoreWeights
	log      *slog.Logger
	now      func() time.Time
}

// NewAdmin builds the handler. A nil pool makes every route answer 503.
func NewAdmin(o AdminOptions) *AdminHandler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &AdminHandler{
		pool: o.Pool, audit: o.Audit, log: o.Logger, now: o.Now, weights: o.Weights,
		timeline: strings.TrimRight(strings.TrimSpace(o.TimelineBaseURL), "/"),
	}
}

func (h *AdminHandler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.ErrorContext(r.Context(), "admin analytics: "+what, "error", err, "request_id", logging.RequestID(r.Context()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"analytics.admin_failed", "the operation failed, please try again", r))
}

func (h *AdminHandler) unavailable(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"dependency.database_unavailable", "database is not available", r))
}

// ── POST /v1/admin/analytics/contact-codes ─────────────────────────────────

type contactCodesRequest struct {
	TwentyPersonIDs []string `json:"twenty_person_ids"`
}

type contactCodeItem struct {
	TwentyPersonID string `json:"twenty_person_id"`
	Code           string `json:"code"`
}

type contactCodesResponse struct {
	Codes []contactCodeItem `json:"codes"`
}

// HandleContactCodes returns one stable cid code per CRM person id.
func (h *AdminHandler) HandleContactCodes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	var req contactCodesRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"analytics.invalid_body", "Send a JSON object with a twenty_person_ids list.", r))
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"analytics.invalid_body", "The request body has data after the JSON object.", r))
		return
	}
	if len(req.TwentyPersonIDs) == 0 || len(req.TwentyPersonIDs) > analytics.MaxContactCodeBatch {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"analytics.invalid_twenty_person_ids", "twenty_person_ids must hold 1 to "+
				strconv.Itoa(analytics.MaxContactCodeBatch)+" ids.", r))
		return
	}
	ids := make([]string, 0, len(req.TwentyPersonIDs))
	seen := make(map[string]bool, len(req.TwentyPersonIDs))
	for _, id := range req.TwentyPersonIDs {
		id = strings.TrimSpace(id)
		if !analytics.ValidTwentyPersonID(id) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"analytics.invalid_twenty_person_ids", "every id must be 1 to 64 characters of letters, digits and _ . : -.", r))
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	if h.pool == nil {
		h.unavailable(w, r)
		return
	}
	// The audit row must commit with the codes: a writer that only logs
	// would let them be issued unrecorded.
	if !audit.IsDurableInTx(h.audit) {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.audit_unavailable", "nothing was changed: the audit log is not available, please try again in a moment", r))
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	codes, err := analytics.EnsureContactCodes(r.Context(), tx, ids)
	if err != nil {
		h.fail(w, r, "issue contact codes", err)
		return
	}
	if err := h.audit.WriteTx(r.Context(), tx, audit.Event{
		OccurredAt:   h.now().UTC(),
		ActorType:    "user",
		ActorID:      uuidOrEmpty(actorID(r)),
		Action:       AuditContactCodesIssue,
		ResourceType: "analytics_contact_codes",
		ResourceID:   "contact_codes",
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     map[string]any{"reason": reason, "contacts": len(ids)},
	}); err != nil {
		h.fail(w, r, "audit contact codes", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.fail(w, r, "commit contact codes", err)
		return
	}
	out := contactCodesResponse{Codes: make([]contactCodeItem, len(ids))}
	for i, id := range ids {
		out.Codes[i] = contactCodeItem{TwentyPersonID: id, Code: codes[i]}
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

func actorID(r *http.Request) string {
	a, _ := auth.ActorFromContext(r.Context())
	return a.ID
}

// uuidOrEmpty keeps audit_events.actor_id a bare uuid (AGENTS.md).
func uuidOrEmpty(s string) string {
	if _, err := uuid.Parse(s); err != nil {
		return ""
	}
	return s
}

// ── GET /v1/admin/analytics/profiles ───────────────────────────────────────

type touchView struct {
	Source   string `json:"source,omitempty"`
	Medium   string `json:"medium,omitempty"`
	Campaign string `json:"campaign,omitempty"`
	RefHost  string `json:"ref_host,omitempty"`
	Kind     string `json:"kind,omitempty"`
	At       string `json:"at,omitempty"`
}

type articleView struct {
	Path    string `json:"path"`
	At      string `json:"at"`
	Seconds int    `json:"seconds"`
}

type profileView struct {
	PersonID            string            `json:"person_id"`
	TwentyPersonID      *string           `json:"twenty_person_id"`
	Email               *string           `json:"email"`
	Stage               string            `json:"stage"`
	FirstTouch          touchView         `json:"first_touch"`
	LastTouch           touchView         `json:"last_touch"`
	FirstSource         string            `json:"first_source"`
	LastSource          string            `json:"last_source"`
	LastSeenAt          *string           `json:"last_seen_at"`
	Visits30d           int               `json:"visits_30d"`
	EngagedMinutes30d   int               `json:"engaged_minutes_30d"`
	Articles            []articleView     `json:"articles"`
	Milestones          map[string]string `json:"milestones"`
	GMV30d              map[string]int64  `json:"gmv_30d"`
	ActivityScore       int               `json:"activity_score"`
	ActivityExplanation string            `json:"activity_explanation"`
	TimelineURL         *string           `json:"timeline_url"`
	ChangedAt           string            `json:"changed_at"`
}

type profilesResponse struct {
	Profiles   []profileView `json:"profiles"`
	NextCursor *string       `json:"next_cursor"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func toTouchView(t analytics.Touch) touchView {
	v := touchView{Source: t.Source, Medium: t.Medium, Campaign: t.Campaign, RefHost: t.RefHost, Kind: t.Kind}
	if !t.At.IsZero() {
		v.At = rfc3339(t.At)
	}
	return v
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// HandleProfiles serves one page of changed profiles.
func (h *AdminHandler) HandleProfiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := analytics.ExportQuery{Now: h.now(), Weights: h.weights}
	if v := strings.TrimSpace(r.URL.Query().Get("changed_since")); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"analytics.invalid_changed_since", "changed_since must be an RFC 3339 time.", r))
			return
		}
		q.ChangedSince = t
	}
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > analytics.MaxExportLimit {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"analytics.invalid_limit", "limit must be a number from 1 to "+strconv.Itoa(analytics.MaxExportLimit)+".", r))
			return
		}
		q.Limit = n
	}
	if v := strings.TrimSpace(r.URL.Query().Get("after")); v != "" {
		c, err := analytics.DecodeProfileCursor(v)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"analytics.invalid_cursor", "after must be the next_cursor of a previous page.", r))
			return
		}
		q.After = &c
	}

	if h.pool == nil {
		h.unavailable(w, r)
		return
	}
	rows, next, err := analytics.ExportProfiles(r.Context(), h.pool, q)
	if err != nil {
		h.fail(w, r, "export profiles", err)
		return
	}
	out := profilesResponse{Profiles: make([]profileView, 0, len(rows)), NextCursor: strPtr(next)}
	for _, p := range rows {
		v := profileView{
			PersonID:            p.PersonID.String(),
			TwentyPersonID:      strPtr(p.TwentyPersonID),
			Email:               strPtr(p.Email),
			Stage:               p.Stage,
			FirstTouch:          toTouchView(p.FirstTouch),
			LastTouch:           toTouchView(p.LastTouch),
			FirstSource:         analytics.TouchLabel(p.FirstTouch),
			LastSource:          analytics.TouchLabel(p.LastTouch),
			Visits30d:           p.Visits30d,
			EngagedMinutes30d:   p.EngagedMinutes30d,
			Articles:            make([]articleView, 0, len(p.Articles)),
			Milestones:          make(map[string]string, len(p.Milestones)),
			GMV30d:              p.GMV30d,
			ActivityScore:       p.ActivityScore,
			ActivityExplanation: p.ScoreExplanation,
			ChangedAt:           rfc3339(p.ChangedAt),
		}
		if v.GMV30d == nil {
			v.GMV30d = map[string]int64{}
		}
		if p.LastSeenAt != nil {
			s := rfc3339(*p.LastSeenAt)
			v.LastSeenAt = &s
		}
		for _, a := range p.Articles {
			v.Articles = append(v.Articles, articleView{Path: a.Path, At: rfc3339(a.At), Seconds: a.Seconds})
		}
		for k, t := range p.Milestones {
			v.Milestones[k] = rfc3339(t)
		}
		if h.timeline != "" {
			u := h.timeline + "/analytics/people/" + p.PersonID.String()
			v.TimelineURL = &u
		}
		out.Profiles = append(out.Profiles, v)
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}
