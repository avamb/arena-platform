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
	// ChangedSince is INCLUSIVE: a profile whose changed_at equals it is
	// returned again. The rollup stamps every profile of one run with the
	// run's start time and commits them one by one, so an exclusive bound
	// would lose the rows that commit after a reader saw the first of them.
	// Zero means "from the beginning".
	ChangedSince time.Time
	After        *ProfileCursor
	// Limit defaults to DefaultExportLimit and is capped at MaxExportLimit.
	Limit int
	// Now nil means time.Now (decay of the activity explanation).
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
	Stage string

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

const exportSQL = `
SELECT pr.person_id, COALESCE(p.twenty_person_id, ''),
       COALESCE(NULLIF(btrim(u.email), ''), app.applicant_email, ''),
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
 WHERE pr.changed_at >= $1
   AND ($2::timestamptz IS NULL OR (pr.changed_at, pr.person_id) > ($2::timestamptz, $3::uuid))
 ORDER BY pr.changed_at, pr.person_id
 LIMIT $4`

// ExportProfiles returns one page of profiles changed since q.ChangedSince,
// oldest change first, and the cursor of the next page ("" on the last one).
// It reads; it never writes.
func ExportProfiles(ctx context.Context, db Querier, q ExportQuery) ([]ExportedProfile, string, error) {
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
	var afterAt, afterID any
	if q.After != nil {
		afterAt, afterID = q.After.ChangedAt.UTC(), q.After.PersonID
	}

	rows, err := db.Query(ctx, exportSQL, since, afterAt, afterID, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("analytics: export profiles: %w", err)
	}
	var out []ExportedProfile
	for rows.Next() {
		var p ExportedProfile
		var ft, lt, art, ms []byte
		if err := rows.Scan(&p.PersonID, &p.TwentyPersonID, &p.Email, &ft, &lt, &p.Stage, &p.LastSeenAt,
			&p.Visits30d, &p.EngagedMinutes30d, &art, &ms, &p.ActivityScore, &p.ChangedAt); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("analytics: scan exported profile: %w", err)
		}
		p.FirstTouch, p.LastTouch = decodeTouch(ft), decodeTouch(lt)
		p.Articles = decodeArticles(art)
		p.Milestones, p.GMV30d = decodeMilestones(ms)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = ProfileCursor{ChangedAt: last.ChangedAt, PersonID: last.PersonID}.Encode()
	}
	for i := range out {
		expl, err := explainPerson(ctx, db, &out[i], now, w)
		if err != nil {
			return nil, "", err
		}
		out[i].ScoreExplanation = expl
	}
	return out, next, nil
}

// explainPerson rebuilds the score items the rollup used (sessions and
// article reads from the raw events, the two application milestones from the
// stored profile) and renders them with ExplainScore.
func explainPerson(ctx context.Context, db Querier, p *ExportedProfile, now time.Time, w ScoreWeights) (string, error) {
	evs, _, err := loadPersonEvents(ctx, db, p.PersonID)
	if err != nil {
		return "", err
	}
	sessions := SplitSessions(evs)
	items := SessionScoreItems(sessions, ArticleReads(sessions))
	if t, ok := p.Milestones[MsApplicationStarted]; ok {
		items = append(items, ScoreItem{Kind: ScoreApplicationStarted, At: t})
	}
	if t, ok := p.Milestones[MsApplicationSubmitted]; ok {
		items = append(items, ScoreItem{Kind: ScoreApplicationSubmitted, At: t})
	}
	return ExplainScore(items, now, w), nil
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
