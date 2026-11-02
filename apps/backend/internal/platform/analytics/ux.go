package analytics

// ux.go — the read side of the UX dashboards of the admin console (spec 50
// section 10, stage A8): where people leave a flow, which errors repeat, and
// what one person did. Everything here only READS the analytics schema; the
// numbers are the ones the hourly rollup (flow_daily, sessions, profiles) and
// the writer (events) already produced. Days are UTC days, like flow_daily.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Querier is the part of a pgx pool or transaction the read side needs.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Limits of the dashboards.
const (
	// DefaultPeriodDays is the period of a request that names none.
	DefaultPeriodDays = 7
	// MaxPeriodDays caps a requested period (the raw events live 13 months).
	MaxPeriodDays = 366
	// MaxErrorRows caps the rows of the errors list.
	MaxErrorRows = 100
	// DefaultTimelineLimit and MaxTimelineLimit bound one page of events.
	DefaultTimelineLimit = 50
	MaxTimelineLimit     = 200
	// maxTimelineSessions is how many of the newest sessions the timeline lists.
	maxTimelineSessions = 100
)

// Error kinds of the errors list. They are the names of the dictionary events
// the list is built from.
const (
	ErrKindUI        = "ui_error"
	ErrKindWizard    = "wizard_input_error"
	ErrKindRageClick = "rage_click"
)

const (
	dayLayout         = "2006-01-02"
	cursorSeparator   = "."
	surfaceAllFilter  = ""
	errorKindsInQuery = "'ui_error', 'wizard_input_error', 'rage_click'"
)

// ErrInvalidPeriod is returned by ParsePeriod.
var ErrInvalidPeriod = errors.New("analytics: invalid period")

// ErrInvalidSurface is returned by CheckSurface.
var ErrInvalidSurface = errors.New("analytics: invalid surface")

// ErrPersonNotFound is returned by LoadTimeline for an unknown person.
var ErrPersonNotFound = errors.New("analytics: person not found")

// ErrInvalidCursor is returned by LoadTimeline for a cursor it did not issue.
var ErrInvalidCursor = errors.New("analytics: invalid cursor")

// CheckSurface validates the surface filter: empty (all) or one of the three
// surfaces of the dictionary.
func CheckSurface(s string) error {
	switch s {
	case surfaceAllFilter, SurfaceSite, SurfaceEventCenter, SurfaceBot:
		return nil
	}
	return ErrInvalidSurface
}

// Period is a range of whole UTC days, both ends inclusive.
type Period struct {
	From time.Time
	To   time.Time
}

// Days is the number of days in the period.
func (p Period) Days() int {
	return int(p.To.Sub(p.From).Hours()/24) + 1
}

// Previous is the period of the same length that ends the day before p.
func (p Period) Previous() Period {
	n := p.Days()
	return Period{From: p.From.AddDate(0, 0, -n), To: p.From.AddDate(0, 0, -1)}
}

// End is the exclusive end instant (the midnight after To).
func (p Period) End() time.Time { return p.To.AddDate(0, 0, 1) }

// ParsePeriod reads the from/to query values (YYYY-MM-DD, UTC days). An empty
// `to` is the day of now, an empty `from` is DefaultPeriodDays-1 days before
// `to`. A reversed or longer than MaxPeriodDays range is refused.
func ParsePeriod(from, to string, now time.Time) (Period, error) {
	var p Period
	var err error
	if to == "" {
		p.To = TruncateDay(now)
	} else if p.To, err = time.Parse(dayLayout, to); err != nil {
		return p, ErrInvalidPeriod
	}
	if from == "" {
		p.From = p.To.AddDate(0, 0, -(DefaultPeriodDays - 1))
	} else if p.From, err = time.Parse(dayLayout, from); err != nil {
		return p, ErrInvalidPeriod
	}
	if p.From.After(p.To) || p.Days() > MaxPeriodDays {
		return p, ErrInvalidPeriod
	}
	return p, nil
}

// ChangePct is the change from prev to cur in percent, nil when prev is zero
// (a change from nothing has no percentage).
func ChangePct(cur, prev int) *float64 {
	if prev <= 0 {
		return nil
	}
	v := math.Round(float64(cur-prev)/float64(prev)*1000) / 10
	return &v
}

// ─── flows ─────────────────────────────────────────────────────────────────

