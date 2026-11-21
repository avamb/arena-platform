package gen

import (
	"context"

	"github.com/google/uuid"
)

// EventVenueCity is the country and city of one venue an event plays at.
type EventVenueCity struct {
	Country string
	City    string
}

const listEventVenueCities = `-- name: ListEventVenueCities :many
SELECT DISTINCT COALESCE(NULLIF(btrim(v.country), ''), co.iso2::text, '') AS country,
       COALESCE(t_en.value, ci.slug, '') AS city
FROM sessions s
JOIN venues v ON v.id = s.venue_id
LEFT JOIN cities ci ON ci.id = v.city_id
LEFT JOIN countries co ON co.id = ci.country_id
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
       AND t_en.key = ci.slug AND t_en.locale = 'en'
WHERE s.event_id = $1
  AND s.deleted_at IS NULL
  AND s.status <> 'cancelled'
  AND v.deleted_at IS NULL`

// ListEventVenueCities lists the distinct (country, city) pairs of the live
// sessions' venues, for the advisory map point check. A venue without a city
// yields an empty city.
func (q *Queries) ListEventVenueCities(ctx context.Context, eventID uuid.UUID) ([]EventVenueCity, error) {
	rows, err := q.db.Query(ctx, listEventVenueCities, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventVenueCity
	for rows.Next() {
		var v EventVenueCity
		if err := rows.Scan(&v.Country, &v.City); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
