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
	FeedToken             string
}

const getHostedPageResolution = `-- name: GetHostedPageResolution :one
SELECT
    o.id, o.slug, o.name, o.logo_media_id, o.default_locale,
    e.id, e.slug, e.name, e.description, e.short_description,
    e.image_url, e.poster_media_id, e.age_rating,
    e.first_session_at, e.last_session_at,
    ft.token
FROM organizations o
JOIN events e ON e.org_id = o.id
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
WHERE o.slug       = $1
  AND o.deleted_at IS NULL
  AND e.slug       = $2
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
		&r.FirstSessionAt, &r.LastSessionAt,
		&r.FeedToken,
	)
	return r, err
}
