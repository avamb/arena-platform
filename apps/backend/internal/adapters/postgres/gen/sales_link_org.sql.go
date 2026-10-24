// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: sales_link_org.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// SalesLinkTargetCheckRow is what the organization routes of sales links
// (QL-4) need to know about a target before a link may point at it. Slugs are
// empty strings when the row is missing, never NULL.
type SalesLinkTargetCheckRow struct {
	OrgSlug      string
	EventID      *uuid.UUID // the live event of the organization the target names, nil when none
	EventSlug    string
	SessionOK    bool // the session is live and belongs to EventID
	PromoterOK   bool // the promoter belongs to the organization (archived or not)
	PromoterSlug string
}

const checkSalesLinkTarget = `-- name: CheckSalesLinkTarget :one
SELECT o.slug,
       e.id,
       COALESCE(e.slug, '')  AS event_slug,
       (s.id IS NOT NULL)    AS session_ok,
       (lp.id IS NOT NULL)   AS promoter_ok,
       COALESCE(lp.slug, '') AS promoter_slug
FROM organizations o
LEFT JOIN events e
       ON e.org_id = o.id
      AND e.deleted_at IS NULL
      AND e.id = COALESCE($2::uuid, (SELECT s0.event_id FROM sessions s0
                                      WHERE s0.id = $3::uuid AND s0.deleted_at IS NULL))
LEFT JOIN sessions s
       ON s.id = $3::uuid
      AND s.event_id = e.id
      AND s.deleted_at IS NULL
LEFT JOIN org_promoters lp
       ON lp.id = $4::uuid
      AND lp.org_id = o.id
WHERE o.id = $1
  AND o.deleted_at IS NULL`

// CheckSalesLinkTarget loads, in one round trip, the rows a new or retargeted
// sales link may point at. Every row is looked up INSIDE the organization:
// an event, session or promoter of another organization reads as missing
// (EventID nil, SessionOK/PromoterOK false), never as an error. eventID,
// sessionID and promoterID may be nil. A session is accepted only when its
// event is the live event of the organization (and equals eventID when both
// are given). pgx.ErrNoRows when the organization does not exist.
func (q *Queries) CheckSalesLinkTarget(ctx context.Context, orgID uuid.UUID, eventID, sessionID, promoterID *uuid.UUID) (SalesLinkTargetCheckRow, error) {
	var r SalesLinkTargetCheckRow
	err := q.db.QueryRow(ctx, checkSalesLinkTarget, orgID, eventID, sessionID, promoterID).Scan(
		&r.OrgSlug, &r.EventID, &r.EventSlug, &r.SessionOK, &r.PromoterOK, &r.PromoterSlug,
	)
	return r, err
}

const getSalesLinkByIDForUpdate = `-- name: GetSalesLinkByIDForUpdate :one
SELECT ` + salesLinkColumns + `
FROM sales_links WHERE id = $1 AND org_id = $2
FOR UPDATE OF sales_links`

// GetSalesLinkByIDForUpdate is GetSalesLinkByID that locks the row until the
// transaction ends, so a retarget reads (and audits) the target it replaces.
// pgx.ErrNoRows for an unknown id or another organization's link.
func (q *Queries) GetSalesLinkByIDForUpdate(ctx context.Context, id, orgID uuid.UUID) (SalesLinkRow, error) {
	return scanSalesLinkRow(q.db.QueryRow(ctx, getSalesLinkByIDForUpdate, id, orgID))
}
