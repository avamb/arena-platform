// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: event_page_stats.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PageTrackTarget is the event a page address names: its id, its
// organization and the IANA zone of its venue (nil when it has no session
// yet), which decides the calendar day a hit belongs to.
type PageTrackTarget struct {
	EventID  uuid.UUID
	OrgID    uuid.UUID
	Timezone *string
}

const resolvePageTrackTarget = `-- name: ResolvePageTrackTarget :one
SELECT e.id, e.org_id,
       (
           SELECT v.timezone
           FROM   sessions s
           JOIN   venues v ON v.id = s.venue_id
           WHERE  s.event_id   = e.id
             AND  s.deleted_at IS NULL
             AND  s.status    <> 'cancelled'
           ORDER BY s.start_at ASC
           LIMIT 1
       ) AS timezone
FROM organizations o
JOIN events e ON e.org_id = o.id
WHERE lower(o.slug) = lower($1)
  AND o.deleted_at IS NULL
  AND lower(e.slug) = lower($2)
  AND e.deleted_at IS NULL
  AND e.status     = 'published'
  AND EXISTS (
      SELECT 1
      FROM   event_publications ep
      JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
      JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
      WHERE  ep.event_id = e.id
        AND  sc.org_id   = o.id
        AND  sc.deleted_at IS NULL
        AND  sc.settings #>> '{hosted_page,enabled}' = 'true'
        AND  ft.is_active = true
  )
LIMIT 1`

// ResolvePageTrackTarget finds the event of an organization's page with the
// eligibility of GetHostedPageResolution, without the page's heavy columns.
// pgx.ErrNoRows on any missing link.
func (q *Queries) ResolvePageTrackTarget(ctx context.Context, orgSlug, eventSlug string) (PageTrackTarget, error) {
	var t PageTrackTarget
	err := q.db.QueryRow(ctx, resolvePageTrackTarget, orgSlug, eventSlug).Scan(&t.EventID, &t.OrgID, &t.Timezone)
	return t, err
}

const resolvePageTrackTargetByPromoter = `-- name: ResolvePageTrackTargetByPromoter :one
SELECT e.id, e.org_id,
       (
           SELECT v.timezone
           FROM   sessions s
           JOIN   venues v ON v.id = s.venue_id
           WHERE  s.event_id   = e.id
             AND  s.deleted_at IS NULL
             AND  s.status    <> 'cancelled'
           ORDER BY s.start_at ASC
           LIMIT 1
       ) AS timezone
FROM org_promoters p
JOIN organizations o ON o.id = p.org_id
JOIN event_promoters epr ON epr.promoter_id = p.id
JOIN events e ON e.id = epr.event_id AND e.org_id = o.id
WHERE lower(p.slug) = lower($1)
  AND p.archived_at IS NULL
  AND o.deleted_at IS NULL
  AND lower(e.slug) = lower($2)
  AND e.deleted_at IS NULL
  AND e.status     = 'published'
  AND EXISTS (
      SELECT 1
      FROM   event_publications ep
      JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
      JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
      WHERE  ep.event_id = e.id
        AND  sc.org_id   = o.id
        AND  sc.deleted_at IS NULL
        AND  sc.settings #>> '{hosted_page,enabled}' = 'true'
        AND  ft.is_active = true
  )
LIMIT 1`

// ResolvePageTrackTargetByPromoter is ResolvePageTrackTarget for a promoter's
// page (GetHostedPageResolutionByPromoter's eligibility).
func (q *Queries) ResolvePageTrackTargetByPromoter(ctx context.Context, promoterSlug, eventSlug string) (PageTrackTarget, error) {
	var t PageTrackTarget
	err := q.db.QueryRow(ctx, resolvePageTrackTargetByPromoter, promoterSlug, eventSlug).Scan(&t.EventID, &t.OrgID, &t.Timezone)
	return t, err
}

const recordEventPageHit = `-- name: RecordEventPageHit :exec
WITH pair AS (
    SELECT (
               ($5::text = '' AND $6::text = '')
               OR EXISTS (
                   SELECT 1 FROM event_page_stats
                   WHERE event_id = $1 AND day = $3 AND utm_source = $5::text AND utm_campaign = $6::text
               )
               OR (
                   SELECT count(DISTINCT (utm_source, utm_campaign))
                   FROM   event_page_stats
                   WHERE  event_id = $1 AND day = $3
                     AND (utm_source <> '' OR utm_campaign <> '')
               ) < $9::bigint
           ) AS keep
)
INSERT INTO event_page_stats (event_id, org_id, day, kind, utm_source, utm_campaign, hits, suspect_hits)
SELECT $1, $2, $3, $4::text,
       CASE WHEN keep THEN $5::text ELSE '' END,
       CASE WHEN keep THEN $6::text ELSE '' END,
       $7::bigint, $8::bigint
FROM pair
ON CONFLICT (event_id, day, kind, utm_source, utm_campaign) DO UPDATE
   SET hits         = event_page_stats.hits         + EXCLUDED.hits,
       suspect_hits = event_page_stats.suspect_hits + EXCLUDED.suspect_hits`

// RecordEventPageHitParams is one beacon hit. Suspect puts the hit in
// suspect_hits instead of hits. MaxUTMPairs bounds the different utm pairs one
// event keeps per day (see the SQL comment).
type RecordEventPageHitParams struct {
	EventID     uuid.UUID
	OrgID       uuid.UUID
	Day         time.Time
	Kind        string
	UTMSource   string
	UTMCampaign string
	Suspect     bool
	MaxUTMPairs int64
}