// FlowStep is the sum of flow_daily over a period for one step.
type FlowStep struct {
	Surface  string
	Flow     string
	Step     string
	Entered  int
	LeftHere int
	Errors   int
	// MedianSeconds is the median of the daily medians, nil when no day had one.
	MedianSeconds *int
}

// DropOffPct is the share of the people who entered the step and left the
// flow there, in percent with one decimal; nil when nobody entered. It is
// capped at 100 (a day's exit can be booked on a step entered the day before).
func (s FlowStep) DropOffPct() *float64 {
	if s.Entered <= 0 {
		return nil
	}
	v := math.Round(math.Min(100, float64(s.LeftHere)/float64(s.Entered)*100)*10) / 10
	return &v
}

// FlowStepCompare is one step in the period and in the one before it.
type FlowStepCompare struct {
	Current  FlowStep
	Previous FlowStep
}

// QueryFlows sums flow_daily over p and over the period before it, per step.
// Steps of one flow are ordered by the people who entered them, most first
// (a funnel narrows), then by name; flows by surface and name.
func QueryFlows(ctx context.Context, q Querier, surface string, p Period) ([]FlowStepCompare, error) {
	prev := p.Previous()
	rows, err := q.Query(ctx, `
SELECT surface, flow, step,
       COALESCE(SUM(entered)  FILTER (WHERE day >= $1::date), 0)::int,
       COALESCE(SUM(left_here) FILTER (WHERE day >= $1::date), 0)::int,
       COALESCE(SUM(errors)   FILTER (WHERE day >= $1::date), 0)::int,
       (percentile_cont(0.5) WITHIN GROUP (ORDER BY median_seconds) FILTER (WHERE day >= $1::date))::float8,
       COALESCE(SUM(entered)  FILTER (WHERE day < $1::date), 0)::int,
       COALESCE(SUM(left_here) FILTER (WHERE day < $1::date), 0)::int,
       COALESCE(SUM(errors)   FILTER (WHERE day < $1::date), 0)::int,
       (percentile_cont(0.5) WITHIN GROUP (ORDER BY median_seconds) FILTER (WHERE day < $1::date))::float8
  FROM analytics.flow_daily
 WHERE day >= $2::date AND day <= $3::date AND ($4 = '' OR surface = $4)
 GROUP BY surface, flow, step`, p.From, prev.From, p.To, surface)
	if err != nil {
		return nil, fmt.Errorf("analytics: query flows: %w", err)
	}
	defer rows.Close()
	var out []FlowStepCompare
	for rows.Next() {
		var c FlowStepCompare
		var curMed, prevMed *float64
		if err := rows.Scan(&c.Current.Surface, &c.Current.Flow, &c.Current.Step,
			&c.Current.Entered, &c.Current.LeftHere, &c.Current.Errors, &curMed,
			&c.Previous.Entered, &c.Previous.LeftHere, &c.Previous.Errors, &prevMed); err != nil {
			return nil, err
		}
		c.Previous.Surface, c.Previous.Flow, c.Previous.Step = c.Current.Surface, c.Current.Flow, c.Current.Step
		c.Current.MedianSeconds = roundedSeconds(curMed)
		c.Previous.MedianSeconds = roundedSeconds(prevMed)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	SortFlowSteps(out)
	return out, nil
}

func roundedSeconds(v *float64) *int {
	if v == nil {
		return nil
	}
	n := int(math.Round(*v))
	return &n
}

// SortFlowSteps orders steps for display (see QueryFlows).
func SortFlowSteps(in []FlowStepCompare) {
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i].Current, in[j].Current
		switch {
		case a.Surface != b.Surface:
			return a.Surface < b.Surface
		case a.Flow != b.Flow:
			return a.Flow < b.Flow
		case a.Entered != b.Entered:
			return a.Entered > b.Entered
		case in[i].Previous.Entered != in[j].Previous.Entered:
			return in[i].Previous.Entered > in[j].Previous.Entered
		}
		return a.Step < b.Step
	})
}

// ─── errors ────────────────────────────────────────────────────────────────

// ErrorStat is one repeating error, counted in the period and in the one
// before it. Key is the code of a ui_error, the reason of a
// wizard_input_error or the screen of a rage_click; Place is the screen of a
// ui_error or the step of a wizard_input_error (empty for a rage click).
type ErrorStat struct {
	Kind     string
	Surface  string
	Key      string
	Place    string
	Count    int
	Previous int
}

