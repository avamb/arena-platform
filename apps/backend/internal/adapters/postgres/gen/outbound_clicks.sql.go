// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: outbound_clicks.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const recordOutboundClick = `INSERT INTO outbound_clicks_daily (org_id, event_id, day, seller, hits)
VALUES ($1, $2, $3, $4, 1)
ON CONFLICT (event_id, day, seller) DO UPDATE
   SET hits = outbound_clicks_daily.hits + 1`

// RecordOutboundClick adds one click on a partner box office (seller) to the
// event's counter of the day.
func (q *Queries) RecordOutboundClick(ctx context.Context, orgID, eventID uuid.UUID, day time.Time, seller string) error {
	_, err := q.db.Exec(ctx, recordOutboundClick, orgID, eventID, day, seller)
	return err
}

const listOutboundClickRows = `SELECT day, seller, hits
FROM   outbound_clicks_daily
WHERE  event_id = $1 AND org_id = $2 AND day BETWEEN $3 AND $4
ORDER  BY day, seller`

// OutboundClickRow is the clicks of one day on one box office.
type OutboundClickRow struct {
	Day    time.Time
	Seller string
	Hits   int64
}

// ListOutboundClickRows reads the clicks of an event over [from, to].
func (q *Queries) ListOutboundClickRows(ctx context.Context, eventID, orgID uuid.UUID, from, to time.Time) ([]OutboundClickRow, error) {
	rows, err := q.db.Query(ctx, listOutboundClickRows, eventID, orgID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboundClickRow
	for rows.Next() {
		var r OutboundClickRow
		if err := rows.Scan(&r.Day, &r.Seller, &r.Hits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listOutboundClickSources = `SELECT utm_source, sum(hits)::bigint
FROM   sales_funnel_daily
WHERE  event_id = $1 AND org_id = $2 AND day BETWEEN $3 AND $4 AND step = 'outbound_click'
GROUP  BY utm_source
HAVING sum(hits) > 0
ORDER  BY sum(hits) DESC, utm_source
LIMIT  50`

// OutboundClickSource is the clicks of one utm source.
type OutboundClickSource struct {
	UTMSource string
	Hits      int64
}

// ListOutboundClickSources sums the clicks of an event over [from, to] by utm source.
func (q *Queries) ListOutboundClickSources(ctx context.Context, eventID, orgID uuid.UUID, from, to time.Time) ([]OutboundClickSource, error) {
	rows, err := q.db.Query(ctx, listOutboundClickSources, eventID, orgID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboundClickSource
	for rows.Next() {
		var r OutboundClickSource
		if err := rows.Scan(&r.UTMSource, &r.Hits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const countExternalSiteVisits = `SELECT coalesce(sum(hits), 0)::bigint
FROM   sales_funnel_daily
WHERE  event_id = $1 AND org_id = $2 AND day BETWEEN $3 AND $4
  AND  step = 'view' AND surface = 'external_site'`

// CountExternalSiteVisits sums the shown-event hits of the static site over [from, to].
func (q *Queries) CountExternalSiteVisits(ctx context.Context, eventID, orgID uuid.UUID, from, to time.Time) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countExternalSiteVisits, eventID, orgID, from, to).Scan(&n)
	return n, err
}