// RecordEventPageHit adds one hit to the event's counter row for the day.
func (q *Queries) RecordEventPageHit(ctx context.Context, p RecordEventPageHitParams) error {
	var hits, suspect int64 = 1, 0
	if p.Suspect {
		hits, suspect = 0, 1
	}
	_, err := q.db.Exec(ctx, recordEventPageHit,
		p.EventID, p.OrgID, p.Day, p.Kind, p.UTMSource, p.UTMCampaign, hits, suspect, p.MaxUTMPairs)
	return err
}

const getEventPageStatsZone = `-- name: GetEventPageStatsZone :one
SELECT (
           SELECT v.timezone
           FROM   sessions s
           JOIN   venues v ON v.id = s.venue_id
           WHERE  s.event_id   = e.id
             AND  s.deleted_at IS NULL
             AND  s.status    <> 'cancelled'
           ORDER BY s.start_at ASC
           LIMIT 1
       ) AS timezone,
       e.slug
FROM events e
WHERE e.id = $1 AND e.org_id = $2 AND e.deleted_at IS NULL`

// EventPageStatsHeader is what the organizer's statistics routes need to know
// about an event: the venue zone and the slug (for a file name).
type EventPageStatsHeader struct {
	Timezone *string
	Slug     *string
}

// GetEventPageStatsHeader reads the header of an event of the organization;
// pgx.ErrNoRows means the event is not in that organization or is deleted.
func (q *Queries) GetEventPageStatsHeader(ctx context.Context, eventID, orgID uuid.UUID) (EventPageStatsHeader, error) {
	var h EventPageStatsHeader
	err := q.db.QueryRow(ctx, getEventPageStatsZone, eventID, orgID).Scan(&h.Timezone, &h.Slug)
	return h, err
}

// EventPageStatDayRow is the sum over utm values of one (day, kind).
type EventPageStatDayRow struct {
	Day         time.Time
	Kind        string
	Hits        int64
	SuspectHits int64
}

const listEventPageStatsDays = `-- name: ListEventPageStatsDays :many
SELECT day, kind, sum(hits)::bigint, sum(suspect_hits)::bigint
FROM   event_page_stats
WHERE  event_id = $1 AND day BETWEEN $2 AND $3
GROUP BY day, kind
ORDER BY day, kind`

// ListEventPageStatsDays lists the (day, kind) sums that have data in [from, to].
func (q *Queries) ListEventPageStatsDays(ctx context.Context, eventID uuid.UUID, from, to time.Time) ([]EventPageStatDayRow, error) {
	rows, err := q.db.Query(ctx, listEventPageStatsDays, eventID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventPageStatDayRow{}
	for rows.Next() {
		var r EventPageStatDayRow
		if err := rows.Scan(&r.Day, &r.Kind, &r.Hits, &r.SuspectHits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventPageStatSourceRow is one utm pair of the page_view counters.
type EventPageStatSourceRow struct {
	UTMSource   string
	UTMCampaign string
	Hits        int64
	SuspectHits int64
}

const listEventPageStatsSources = `-- name: ListEventPageStatsSources :many
SELECT utm_source, utm_campaign, sum(hits)::bigint, sum(suspect_hits)::bigint
FROM   event_page_stats
WHERE  event_id = $1 AND kind = 'page_view' AND day BETWEEN $2 AND $3
GROUP BY utm_source, utm_campaign
ORDER BY sum(hits) DESC, utm_source, utm_campaign
LIMIT $4`

// ListEventPageStatsSources lists the utm pairs of the page views in
// [from, to], biggest first, at most limit.
func (q *Queries) ListEventPageStatsSources(ctx context.Context, eventID uuid.UUID, from, to time.Time, limit int32) ([]EventPageStatSourceRow, error) {
	rows, err := q.db.Query(ctx, listEventPageStatsSources, eventID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventPageStatSourceRow{}
	for rows.Next() {
		var r EventPageStatSourceRow
		if err := rows.Scan(&r.UTMSource, &r.UTMCampaign, &r.Hits, &r.SuspectHits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventPageStatRow is one raw counter row.
type EventPageStatRow struct {
	Day         time.Time
	Kind        string
	UTMSource   string
	UTMCampaign string
	Hits        int64
	SuspectHits int64
}

const listEventPageStatsRows = `-- name: ListEventPageStatsRows :many
SELECT day, kind, utm_source, utm_campaign, hits, suspect_hits
FROM   event_page_stats
WHERE  event_id = $1 AND day BETWEEN $2 AND $3
ORDER BY day, kind, utm_source, utm_campaign
LIMIT $4`

// ListEventPageStatsRows lists the raw counter rows in [from, to] for the CSV,
// at most limit (the caller asks for one more than it will write to tell a
// file over the cap from one that fits).
func (q *Queries) ListEventPageStatsRows(ctx context.Context, eventID uuid.UUID, from, to time.Time, limit int32) ([]EventPageStatRow, error) {
	rows, err := q.db.Query(ctx, listEventPageStatsRows, eventID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventPageStatRow{}
	for rows.Next() {
		var r EventPageStatRow
		if err := rows.Scan(&r.Day, &r.Kind, &r.UTMSource, &r.UTMCampaign, &r.Hits, &r.SuspectHits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
