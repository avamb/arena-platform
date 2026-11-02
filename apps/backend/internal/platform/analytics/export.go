package analytics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Export of profiles to the CRM (spec 50 section 9). The n8n workflow pulls
// the profiles that changed since its cursor and writes them into the card of
// the CRM contact. The e-mail address is resolved HERE, at query time, from
// the person's user or from their organizer application - the analytics
// schema itself keeps only its SHA-256.

// Page limits of ExportProfiles.
const (
	DefaultExportLimit = 100
	MaxExportLimit     = 200
)

// ErrBadCursor is returned for an `after` value that ExportProfiles did not
// issue.
var ErrBadCursor = errors.New("analytics: invalid profile cursor")

// ProfileCursor is the keyset position of a page: profiles are ordered by
// (changed_at, person_id), and the cursor is the last row already served.
// The next page starts strictly after it.
type ProfileCursor struct {
	ChangedAt time.Time
	PersonID  uuid.UUID
}

// Encode is the opaque form handed to the client.
func (c ProfileCursor) Encode() string {
	raw := strconv.FormatInt(c.ChangedAt.UTC().UnixMicro(), 10) + "." + c.PersonID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeProfileCursor parses what Encode produced.
func DecodeProfileCursor(s string) (ProfileCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return ProfileCursor{}, ErrBadCursor
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return ProfileCursor{}, ErrBadCursor
	}
	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return ProfileCursor{}, ErrBadCursor
	}
	pid, err := uuid.Parse(id)
	if err != nil {
		return ProfileCursor{}, ErrBadCursor
	}
	return ProfileCursor{ChangedAt: time.UnixMicro(us).UTC(), PersonID: pid}, nil
}

// ExportQuery selects a page of changed profiles.
type ExportQuery struct {
	// ChangedSince is a lower bound (inclusive) on changed_at, used by a
	// client that has no cursor yet. Zero means "from the beginning".
	ChangedSince time.Time
	// After is the cursor of the previous page: the page starts strictly
	// after it.
	After *ProfileCursor
	// Limit defaults to DefaultExportLimit and is capped at MaxExportLimit.
	Limit int
	// Now zero means time.Now (decay of the activity explanation).
	Now     time.Time
	Weights ScoreWeights
}

// ExportedProfile is one profile as the CRM sync needs it.
type ExportedProfile struct {
	PersonID uuid.UUID
	// TwentyPersonID is empty when the person has no CRM contact yet.
	TwentyPersonID string
	// Email is empty when none is known (a visitor who never gave one, or an
	// application whose personal data was erased).
	Email string
	// MayCreateContact says that the CRM sync may CREATE a contact for this
	// person when it finds none: the person has an organizer application (they
	// gave their address to us for that) or is already tied to a CRM contact.
	// An account-only user (an invited manager, staff) can have an existing
	// card updated but never gets a new one.
	MayCreateContact bool
	Stage            string

	FirstTouch, LastTouch Touch
	LastSeenAt            *time.Time

	Visits30d         int
	EngagedMinutes30d int
	Articles          []ArticleRead
	// Milestones maps a milestone name to the time it happened.
	Milestones map[string]time.Time
	// GMV30d maps a currency to minor units sold in the last 30 days.
	GMV30d map[string]int64

	ActivityScore    int
	ScoreExplanation string
	ChangedAt        time.Time
}

// ExportPage is one answer of ExportProfiles.
type ExportPage struct {
	Profiles []ExportedProfile
	// NextCursor is ALWAYS set: the position after the last profile served,
	// or - when the page is empty - the position the caller already had. A
	// client stores it and sends it as `after` next time.
	NextCursor string
	// Through is the start time of the last FINISHED rollup run. Only
	// profiles changed at or before it are served (zero: no run finished,
	// nothing is served).
	Through time.Time
}

// TouchLabel is the short text of a touch for a CRM field: "email / outreach",
// "google", "direct". Empty for an empty touch.
func TouchLabel(t Touch) string {
	if t.Empty() {
		return ""
	}
	label := t.Source
	if label == "" {
		label = t.Kind
	}
	if t.Campaign != "" {
		label += " / " + t.Campaign
	}
	return label
}

