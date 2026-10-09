package gen

import (
	"context"

	"github.com/google/uuid"
)

// public_page_promoter.sql.go — the promoter's own hosted pages
// (tickets.arenasoldout.com/{promoter_slug} and /{promoter_slug}/{event_slug},
// migration 0117). Hand-maintained wrappers for the queries at the end of
// queries/public_page.sql; the row shapes are shared with the organization
// pages so hfeed renders both through the same code.

// ─────────────────────────────────────────────────────────────────────────────
// GetHostedPageResolutionByPromoter
// ─────────────────────────────────────────────────────────────────────────────

const getHostedPageResolutionByPromoter = `-- name: GetHostedPageResolutionByPromoter :one
SELECT
    o.id, p.slug, p.name, o.logo_media_id, o.default_locale,
    e.id, e.slug, e.name, e.description, e.short_description,
    e.image_url, e.poster_media_id, e.age_rating,
    e.first_session_at, e.last_session_at,
    (
        SELECT v.timezone
        FROM   sessions s
        JOIN   venues v ON v.id = s.venue_id
        WHERE  s.event_id   = e.id
          AND  s.deleted_at IS NULL
          AND  s.status    <> 'cancelled'
        ORDER BY s.start_at ASC
        LIMIT 1
    ) AS first_session_timezone,
    (
        SELECT s.doors_open_at
        FROM   sessions s
        WHERE  s.event_id   = e.id
          AND  s.deleted_at IS NULL
          AND  s.status    <> 'cancelled'
        ORDER BY s.start_at ASC
        LIMIT 1
    ) AS first_session_doors_at,
    (
        SELECT count(*)
        FROM   sessions s
        WHERE  s.event_id   = e.id
          AND  s.deleted_at IS NULL
          AND  s.status    <> 'cancelled'
    ) AS session_count,
    ft.token
FROM org_promoters p
JOIN organizations o ON o.id = p.org_id
JOIN event_promoters epr ON epr.promoter_id = p.id
JOIN events e ON e.id = epr.event_id AND e.org_id = o.id
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE lower(p.slug) = lower($1)
  AND p.archived_at IS NULL
  AND o.deleted_at IS NULL
  AND lower(e.slug) = lower($2)
  AND e.deleted_at IS NULL
  AND e.status     = 'published'
  AND sc.org_id    = o.id
  AND sc.deleted_at IS NULL
  AND sc.settings #>> '{hosted_page,enabled}' = 'true'
  AND ft.is_active = true
ORDER BY ft.created_at DESC
LIMIT 1`

