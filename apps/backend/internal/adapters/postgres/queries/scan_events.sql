-- scan_events.sql — sqlc query definitions for the scan_events table (feature #293).
--
-- The scanner callback endpoint (POST /v1/scanner/scan-events) ingests a batch
-- of scan reports.  Each row is inserted idempotently: a duplicate
-- (credential_code, scanned_at) pair from a retried request collapses to a
-- no-op (ON CONFLICT DO NOTHING), and the original row id is returned so
-- the handler can report consistent results across retries.

-- name: InsertScanEvent :one
INSERT INTO scan_events (
    org_id, event_id, session_id, ticket_id,
    credential_code, scanned_at, gate, device_id, result
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (credential_code, scanned_at) DO UPDATE
    SET credential_code = EXCLUDED.credential_code
RETURNING id, org_id, event_id, session_id, ticket_id,
          credential_code, scanned_at, gate, device_id, result, received_at,
          -- xmax = 0 on a real INSERT, non-zero on an ON CONFLICT path,
          -- so the handler can tell first-insert from idempotent replay
          -- without a separate SELECT.
          (xmax = 0)::boolean AS inserted;

-- name: ResolveFeedTokenScannerScope :one
-- Resolve a presented feed-token value to its sales_channel + org so the
-- scanner callback can persist tenancy on each scan_events row.  Rejects
-- revoked tokens (is_active = false).
SELECT ft.id               AS feed_token_id,
       ft.sales_channel_id AS sales_channel_id,
       sc.org_id           AS org_id
FROM   agent_feed_tokens ft
JOIN   sales_channels    sc ON sc.id = ft.sales_channel_id
WHERE  ft.token     = $1
  AND  ft.is_active = true
  AND  sc.deleted_at IS NULL;

-- name: GetScanEventByCredential :one
SELECT id, org_id, event_id, session_id, ticket_id,
       credential_code, scanned_at, gate, device_id, result, received_at
FROM   scan_events
WHERE  credential_code = $1
  AND  scanned_at      = $2;

-- name: ResolveScanCredentialByTicketQR :one
-- Resolve a credential_code presented at the gate to its parent ticket.
-- Matches ticket_credentials.payload for the static_qr credential type,
-- returning the ticket lineage required for tenancy / event linkage.
SELECT t.id          AS ticket_id,
       t.session_id  AS session_id,
       s.event_id    AS event_id,
       e.org_id      AS org_id,
       t.status      AS ticket_status,
       t.used_at     AS ticket_used_at
FROM   ticket_credentials tc
JOIN   tickets  t ON t.id = tc.ticket_id
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  tc.type    = 'static_qr'
  AND  tc.payload = $1;

-- name: MarkTicketUsedAtIfUnset :exec
-- Idempotent: only sets used_at when the column is currently NULL.  The
-- first admitted scan wins; subsequent scans (even with earlier scanned_at)
-- do not overwrite the column.
UPDATE tickets
SET    used_at    = $2,
       updated_at = now()
WHERE  id        = $1
  AND  used_at IS NULL;
