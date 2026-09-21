// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: public_page.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// GetHostedPageResolution
// ─────────────────────────────────────────────────────────────────────────────

// HostedPageResolutionRow is the result of resolving an (org_slug, event_slug)
// pair to the org/event/feed-token context the hosted sales page needs to
// render. See public_page.sql for the exact resolution rule.
type HostedPageResolutionRow struct {
	OrgID                 uuid.UUID
	OrgSlug               string
	OrgName               string
	OrgLogoMediaID        *uuid.UUID
	OrgDefaultLocale      string
	EventID               uuid.UUID
	EventSlug             *string
	EventName             string
	EventDescription      *string
	EventShortDescription *string
	EventImageURL         *string
	EventPosterMediaID    *uuid.UUID
	EventAgeRating        *string
	FirstSessionAt        *time.Time
	LastSessionAt         *time.Time
	FirstSessionTimezone  *string
	FeedToken             string
}

const getHostedPageResolution = `-- name: GetHostedPageResolution :one
SELECT
    o.id, o.slug, o.name, o.logo_media_id, o.default_locale,
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
    ft.token
FROM organizations o
JOIN events e ON e.org_id = o.id
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE lower(o.slug) = lower($1)
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

// GetHostedPageResolution resolves the hosted-page context for an
// (org_slug, event_slug) pair in one round trip: active org -> published
// non-deleted event owned by it -> an active publication of that event on a
// channel of the SAME org with settings.hosted_page.enabled = true and not
// soft-deleted -> the newest active, non-revoked feed token of that channel.
// Slugs are matched case-insensitively (lower() on both sides).
//
// Returns pgx.ErrNoRows when any link of that chain is missing. The caller
// (hfeed.HandlePublicPage) must map every miss to the SAME 404 response so
// the failing step is never revealed to the caller.
func (q *Queries) GetHostedPageResolution(ctx context.Context, orgSlug, eventSlug string) (HostedPageResolutionRow, error) {
	row := q.db.QueryRow(ctx, getHostedPageResolution, orgSlug, eventSlug)
	var r HostedPageResolutionRow
	err := row.Scan(
		&r.OrgID, &r.OrgSlug, &r.OrgName, &r.OrgLogoMediaID, &r.OrgDefaultLocale,
		&r.EventID, &r.EventSlug, &r.EventName, &r.EventDescription, &r.EventShortDescription,
		&r.EventImageURL, &r.EventPosterMediaID, &r.EventAgeRating,
		&r.FirstSessionAt, &r.LastSessionAt, &r.FirstSessionTimezone,
		&r.FeedToken,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetHostedPromoterPageOrg
// ─────────────────────────────────────────────────────────────────────────────

// HostedPromoterPageOrgRow is the org-branding slice of the promoter landing
// page (tickets.arenasoldout.com/{org_slug}) plus whether the org has at
// least one hosted-page-eligible channel. See public_page.sql for the exact
// eligibility rule.
type HostedPromoterPageOrgRow struct {
	OrgID            uuid.UUID
	OrgSlug          string
	OrgName          string
	OrgLogoMediaID   *uuid.UUID
	OrgDefaultLocale string
	HasHostedChannel bool
}

const getHostedPromoterPageOrg = `-- name: GetHostedPromoterPageOrg :one
SELECT
    o.id, o.slug, o.name, o.logo_media_id, o.default_locale,
    EXISTS (
        SELECT 1
        FROM   sales_channels sc
        JOIN   agent_feed_tokens ft ON ft.sales_channel_id = sc.id
        WHERE  sc.org_id      = o.id
          AND  sc.deleted_at  IS NULL
          AND  sc.settings #>> '{hosted_page,enabled}' = 'true'
          AND  ft.is_active   = true
    ) AS has_hosted_channel
FROM organizations o
WHERE lower(o.slug) = lower($1)
  AND o.deleted_at IS NULL`

// GetHostedPromoterPageOrg resolves the org-branding slice of the promoter
// landing page and, in the same round trip, whether the org has at least one
// hosted-page-eligible channel. Returns pgx.ErrNoRows when the org slug is
// unknown or the org is soft-deleted; the caller must ALSO check
// HasHostedChannel and answer the same 404 page.not_found when it is false
// (an org can exist with zero hosted channels — that must not be
// distinguishable from an unknown org, same indistinguishable-404 rule as
// GetHostedPageResolution).
func (q *Queries) GetHostedPromoterPageOrg(ctx context.Context, orgSlug string) (HostedPromoterPageOrgRow, error) {
	row := q.db.QueryRow(ctx, getHostedPromoterPageOrg, orgSlug)
	var r HostedPromoterPageOrgRow
	err := row.Scan(
		&r.OrgID, &r.OrgSlug, &r.OrgName, &r.OrgLogoMediaID, &r.OrgDefaultLocale,
		&r.HasHostedChannel,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListHostedPromoterPageEvents
// ─────────────────────────────────────────────────────────────────────────────

// HostedPromoterPageEventRow is one event of the promoter landing page's
// event list. venue_names is hydrated separately by the caller via
// ListEventVenueNames (see public_page.sql).
type HostedPromoterPageEventRow struct {
	EventID               uuid.UUID
	EventSlug             *string
	EventName             string
	EventShortDescription *string
	EventImageURL         *string
	EventPosterMediaID    *uuid.UUID
	EventAgeRating        *string
	FirstSessionAt        *time.Time
	LastSessionAt         *time.Time
	FirstSessionTimezone  *string
	// FeedToken is the active feed token the event is published through —
	// the same value GetHostedPageResolution returns on the event's own
	// page. The promoter page mounts a ticket picker per date and cannot
	// do it without one.
	FeedToken string
}

const listHostedPromoterPageEvents = `-- name: ListHostedPromoterPageEvents :many
SELECT id, slug, name, short_description, image_url, poster_media_id,
       age_rating, first_session_at, last_session_at, first_session_timezone,
       feed_token
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
        ) AS first_session_timezone
    FROM events e
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

// ListHostedPromoterPageEvents lists every event of the org that would
// individually resolve on GET /v1/public/pages/{org_slug}/{event_slug},
// upcoming-first (first_session_at ascending, NULLs last), capped at 200.
// See public_page.sql for the exact eligibility rule.
func (q *Queries) ListHostedPromoterPageEvents(ctx context.Context, orgID uuid.UUID) ([]HostedPromoterPageEventRow, error) {
	rows, err := q.db.Query(ctx, listHostedPromoterPageEvents, orgID)
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
			&r.FirstSessionAt, &r.LastSessionAt, &r.FirstSessionTimezone,
			&r.FeedToken,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
