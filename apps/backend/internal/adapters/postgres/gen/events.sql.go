// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: events.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// EventRow — shared result type for all events queries
// ─────────────────────────────────────────────────────────────────────────────

// EventRow is the result type returned by all events queries.
// deleted_at is nil for active events and non-nil for soft-deleted ones.
// description is nil when not provided.
// image_url is nil when no image was supplied.
// first_session_at / last_session_at are the trigger-maintained cache over
// the event's active, non-cancelled sessions (migration 0080, AB-37); both
// are nil for an event with no sessions. The event carries no venue and no
// own dates — those belong to its sessions (AB-36/AB-37).
// The name and description fields reflect locale-resolved values when the
// query joins i18n_text; for write-result rows they hold the stored values.
type EventRow struct {
	ID             uuid.UUID  `json:"id"`
	DisplayNumber  int64      `json:"display_number"`
	OrgID          uuid.UUID  `json:"org_id"`
	Name           string     `json:"name"`
	Description    *string    `json:"description"`
	Status         string     `json:"status"`
	FirstSessionAt *time.Time `json:"first_session_at"`
	LastSessionAt  *time.Time `json:"last_session_at"`
	Visibility     string     `json:"visibility"`
	ImageURL       *string    `json:"image_url"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeletedAt      *time.Time `json:"deleted_at"`
}

// scanEventRow scans a single events row into an EventRow.
func scanEventRow(row interface {
	Scan(dest ...any) error
}) (EventRow, error) {
	var e EventRow
	err := row.Scan(
		&e.ID,
		&e.DisplayNumber,
		&e.OrgID,
		&e.Name,
		&e.Description,
		&e.Status,
		&e.FirstSessionAt,
		&e.LastSessionAt,
		&e.Visibility,
		&e.ImageURL,
		&e.CreatedAt,
		&e.UpdatedAt,
		&e.DeletedAt,
	)
	return e, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertEvent
// ─────────────────────────────────────────────────────────────────────────────

const insertEvent = `-- name: InsertEvent :one
INSERT INTO events (org_id, name, description, status, visibility, image_url)
VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'draft'), COALESCE(NULLIF($5, ''), 'public'), $6)
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, created_at, updated_at, deleted_at`

// InsertEvent creates a new active event row owned by the given org.
// status defaults to 'draft' when empty; visibility defaults to 'public' when empty.
// first_session_at / last_session_at start NULL — a new event has no sessions.
// Returns the created row including the uuidv7 PK assigned by the database.
func (q *Queries) InsertEvent(ctx context.Context, orgID uuid.UUID, name string, description *string, status, visibility string, imageURL *string) (EventRow, error) {
	row := q.db.QueryRow(ctx, insertEvent,
		orgID, name, description, status, visibility, imageURL,
	)
	return scanEventRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetEventByID (with i18n joins)
// ─────────────────────────────────────────────────────────────────────────────

const getEventByID = `-- name: GetEventByID :one
SELECT
    e.id,
    e.display_number,
    e.org_id,
    COALESCE(n_loc.value, n_en.value, e.name)               AS name,
    COALESCE(d_loc.value, d_en.value, e.description)         AS description,
    e.status,
    e.first_session_at,
    e.last_session_at,
    e.visibility,
    e.image_url,
    e.created_at,
    e.updated_at,
    e.deleted_at
FROM events e
LEFT JOIN i18n_text n_loc ON n_loc.namespace = 'event.name'
    AND n_loc.key = e.id::text
    AND n_loc.locale = $2
LEFT JOIN i18n_text n_en ON n_en.namespace = 'event.name'
    AND n_en.key = e.id::text
    AND n_en.locale = 'en'
LEFT JOIN i18n_text d_loc ON d_loc.namespace = 'event.description'
    AND d_loc.key = e.id::text
    AND d_loc.locale = $2
LEFT JOIN i18n_text d_en ON d_en.namespace = 'event.description'
    AND d_en.key = e.id::text
    AND d_en.locale = 'en'