// GetHostedPageResolutionByPromoter is GetHostedPageResolution for the
// promoter's page: the event must be linked to the active promoter whose
// slug is promoterSlug. OrgSlug/OrgName of the returned row carry the
// PROMOTER's slug and name (the page is the promoter's); the logo and the
// default locale are the organization's. pgx.ErrNoRows on any missing
// link, which the handler maps to the same 404 as an unknown page.
func (q *Queries) GetHostedPageResolutionByPromoter(ctx context.Context, promoterSlug, eventSlug string) (HostedPageResolutionRow, error) {
	row := q.db.QueryRow(ctx, getHostedPageResolutionByPromoter, promoterSlug, eventSlug)
	var r HostedPageResolutionRow
	err := row.Scan(
		&r.OrgID, &r.OrgSlug, &r.OrgName, &r.OrgLogoMediaID, &r.OrgDefaultLocale,
		&r.EventID, &r.EventSlug, &r.EventName, &r.EventDescription, &r.EventShortDescription,
		&r.EventImageURL, &r.EventPosterMediaID, &r.EventAgeRating,
		&r.FirstSessionAt, &r.LastSessionAt, &r.FirstSessionTimezone, &r.FirstSessionDoorsAt,
		&r.SessionCount, &r.FeedToken,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetHostedPromoterPageByPromoter
// ─────────────────────────────────────────────────────────────────────────────

// HostedPromoterPagePromoterRow is the branding slice of a promoter's
// landing page: the promoter itself plus the organization it belongs to
// (logo, locale, hosted-channel eligibility — same rule as
// GetHostedPromoterPageOrg).
type HostedPromoterPagePromoterRow struct {
	PromoterID       uuid.UUID
	OrgID            uuid.UUID
	Slug             string
	Name             string
	OrgLogoMediaID   *uuid.UUID
	OrgDefaultLocale string
	HasHostedChannel bool
}

const getHostedPromoterPageByPromoter = `-- name: GetHostedPromoterPageByPromoter :one
SELECT
    p.id, o.id, p.slug, p.name, o.logo_media_id, o.default_locale,
    EXISTS (
        SELECT 1
        FROM   sales_channels sc
        JOIN   agent_feed_tokens ft ON ft.sales_channel_id = sc.id
        WHERE  sc.org_id      = o.id
          AND  sc.deleted_at  IS NULL
          AND  sc.settings #>> '{hosted_page,enabled}' = 'true'
          AND  ft.is_active   = true
    ) AS has_hosted_channel
FROM org_promoters p
JOIN organizations o ON o.id = p.org_id
WHERE lower(p.slug) = lower($1)
  AND p.archived_at IS NULL
  AND o.deleted_at IS NULL`

// GetHostedPromoterPageByPromoter resolves a promoter landing page by the
// promoter's slug (case-insensitive). pgx.ErrNoRows for an unknown or
// archived promoter, or a deleted organization.
func (q *Queries) GetHostedPromoterPageByPromoter(ctx context.Context, promoterSlug string) (HostedPromoterPagePromoterRow, error) {
	row := q.db.QueryRow(ctx, getHostedPromoterPageByPromoter, promoterSlug)
	var r HostedPromoterPagePromoterRow
	err := row.Scan(
		&r.PromoterID, &r.OrgID, &r.Slug, &r.Name, &r.OrgLogoMediaID, &r.OrgDefaultLocale,
		&r.HasHostedChannel,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListHostedPromoterPageEventsByPromoter
// ─────────────────────────────────────────────────────────────────────────────

const listHostedPromoterPageEventsByPromoter = `-- name: ListHostedPromoterPageEventsByPromoter :many
SELECT id, slug, name, short_description, image_url, poster_media_id,
       age_rating, first_session_at, last_session_at, first_session_timezone, first_session_doors_at,
       session_count, feed_token
FROM (
    SELECT DISTINCT ON (e.id)
        e.id, e.slug, e.name, e.short_description, e.image_url,
        e.poster_media_id, e.age_rating, e.first_session_at, e.last_session_at,
        ft.token AS feed_token,
        (
            SELECT v.timezone
            FROM   sessions s
            JOIN   venues v ON v.id = s.venue_id
            WHERE  s.event_id   = e.id
              AND  s.deleted_at IS NULL
              AND  s.status    <> 'cancelled'
            ORDER BY s.start_at ASC
            LIMIT 1
        ) AS first_session_timezone,
        (
            SELECT s.doors_open_at
            FROM   sessions s
            WHERE  s.event_id   = e.id
              AND  s.deleted_at IS NULL
              AND  s.status    <> 'cancelled'
            ORDER BY s.start_at ASC
            LIMIT 1
        ) AS first_session_doors_at,
        (
            SELECT count(*)
            FROM   sessions s
            WHERE  s.event_id   = e.id
              AND  s.deleted_at IS NULL
              AND  s.status    <> 'cancelled'
        ) AS session_count
    FROM events e
    JOIN event_promoters epr ON epr.event_id = e.id AND epr.promoter_id = $2
    JOIN event_publications ep ON ep.event_id = e.id
    JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
    JOIN sales_channels sc ON sc.id = ft.sales_channel_id
    WHERE e.org_id      = $1
      AND e.deleted_at  IS NULL
      AND e.status      = 'published'
      AND e.slug        IS NOT NULL
      AND sc.org_id     = e.org_id
      AND sc.deleted_at IS NULL
      AND sc.settings #>> '{hosted_page,enabled}' = 'true'
      AND ft.is_active  = true
      AND (e.last_session_at IS NULL OR e.last_session_at >= now())
    ORDER BY e.id, ft.created_at DESC
) matched
ORDER BY first_session_at ASC NULLS LAST, id ASC
LIMIT 200`

// ListHostedPromoterPageEventsByPromoter is ListHostedPromoterPageEvents
// narrowed to the events linked to promoterID (event_promoters) — the
// items of the promoter's own landing page.
func (q *Queries) ListHostedPromoterPageEventsByPromoter(ctx context.Context, orgID, promoterID uuid.UUID) ([]HostedPromoterPageEventRow, error) {
	rows, err := q.db.Query(ctx, listHostedPromoterPageEventsByPromoter, orgID, promoterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []HostedPromoterPageEventRow
	for rows.Next() {
		var r HostedPromoterPageEventRow
		if err := rows.Scan(
			&r.EventID, &r.EventSlug, &r.EventName, &r.EventShortDescription,
			&r.EventImageURL, &r.EventPosterMediaID, &r.EventAgeRating,
			&r.FirstSessionAt, &r.LastSessionAt, &r.FirstSessionTimezone, &r.FirstSessionDoorsAt,
			&r.SessionCount, &r.FeedToken,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