// LastFinishedRollup is the start time of the last rollup run that finished
// without a failed person (analytics.state); zero when none did. Every profile
// stamped at or before it is complete, so a client that reads only up to it
// and keeps a keyset cursor can never miss a row that commits late.
func LastFinishedRollup(ctx context.Context, q Querier) (time.Time, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT value FROM analytics.state WHERE key = $1`, rollupStateKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("analytics: read rollup state: %w", err)
	}
	var st rollupState
	_ = json.Unmarshal(raw, &st)
	return st.Through, nil
}

const exportSQL = `
SELECT pr.person_id, COALESCE(p.twenty_person_id, ''),
       COALESCE(NULLIF(btrim(u.email), ''), app.applicant_email, ''),
       (p.twenty_person_id IS NOT NULL OR app.applicant_email IS NOT NULL),
       p.first_touch, p.last_touch, pr.stage, pr.last_seen_at, pr.visits_30d, pr.engaged_minutes_30d,
       pr.articles, pr.milestones, pr.activity_score, pr.changed_at
  FROM analytics.profiles pr
  JOIN analytics.people p ON p.id = pr.person_id
  LEFT JOIN users u ON u.id = p.user_id
  LEFT JOIN LATERAL (
        SELECT a.applicant_email
          FROM onboarding_applications a
         WHERE a.purged_at IS NULL
           AND encode(sha256(convert_to(a.applicant_email, 'UTF8')), 'hex') IN
               (SELECT i.value FROM analytics.identities i
                 WHERE i.person_id = pr.person_id AND i.kind = 'email_sha256')
         ORDER BY a.created_at DESC
         LIMIT 1) app ON TRUE
 WHERE pr.changed_at >= $1 AND pr.changed_at <= $2
   AND ($3::timestamptz IS NULL OR (pr.changed_at, pr.person_id) > ($3::timestamptz, $4::uuid))
 ORDER BY pr.changed_at, pr.person_id
 LIMIT $5`

// ExportProfiles returns one page of profiles changed since q, oldest change
// first. It reads; it never writes.
func ExportProfiles(ctx context.Context, db Querier, q ExportQuery) (ExportPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultExportLimit
	}
	if limit > MaxExportLimit {
		limit = MaxExportLimit
	}
	now := q.Now
	if now.IsZero() {
		now = time.Now()
	}
	w := q.Weights
	if w == (ScoreWeights{}) {
		w = DefaultScoreWeights
	}
	since := q.ChangedSince
	if since.IsZero() {
		since = time.Unix(0, 0).UTC()
	}

	// The cursor of an empty answer is the position the caller came with.
	hold := ProfileCursor{ChangedAt: time.Unix(0, 0).UTC(), PersonID: uuid.Nil}
	if !q.ChangedSince.IsZero() {
		hold.ChangedAt = q.ChangedSince.UTC().Add(-time.Microsecond)
	}
	if q.After != nil {
		hold = *q.After
	}
	page := ExportPage{NextCursor: hold.Encode()}

	through, err := LastFinishedRollup(ctx, db)
	if err != nil {
		return page, err
	}
	page.Through = through
	if through.IsZero() {
		return page, nil
	}

	var afterAt, afterID any
	if q.After != nil {
		afterAt, afterID = q.After.ChangedAt.UTC(), q.After.PersonID
	}
	rows, err := db.Query(ctx, exportSQL, since, through, afterAt, afterID, limit)
	if err != nil {
		return page, fmt.Errorf("analytics: export profiles: %w", err)
	}
	var out []ExportedProfile
	for rows.Next() {
		var p ExportedProfile
		var ft, lt, art, ms []byte
		if err := rows.Scan(&p.PersonID, &p.TwentyPersonID, &p.Email, &p.MayCreateContact, &ft, &lt, &p.Stage, &p.LastSeenAt,
			&p.Visits30d, &p.EngagedMinutes30d, &art, &ms, &p.ActivityScore, &p.ChangedAt); err != nil {
			rows.Close()
			return page, fmt.Errorf("analytics: scan exported profile: %w", err)
		}
		p.FirstTouch, p.LastTouch = decodeTouch(ft), decodeTouch(lt)
		p.Articles = decodeArticles(art)
		p.Milestones, p.GMV30d = decodeMilestones(ms)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(out) == 0 {
		return page, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i := range out {
		ids[i] = out[i].PersonID
	}
	sessions, err := loadStoredSessions(ctx, db, ids)
	if err != nil {
		return page, err
	}
	for i := range out {
		p := &out[i]
		p.ScoreExplanation = explainStored(storedProfile{
			Score: p.ActivityScore, Articles: p.Articles, Milestones: p.Milestones,
		}, sessions[p.PersonID], now, w)
	}
	last := out[len(out)-1]
	page.Profiles = out
	page.NextCursor = ProfileCursor{ChangedAt: last.ChangedAt, PersonID: last.PersonID}.Encode()
	return page, nil
}

// ─── the one-line explanation of the activity index ────────────────────────

// storedProfile is what the explanation needs from analytics.profiles.
type storedProfile struct {
	Score      int
	Articles   []ArticleRead
	Milestones map[string]time.Time
}

// storedSession is one analytics.sessions row, reduced to what the index
// counts.
type storedSession struct {
	Start      time.Time
	EmailClick bool
}

func loadStoredSessions(ctx context.Context, db Querier, people []uuid.UUID) (map[uuid.UUID][]storedSession, error) {
	rows, err := db.Query(ctx, `
SELECT person_id, started_at, COALESCE(entry->>'kind', '') = 'email_click'
  FROM analytics.sessions WHERE person_id = ANY($1::uuid[])`, people)
	if err != nil {
		return nil, fmt.Errorf("analytics: read stored sessions: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID][]storedSession{}
	for rows.Next() {
		var id uuid.UUID
		var s storedSession
		if err := rows.Scan(&id, &s.Start, &s.EmailClick); err != nil {
			return nil, err
		}
		out[id] = append(out[id], s)
	}
	return out, rows.Err()
}

// explainStored renders the activity index from the STORED profile and
// sessions - it never touches the raw events. Everything the index counts is
// stored except visits to the price page, so when the items rebuilt here add
// up to less than the stored score the difference is shown as one line of its
// own instead of a number that does not match.
func explainStored(p storedProfile, sessions []storedSession, now time.Time, w ScoreWeights) string {
	var items []ScoreItem
	for _, s := range sessions {
		items = append(items, ScoreItem{Kind: ScoreVisit, At: s.Start})
		if s.EmailClick {
			items = append(items, ScoreItem{Kind: ScoreEmailClick, At: s.Start})
		}
	}
	for _, a := range p.Articles {
		items = append(items, ScoreItem{Kind: ScoreArticle, At: a.At})
	}
	if t, ok := p.Milestones[MsApplicationStarted]; ok {
		items = append(items, ScoreItem{Kind: ScoreApplicationStarted, At: t})
	}
	if t, ok := p.Milestones[MsApplicationSubmitted]; ok {
		items = append(items, ScoreItem{Kind: ScoreApplicationSubmitted, At: t})
	}
	text := ExplainScore(items, now, w)
	if delta := p.Score - Score(items, now, w); delta != 0 {
		extra := fmt.Sprintf("price page and other = %+d", delta)
		if text == "no activity" {
			return extra
		}
		text += "; " + extra
	}
	return text
}

// ExplainProfile returns the one-line "why this number" of a person's
// activity index, built from the stored profile and sessions. It returns ""
// when the person has no profile.
func ExplainProfile(ctx context.Context, db Querier, personID uuid.UUID, now time.Time, w ScoreWeights) (string, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if w == (ScoreWeights{}) {
		w = DefaultScoreWeights
	}
	var sp storedProfile
	var art, ms []byte
	err := db.QueryRow(ctx, `SELECT activity_score, articles, milestones FROM analytics.profiles WHERE person_id = $1`, personID).
		Scan(&sp.Score, &art, &ms)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("analytics: read profile: %w", err)
	}
	sp.Articles = decodeArticles(art)
	sp.Milestones, _ = decodeMilestones(ms)
	sessions, err := loadStoredSessions(ctx, db, []uuid.UUID{personID})
	if err != nil {
		return "", err
	}
	return explainStored(sp, sessions[personID], now, w), nil
}

func decodeArticles(raw []byte) []ArticleRead {
	var in []struct {
		Path    string `json:"path"`
		At      string `json:"at"`
		Seconds int    `json:"seconds"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return nil
	}
	out := make([]ArticleRead, 0, len(in))
	for _, a := range in {
		t, _ := time.Parse(time.RFC3339, a.At)
		out = append(out, ArticleRead{Path: a.Path, At: t, Seconds: a.Seconds})
	}
	return out
}

// decodeMilestones splits the stored milestones object: every key is a time
// except gmv_30d, a currency -> minor units map.
func decodeMilestones(raw []byte) (map[string]time.Time, map[string]int64) {
	ms := map[string]time.Time{}
	var gmv map[string]int64
	var in map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return ms, nil
	}
	for k, v := range in {
		if k == MsGMV30d {
			_ = json.Unmarshal(v, &gmv)
			continue
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			ms[k] = t
		}
	}
	return ms, gmv
}
