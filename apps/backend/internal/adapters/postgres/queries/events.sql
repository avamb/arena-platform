-- events.sql — sqlc query definitions for the events table (feature #125).
-- All write queries are scoped by org_id to enforce owner-gated mutation policy.
-- GET queries may include i18n_text joins for localized name/description.
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.
--
-- Wave 4 (AB-36/AB-37): events no longer carry venue_id or start_at/end_at.
-- The venue belongs to each session; the event's date window is the
-- trigger-maintained cache first_session_at / last_session_at (see 0080).
-- Handlers must never write the cached columns.

-- name: InsertEvent :one
-- InsertEvent creates a new event row owned by the given org.
-- Returns the created row including the uuidv7 PK assigned by the database.
-- first_session_at / last_session_at start NULL — a new event has no sessions.
INSERT INTO events (org_id, name, description, status, visibility, image_url)
VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'draft'), COALESCE(NULLIF($5, ''), 'public'), $6)
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, poster_media_id, slug, short_description, genre, age_rating, duration_minutes, teaser_url, trailer_url, meta_description, meta_keywords, created_at, updated_at, deleted_at;

-- name: GetEventByID :one
-- GetEventByID fetches an active event by its UUID primary key.
-- Takes a locale for i18n_text name/description resolution (fallback: stored value).
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
    e.slug,
    e.short_description,
    e.genre,
    e.age_rating,
    e.duration_minutes,
    e.teaser_url,
    e.trailer_url,
    e.meta_description,
    e.meta_keywords,
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
SELECT id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, poster_media_id, slug, short_description, genre, age_rating, duration_minutes, teaser_url, trailer_url, meta_description, meta_keywords, created_at, updated_at, deleted_at
FROM   events
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: ListEvents :many
-- ListEvents returns all active events across all organizations.
-- Takes a locale for i18n_text name resolution (fallback: stored name).
-- Optionally filtered by visibility: pass empty string to return all.
-- Ordered by the cached first-session timestamp; events without sessions
-- (NULL cache) sort last.
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
    e.slug,
    e.short_description,
    e.genre,
    e.age_rating,
    e.duration_minutes,
    e.teaser_url,
    e.trailer_url,
    e.meta_description,
    e.meta_keywords,
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
ORDER BY e.first_session_at ASC NULLS LAST, e.id ASC;

-- name: ListEventsByOrg :many
-- ListEventsByOrg returns all active events for the given organization.
-- Takes a locale for i18n_text name resolution (fallback: stored name).
-- Ordered by the cached first-session timestamp; sessionless events last.
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
    e.slug,
    e.short_description,
    e.genre,
    e.age_rating,
    e.duration_minutes,
    e.teaser_url,
    e.trailer_url,
    e.meta_description,
    e.meta_keywords,
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
ORDER BY e.first_session_at ASC NULLS LAST, e.id ASC;

-- name: ListEventVenueNames :many
-- ListEventVenueNames aggregates the distinct venue names of each event's
-- active sessions (AB-36 step 5: the event renders its venue(s) from its
-- sessions — one name, or several for a tour). Events without sessions are
-- absent from the result.
SELECT s.event_id,
       array_agg(DISTINCT v.name ORDER BY v.name) AS venue_names
FROM   sessions s
JOIN   venues v ON v.id = s.venue_id
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
GROUP BY s.event_id;

-- name: UpdateEvent :one
-- UpdateEvent applies a partial update to an active event (non-status fields).
-- Scoped by org_id to enforce owner-gated mutation policy.
-- Empty string for name keeps the existing value; nil optional fields keep existing.
UPDATE events
SET    name        = COALESCE(NULLIF($3, ''), name),
       description = CASE WHEN $4::text IS NOT NULL THEN $4::text ELSE description END,
       visibility  = COALESCE(NULLIF($5, ''), visibility),
       image_url   = CASE WHEN $6::text IS NOT NULL THEN $6::text ELSE image_url END,
       updated_at  = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, slug, short_description, genre, age_rating, duration_minutes, teaser_url, trailer_url, meta_description, meta_keywords, created_at, updated_at, deleted_at;

-- name: UpdateEventStatus :one
-- UpdateEventStatus transitions an event to a new status.
-- Scoped by org_id. Status invariant is enforced at the application layer.
UPDATE events
SET    status     = $3,
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, slug, short_description, genre, age_rating, duration_minutes, teaser_url, trailer_url, meta_description, meta_keywords, created_at, updated_at, deleted_at;

-- name: SoftDeleteEvent :one
-- SoftDeleteEvent marks an event as deleted by setting deleted_at.
-- Scoped by org_id to enforce owner-gated mutation policy.
UPDATE events
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, slug, short_description, genre, age_rating, duration_minutes, teaser_url, trailer_url, meta_description, meta_keywords, created_at, updated_at, deleted_at;

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

-- name: UpdateEventMetadata :one
-- UpdateEventMetadata sets the content-management metadata fields (AB-45c, migration 0051).
-- The caller resolves tri-state (absent=keep, null=clear, value=set) before calling;
-- params $3–$11 hold the already-resolved values so direct assignment is correct.
UPDATE events
SET    slug              = $3,
       short_description = $4,
       genre             = $5,
       age_rating        = $6,
       duration_minutes  = $7,
       teaser_url        = $8,
       trailer_url       = $9,
       meta_description  = $10,
       meta_keywords     = $11,
       updated_at        = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, description, status, first_session_at, last_session_at, visibility, image_url, poster_media_id, slug, short_description, genre, age_rating, duration_minutes, teaser_url, trailer_url, meta_description, meta_keywords, created_at, updated_at, deleted_at;

-- name: ListEventArtists :many
-- ListEventArtists returns all active artists for an event, ordered by sort_order.
SELECT id, event_id, name, role, bio, photo_media_id, sort_order, created_at, updated_at
FROM   event_artists
WHERE  event_id = $1
  AND  deleted_at IS NULL
ORDER  BY sort_order ASC, id ASC;

-- name: InsertEventArtist :one
INSERT INTO event_artists (event_id, name, role, bio, photo_media_id, sort_order)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, event_id, name, role, bio, photo_media_id, sort_order, created_at, updated_at;

-- name: UpdateEventArtist :one
UPDATE event_artists
SET    name           = COALESCE(NULLIF($3, ''), name),
       role           = $4,
       bio            = $5,
       photo_media_id = $6,
       sort_order     = CASE WHEN $7::integer IS NOT NULL THEN $7::integer ELSE sort_order END,
       updated_at     = now()
WHERE  id = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, name, role, bio, photo_media_id, sort_order, created_at, updated_at;

-- name: SoftDeleteEventArtist :one
UPDATE event_artists
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, name, role, bio, photo_media_id, sort_order, created_at, updated_at;