WHERE e.id = $1
  AND e.deleted_at IS NULL`

// GetEventByID fetches an active event by its UUID primary key with i18n support.
// locale is used to resolve localized name/description from i18n_text; falls back to stored value.
// Returns pgx.ErrNoRows when not found or deleted.
func (q *Queries) GetEventByID(ctx context.Context, id uuid.UUID, locale string) (EventRow, error) {
	row := q.db.QueryRow(ctx, getEventByID, id, locale)
	return scanEventRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetEventRaw (no i18n joins — for status transitions)
// ─────────────────────────────────────────────────────────────────────────────

const getEventRaw = `-- name: GetEventRaw :one
SELECT id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, created_at, updated_at, deleted_at
FROM   events
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetEventRaw fetches an active event without i18n joins.
// Used internally for status-transition guards that need the current status.
func (q *Queries) GetEventRaw(ctx context.Context, id uuid.UUID) (EventRow, error) {
	row := q.db.QueryRow(ctx, getEventRaw, id)
	return scanEventRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEvents
// ─────────────────────────────────────────────────────────────────────────────

const listEvents = `-- name: ListEvents :many
SELECT
    e.id,
    e.display_number,
    e.org_id,
    COALESCE(n_loc.value, n_en.value, e.name)               AS name,
    COALESCE(d_loc.value, d_en.value, e.description)         AS description,
    e.status,
    e.first_session_at,
    e.last_session_at,
    e.visibility,
    e.image_url,
    e.created_at,
    e.updated_at,
    e.deleted_at
FROM events e
LEFT JOIN i18n_text n_loc ON n_loc.namespace = 'event.name'
    AND n_loc.key = e.id::text
    AND n_loc.locale = $1
LEFT JOIN i18n_text n_en ON n_en.namespace = 'event.name'
    AND n_en.key = e.id::text
    AND n_en.locale = 'en'
LEFT JOIN i18n_text d_loc ON d_loc.namespace = 'event.description'
    AND d_loc.key = e.id::text
    AND d_loc.locale = $1
LEFT JOIN i18n_text d_en ON d_en.namespace = 'event.description'
    AND d_en.key = e.id::text
    AND d_en.locale = 'en'
WHERE e.deleted_at IS NULL
  AND ($2::text = '' OR e.visibility = $2::text)
ORDER BY e.first_session_at ASC NULLS LAST, e.id ASC`

// ListEvents returns all active (non-deleted) events across all organizations.
// locale is used to resolve localized names from i18n_text.
// visibilityFilter filters by visibility value; pass "" to return all.
// Ordered by the cached first-session timestamp; sessionless events sort last.
func (q *Queries) ListEvents(ctx context.Context, locale, visibilityFilter string) ([]EventRow, error) {
	rows, err := q.db.Query(ctx, listEvents, locale, visibilityFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []EventRow
	for rows.Next() {
		e, err := scanEventRow(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEventsByOrg
// ─────────────────────────────────────────────────────────────────────────────

const listEventsByOrg = `-- name: ListEventsByOrg :many
SELECT
    e.id,
    e.display_number,
    e.org_id,
    COALESCE(n_loc.value, n_en.value, e.name)               AS name,
    COALESCE(d_loc.value, d_en.value, e.description)         AS description,
    e.status,
    e.first_session_at,
    e.last_session_at,
    e.visibility,
    e.image_url,
    e.created_at,
    e.updated_at,
    e.deleted_at
FROM events e
LEFT JOIN i18n_text n_loc ON n_loc.namespace = 'event.name'
    AND n_loc.key = e.id::text
    AND n_loc.locale = $2
LEFT JOIN i18n_text n_en ON n_en.namespace = 'event.name'
    AND n_en.key = e.id::text
    AND n_en.locale = 'en'
LEFT JOIN i18n_text d_loc ON d_loc.namespace = 'event.description'
    AND d_loc.key = e.id::text
    AND d_loc.locale = $2
LEFT JOIN i18n_text d_en ON d_en.namespace = 'event.description'
    AND d_en.key = e.id::text
    AND d_en.locale = 'en'
WHERE e.org_id = $1
  AND e.deleted_at IS NULL
ORDER BY e.first_session_at ASC NULLS LAST, e.id ASC`

// ListEventsByOrg returns all active (non-deleted) events for the given organization.
// locale is used to resolve localized names from i18n_text.
// Ordered by the cached first-session timestamp; sessionless events sort last.
func (q *Queries) ListEventsByOrg(ctx context.Context, orgID uuid.UUID, locale string) ([]EventRow, error) {
	rows, err := q.db.Query(ctx, listEventsByOrg, orgID, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []EventRow
	for rows.Next() {
		e, err := scanEventRow(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEventVenueNames
// ─────────────────────────────────────────────────────────────────────────────

const listEventVenueNames = `-- name: ListEventVenueNames :many
SELECT s.event_id,
       array_agg(DISTINCT v.name ORDER BY v.name) AS venue_names
FROM   sessions s
JOIN   venues v ON v.id = s.venue_id
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
GROUP BY s.event_id`

// ListEventVenueNames aggregates the distinct venue names of each event's
// active sessions (AB-36 step 5: an event renders its venue(s) from its
// sessions — one name, or several for a tour). Events without sessions are
// simply absent from the returned map.
func (q *Queries) ListEventVenueNames(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	rows, err := q.db.Query(ctx, listEventVenueNames, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID][]string, len(eventIDs))
	for rows.Next() {
		var eventID uuid.UUID
		var names []string
		if err := rows.Scan(&eventID, &names); err != nil {
			return nil, err
		}
		result[eventID] = names
	}
	return result, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateEvent
// ─────────────────────────────────────────────────────────────────────────────

const updateEvent = `-- name: UpdateEvent :one
UPDATE events
SET    name        = COALESCE(NULLIF($3, ''), name),
       description = CASE WHEN $4::text IS NOT NULL THEN $4::text ELSE description END,
       visibility  = COALESCE(NULLIF($5, ''), visibility),
       image_url   = CASE WHEN $6::text IS NOT NULL THEN $6::text ELSE image_url END,
       updated_at  = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, created_at, updated_at, deleted_at`

// UpdateEvent applies a partial update to an active event (non-status fields).
// Scoped by org_id to enforce owner-gated mutation policy.
// Empty string name and nil optional fields keep the existing values.
// Returns pgx.ErrNoRows when the event does not exist, does not belong to the org,
// or has been soft-deleted.
func (q *Queries) UpdateEvent(ctx context.Context, id, orgID uuid.UUID, name string, description *string, visibility string, imageURL *string) (EventRow, error) {
	row := q.db.QueryRow(ctx, updateEvent,
		id, orgID, name, description, visibility, imageURL,
	)
	return scanEventRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateEventStatus
// ─────────────────────────────────────────────────────────────────────────────

const updateEventStatus = `-- name: UpdateEventStatus :one
UPDATE events
SET    status     = $3,
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, created_at, updated_at, deleted_at`

// UpdateEventStatus transitions an event to a new status.
// Scoped by org_id to enforce owner-gated mutation policy.
// Status transition invariants are enforced at the application layer before this call.
// Returns pgx.ErrNoRows when the event does not exist, does not belong to the org,
// or has been soft-deleted.
func (q *Queries) UpdateEventStatus(ctx context.Context, id, orgID uuid.UUID, status string) (EventRow, error) {
	row := q.db.QueryRow(ctx, updateEventStatus, id, orgID, status)
	return scanEventRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SoftDeleteEvent
// ─────────────────────────────────────────────────────────────────────────────

const softDeleteEvent = `-- name: SoftDeleteEvent :one
UPDATE events
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, created_at, updated_at, deleted_at`

// SoftDeleteEvent marks an event as deleted by setting deleted_at.
// Scoped by org_id to enforce owner-gated mutation policy.
// The row is not physically removed. Returns pgx.ErrNoRows when the event
// does not exist, does not belong to the org, or has already been deleted.
func (q *Queries) SoftDeleteEvent(ctx context.Context, id, orgID uuid.UUID) (EventRow, error) {
	row := q.db.QueryRow(ctx, softDeleteEvent, id, orgID)
	return scanEventRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// UpsertEventI18nName
// ─────────────────────────────────────────────────────────────────────────────

const upsertEventI18nName = `-- name: UpsertEventI18nName :exec
INSERT INTO i18n_text (namespace, key, locale, value)
VALUES ('event.name', $1, $2, $3)
ON CONFLICT (namespace, key, locale) DO UPDATE SET value = EXCLUDED.value`

// UpsertEventI18nName stores or updates the localized name for an event.
// key is the event UUID as a string, locale is the target locale (e.g. "ru"),
// value is the translated event name.
func (q *Queries) UpsertEventI18nName(ctx context.Context, eventIDStr, locale, value string) error {
	_, err := q.db.Exec(ctx, upsertEventI18nName, eventIDStr, locale, value)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// UpsertEventI18nDescription
// ─────────────────────────────────────────────────────────────────────────────

const upsertEventI18nDescription = `-- name: UpsertEventI18nDescription :exec
INSERT INTO i18n_text (namespace, key, locale, value)
VALUES ('event.description', $1, $2, $3)
ON CONFLICT (namespace, key, locale) DO UPDATE SET value = EXCLUDED.value`

// UpsertEventI18nDescription stores or updates the localized description for an event.
// key is the event UUID as a string, locale is the target locale (e.g. "ru"),
// value is the translated event description.
func (q *Queries) UpsertEventI18nDescription(ctx context.Context, eventIDStr, locale, value string) error {
	_, err := q.db.Exec(ctx, upsertEventI18nDescription, eventIDStr, locale, value)
	return err
}