// QueryErrors counts ui_error codes, wizard_input_error reasons and rage
// clicks by screen in p and in the period before it, most frequent first.
// Suspect (robot) events are left out. At most limit rows (MaxErrorRows when
// limit is out of range) are returned.
func QueryErrors(ctx context.Context, q Querier, surface string, p Period, limit int) ([]ErrorStat, error) {
	if limit <= 0 || limit > MaxErrorRows {
		limit = MaxErrorRows
	}
	prev := p.Previous()
	rows, err := q.Query(ctx, `
SELECT name, surface, key, place,
       count(*) FILTER (WHERE at >= $1)::int AS cur,
       count(*) FILTER (WHERE at <  $1)::int AS prev
  FROM (
    SELECT at, name, surface,
           CASE name WHEN 'ui_error' THEN COALESCE(props->>'code', '')
                     WHEN 'wizard_input_error' THEN COALESCE(props->>'reason', '')
                     ELSE COALESCE(props->>'screen', '') END AS key,
           CASE name WHEN 'ui_error' THEN COALESCE(props->>'screen', '')
                     WHEN 'wizard_input_error' THEN COALESCE(props->>'step', '')
                     ELSE '' END AS place
      FROM analytics.events
     WHERE at >= $2 AND at < $3 AND NOT suspect
       AND name IN (`+errorKindsInQuery+`) AND ($4 = '' OR surface = $4)
  ) e
 GROUP BY name, surface, key, place
 ORDER BY cur DESC, prev DESC, name, key, place
 LIMIT $5`, p.From, prev.From, p.End(), surface, limit)
	if err != nil {
		return nil, fmt.Errorf("analytics: query errors: %w", err)
	}
	defer rows.Close()
	var out []ErrorStat
	for rows.Next() {
		var e ErrorStat
		if err := rows.Scan(&e.Kind, &e.Surface, &e.Key, &e.Place, &e.Count, &e.Previous); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─── person timeline ───────────────────────────────────────────────────────

// TimelineProfile is the stored profile of a person plus the one-line reason
// for the activity score.
type TimelineProfile struct {
	Stage             string
	LastSeenAt        *time.Time
	Visits30d         int
	EngagedMinutes30d int
	Articles          json.RawMessage
	Milestones        json.RawMessage
	ActivityScore     int
	ScoreExplanation  string
	ChangedAt         time.Time
}

// TimelineSession is a stored session of the person.
type TimelineSession struct {
	ID             uuid.UUID
	Surface        string
	StartedAt      time.Time
	EndedAt        time.Time
	Pages          int
	EngagedSeconds int
	Entry          json.RawMessage
}

// TimelineEvent is one raw event of the person.
type TimelineEvent struct {
	At      time.Time
	Surface string
	Name    string
	Props   json.RawMessage
}

// Timeline is everything the admin console shows for one person.
type Timeline struct {
	PersonID       uuid.UUID
	TwentyPersonID *string
	UserID         *uuid.UUID
	FirstTouch     json.RawMessage
	LastTouch      json.RawMessage
	CreatedAt      time.Time
	// Profile is nil until the rollup has written one.
	Profile *TimelineProfile
	// Sessions are the newest sessions, only on the first page.
	Sessions []TimelineSession
	// Events are one page of the person's events, newest first.
	Events []TimelineEvent
	// NextCursor continues the events; empty on the last page.
	NextCursor string
}

// EncodeCursor makes the opaque continuation token of an event.
func EncodeCursor(at time.Time, id int64) string {
	return strconv.FormatInt(at.UnixMicro(), 10) + cursorSeparator + strconv.FormatInt(id, 10)
}

// DecodeCursor reverses EncodeCursor.
func DecodeCursor(c string) (time.Time, int64, error) {
	parts := strings.Split(c, cursorSeparator)
	if len(parts) != 2 {
		return time.Time{}, 0, ErrInvalidCursor
	}
	us, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return time.UnixMicro(us).UTC(), id, nil
}

// LoadTimeline reads one person: identity-free summary, profile with the
// score explained, the newest sessions (first page only) and one page of
// events, newest first. Events are those written under any of the person's
// identifiers, suspect ones excluded, like the rollup reads them. cursor is
// the NextCursor of the previous page ("" for the first). limit is clamped to
// 1..MaxTimelineLimit (DefaultTimelineLimit when not positive).
func LoadTimeline(ctx context.Context, q Querier, personID uuid.UUID, cursor string, limit int, now time.Time) (*Timeline, error) {
	if limit <= 0 {
		limit = DefaultTimelineLimit
	}
	if limit > MaxTimelineLimit {
		limit = MaxTimelineLimit
	}
	var curAt *time.Time
	var curID *int64
	if cursor != "" {
		at, id, err := DecodeCursor(cursor)
		if err != nil {
			return nil, err
		}
		curAt, curID = &at, &id
	}

	t := &Timeline{PersonID: personID}
	err := q.QueryRow(ctx, `
SELECT twenty_person_id, user_id, first_touch, last_touch, created_at
  FROM analytics.people WHERE id = $1`, personID).
		Scan(&t.TwentyPersonID, &t.UserID, &t.FirstTouch, &t.LastTouch, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPersonNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("analytics: read person: %w", err)
	}

	if cursor == "" {
		if t.Profile, err = loadTimelineProfile(ctx, q, personID, now); err != nil {
			return nil, err
		}
		if t.Sessions, err = loadTimelineSessions(ctx, q, personID); err != nil {
			return nil, err
		}
	}

	rows, err := q.Query(ctx, `
SELECT e.at, e.id, e.surface, e.name, e.props
  FROM analytics.identities i
  JOIN analytics.events e ON e.id_kind = i.kind AND e.id_value = i.value
 WHERE i.person_id = $1 AND i.kind IN ('aid', 'cid', 'user', 'tg') AND NOT e.suspect
   AND ($2::timestamptz IS NULL OR (e.at, e.id) < ($2::timestamptz, $3::bigint))
 ORDER BY e.at DESC, e.id DESC
 LIMIT $4`, personID, curAt, curID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("analytics: read person events: %w", err)
	}
	defer rows.Close()
	var lastID int64
	for rows.Next() {
		var ev TimelineEvent
		var id int64
		if err := rows.Scan(&ev.At, &id, &ev.Surface, &ev.Name, &ev.Props); err != nil {
			return nil, err
		}
		if len(t.Events) == limit {
			// One more than a page exists: the page ends at the last kept event.
			last := t.Events[len(t.Events)-1]
			t.NextCursor = EncodeCursor(last.At, lastID)
			break
		}
		t.Events = append(t.Events, ev)
		lastID = id
	}
	return t, rows.Err()
}

func loadTimelineSessions(ctx context.Context, q Querier, personID uuid.UUID) ([]TimelineSession, error) {
	rows, err := q.Query(ctx, `
SELECT id, surface, started_at, ended_at, pages, engaged_seconds, entry
  FROM analytics.sessions WHERE person_id = $1
 ORDER BY started_at DESC LIMIT $2`, personID, maxTimelineSessions)
	if err != nil {
		return nil, fmt.Errorf("analytics: read person sessions: %w", err)
	}
	defer rows.Close()
	var out []TimelineSession
	for rows.Next() {
		var s TimelineSession
		if err := rows.Scan(&s.ID, &s.Surface, &s.StartedAt, &s.EndedAt, &s.Pages, &s.EngagedSeconds, &s.Entry); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func loadTimelineProfile(ctx context.Context, q Querier, personID uuid.UUID, now time.Time) (*TimelineProfile, error) {
	var p TimelineProfile
	err := q.QueryRow(ctx, `
SELECT stage, last_seen_at, visits_30d, engaged_minutes_30d, articles, milestones, activity_score, changed_at
  FROM analytics.profiles WHERE person_id = $1`, personID).
		Scan(&p.Stage, &p.LastSeenAt, &p.Visits30d, &p.EngagedMinutes30d, &p.Articles, &p.Milestones, &p.ActivityScore, &p.ChangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("analytics: read profile: %w", err)
	}
	evs, _, err := loadPersonEvents(ctx, q, personID)
	if err != nil {
		return nil, err
	}
	sessions := SplitSessions(evs)
	items := SessionScoreItems(sessions, ArticleReads(sessions))
	var ms map[string]any
	_ = json.Unmarshal(p.Milestones, &ms)
	for key, kind := range map[string]string{
		MsApplicationStarted:   ScoreApplicationStarted,
		MsApplicationSubmitted: ScoreApplicationSubmitted,
	} {
		if s, ok := ms[key].(string); ok {
			if at, err := time.Parse(time.RFC3339, s); err == nil {
				items = append(items, ScoreItem{Kind: kind, At: at})
			}
		}
	}
	p.ScoreExplanation = ExplainScore(items, now, DefaultScoreWeights)
	return &p, nil
}
