package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ListEventVenueDetails (sales page "where it takes place").
//
// One row per (event, venue) over the event's sessions a buyer can see
// (scheduled or completed, not deleted): the venue's own name and postal
// address plus the start times of the event's sessions held there. The
// address is the VENUE's (venues.address_line1 / address_line2 /
// postal_code / address / country), never the seller's legal address. The
// city is the English name of venues.city_id from i18n_text 'geo.cities';
// a city without that translation stays NULL rather than a slug-made guess.
const listEventVenueDetails = `-- name: ListEventVenueDetails :many
SELECT s.event_id,
       v.id,
       v.name,
       v.address_line1,
       v.address_line2,
       v.postal_code,
       v.address,
       v.country,
       v.timezone,
       t_en.value AS city_name,
       array_agg(s.start_at ORDER BY s.start_at) AS starts
FROM   sessions s
JOIN   venues v ON v.id = s.venue_id
LEFT JOIN cities ci ON ci.id = v.city_id
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
    AND t_en.key = ci.slug
    AND t_en.locale = 'en'
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
  AND  s.status IN ('scheduled', 'completed')
GROUP BY s.event_id, v.id, v.name, v.address_line1, v.address_line2,
         v.postal_code, v.address, v.country, v.timezone, t_en.value
ORDER BY s.event_id, min(s.start_at), v.name`

// EventVenueDetailRow is one venue of one event with its session starts.
type EventVenueDetailRow struct {
	EventID      uuid.UUID
	VenueID      uuid.UUID
	Name         string
	AddressLine1 *string
	AddressLine2 *string
	PostalCode   *string
	Address      *string
	Country      *string
	Timezone     *string
	CityName     *string
	Starts       []time.Time
}

// ListEventVenueDetails returns the venues of the given events with their
// postal addresses and session start times. Events without a visible
// session are absent from the result.
func (q *Queries) ListEventVenueDetails(ctx context.Context, eventIDs []uuid.UUID) ([]EventVenueDetailRow, error) {
	rows, err := q.db.Query(ctx, listEventVenueDetails, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventVenueDetailRow
	for rows.Next() {
		var r EventVenueDetailRow
		if err := rows.Scan(&r.EventID, &r.VenueID, &r.Name, &r.AddressLine1, &r.AddressLine2,
			&r.PostalCode, &r.Address, &r.Country, &r.Timezone, &r.CityName, &r.Starts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
