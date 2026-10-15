-- event_partner_report.sql — task 1-2b of the event-center execution spec:
-- the "where else to buy" partner links, the map point and the photo report
-- of an event (tables of migration 0153). Hand-maintained wrappers live in
-- gen/event_partner_report.sql.go. (The gallery and videos of task 1-2a are
-- in a separate file.)
--
-- Positions: (event_id, position) is UNIQUE DEFERRABLE, which cannot be an
-- ON CONFLICT target, so an insert picks the lowest free position itself and
-- a lost race surfaces as 23505 (the handler retries, then answers 409).
-- A reorder is ONE UPDATE ... FROM unnest(...): a deferrable constraint is
-- checked at the end of the statement, so positions may be swapped freely.

-- name: GetEventOrgForMaterials :one
-- The organization of an active (not deleted) event; ErrNoRows otherwise.
SELECT org_id FROM events WHERE id = $1 AND deleted_at IS NULL;

-- ── 1-2b partner links ───────────────────────────────────────────────────

-- name: ListEventPartnerLinks :many
SELECT id, org_id, event_id, url, seller_name, position, created_at, updated_at
FROM event_partner_links
WHERE event_id = $1
ORDER BY position;

-- name: GetEventPartnerLink :one
SELECT id, org_id, event_id, url, seller_name, position, created_at, updated_at
FROM event_partner_links
WHERE id = $1 AND event_id = $2;

-- name: InsertEventPartnerLink :one
-- $5 is the limit (eventmaterials.MaxPartnerLinks); no row = the event is full.
INSERT INTO event_partner_links (org_id, event_id, url, seller_name, position)
SELECT $1::uuid, $2::uuid, $3::text, $4::text, p.pos::smallint
FROM generate_series(0, $5::int - 1) AS p(pos)
WHERE NOT EXISTS (
    SELECT 1 FROM event_partner_links x WHERE x.event_id = $2::uuid AND x.position = p.pos
)
ORDER BY p.pos
LIMIT 1
RETURNING id, org_id, event_id, url, seller_name, position, created_at, updated_at;

-- name: UpdateEventPartnerLink :one
UPDATE event_partner_links
SET url = $3, seller_name = $4, updated_at = now()
WHERE id = $1 AND event_id = $2
RETURNING id, org_id, event_id, url, seller_name, position, created_at, updated_at;

-- name: DeleteEventPartnerLink :execrows
DELETE FROM event_partner_links WHERE id = $1 AND event_id = $2;

-- name: CompactEventPartnerLinks :exec
-- Closes the gap a delete left: positions become 0..n-1 in the same order.
UPDATE event_partner_links t
SET position = r.rn - 1, updated_at = now()
FROM (
    SELECT id, (row_number() OVER (ORDER BY position))::int AS rn
    FROM event_partner_links WHERE event_id = $1
) r
WHERE t.id = r.id AND t.position <> r.rn - 1;

-- name: ReorderEventPartnerLinks :execrows
-- $2 lists the event's link ids in the wanted order; ONE statement.
UPDATE event_partner_links t
SET position = (v.ord - 1)::smallint, updated_at = now()
FROM unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE t.id = v.id AND t.event_id = $1;

-- ── 1-2b map point ───────────────────────────────────────────────────────

-- name: GetEventMapPoint :one
SELECT event_id, org_id, latitude::float8, longitude::float8, label, created_at, updated_at
FROM event_map_points
WHERE event_id = $1;

-- name: UpsertEventMapPoint :one
-- PRIMARY KEY (event_id) is a plain (non-deferrable) arbiter.
INSERT INTO event_map_points (event_id, org_id, latitude, longitude, label)
VALUES ($1, $2, $3::numeric, $4::numeric, $5)
ON CONFLICT (event_id) DO UPDATE
SET latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude,
    label = EXCLUDED.label, updated_at = now()
RETURNING event_id, org_id, latitude::float8, longitude::float8, label, created_at, updated_at;

-- name: DeleteEventMapPoint :execrows
DELETE FROM event_map_points WHERE event_id = $1;

-- ── 1-2b photo report ────────────────────────────────────────────────────

-- name: IsEventReportMedia :one
-- True when the media object is an active event_report object of this
-- organization (the foreign key alone proves only that the object exists).
SELECT EXISTS (
    SELECT 1 FROM media_objects
    WHERE id = $1 AND org_id = $2 AND owner_type = 'event_report' AND deleted_at IS NULL
);

-- name: ListEventReportPhotos :many
-- The organizer's view: every photo, always.
SELECT id, org_id, event_id, media_id, position, caption, created_at, updated_at
FROM event_report_photos
WHERE event_id = $1
ORDER BY position;

-- name: ListEventReportPhotosPublic :many
-- The buyers' view (task 1-2c wires it): hidden until the event is over. $2 is
-- "now", passed in so the rule is testable. events.last_session_at is the END
-- of the last live (not deleted, not cancelled) session, so the report opens
-- when the event has ended, follows a session that moves, and stays hidden
-- for an event without a live session.
SELECT p.id, p.org_id, p.event_id, p.media_id, p.position, p.caption, p.created_at, p.updated_at
FROM event_report_photos p
JOIN events e ON e.id = p.event_id AND e.deleted_at IS NULL
WHERE p.event_id = $1
  AND e.last_session_at IS NOT NULL
  AND e.last_session_at <= $2
ORDER BY p.position;

-- name: GetEventReportPhoto :one
SELECT id, org_id, event_id, media_id, position, caption, created_at, updated_at
FROM event_report_photos
WHERE id = $1 AND event_id = $2;

-- name: InsertEventReportPhoto :one
-- $6 is the limit (eventmaterials.MaxReportPhotos); no row = the report is full.
INSERT INTO event_report_photos (org_id, event_id, media_id, caption, position)
SELECT $1::uuid, $2::uuid, $3::uuid, $4::text, p.pos::smallint
FROM generate_series(0, $5::int - 1) AS p(pos)
WHERE NOT EXISTS (
    SELECT 1 FROM event_report_photos x WHERE x.event_id = $2::uuid AND x.position = p.pos
)
ORDER BY p.pos
LIMIT 1
RETURNING id, org_id, event_id, media_id, position, caption, created_at, updated_at;

-- name: UpdateEventReportPhotoCaption :one
UPDATE event_report_photos
SET caption = $3, updated_at = now()
WHERE id = $1 AND event_id = $2
RETURNING id, org_id, event_id, media_id, position, caption, created_at, updated_at;

-- name: DeleteEventReportPhoto :execrows
DELETE FROM event_report_photos WHERE id = $1 AND event_id = $2;

-- name: CompactEventReportPhotos :exec
UPDATE event_report_photos t
SET position = r.rn - 1, updated_at = now()
FROM (
    SELECT id, (row_number() OVER (ORDER BY position))::int AS rn
    FROM event_report_photos WHERE event_id = $1
) r
WHERE t.id = r.id AND t.position <> r.rn - 1;

-- name: ReorderEventReportPhotos :execrows
UPDATE event_report_photos t
SET position = (v.ord - 1)::smallint, updated_at = now()
FROM unnest($2::uuid[]) WITH ORDINALITY AS v(id, ord)
WHERE t.id = v.id AND t.event_id = $1;
