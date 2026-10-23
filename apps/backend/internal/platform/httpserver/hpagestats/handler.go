// Package hpagestats serves the organizer's read of the hosted page's
// statistics (HP-2a, task 2-3):
//
//	GET /v1/organizations/{org_id}/events/{event_id}/page-stats?from=&to=   (event.read)
//
// The counters are written by the public beacon (hfeed, POST
// /v1/public/pages/{slug}/{event_slug}/track): per event, per calendar day in
// the venue's zone, per kind (page_view, video_play, partner_click) and per
// cleaned utm pair, with obvious bots counted apart in suspect_hits. They are
// a guide for the organizer, not proof for an advertising account: a bot may
// look like a person and a person may block the beacon.
//
// An event of another organization (or a deleted one) is the route's own 404
// event.not_found. The CSV twin lives in hexport (page-stats.csv) and shares
// ParseRange.
package hpagestats

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
)

const (
	// DefaultDays is the window of the answer when the caller names none.
	DefaultDays = 30
	// MaxDays is the longest window one request may read.
	MaxDays = 366
	// maxSources caps the utm list.
	maxSources = 20
)

// Handler serves the statistics read.
type Handler struct {
	queries *gen.Queries
	logger  *slog.Logger
}

// New builds the handler. A nil queries is allowed: the route answers 503.
func New(queries *gen.Queries, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{queries: queries, logger: logger}
}

// Counts is a pair of counters: the hits of people, and the hits counted
// apart because the browser looked like a bot.
type Counts struct {
	Hits        int64 `json:"hits"`
	SuspectHits int64 `json:"suspect_hits"`
}

// KindCounts holds the counters of the three kinds.
type KindCounts struct {
	PageView     Counts `json:"page_view"`
	VideoPlay    Counts `json:"video_play"`
	PartnerClick Counts `json:"partner_click"`
}

func (k *KindCounts) add(kind string, hits, suspect int64) {
	var c *Counts
	switch kind {
	case "page_view":
		c = &k.PageView
	case "video_play":
		c = &k.VideoPlay
	case "partner_click":
		c = &k.PartnerClick
	default:
		return
	}
	c.Hits += hits
	c.SuspectHits += suspect
}

// DayEntry is one day that has data.
type DayEntry struct {
	Day string `json:"day"`
	KindCounts
}

// SourceEntry is one utm pair of the page views.
type SourceEntry struct {
	UTMSource   string `json:"utm_source"`
	UTMCampaign string `json:"utm_campaign"`
	Counts
}

// Response is the answer of GET .../page-stats.
type Response struct {
	EventID  string        `json:"event_id"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Timezone *string       `json:"timezone"`
	Totals   KindCounts    `json:"totals"`
	Days     []DayEntry    `json:"days"`
	Sources  []SourceEntry `json:"sources"`
}

// DateString renders a calendar date. The value is a date, not an instant.
func DateString(t time.Time) string {
	return t.Format("2006-01-02") // allow:timeformat: a calendar date, not a timestamp
}

// RangeError is a refused ?from / ?to.
type RangeError struct{ Code, Message string }

// ParseRange reads ?from and ?to (YYYY-MM-DD). The window ends today in the
// event's venue zone by default and covers DefaultDays days; it may not run
// backwards or cover more than MaxDays days.
func ParseRange(r *http.Request, tz *string) (from, to time.Time, rerr *RangeError) {
	zone := ""
	if tz != nil {
		zone = *tz
	}
	today := pagestats.Day(time.Now(), zone)
	q := r.URL.Query()
	to = today
	if v := q.Get("to"); v != "" {
		t, err := time.Parse("2006-01-02", v) // allow:timeformat: a calendar date
		if err != nil {
			return from, to, &RangeError{"page_stats.invalid_date", "to must be a date like 2026-05-31"}
		}
		to = t
	}
	from = to.AddDate(0, 0, -(DefaultDays - 1))
	if v := q.Get("from"); v != "" {
		t, err := time.Parse("2006-01-02", v) // allow:timeformat: a calendar date
		if err != nil {
			return from, to, &RangeError{"page_stats.invalid_date", "from must be a date like 2026-05-01"}
		}
		from = t
	}
	if from.After(to) {
		return from, to, &RangeError{"page_stats.invalid_range", "from must not be after to"}
	}
	if int(to.Sub(from).Hours()/24)+1 > MaxDays {
		return from, to, &RangeError{"page_stats.range_too_long", "ask for at most 366 days at a time"}
	}
	return from, to, nil
}

// HandleGet serves GET /v1/organizations/{org_id}/events/{event_id}/page-stats.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	ctx := r.Context()
	header, err := h.queries.GetEventPageStatsHeader(ctx, eventID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"event.not_found", "We cannot find this event.", r,
		))
		return
	}
	if err != nil {
		h.fail(w, r, eventID, "header", err)
		return
	}
	from, to, rerr := ParseRange(r, header.Timezone)
	if rerr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(rerr.Code, rerr.Message, r))
		return
	}
	days, err := h.queries.ListEventPageStatsDays(ctx, eventID, from, to)
	if err != nil {
		h.fail(w, r, eventID, "days", err)
		return
	}
	sources, err := h.queries.ListEventPageStatsSources(ctx, eventID, from, to, maxSources)
	if err != nil {
		h.fail(w, r, eventID, "sources", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, buildResponse(eventID, from, to, header.Timezone, days, sources))
}

// buildResponse folds the query rows into the answer. Pure.
func buildResponse(eventID uuid.UUID, from, to time.Time, tz *string, days []gen.EventPageStatDayRow, sources []gen.EventPageStatSourceRow) Response {
	out := Response{
		EventID:  eventID.String(),
		From:     DateString(from),
		To:       DateString(to),
		Timezone: tz,
		Days:     []DayEntry{},
		Sources:  []SourceEntry{},
	}
	var cur *DayEntry
	for _, d := range days {
		out.Totals.add(d.Kind, d.Hits, d.SuspectHits)
		day := DateString(d.Day)
		if cur == nil || cur.Day != day {
			out.Days = append(out.Days, DayEntry{Day: day})
			cur = &out.Days[len(out.Days)-1]
		}
		cur.add(d.Kind, d.Hits, d.SuspectHits)
	}
	for _, s := range sources {
		out.Sources = append(out.Sources, SourceEntry{
			UTMSource: s.UTMSource, UTMCampaign: s.UTMCampaign,
			Counts: Counts{Hits: s.Hits, SuspectHits: s.SuspectHits},
		})
	}
	return out
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, eventID uuid.UUID, part string, err error) {
	h.logger.Error("hpagestats: read failed",
		slog.String("event_id", eventID.String()), slog.String("part", part), slog.Any("error", err))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"page_stats.failed", "We could not load the page statistics. Try again in a minute.", r,
	))
}
