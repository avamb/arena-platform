-- +goose Up
-- =====================================================================
-- arena_new — Session owns venue and seating (Wave 4, AB-36)
--
-- Bil24 and our own spec (08_architecture/03 §EventSessionVenueAssignment)
-- bind the venue to the SESSION, not the event. This migration moves the
-- binding: sessions.venue_id becomes the single source of truth and
-- events.venue_id is dropped. A tour (one event, several cities) becomes
-- representable; the session form can derive capacity from its venue.
--
-- Capacity resolution order (application layer, AB-36 step 4):
--   bound seating plan version (assigned_seats / hybrid)
--     -> sessions.capacity_override
--     -> venues.capacity_default
--
-- Destructive-migration note: stand data is disposable (owner decision,
-- 2026-07-31). Existing sessions are backfilled from their event's venue
-- when it had one; sessions of venue-less events fall back to any active
-- venue of the owning org; if the org has no venue at all a placeholder
-- venue is created so the NOT NULL constraint can be applied without
-- cascading deletes through reservations / tickets.
-- =====================================================================

ALTER TABLE sessions
    ADD COLUMN venue_id uuid NULL REFERENCES venues(id) ON DELETE RESTRICT,
    ADD COLUMN capacity_override integer NULL
        CONSTRAINT sessions_capacity_override_positive
        CHECK (capacity_override IS NULL OR capacity_override > 0);

-- Backfill 1: inherit the venue the owning event was bound to.
UPDATE sessions s
SET    venue_id = e.venue_id
FROM   events e
WHERE  s.event_id = e.id
  AND  e.venue_id IS NOT NULL;

-- Backfill 2: venue-less events — use any active venue of the owning org
-- (oldest first for determinism).
UPDATE sessions s
SET    venue_id = pick.id
FROM   events e,
       LATERAL (
           SELECT v.id
           FROM   venues v
           WHERE  v.org_id = e.org_id
             AND  v.deleted_at IS NULL
           ORDER BY v.created_at ASC, v.id ASC
           LIMIT  1
       ) pick
WHERE  s.event_id = e.id
  AND  s.venue_id IS NULL;

-- Backfill 3: orgs with sessions but zero venues — create one placeholder
-- venue per org so the NOT NULL constraint holds without deleting data.
INSERT INTO venues (org_id, name)
SELECT DISTINCT e.org_id, 'Migrated venue (0079)'
FROM   sessions s
JOIN   events e ON e.id = s.event_id
WHERE  s.venue_id IS NULL
  AND  NOT EXISTS (
           SELECT 1 FROM venues v
           WHERE  v.org_id = e.org_id
             AND  v.name = 'Migrated venue (0079)'
             AND  v.deleted_at IS NULL
       );

UPDATE sessions s
SET    venue_id = v.id
FROM   events e, venues v
WHERE  s.event_id = e.id
  AND  s.venue_id IS NULL
  AND  v.org_id = e.org_id
  AND  v.name = 'Migrated venue (0079)'
  AND  v.deleted_at IS NULL;

ALTER TABLE sessions
    ALTER COLUMN venue_id SET NOT NULL;

CREATE INDEX sessions_venue_id_active ON sessions (venue_id)
    WHERE deleted_at IS NULL;

COMMENT ON COLUMN sessions.venue_id IS
    'Venue this session takes place at. NOT NULL — every session has a '
    'physical (or logical) venue; the event no longer carries one. '
    'Must belong to the same organization as the owning event (enforced '
    'at the application layer). Wave 4 — AB-36.';

COMMENT ON COLUMN sessions.capacity_override IS
    'Operator-supplied capacity for general-admission sessions, taking '
    'precedence over venues.capacity_default. Ignored when a seating '
    'plan version is bound (the plan''s seat count wins). Wave 4 — AB-36.';

-- The event no longer owns a venue.
DROP INDEX IF EXISTS events_venue_id_active;

ALTER TABLE events
    DROP COLUMN venue_id;

-- +goose Down
ALTER TABLE events
    ADD COLUMN venue_id uuid NULL REFERENCES venues(id) ON DELETE RESTRICT;

CREATE INDEX events_venue_id_active ON events (venue_id)
    WHERE deleted_at IS NULL AND venue_id IS NOT NULL;

-- Best-effort reverse backfill: give the event the venue of its earliest
-- session (information is lost when sessions of one event span venues).
UPDATE events e
SET    venue_id = pick.venue_id
FROM   (
           SELECT DISTINCT ON (s.event_id) s.event_id, s.venue_id
           FROM   sessions s
           WHERE  s.deleted_at IS NULL
           ORDER BY s.event_id, s.start_at ASC, s.id ASC
       ) pick
WHERE  pick.event_id = e.id;

DROP INDEX IF EXISTS sessions_venue_id_active;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS capacity_override,
    DROP COLUMN IF EXISTS venue_id;
