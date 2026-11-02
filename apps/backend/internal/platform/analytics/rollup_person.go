package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// sessionNS makes session ids deterministic: the same person, surface and
// start always get the same id, so a re-run rewrites nothing.
var sessionNS = uuid.MustParse("5b0c5d3e-7a5e-4f43-9e44-0f5a1a0a7b50")

func sessionID(person uuid.UUID, surface string, start time.Time) uuid.UUID {
	return uuid.NewSHA1(sessionNS, []byte(fmt.Sprintf("%s|%s|%d", person, surface, start.UnixMicro())))
}

// canonJSON re-encodes JSON with sorted keys so two spellings of the same
// value compare equal.
func canonJSON(raw []byte) string {
	var v any
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func fmtTime(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

type sessionRow struct {
	id             uuid.UUID
	surface        string
	started, ended time.Time
	pages, engaged int
	entry          []byte
}

func entryJSON(s Session) []byte {
	m := map[string]any{}
	if s.EntryPath != "" {
		m["path"] = s.EntryPath
	}
	if s.HasTouch {
		t := s.Touch
		for k, v := range map[string]string{"source": t.Source, "medium": t.Medium, "campaign": t.Campaign,
			"ref_host": t.RefHost, "kind": t.Kind} {
			if v != "" {
				m[k] = v
			}
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// processPerson recomputes one person from raw events and the business
// tables: sessions, touches, milestones, profile. It writes only what
// differs from the stored rows.
func (r *Rollup) processPerson(ctx context.Context, personID uuid.UUID, now time.Time, res *RollupResult) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The person may have been merged away or erased since the list was read.
	var userID *uuid.UUID
	var ftRaw, ltRaw []byte
	err = tx.QueryRow(ctx,
		`SELECT user_id, first_touch, last_touch FROM analytics.people WHERE id = $1 FOR UPDATE`, personID).
		Scan(&userID, &ftRaw, &ltRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	evs, err := loadPersonEvents(ctx, tx, personID)
	if err != nil {
		return err
	}
	lastSeen, err := lastSeenOf(ctx, tx, personID)
	if err != nil {
		return err
	}
	sessions := SplitSessions(evs)
	reads := ArticleReads(sessions)

	n, err := syncSessions(ctx, tx, personID, sessions, evs)
	if err != nil {
		return err
	}
	res.SessionsWritten += n

	// Touches: keep a first touch that outlived the raw events.
	firstNew, lastNew := SessionTouches(sessions)
	first := EarlierTouch(decodeTouch(ftRaw), firstNew)
	last := LaterTouch(decodeTouch(ltRaw), lastNew)
	if canonJSON(encodeTouch(first)) != canonJSON(ftRaw) || canonJSON(encodeTouch(last)) != canonJSON(ltRaw) {
		if _, err := tx.Exec(ctx, `UPDATE analytics.people SET first_touch = $2, last_touch = $3 WHERE id = $1`,
			personID, encodeTouch(first), encodeTouch(last)); err != nil {
			return err
		}
	}

	ms, gmv, err := loadMilestones(ctx, tx, personID, userID, now)
	if err != nil {
		return err
	}

	items := SessionScoreItems(sessions, reads)
	if t, ok := ms[MsApplicationStarted]; ok {
		items = append(items, ScoreItem{Kind: ScoreApplicationStarted, At: t})
	}
	if t, ok := ms[MsApplicationSubmitted]; ok {
		items = append(items, ScoreItem{Kind: ScoreApplicationSubmitted, At: t})
	}

	// 30-day window in whole UTC days, so the counters change once a day.
	cut := TruncateDay(now).AddDate(0, 0, -30)
	visits, engagedSec := 0, 0
	for _, s := range sessions {
		if !s.Start.Before(cut) {
			visits++
			engagedSec += s.EngagedSeconds
		}
	}

	msJSON := map[string]any{}
	for k, t := range ms {
		msJSON[k] = fmtTime(t)
	}
	if len(gmv) > 0 {
		msJSON[MsGMV30d] = gmv
	}
	articles := make([]map[string]any, 0, len(reads))
	for _, a := range reads {
		articles = append(articles, map[string]any{"path": a.Path, "at": fmtTime(a.At), "seconds": a.Seconds})
	}
	articlesRaw, _ := json.Marshal(articles)
	msRaw, _ := json.Marshal(msJSON)
	var lastSeenArg any
	if !lastSeen.IsZero() {
		lastSeenArg = lastSeen.UTC().Truncate(time.Microsecond)
	}
	want := profileRow{
		stage: StageFor(ms), visits: visits, minutes: engagedSec / 60,
		score: Score(items, now, r.w),
	}
	want.lastSeen = lastSeen

	changed, err := upsertProfile(ctx, tx, personID, want, lastSeenArg, articlesRaw, msRaw, now)
	if err != nil {
		return err
	}
	res.ProfilesWritten++
	if changed {
		res.ProfilesChanged++
	}
	return tx.Commit(ctx)
}

type profileRow struct {
	stage    string
	visits   int
	minutes  int
	score    int
	lastSeen time.Time
}

// upsertProfile writes the profile and moves changed_at only when a value
// differs from the stored row.
func upsertProfile(ctx context.Context, tx pgx.Tx, personID uuid.UUID, want profileRow, lastSeenArg any,
	articles, milestones []byte, now time.Time) (bool, error) {
	var (
		stage                  string
		lastSeen               *time.Time
		visits, minutes, score int
		oldArt, oldMs          []byte
	)
	err := tx.QueryRow(ctx, `
SELECT stage, last_seen_at, visits_30d, engaged_minutes_30d, activity_score, articles, milestones
  FROM analytics.profiles WHERE person_id = $1`, personID).
		Scan(&stage, &lastSeen, &visits, &minutes, &score, &oldArt, &oldMs)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `
INSERT INTO analytics.profiles
  (person_id, stage, last_seen_at, visits_30d, engaged_minutes_30d, articles, milestones, activity_score, changed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			personID, want.stage, lastSeenArg, want.visits, want.minutes, articles, milestones, want.score, now)
		return true, err
	}
	if err != nil {
		return false, err
	}
	same := stage == want.stage && visits == want.visits && minutes == want.minutes && score == want.score &&
		canonJSON(oldArt) == canonJSON(articles) && canonJSON(oldMs) == canonJSON(milestones) &&
		((lastSeen == nil) == want.lastSeen.IsZero()) &&
		(lastSeen == nil || lastSeen.UTC().Truncate(time.Microsecond).Equal(want.lastSeen.UTC().Truncate(time.Microsecond)))
	if same {
		return false, nil
	}
	_, err = tx.Exec(ctx, `
UPDATE analytics.profiles
   SET stage = $2, last_seen_at = $3, visits_30d = $4, engaged_minutes_30d = $5,
       articles = $6, milestones = $7, activity_score = $8, changed_at = $9
 WHERE person_id = $1`,
		personID, want.stage, lastSeenArg, want.visits, want.minutes, articles, milestones, want.score, now)
	return true, err
}

// loadPersonEvents reads the non-suspect site and event-center events of the
// person through their identities, oldest first. Bot events make no sessions
// and are not loaded.
func loadPersonEvents(ctx context.Context, tx pgx.Tx, personID uuid.UUID) ([]Ev, error) {
	rows, err := tx.Query(ctx, `
SELECT e.at, e.surface, e.name, e.props
  FROM analytics.identities i
  JOIN analytics.events e ON e.id_kind = i.kind AND e.id_value = i.value
 WHERE i.person_id = $1 AND i.kind IN ('aid', 'cid', 'user', 'tg') AND NOT e.suspect
   AND e.surface IN ('site', 'event_center')
 ORDER BY e.at, e.id`, personID)
	if err != nil {
		return nil, fmt.Errorf("analytics: load person events: %w", err)
	}
	defer rows.Close()
	var evs []Ev
	for rows.Next() {
		var e Ev
		var props []byte
		if err := rows.Scan(&e.At, &e.Surface, &e.Name, &props); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(props, &e.Props)
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

// lastSeenOf is the time of the person's latest non-suspect event of any
// surface (the bot included); zero when there is none.
func lastSeenOf(ctx context.Context, tx pgx.Tx, personID uuid.UUID) (time.Time, error) {
	var last *time.Time
	err := tx.QueryRow(ctx, `
SELECT max(e.at)
  FROM analytics.identities i
  JOIN analytics.events e ON e.id_kind = i.kind AND e.id_value = i.value
 WHERE i.person_id = $1 AND i.kind IN ('aid', 'cid', 'user', 'tg') AND NOT e.suspect`, personID).Scan(&last)
	if err != nil {
		return time.Time{}, fmt.Errorf("analytics: last seen: %w", err)
	}
	if last == nil {
		return time.Time{}, nil
	}
	return *last, nil
}

// syncSessions makes the stored sessions of the person equal the computed
// ones, touching only rows that differ. It returns the rows written.
//
// Raw events live 13 months and sessions 25: a stored session that starts
// before the oldest raw event still kept is the only record of that visit, so
// it is never deleted (and with no events at all nothing is deleted).
func syncSessions(ctx context.Context, tx pgx.Tx, personID uuid.UUID, sessions []Session, evs []Ev) (int, error) {
	rows, err := tx.Query(ctx, `
SELECT id, surface, started_at, ended_at, pages, engaged_seconds, entry
  FROM analytics.sessions WHERE person_id = $1`, personID)
	if err != nil {
		return 0, err
	}
	stored := map[uuid.UUID]sessionRow{}
	for rows.Next() {
		var s sessionRow
		if err := rows.Scan(&s.id, &s.surface, &s.started, &s.ended, &s.pages, &s.engaged, &s.entry); err != nil {
			rows.Close()
			return 0, err
		}
		stored[s.id] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	written := 0
	keep := map[uuid.UUID]bool{}
	for _, s := range sessions {
		id := sessionID(personID, s.Surface, s.Start)
		keep[id] = true
		entry := entryJSON(s)
		if old, ok := stored[id]; ok &&
			old.ended.UTC().Truncate(time.Microsecond).Equal(s.End.UTC().Truncate(time.Microsecond)) &&
			old.pages == s.Pages && old.engaged == s.EngagedSeconds && canonJSON(old.entry) == canonJSON(entry) {
			continue
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO analytics.sessions (id, person_id, surface, started_at, ended_at, pages, engaged_seconds, entry)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET ended_at = EXCLUDED.ended_at, pages = EXCLUDED.pages,
  engaged_seconds = EXCLUDED.engaged_seconds, entry = EXCLUDED.entry`,
			id, personID, s.Surface, s.Start, s.End, s.Pages, s.EngagedSeconds, entry); err != nil {
			return written, fmt.Errorf("analytics: write session: %w", err)
		}
		written++
	}
	var stale []string
	for id, row := range stored {
		if keep[id] || len(evs) == 0 || row.started.Before(evs[0].At) {
			continue
		}
		stale = append(stale, id.String())
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM analytics.sessions WHERE id = ANY($1::uuid[])`, stale); err != nil {
			return written, err
		}
		written += len(stale)
	}
	return written, nil
}
