-- +goose Up
-- =====================================================================
-- arena_new — Add external_bil24_id to events (feature #386)
--
-- Adds a nullable text column external_bil24_id to the events table to
-- support the one-shot Bil24 catalog snapshot import. The column is:
--   * NULL for all events created natively inside arena_new.
--   * Set to the source Bil24 event ID for events imported via the
--     arena-bil24-import operator tool.
--   * Protected by a partial unique index so a second importer run is
--     a safe no-op (ON CONFLICT skips duplicates).
-- =====================================================================

ALTER TABLE events
    ADD COLUMN external_bil24_id text NULL;

-- Partial unique index: when set, external_bil24_id must be unique.
-- Multiple NULL values are allowed (native events).
CREATE UNIQUE INDEX events_external_bil24_id_unique
    ON events (external_bil24_id)
    WHERE external_bil24_id IS NOT NULL;

COMMENT ON COLUMN events.external_bil24_id IS
    'Optional source identifier from a Bil24 catalog snapshot import. '
    'NULL for events created natively in arena_new. '
    'Unique among rows where it is set (partial index). '
    'Feature #386 — one-shot Bil24 migration.';

-- +goose Down
DROP INDEX IF EXISTS events_external_bil24_id_unique;
ALTER TABLE events DROP COLUMN IF EXISTS external_bil24_id;
