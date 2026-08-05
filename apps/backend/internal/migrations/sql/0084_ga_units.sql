-- +goose Up
-- =====================================================================
-- AB-51: every General Admission place gets its own session_seats row
-- ("так же, как в API Bil24" — owner decision 2026-08-01). GA rows use
-- the same status set and the same conditional-UPDATE transition table
-- as assigned seats; they are distinguished by kind='ga_unit' and empty
-- coordinate columns, exactly like the Bil24 seat-management table.
--
--   * kind='seat'     — coordinate-bearing assigned seat (as before).
--   * kind='ga_unit'  — one General Admission place. seat_key follows
--     "ga|c<categoryIndex>|<n>" for plan-bound categories and
--     "ga|pool|<n>" for plan-less GA sessions (n zero-padded to 6).
--   * tier_id on a ga_unit: fixed to the category tier for plan-bound
--     units; NULL while available for plan-less pool units (stamped on
--     hold, reset to NULL on release — pool units are fungible across
--     tiers).
--
-- inventory_ledger remains the transactional capacity gate; unit rows
-- are the source of truth. Every unit transition happens in the same
-- transaction as its ledger counterpart (the pre-existing seated
-- pattern), so the two cannot disagree.
-- =====================================================================

ALTER TABLE session_seats
    ADD COLUMN kind text NOT NULL DEFAULT 'seat'
        CHECK (kind IN ('seat', 'ga_unit'));

COMMENT ON COLUMN session_seats.kind IS
    'seat = coordinate-bearing assigned seat; ga_unit = one General '
    'Admission place (AB-51). GA units carry empty sector/row/number '
    'and live in the same status machine as seats.';

-- Allocation scans: available GA units of a session (optionally by tier).
CREATE INDEX session_seats_ga_alloc_idx
    ON session_seats (session_id, kind, status, tier_id);

-- ---------------------------------------------------------------------
-- Backfill: materialize units for existing GA sessions from their
-- resolved capacity. Active holds cannot be mapped to units (holds are
-- short-lived; the stand is staging) — held counts are treated as
-- available. Sold capacity is represented by marking the lowest-
-- numbered units sold WITHOUT a reservation linkage (historical rows;
-- release/refund paths for them go through capacity counters as
-- before). Hybrid sessions get pool units for their standing capacity.
-- ---------------------------------------------------------------------

INSERT INTO session_seats
    (session_id, seat_key, sector_name, row_name, seat_number,
     tier_id, status, kind)
SELECT s.id,
       'ga|pool|' || lpad(gs::text, 6, '0'),
       '', '', '',
       NULL,
       CASE WHEN gs <= sold.cnt THEN 'sold' ELSE 'available' END,
       'ga_unit'
FROM   sessions s
CROSS  JOIN LATERAL (
        SELECT COALESCE(SUM(l.capacity_sold), 0)::int AS cnt
        FROM   inventory_ledger l
        WHERE  l.session_id = s.id
          AND  l.tier_id IS NOT NULL
       ) sold
CROSS  JOIN LATERAL generate_series(1, s.capacity_total) gs
WHERE  s.admission_mode = 'general_admission'
  AND  s.capacity_total > 0
  AND  NOT EXISTS (
        SELECT 1 FROM session_seats x
        WHERE  x.session_id = s.id AND x.kind = 'ga_unit');

INSERT INTO session_seats
    (session_id, seat_key, sector_name, row_name, seat_number,
     tier_id, status, kind)
SELECT s.id,
       'ga|pool|' || lpad(gs::text, 6, '0'),
       '', '', '',
       NULL,
       'available',
       'ga_unit'
FROM   sessions s
JOIN   seating_plan_versions v ON v.id = s.seating_plan_version_id
CROSS  JOIN LATERAL generate_series(1, v.capacity_standing) gs
WHERE  s.admission_mode = 'hybrid'
  AND  v.capacity_standing > 0
  AND  NOT EXISTS (
        SELECT 1 FROM session_seats x
        WHERE  x.session_id = s.id AND x.kind = 'ga_unit');

-- +goose Down
DROP INDEX IF EXISTS session_seats_ga_alloc_idx;
DELETE FROM session_seats WHERE kind = 'ga_unit';
ALTER TABLE session_seats DROP COLUMN IF EXISTS kind;
