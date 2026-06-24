-- event_reports.sql — queries for post-event report generation (feature #159).
--
-- These queries support:
--   1. Creating and updating event_reports rows (state machine).
--   2. Inserting and listing event_report_lines (one row per category).
--   3. Aggregation queries that compute the metric values for each category
--      by joining tickets, checkout_sessions, refunds, and barcodes.

-- ── event_reports CRUD ────────────────────────────────────────────────────────

-- name: InsertEventReport :one
INSERT INTO event_reports (
    event_id,
    org_id,
    state,
    report_window_start,
    report_window_end
)
VALUES ($1, $2, 'pending', $3, $4)
RETURNING id, event_id, org_id, state, report_window_start, report_window_end,
          error_msg, generated_at, created_at, updated_at;

-- name: GetEventReportByID :one
SELECT id, event_id, org_id, state, report_window_start, report_window_end,
       error_msg, generated_at, created_at, updated_at
FROM   event_reports
WHERE  id = $1;

-- name: GetEventReportByEventID :one
-- Returns the most recent report for an event (latest created_at).
SELECT id, event_id, org_id, state, report_window_start, report_window_end,
       error_msg, generated_at, created_at, updated_at
FROM   event_reports
WHERE  event_id = $1
ORDER  BY created_at DESC
LIMIT  1;

-- name: UpdateEventReportState :one
UPDATE event_reports
SET    state        = $2,
       error_msg    = $3,
       generated_at = CASE WHEN $2 = 'ready' THEN now() ELSE generated_at END,
       updated_at   = now()
WHERE  id = $1
RETURNING id, event_id, org_id, state, report_window_start, report_window_end,
          error_msg, generated_at, created_at, updated_at;

-- ── event_report_lines CRUD ───────────────────────────────────────────────────

-- name: InsertEventReportLine :one
INSERT INTO event_report_lines (
    report_id,
    category,
    quantity,
    gross_amount,
    net_amount,
    currency
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, report_id, category, quantity, gross_amount, net_amount, currency, created_at;

-- name: ListEventReportLinesByReport :many
SELECT id, report_id, category, quantity, gross_amount, net_amount, currency, created_at
FROM   event_report_lines
WHERE  report_id = $1
ORDER  BY category;

-- ── Aggregation queries ───────────────────────────────────────────────────────
-- These queries drive the report generation worker (event.generate_report job).
-- Each query aggregates one category of metrics for a given event_id.

-- name: AggregateSalesForEvent :one
-- Counts paid (non-free) tickets and their checkout totals for an event.
-- Joins tickets → sessions (for event scoping) → checkout_sessions (for amounts).
-- Only counts completed checkouts where total > 0 (paid sales, not complimentary).
SELECT
    COUNT(t.id)::bigint                                                           AS quantity,
    COALESCE(SUM(COALESCE(cs.total, 0)), 0)::bigint                              AS gross_amount,
    COALESCE(SUM(
        COALESCE(cs.total, 0)
        - COALESCE(cs.platform_fee, 0)
        - COALESCE(cs.provider_fee, 0)
    ), 0)::bigint                                                                 AS net_amount,
    COALESCE(MAX(cs.currency), 'usd')                                             AS currency
FROM   tickets t
JOIN   sessions s  ON t.session_id        = s.id
JOIN   checkout_sessions cs ON t.checkout_session_id = cs.id
WHERE  s.event_id  = $1
  AND  cs.state    = 'completed'
  AND  COALESCE(cs.total, 0) > 0
  AND  t.status    = 'active';

-- name: AggregateComplimentaryForEvent :one
-- Counts free (zero-price) tickets for an event.
-- Complimentary tickets have total = 0 on a completed checkout session.
SELECT
    COUNT(t.id)::bigint AS quantity,
    0::bigint           AS gross_amount,
    0::bigint           AS net_amount,
    'usd'               AS currency
FROM   tickets t
JOIN   sessions s  ON t.session_id        = s.id
JOIN   checkout_sessions cs ON t.checkout_session_id = cs.id
WHERE  s.event_id  = $1
  AND  cs.state    = 'completed'
  AND  COALESCE(cs.total, 0) = 0;

-- name: AggregateRefundsForEvent :one
-- Counts succeeded refunds linked to tickets for an event.
-- Joins refunds → payment_intents → checkout_sessions → tickets → sessions.
SELECT
    COUNT(DISTINCT r.id)::bigint                       AS quantity,
    COALESCE(SUM(r.amount), 0)::bigint                 AS gross_amount,
    COALESCE(SUM(r.amount), 0)::bigint                 AS net_amount,
    COALESCE(MAX(r.currency), 'usd')                   AS currency
FROM   refunds r
JOIN   payment_intents pi ON r.payment_intent_id = pi.id
JOIN   checkout_sessions cs ON pi.checkout_session_id = cs.id
JOIN   tickets t  ON t.checkout_session_id = cs.id
JOIN   sessions s ON t.session_id          = s.id
WHERE  s.event_id = $1
  AND  r.state    = 'succeeded';

-- name: AggregateScansForEvent :one
-- Counts successfully scanned barcodes for tickets in an event.
-- A barcode is considered scanned when scanned_at IS NOT NULL.
SELECT
    COUNT(b.id)::bigint AS quantity,
    0::bigint           AS gross_amount,
    0::bigint           AS net_amount,
    'usd'               AS currency
FROM   barcodes b
JOIN   tickets t  ON b.ticket_id  = t.id
JOIN   sessions s ON t.session_id = s.id
WHERE  s.event_id     = $1
  AND  b.scanned_at  IS NOT NULL;
