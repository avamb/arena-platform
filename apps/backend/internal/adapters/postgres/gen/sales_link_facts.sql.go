// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: sales_link_facts.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// SalesLinkTargetFactsRow is what the sales-link fall chain looks at. Slugs are
// empty strings when the row is missing, never NULL.
type SalesLinkTargetFactsRow struct {
	OrgSlug               string
	EventSlug             string
	EventPublished        bool
	EventPromoterSlug     string
	EventPromoterArchived bool
	SessionLive           bool
	PromoterSlug          string
	PromoterArchived      bool
}

const getSalesLinkTargetFacts = `-- name: GetSalesLinkTargetFacts :one
SELECT o.slug,
       COALESCE(e.slug, '')                                              AS event_slug,
       COALESCE(e.status = 'published' AND e.deleted_at IS NULL, false)  AS event_published,
       COALESCE(ep.slug, '')                                             AS event_promoter_slug,
       COALESCE(ep.archived_at IS NOT NULL, false)                       AS event_promoter_archived,
       (s.id IS NOT NULL)                                                AS session_live,
       COALESCE(lp.slug, '')                                             AS promoter_slug,
       COALESCE(lp.archived_at IS NOT NULL, false)                       AS promoter_archived
FROM organizations o
LEFT JOIN events e
       ON e.org_id = o.id
      AND e.id = COALESCE($2::uuid, (SELECT s0.event_id FROM sessions s0 WHERE s0.id = $3::uuid))
LEFT JOIN sessions s
       ON s.id = $3::uuid
      AND s.event_id = e.id
      AND s.deleted_at IS NULL
      AND s.status <> 'cancelled'
LEFT JOIN event_promoters epr
       ON epr.event_id = e.id
      AND epr.org_id = o.id
LEFT JOIN org_promoters ep
       ON ep.id = epr.promoter_id
      AND ep.org_id = o.id
LEFT JOIN org_promoters lp
       ON lp.id = $4::uuid
      AND lp.org_id = o.id
WHERE o.id = $1
  AND o.deleted_at IS NULL`

// GetSalesLinkTargetFacts loads the facts of a link target in one round trip.
// eventID, sessionID and promoterID may be nil. pgx.ErrNoRows when the
// organization does not exist or is deleted.
func (q *Queries) GetSalesLinkTargetFacts(ctx context.Context, orgID uuid.UUID, eventID, sessionID, promoterID *uuid.UUID) (SalesLinkTargetFactsRow, error) {
	var r SalesLinkTargetFactsRow
	err := q.db.QueryRow(ctx, getSalesLinkTargetFacts, orgID, eventID, sessionID, promoterID).Scan(
		&r.OrgSlug, &r.EventSlug, &r.EventPublished, &r.EventPromoterSlug, &r.EventPromoterArchived,
		&r.SessionLive, &r.PromoterSlug, &r.PromoterArchived,
	)
	return r, err
}
