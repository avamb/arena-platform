-- +goose Up
-- =====================================================================
-- arena_new — index barcodes.external_ref on its own
--
-- Two hot paths filter on external_ref ALONE, with no authority_id:
--
--   * the scanner's GetBarcodeByExternalRefAny — a turnstile scans a code
--     without knowing which authority minted it;
--   * the mint helper's cross-authority NOT EXISTS guard, which must prove a
--     candidate code is unused by ANY authority before issuing it.
--
-- The existing UNIQUE constraint is on (authority_id, external_ref). A
-- composite index can only serve a predicate that constrains its LEADING
-- column, so neither query can use it and both fall back to a sequential
-- scan. That is survivable on today's table and emphatically not survivable
-- on the legacy imports coming next, which are several hundred thousand
-- tickets each — the mint helper runs its NOT EXISTS once PER CODE.
--
-- A plain CREATE INDEX holds an ACCESS EXCLUSIVE lock for its duration, which
-- is why large tables want CONCURRENTLY — but CONCURRENTLY cannot run inside
-- a transaction and goose wraps this file in one. At the current row count
-- the build is short enough that the plain form is the right trade: it keeps
-- the migration atomic and cannot leave an INVALID index behind on failure.
-- Revisit if this ever has to be applied to an already-imported table.
-- =====================================================================

CREATE INDEX IF NOT EXISTS barcodes_external_ref ON barcodes (external_ref);

COMMENT ON INDEX barcodes_external_ref IS
    'Serves authority-agnostic lookups by external_ref. The UNIQUE constraint leads with authority_id and cannot be used for them.';

-- +goose Down
DROP INDEX IF EXISTS barcodes_external_ref;
