// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: event_series.sql
//
// Tours of an organization (migration 0218, spec 63 section 15.1). A tour
// groups several events under one name, address and set of page materials;
// an event names its tour in events.series_id. The shared EventRow scanners
// do NOT read series_id: callers read it through ListSeriesIDsForEvents.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EventSeriesRow is one event_series row.
type EventSeriesRow struct {
	ID                  uuid.UUID  `json:"id"`
	OrgID               uuid.UUID  `json:"org_id"`
	Slug                string     `json:"slug"`
	Name                string     `json:"name"`
	DescriptionHTML     *string    `json:"description_html"`
	PosterMediaID       *uuid.UUID `json:"poster_media_id"`
	HeroMediaID         *uuid.UUID `json:"hero_media_id"`
	HeroVideoURL        *string    `json:"hero_video_url"`
	PartnersTextHTML    *string    `json:"partners_text_html"`
	HideChildrenInLists bool       `json:"hide_children_in_lists"`
	Position            int32      `json:"position"`
	ArchivedAt          *time.Time `json:"archived_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// EventSeriesParams are the editable fields of a tour, for create and update.
type EventSeriesParams struct {
	Slug                string
	Name                string
	DescriptionHTML     *string
	PosterMediaID       *uuid.UUID
	HeroMediaID         *uuid.UUID
	HeroVideoURL        *string
	PartnersTextHTML    *string
	HideChildrenInLists bool
	Position            int32
}

const eventSeriesColumns = `id, org_id, slug, name, description_html, poster_media_id, hero_media_id,
       hero_video_url, partners_text_html, hide_children_in_lists, position,
       archived_at, created_at, updated_at`

func scanEventSeriesRow(row interface{ Scan(dest ...any) error }) (EventSeriesRow, error) {
	var s EventSeriesRow
	err := row.Scan(&s.ID, &s.OrgID, &s.Slug, &s.Name, &s.DescriptionHTML, &s.PosterMediaID,
		&s.HeroMediaID, &s.HeroVideoURL, &s.PartnersTextHTML, &s.HideChildrenInLists,
		&s.Position, &s.ArchivedAt, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateEventSeries
// ─────────────────────────────────────────────────────────────────────────────

const createEventSeries = `-- name: CreateEventSeries :one
INSERT INTO event_series (org_id, slug, name, description_html, poster_media_id,
                          hero_media_id, hero_video_url, partners_text_html,
                          hide_children_in_lists, position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING ` + eventSeriesColumns

// CreateEventSeries creates a tour. A slug already used by another tour of
// the organization fails with 23505 on event_series_org_slug_uq; one used by
// an active event of the organization fails with 23505 on
// event_series_slug_namespace (trigger). Check SeriesSlugTaken first.
func (q *Queries) CreateEventSeries(ctx context.Context, orgID uuid.UUID, p EventSeriesParams) (EventSeriesRow, error) {
	return scanEventSeriesRow(q.db.QueryRow(ctx, createEventSeries, orgID, p.Slug, p.Name,
		p.DescriptionHTML, p.PosterMediaID, p.HeroMediaID, p.HeroVideoURL, p.PartnersTextHTML,
		p.HideChildrenInLists, p.Position))
}

// ─────────────────────────────────────────────────────────────────────────────
// GetEventSeries
// ─────────────────────────────────────────────────────────────────────────────

const getEventSeries = `-- name: GetEventSeries :one
SELECT ` + eventSeriesColumns + `
FROM   event_series
WHERE  id = $1
  AND  org_id = $2`

// GetEventSeries fetches one tour of the organization, archived or not.
// Returns pgx.ErrNoRows for an unknown id or another organization's tour.
func (q *Queries) GetEventSeries(ctx context.Context, id, orgID uuid.UUID) (EventSeriesRow, error) {
	return scanEventSeriesRow(q.db.QueryRow(ctx, getEventSeries, id, orgID))
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEventSeries
// ─────────────────────────────────────────────────────────────────────────────

const listEventSeries = `-- name: ListEventSeries :many
SELECT ` + eventSeriesColumns + `
FROM   event_series
WHERE  org_id = $1
  AND  ($2::boolean OR archived_at IS NULL)
ORDER  BY position, lower(name), id`

// ListEventSeries returns the organization's tours ordered by position, then
// name. Archived tours are included only when includeArchived is true.
func (q *Queries) ListEventSeries(ctx context.Context, orgID uuid.UUID, includeArchived bool) ([]EventSeriesRow, error) {
	rows, err := q.db.Query(ctx, listEventSeries, orgID, includeArchived)
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

// ─────────────────────────────────────────────────────────────────────────────
// UpdateEventSeries
// ─────────────────────────────────────────────────────────────────────────────

const updateEventSeries = `-- name: UpdateEventSeries :one
UPDATE event_series
SET    slug                   = $3,
       name                   = $4,
       description_html       = $5,
       poster_media_id        = $6,
       hero_media_id          = $7,
       hero_video_url         = $8,
       partners_text_html     = $9,
       hide_children_in_lists = $10,
       position               = $11,
       updated_at             = now()
WHERE  id = $1
  AND  org_id = $2
RETURNING ` + eventSeriesColumns

// UpdateEventSeries replaces every editable field of a tour. Returns
// pgx.ErrNoRows for an unknown id or another organization's tour; a slug
// collision fails with 23505 as in CreateEventSeries.
func (q *Queries) UpdateEventSeries(ctx context.Context, id, orgID uuid.UUID, p EventSeriesParams) (EventSeriesRow, error) {
	return scanEventSeriesRow(q.db.QueryRow(ctx, updateEventSeries, id, orgID, p.Slug, p.Name,
		p.DescriptionHTML, p.PosterMediaID, p.HeroMediaID, p.HeroVideoURL, p.PartnersTextHTML,
		p.HideChildrenInLists, p.Position))
}

// ─────────────────────────────────────────────────────────────────────────────
// ArchiveEventSeries
// ─────────────────────────────────────────────────────────────────────────────

const archiveEventSeries = `-- name: ArchiveEventSeries :one
UPDATE event_series
SET    archived_at = COALESCE(archived_at, now()),
       updated_at  = now()
WHERE  id = $1
  AND  org_id = $2
RETURNING ` + eventSeriesColumns

// ArchiveEventSeries archives a tour (an already archived one keeps its first
// archived_at). Its events keep the link. Returns pgx.ErrNoRows for an unknown
// id or another organization's tour.
func (q *Queries) ArchiveEventSeries(ctx context.Context, id, orgID uuid.UUID) (EventSeriesRow, error) {
	return scanEventSeriesRow(q.db.QueryRow(ctx, archiveEventSeries, id, orgID))
}

// ─────────────────────────────────────────────────────────────────────────────
// SeriesSlugTaken
// ─────────────────────────────────────────────────────────────────────────────

const seriesSlugTaken = `-- name: SeriesSlugTaken :one
SELECT EXISTS (
    SELECT 1 FROM events
    WHERE  org_id = $1
      AND  lower(slug) = lower($2)
      AND  deleted_at IS NULL
) OR EXISTS (
    SELECT 1 FROM event_series
    WHERE  org_id = $1
      AND  lower(slug) = lower($2)
      AND  ($3::uuid IS NULL OR id <> $3::uuid)
)`

// SeriesSlugTaken reports whether the slug is used by an active event of the
// organization or by another of its tours (case-insensitive). excludeSeriesID
// is the tour being edited (it may keep its own slug); nil for a new tour.
func (q *Queries) SeriesSlugTaken(ctx context.Context, orgID uuid.UUID, slug string, excludeSeriesID *uuid.UUID) (bool, error) {
	var taken bool
	err := q.db.QueryRow(ctx, seriesSlugTaken, orgID, slug, excludeSeriesID).Scan(&taken)
	return taken, err
}

// ─────────────────────────────────────────────────────────────────────────────
// SetEventSeries
// ─────────────────────────────────────────────────────────────────────────────

const setEventSeries = `-- name: SetEventSeries :execrows
UPDATE events
SET    series_id  = $3,
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL`

// SetEventSeries links an active event of the organization to a tour, or
// unlinks it when seriesID is nil, and reports whether a row changed. A tour
// of another organization fails the composite foreign key events_series_fk
// (23503).
func (q *Queries) SetEventSeries(ctx context.Context, eventID, orgID uuid.UUID, seriesID *uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, setEventSeries, eventID, orgID, seriesID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSeriesIDsForEvents
// ─────────────────────────────────────────────────────────────────────────────

const listSeriesIDsForEvents = `-- name: ListSeriesIDsForEvents :many
SELECT id, series_id
FROM   events
WHERE  id = ANY($1::uuid[])
  AND  series_id IS NOT NULL`

// ListSeriesIDsForEvents returns the tour of each given event that has one,
// keyed by event id. Events without a tour are absent from the map.
func (q *Queries) ListSeriesIDsForEvents(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	out := make(map[uuid.UUID]uuid.UUID, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listSeriesIDsForEvents, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eventID, seriesID uuid.UUID
		if err := rows.Scan(&eventID, &seriesID); err != nil {
			return nil, err
		}
		out[eventID] = seriesID
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListActiveSeriesIDsForEvents
// ─────────────────────────────────────────────────────────────────────────────

const listActiveSeriesIDsForEvents = `-- name: ListActiveSeriesIDsForEvents :many
SELECT e.id, e.series_id
FROM   events e
JOIN   event_series s ON s.id = e.series_id
WHERE  e.id = ANY($1::uuid[])
  AND  s.archived_at IS NULL`

// ListActiveSeriesIDsForEvents is the public variant of ListSeriesIDsForEvents:
// an event of an archived tour is absent from the map.
func (q *Queries) ListActiveSeriesIDsForEvents(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	out := make(map[uuid.UUID]uuid.UUID, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listActiveSeriesIDsForEvents, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eventID, seriesID uuid.UUID
		if err := rows.Scan(&eventID, &seriesID); err != nil {
			return nil, err
		}
		out[eventID] = seriesID
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEventIDsForSeries
// ─────────────────────────────────────────────────────────────────────────────

const listEventIDsForSeries = `-- name: ListEventIDsForSeries :many
SELECT id
FROM   events
WHERE  series_id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
ORDER  BY created_at, id`

// ListEventIDsForSeries returns the active events of a tour of the
// organization, oldest first.
func (q *Queries) ListEventIDsForSeries(ctx context.Context, seriesID, orgID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, listEventIDsForSeries, seriesID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// CountEventsPerSeries
// ─────────────────────────────────────────────────────────────────────────────

const countEventsPerSeries = `-- name: CountEventsPerSeries :many
SELECT series_id, count(*)
FROM   events
WHERE  series_id = ANY($1::uuid[])
  AND  deleted_at IS NULL
GROUP  BY series_id`

// CountEventsPerSeries returns the number of active events of each given
// tour, keyed by tour id. Tours without events are absent from the map.
func (q *Queries) CountEventsPerSeries(ctx context.Context, seriesIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	out := make(map[uuid.UUID]int, len(seriesIDs))
	if len(seriesIDs) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, countEventsPerSeries, seriesIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = int(n)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEventSeriesNames
// ─────────────────────────────────────────────────────────────────────────────

const listEventSeriesNames = `-- name: ListEventSeriesNames :many
SELECT id, name
FROM   event_series
WHERE  id = ANY($1::uuid[])`

// ListEventSeriesNames returns the name of each given tour, keyed by tour id.
func (q *Queries) ListEventSeriesNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listEventSeriesNames, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEventCardsForSeries
// ─────────────────────────────────────────────────────────────────────────────

const listEventCardsForSeries = `-- name: ListEventCardsForSeries :many
SELECT id, name, slug, status, first_session_at
FROM   events
WHERE  series_id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
ORDER  BY first_session_at NULLS LAST, created_at, id`

// SeriesEventCard is the short card of an event inside a tour.
type SeriesEventCard struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	Slug           *string    `json:"slug"`
	Status         string     `json:"status"`
	FirstSessionAt *time.Time `json:"first_session_at"`
}

// ListEventCardsForSeries returns the active events of a tour of the
// organization, by first session (events without sessions last).
func (q *Queries) ListEventCardsForSeries(ctx context.Context, seriesID, orgID uuid.UUID) ([]SeriesEventCard, error) {
	rows, err := q.db.Query(ctx, listEventCardsForSeries, seriesID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesEventCard
	for rows.Next() {
		var c SeriesEventCard
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Status, &c.FirstSessionAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
