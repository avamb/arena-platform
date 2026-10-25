// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: sales_link_insert_facts.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SalesLinkInsertFactsRow is what the printable insert of a sales link shows
// (spec 43 section 6.5). Names are empty strings when the row is missing,
// never NULL; SessionStartAt is nil when the link names no live session.
type SalesLinkInsertFactsRow struct {
	OrgName          string
	OrgLogoMediaID   *uuid.UUID
	OrgDefaultLocale string
	EventName        string
	SessionStartAt   *time.Time
	VenueTimezone    string
	PromoterName     string
}

const getSalesLinkInsertFacts = `-- name: GetSalesLinkInsertFacts :one
SELECT o.name                                   AS org_name,
       o.logo_media_id                          AS org_logo_media_id,
       o.default_locale                         AS org_default_locale,
       COALESCE(e.name, '')                     AS event_name,
       s.start_at                               AS session_start_at,
       COALESCE(v.timezone, '')                 AS venue_timezone,
       COALESCE(lp.name, '')                    AS promoter_name
FROM organizations o
LEFT JOIN events e
       ON e.org_id = o.id
      AND e.deleted_at IS NULL
      AND e.id = COALESCE($2::uuid, (SELECT s0.event_id FROM sessions s0 WHERE s0.id = $3::uuid))
LEFT JOIN sessions s
       ON s.id = $3::uuid
      AND s.event_id = e.id
      AND s.deleted_at IS NULL
LEFT JOIN venues v
       ON v.id = s.venue_id
LEFT JOIN org_promoters lp
       ON lp.id = $4::uuid
      AND lp.org_id = o.id
WHERE o.id = $1
  AND o.deleted_at IS NULL
`

// GetSalesLinkInsertFacts reads the names the insert prints for the
// organization orgID and the optional targets eventID, sessionID and
// promoterID (the promoter of the LINK). A session of another event, a
// deleted event or session and a foreign promoter leave their fields empty.
func (q *Queries) GetSalesLinkInsertFacts(ctx context.Context, orgID uuid.UUID, eventID, sessionID, promoterID *uuid.UUID) (SalesLinkInsertFactsRow, error) {
	var r SalesLinkInsertFactsRow
	err := q.db.QueryRow(ctx, getSalesLinkInsertFacts, orgID, eventID, sessionID, promoterID).Scan(
		&r.OrgName, &r.OrgLogoMediaID, &r.OrgDefaultLocale, &r.EventName,
		&r.SessionStartAt, &r.VenueTimezone, &r.PromoterName,
	)
	return r, err
}
