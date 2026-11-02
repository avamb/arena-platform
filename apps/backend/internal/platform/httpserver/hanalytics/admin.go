package hanalytics

// admin.go — the read-only UX dashboards of the admin console (spec 50
// section 10, stage A8):
//
//	GET /v1/admin/analytics/flows
//	GET /v1/admin/analytics/errors
//	GET /v1/admin/analytics/people/{person_id}/timeline
//
// They are mounted behind superadmin.read and read the analytics schema only.
// A read needs no X-Admin-Reason. The answers carry counts, dictionary names
// and ids: no e-mail, no name, nothing a person typed.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// AdminOptions are the dependencies of an AdminHandler.
type AdminOptions struct {
	// DB nil makes every route answer 503.
	DB     analytics.Querier
	Logger *slog.Logger
	// Now nil means time.Now.
	Now func() time.Time
}

// AdminHandler serves the dashboard routes.
type AdminHandler struct {
	db  analytics.Querier
	log *slog.Logger
	now func() time.Time
}

// NewAdmin builds an AdminHandler.
func NewAdmin(o AdminOptions) *AdminHandler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &AdminHandler{db: o.DB, log: o.Logger, now: o.Now}
}

func (h *AdminHandler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.log.Error("analytics admin: "+what, "error", err.Error())
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"dependency.database_unavailable", "The analytics data could not be read.", r))
}

func (h *AdminHandler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h.db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The analytics data could not be read.", r))
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

// query reads the shared surface/from/to parameters.
func (h *AdminHandler) query(w http.ResponseWriter, r *http.Request) (surface string, p analytics.Period, ok bool) {
	q := r.URL.Query()
	surface = q.Get("surface")
	if err := analytics.CheckSurface(surface); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"analytics.invalid_surface", "surface must be site, event_center or bot.", r))
		return "", p, false
	}
	p, err := analytics.ParsePeriod(q.Get("from"), q.Get("to"), h.now())
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"analytics.invalid_period", "from and to must be dates (YYYY-MM-DD), from not after to, at most 366 days.", r))
		return "", p, false
	}
	return surface, p, true
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") } // allow:timeformat: a calendar date of the API, not a timestamp

// ─── flows ─────────────────────────────────────────────────────────────────

type flowNumbers struct {
	Entered       int      `json:"entered"`
	LeftHere      int      `json:"left_here"`
	DropOffPct    *float64 `json:"drop_off_pct"`
	Errors        int      `json:"errors"`
	MedianSeconds *int     `json:"median_seconds"`
}

type flowStepOut struct {
	Step string `json:"step"`
	flowNumbers
	Previous flowNumbers `json:"previous"`
}

type flowOut struct {
	Surface string        `json:"surface"`
	Flow    string        `json:"flow"`
	Steps   []flowStepOut `json:"steps"`
}

