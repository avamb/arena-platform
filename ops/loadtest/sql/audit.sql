-- ops/loadtest/sql/audit.sql — inventory correctness audit after a load run.
-- LOCAL STAND. Read-only.
--
--   docker exec -i arena_postgres psql -U arena -d arena -v session_id=<uuid> < ops/loadtest/sql/audit.sql
--
-- Every row of the "violations" section must be 0. Any non-zero value is an
-- oversell, a double-sold unit, or inventory that never returns to sale.

\pset footer off
\echo '== session ledger (capacity / held / sold) =='
SELECT tier_id IS NULL AS session_level, capacity_total, capacity_held, capacity_sold,
       capacity_total - capacity_held - capacity_sold AS free
FROM inventory_ledger WHERE session_id = :'session_id'
ORDER BY session_level DESC, tier_id;

\echo '== ga units by status =='
SELECT kind, status, count(*) FROM session_seats WHERE session_id = :'session_id'
GROUP BY kind, status ORDER BY kind, status;

\echo '== reservations by state (expired_but_not_released = still active/draft past expires_at) =='
SELECT state, count(*) AS n, sum(quantity) AS qty,
       count(*) FILTER (WHERE expires_at < now() AND state IN ('draft','active')) AS expired_but_not_released
FROM reservations WHERE session_id = :'session_id' GROUP BY state ORDER BY state;

\echo '== tickets by status =='
SELECT status, count(*) FROM tickets WHERE session_id = :'session_id' GROUP BY status ORDER BY status;

\echo '== violations (all must be 0) =='
WITH led AS (
  SELECT capacity_total, capacity_held, capacity_sold
  FROM inventory_ledger WHERE session_id = :'session_id' AND tier_id IS NULL
), live_tickets AS (
  SELECT * FROM tickets
  WHERE session_id = :'session_id' AND cancelled_at IS NULL AND status NOT IN ('cancelled','refunded','void')
)
SELECT
  (SELECT count(*) FROM (SELECT seat_key FROM live_tickets WHERE seat_key IS NOT NULL
                         GROUP BY seat_key HAVING count(*) > 1) d)                       AS double_sold_units,
  (SELECT greatest(0, count(*) - (SELECT capacity_total FROM led)) FROM live_tickets)    AS tickets_over_capacity,
  (SELECT abs(count(*) - (SELECT capacity_sold FROM led)) FROM live_tickets)             AS ledger_sold_vs_tickets_drift,
  (SELECT abs((SELECT count(*) FROM session_seats WHERE session_id = :'session_id'
                AND kind = 'ga_unit' AND status = 'sold') - (SELECT capacity_sold FROM led))) AS units_sold_vs_ledger_drift,
  (SELECT count(*) FROM reservations WHERE session_id = :'session_id'
     AND state IN ('draft','active') AND expires_at < now() - interval '2 minutes')      AS holds_expired_over_2m_not_released;
