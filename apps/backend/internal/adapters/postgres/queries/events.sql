-- events.sql — sqlc query definitions for the events table (feature #125).
-- All write queries are scoped by org_id to enforce owner-gated mutation policy.
-- GET queries may include i18n_text joins for localized name/description.
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertEvent :one
-- InsertEvent creates a new event row owned by the given org.
-- Returns the created row including the uuidv7 PK assigned by the database.
INSERT INTO events (org_id, venue_id, name, description, status, start_at, end_at, visibility, image_url)
VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5, ''), 'draft'), $6, $7, COALESCE(NULLIF($8, ''), 'public'), $9)
RETURNING id, org_id, venue_id, name, description, status, start_at, end_at, visibility, image_url, created_at, updated_at, deleted_at;

-- name: GetEventByID :one
-- GetEventByID fetches an active event by its UUID primary key.
-- Takes a locale for i18n_text name/description resolution (fallback: stored value).
SELECT
    e.id,
    e.org_id,
    e.venue_id,
    COALESCE(n_loc.value, n_en.value, e.name)               AS name,
    COALESCE(d_loc.value, d_en.value, e.description)         AS description,
    e.status,
    e.start_at,
    e.end_at,
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
  AND e.deleted_at IS NULL;

-- name: GetEventRaw :one
-- GetEventRaw fetches an active event without i18n joins (used for status transitions).
SELECT id, org_id, venue_id, name, description, status, start_at, end_at, visibility, image_url, created_at, updated_at, deleted_at
FROM   events
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: ListEvents :many
-- ListEvents returns all active events across all organizations.
-- Takes a locale for i18n_text name resolution (fallback: stored name).
-- Optionally filtered by visibility: pass empty string to return all.
SELECT
    e.id,
    e.org_id,
    e.venue_id,
    COALESCE(n_loc.value, n_en.value, e.name)               AS name,
    COALESCE(d_loc.value, d_en.value, e.description)         AS description,
    e.status,
    e.start_at,
    e.end_at,
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
ORDER BY e.start_at ASC, e.id ASC;

-- name: ListEventsByOrg :many
-- ListEventsByOrg returns all active events for the given organization.
-- Takes a locale for i18n_text name resolution (fallback: stored name).
SELECT
    e.id,
    e.org_id,
    e.venue_id,
    COALESCE(n_loc.value, n_en.value, e.name)               AS name,
    COALESCE(d_loc.value, d_en.value, e.description)         AS description,
    e.status,
    e.start_at,
    e.end_at,
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
ORDER BY e.start_at ASC, e.id ASC;

-- name: UpdateEvent :one
-- UpdateEvent applies a partial update to an active event (non-status fields).
-- Scoped by org_id to enforce owner-gated mutation policy.
-- Empty string for name keeps the existing value; nil optional fields keep existing.
UPDATE events
SET    venue_id    = CASE WHEN $3::uuid IS NOT NULL THEN $3::uuid ELSE venue_id END,
       name        = COALESCE(NULLIF($4, ''), name),
       description = CASE WHEN $5::text IS NOT NULL THEN $5::text ELSE description END,
       start_at    = CASE WHEN $6::timestamptz IS NOT NULL THEN $6::timestamptz ELSE start_at END,
       end_at      = CASE WHEN $7::timestamptz IS NOT NULL THEN $7::timestamptz ELSE end_at END,
       visibility  = COALESCE(NULLIF($8, ''), visibility),
       image_url   = CASE WHEN $9::text IS NOT NULL THEN $9::text ELSE image_url END,
       updated_at  = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, venue_id, name, description, status, start_at, end_at, visibility, image_url, created_at, updated_at, deleted_at;

-- name: UpdateEventStatus :one
-- UpdateEventStatus transitions an event to a new status.
-- Scoped by org_id. Status invariant is enforced at the application layer.
UPDATE events
SET    status     = $3,
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, venue_id, name, description, status, start_at, end_at, visibility, image_url, created_at, updated_at, deleted_at;

-- name: SoftDeleteEvent :one
-- SoftDeleteEvent marks an event as deleted by setting deleted_at.
-- Scoped by org_id to enforce owner-gated mutation policy.
UPDATE events
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, venue_id, name, description, status, start_at, end_at, visibility, image_url, created_at, updated_at, deleted_at;

-- name: UpsertEventI18nName :exec
-- UpsertEventI18nName stores or updates the localized name for an event.
-- namespace='event.name', key=event_id::text, locale=$2, value=$3.
INSERT INTO i18n_text (namespace, key, locale, value)
VALUES ('event.name', $1, $2, $3)
ON CONFLICT (namespace, key, locale) DO UPDATE SET value = EXCLUDED.value;

-- name: UpsertEventI18nDescription :exec
-- UpsertEventI18nDescription stores or updates the localized description for an event.
-- namespace='event.description', key=event_id::text, locale=$2, value=$3.
INSERT INTO i18n_text (namespace, key, locale, value)
VALUES ('event.description', $1, $2, $3)
ON CONFLICT (namespace, key, locale) DO UPDATE SET value = EXCLUDED.value;
