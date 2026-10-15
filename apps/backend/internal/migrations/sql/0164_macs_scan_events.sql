-- +goose Up
-- =====================================================================
-- arena_new — scans from MACS into scan_events (wave B, line M, task M1)
--
-- MACS, the gate system of the WordPress clients, posts its scans back to
-- arena through POST /v1/macs/scan-events, signed with the HMAC secret of
-- the organization's MACS subscriber (webhook_subscribers kind='macs').
-- Those scans land in the same scan_events table as arena's own scanner,
-- told apart by `source`:
--
--   * arena_scanner — POST /v1/scanner/scan-events (every row before 0164).
--   * macs          — POST /v1/macs/scan-events; subscriber_id and
--                     external_scan_id are then both set.
--
-- Idempotency of a MACS scan is (subscriber_id, external_scan_id): MACS
-- re-sends a whole batch after a timeout or from an offline scanner days
-- later, and a scan it already delivered is a no-op. The older
-- (credential_code, scanned_at) unique index stays and keeps meaning "the
-- same physical scan".
--
-- tickets.used_at (0055) stays the first admission: no first_admitted_at
-- column (run decision R-6).
-- =====================================================================

SET LOCAL lock_timeout = '5s';

ALTER TABLE scan_events
    ADD COLUMN source           text NOT NULL DEFAULT 'arena_scanner',
    ADD COLUMN scanner_label    text NOT NULL DEFAULT '',
    ADD COLUMN external_scan_id text,
    ADD COLUMN subscriber_id    uuid REFERENCES webhook_subscribers(id);

ALTER TABLE scan_events
    ADD CONSTRAINT scan_events_source_check
        CHECK (source IN ('arena_scanner', 'macs')),
    ADD CONSTRAINT scan_events_source_lineage_check
        CHECK ((source = 'macs' AND subscriber_id IS NOT NULL AND external_scan_id IS NOT NULL)
            OR (source <> 'macs' AND subscriber_id IS NULL AND external_scan_id IS NULL)),
    ADD CONSTRAINT scan_events_external_scan_id_length
        CHECK (external_scan_id IS NULL OR char_length(external_scan_id) BETWEEN 1 AND 128),
    ADD CONSTRAINT scan_events_scanner_label_length
        CHECK (char_length(scanner_label) <= 200),
    ADD CONSTRAINT scan_events_subscriber_scan_unique
        UNIQUE (subscriber_id, external_scan_id);

-- Per-session admission counts by source (session summary).
CREATE INDEX scan_events_session_source_admitted
    ON scan_events (session_id, source)
    WHERE session_id IS NOT NULL AND result = 'admitted';

COMMENT ON COLUMN scan_events.source IS
    'Who reported the scan: arena_scanner (POST /v1/scanner/scan-events) or macs (POST /v1/macs/scan-events).';
COMMENT ON COLUMN scan_events.scanner_label IS
    'Human name of the scanner device as the reporting system names it (MACS scanner_name). Empty for arena_scanner rows.';
COMMENT ON COLUMN scan_events.external_scan_id IS
    'The reporting system''s own scan id (MACS scan_id). Unique per subscriber; NULL for arena_scanner rows.';
COMMENT ON COLUMN scan_events.subscriber_id IS
    'The MACS webhook_subscribers row whose secret signed the batch. NULL for arena_scanner rows.';

-- +goose Down

DROP INDEX IF EXISTS scan_events_session_source_admitted;

ALTER TABLE scan_events
    DROP CONSTRAINT IF EXISTS scan_events_subscriber_scan_unique,
    DROP CONSTRAINT IF EXISTS scan_events_scanner_label_length,
    DROP CONSTRAINT IF EXISTS scan_events_external_scan_id_length,
    DROP CONSTRAINT IF EXISTS scan_events_source_lineage_check,
    DROP CONSTRAINT IF EXISTS scan_events_source_check;

ALTER TABLE scan_events
    DROP COLUMN IF EXISTS subscriber_id,
    DROP COLUMN IF EXISTS external_scan_id,
    DROP COLUMN IF EXISTS scanner_label,
    DROP COLUMN IF EXISTS source;