type flowsResponse struct {
	Surface      string    `json:"surface"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	PreviousFrom string    `json:"previous_from"`
	PreviousTo   string    `json:"previous_to"`
	Flows        []flowOut `json:"flows"`
}

func numbers(s analytics.FlowStep) flowNumbers {
	return flowNumbers{Entered: s.Entered, LeftHere: s.LeftHere, DropOffPct: s.DropOffPct(),
		Errors: s.Errors, MedianSeconds: s.MedianSeconds}
}

// HandleFlows serves GET /v1/admin/analytics/flows.
func (h *AdminHandler) HandleFlows(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	surface, p, ok := h.query(w, r)
	if !ok {
		return
	}
	rows, err := analytics.QueryFlows(r.Context(), h.db, surface, p)
	if err != nil {
		h.fail(w, r, "flows", err)
		return
	}
	prev := p.Previous()
	resp := flowsResponse{Surface: surface, From: day(p.From), To: day(p.To),
		PreviousFrom: day(prev.From), PreviousTo: day(prev.To), Flows: []flowOut{}}
	for _, c := range rows {
		n := len(resp.Flows)
		if n == 0 || resp.Flows[n-1].Surface != c.Current.Surface || resp.Flows[n-1].Flow != c.Current.Flow {
			resp.Flows = append(resp.Flows, flowOut{Surface: c.Current.Surface, Flow: c.Current.Flow, Steps: []flowStepOut{}})
			n++
		}
		resp.Flows[n-1].Steps = append(resp.Flows[n-1].Steps, flowStepOut{
			Step: c.Current.Step, flowNumbers: numbers(c.Current), Previous: numbers(c.Previous)})
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ─── errors ────────────────────────────────────────────────────────────────

type errorItem struct {
	Kind          string   `json:"kind"`
	Surface       string   `json:"surface"`
	Key           string   `json:"key"`
	Place         string   `json:"place"`
	Count         int      `json:"count"`
	PreviousCount int      `json:"previous_count"`
	ChangePct     *float64 `json:"change_pct"`
}

type errorsResponse struct {
	Surface      string      `json:"surface"`
	From         string      `json:"from"`
	To           string      `json:"to"`
	PreviousFrom string      `json:"previous_from"`
	PreviousTo   string      `json:"previous_to"`
	Items        []errorItem `json:"items"`
}

// HandleErrors serves GET /v1/admin/analytics/errors.
func (h *AdminHandler) HandleErrors(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	surface, p, ok := h.query(w, r)
	if !ok {
		return
	}
	rows, err := analytics.QueryErrors(r.Context(), h.db, surface, p, analytics.MaxErrorRows)
	if err != nil {
		h.fail(w, r, "errors", err)
		return
	}
	prev := p.Previous()
	resp := errorsResponse{Surface: surface, From: day(p.From), To: day(p.To),
		PreviousFrom: day(prev.From), PreviousTo: day(prev.To), Items: []errorItem{}}
	for _, e := range rows {
		resp.Items = append(resp.Items, errorItem{Kind: e.Kind, Surface: e.Surface, Key: e.Key, Place: e.Place,
			Count: e.Count, PreviousCount: e.Previous, ChangePct: analytics.ChangePct(e.Count, e.Previous)})
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ─── timeline ──────────────────────────────────────────────────────────────

type timelineProfile struct {
	Stage             string          `json:"stage"`
	LastSeenAt        *time.Time      `json:"last_seen_at"`
	Visits30d         int             `json:"visits_30d"`
	EngagedMinutes30d int             `json:"engaged_minutes_30d"`
	Articles          json.RawMessage `json:"articles"`
	Milestones        json.RawMessage `json:"milestones"`
	ActivityScore     int             `json:"activity_score"`
	ScoreExplanation  string          `json:"score_explanation"`
	ChangedAt         time.Time       `json:"changed_at"`
}

type timelineSession struct {
	ID             uuid.UUID       `json:"id"`
	Surface        string          `json:"surface"`
	StartedAt      time.Time       `json:"started_at"`
	EndedAt        time.Time       `json:"ended_at"`
	Pages          int             `json:"pages"`
	EngagedSeconds int             `json:"engaged_seconds"`
	Entry          json.RawMessage `json:"entry"`
}

type timelineEvent struct {
	At      time.Time       `json:"at"`
	Surface string          `json:"surface"`
	Name    string          `json:"name"`
	Props   json.RawMessage `json:"props"`
}

type timelineResponse struct {
	PersonID       uuid.UUID         `json:"person_id"`
	TwentyPersonID *string           `json:"twenty_person_id"`
	UserID         *uuid.UUID        `json:"user_id"`
	FirstTouch     json.RawMessage   `json:"first_touch"`
	LastTouch      json.RawMessage   `json:"last_touch"`
	CreatedAt      time.Time         `json:"created_at"`
	Profile        *timelineProfile  `json:"profile"`
	Sessions       []timelineSession `json:"sessions"`
	Events         []timelineEvent   `json:"events"`
	NextCursor     *string           `json:"next_cursor"`
}

// HandleTimeline serves GET /v1/admin/analytics/people/{person_id}/timeline.
func (h *AdminHandler) HandleTimeline(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "person_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"analytics.invalid_person_id", "person_id must be a UUID.", r))
		return
	}
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"analytics.invalid_limit", "limit must be a positive integer.", r))
			return
		}
		limit = n
	}
	t, err := analytics.LoadTimeline(r.Context(), h.db, id, r.URL.Query().Get("cursor"), limit, h.now())
	switch {
	case errors.Is(err, analytics.ErrPersonNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"analytics.person_not_found", "No such person.", r))
		return
	case errors.Is(err, analytics.ErrInvalidCursor):
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"analytics.invalid_cursor", "cursor must be the next_cursor of a previous answer.", r))
		return
	case err != nil:
		h.fail(w, r, "timeline", err)
		return
	}
	resp := timelineResponse{PersonID: t.PersonID, TwentyPersonID: t.TwentyPersonID, UserID: t.UserID,
		FirstTouch: t.FirstTouch, LastTouch: t.LastTouch, CreatedAt: t.CreatedAt,
		Sessions: []timelineSession{}, Events: []timelineEvent{}}
	if t.Profile != nil {
		p := t.Profile
		resp.Profile = &timelineProfile{Stage: p.Stage, LastSeenAt: p.LastSeenAt, Visits30d: p.Visits30d,
			EngagedMinutes30d: p.EngagedMinutes30d, Articles: p.Articles, Milestones: p.Milestones,
			ActivityScore: p.ActivityScore, ScoreExplanation: p.ScoreExplanation, ChangedAt: p.ChangedAt}
	}
	for _, s := range t.Sessions {
		resp.Sessions = append(resp.Sessions, timelineSession{ID: s.ID, Surface: s.Surface, StartedAt: s.StartedAt,
			EndedAt: s.EndedAt, Pages: s.Pages, EngagedSeconds: s.EngagedSeconds, Entry: s.Entry})
	}
	for _, e := range t.Events {
		resp.Events = append(resp.Events, timelineEvent{At: e.At, Surface: e.Surface, Name: e.Name, Props: e.Props})
	}
	if t.NextCursor != "" {
		resp.NextCursor = &t.NextCursor
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}
