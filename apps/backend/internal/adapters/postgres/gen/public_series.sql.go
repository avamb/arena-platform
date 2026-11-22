// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: public_series.sql
//
// Public reads of tours (event_series, migration 0221, spec 63 section 15.1):
// the feed's /series routes and the hosted tour page.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// FeedSeriesEventRow is one event of a tour that is visible in a feed.
type FeedSeriesEventRow struct {
	EventID        uuid.UUID
	SeriesID       uuid.UUID
	FirstSessionAt *time.Time
}

const listFeedSeriesEvents = `-- name: ListFeedSeriesEvents :many
SELECT e.id, e.series_id, e.first_session_at
FROM events e
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
JOIN event_series es ON es.id = e.series_id AND es.org_id = e.org_id
WHERE ft.token     = $1
  AND ft.is_active = true
  AND e.status     = 'published'
  AND e.deleted_at IS NULL
  AND e.series_id  IS NOT NULL
  AND es.archived_at IS NULL
  AND ($2::uuid IS NULL OR e.series_id = $2::uuid)
GROUP BY e.id, e.series_id, e.first_session_at
ORDER BY e.first_session_at ASC NULLS LAST, e.id ASC`

// ListFeedSeriesEvents returns the events of non-archived tours that are
// visible in the feed of the token (the visibility rule of
// ListPublishedEventsByFeedToken), optionally of one tour only, by first
// session. An event published to the token in several cities appears once.
func (q *Queries) ListFeedSeriesEvents(ctx context.Context, token string, seriesID *uuid.UUID) ([]FeedSeriesEventRow, error) {
	rows, err := q.db.Query(ctx, listFeedSeriesEvents, token, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedSeriesEventRow
	for rows.Next() {
		var r FeedSeriesEventRow
		if err := rows.Scan(&r.EventID, &r.SeriesID, &r.FirstSessionAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listEventSeriesByIDs = `-- name: ListEventSeriesByIDs :many
SELECT ` + eventSeriesColumns + `
FROM   event_series
WHERE  id = ANY($1::uuid[])
  AND  archived_at IS NULL
ORDER  BY position, lower(name), id`

// ListEventSeriesByIDs returns the non-archived tours with the given ids.
func (q *Queries) ListEventSeriesByIDs(ctx context.Context, ids []uuid.UUID) ([]EventSeriesRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, listEventSeriesByIDs, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventSeriesRow
	for rows.Next() {
		s, err := scanEventSeriesRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const getActiveEventSeriesBySlug = `-- name: GetActiveEventSeriesBySlug :one
SELECT ` + eventSeriesColumns + `
FROM   event_series
WHERE  org_id = $1
  AND  lower(slug) = lower($2)
  AND  archived_at IS NULL`

// GetActiveEventSeriesBySlug fetches a non-archived tour of the organization
// by slug (case-insensitive). pgx.ErrNoRows when there is none.
func (q *Queries) GetActiveEventSeriesBySlug(ctx context.Context, orgID uuid.UUID, slug string) (EventSeriesRow, error) {
	return scanEventSeriesRow(q.db.QueryRow(ctx, getActiveEventSeriesBySlug, orgID, slug))
}

// SeriesSessionRow is one dated session of an event of a tour.
type SeriesSessionRow struct {
	SessionID uuid.UUID
	EventID   uuid.UUID
	StartAt   time.Time
	// CityID and CityName are the venue's city (nil when the venue has none).
	CityID   *uuid.UUID
	CityName *string
}

const listSeriesSessions = `-- name: ListSeriesSessions :many
SELECT s.id, s.event_id, s.start_at, v.city_id,
       COALESCE(t_en.value, ci.slug)
FROM   sessions s
JOIN   venues v ON v.id = s.venue_id
LEFT JOIN cities ci ON ci.id = v.city_id
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
    AND t_en.key = ci.slug
    AND t_en.locale = 'en'
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
  AND  s.status = 'scheduled'
ORDER BY s.start_at, s.id`

// ListSeriesSessions returns the scheduled sessions of the given events with
// the venue's city, by start.
func (q *Queries) ListSeriesSessions(ctx context.Context, eventIDs []uuid.UUID) ([]SeriesSessionRow, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, listSeriesSessions, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesSessionRow
	for rows.Next() {
		var r SeriesSessionRow
		if err := rows.Scan(&r.SessionID, &r.EventID, &r.StartAt, &r.CityID, &r.CityName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SeriesTierPriceRow is one open, sellable main category of a session.
type SeriesTierPriceRow struct {
	TierID      uuid.UUID
	SessionID   uuid.UUID
	Currency    string
	PriceAmount int64
}

const listSellableTierPrices = `-- name: ListSellableTierPrices :many
SELECT t.id, t.session_id, t.currency, t.price_amount
FROM   ticket_tiers t
WHERE  t.session_id = ANY($1::uuid[])
  AND  t.deleted_at IS NULL
  AND  t.is_open
  AND  NOT t.is_addon
  AND  (t.sale_window_start IS NULL OR t.sale_window_start <= $2)
  AND  (t.sale_window_end   IS NULL OR t.sale_window_end   >= $2)`

// ListSellableTierPrices returns the open main categories of the given
// sessions inside their sale window at now (the hold gate's rule,
// hcheckout.CategorySellable), with price in minor units.
func (q *Queries) ListSellableTierPrices(ctx context.Context, sessionIDs []uuid.UUID, now time.Time) ([]SeriesTierPriceRow, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, listSellableTierPrices, sessionIDs, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesTierPriceRow
	for rows.Next() {
		var r SeriesTierPriceRow
		if err := rows.Scan(&r.TierID, &r.SessionID, &r.Currency, &r.PriceAmount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
