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

-- name: ResolveScanCredentialByHumanCode :one
-- Resolve a SEAT-C4 human-readable credential code presented at the gate
-- to its parent ticket. Same lineage projection as
-- ResolveScanCredentialByTicketQR, but matched against
-- ticket_credentials.human_code (canonical 8-char Crockford Base32 form;
-- callers normalize input via humancode.Normalize before lookup).
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
WHERE  tc.type       = 'static_qr'
  AND  tc.human_code = $1;

-- name: ListScanEventsByTicketID :many
-- Read-only list of scan events for a single ticket. Used by the support
-- console drawer (feature #295, S-4). Ordered newest-first so the drawer
-- can show the most recent admission attempt at the top. The limit guards
-- against runaway result sets on pathological replays / DOS-shaped scans
-- (e.g. a single ticket scanned thousands of times in an offline-backfill
-- window).
SELECT id, org_id, event_id, session_id, ticket_id,
       credential_code, scanned_at, gate, device_id, result, received_at
FROM   scan_events
WHERE  ticket_id = $1
ORDER  BY scanned_at DESC, id DESC
LIMIT  $2;

-- name: MarkTicketUsedAtIfUnset :exec
-- Idempotent: only sets used_at when the column is currently NULL.  The
-- first admitted scan wins; subsequent scans (even with earlier scanned_at)
-- do not overwrite the column.
UPDATE tickets
SET    used_at    = $2,
       updated_at = now()
WHERE  id        = $1
  AND  used_at IS NULL;

-- ─── MACS scans (POST /v1/macs/scan-events, migration 0164) ────────────────

-- name: InsertMACSScanEvent :one
-- One scan reported by MACS. ON CONFLICT DO NOTHING covers BOTH unique
-- indexes: (subscriber_id, external_scan_id) — MACS re-sent a scan it
-- already delivered — and (credential_code, scanned_at) — the same physical
-- scan is already recorded. No row back means "already recorded"; the
-- handler then looks the existing row up.
INSERT INTO scan_events (
    org_id, event_id, session_id, ticket_id,
    credential_code, scanned_at, gate, device_id, result,
    source, scanner_label, external_scan_id, subscriber_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'macs', $10, $11, $12)
ON CONFLICT DO NOTHING
RETURNING id, org_id, event_id, session_id, ticket_id,
          credential_code, scanned_at, gate, device_id, result, received_at;

-- name: GetScanEventBySubscriberScan :one
SELECT id, org_id, event_id, session_id, ticket_id,
       credential_code, scanned_at, gate, device_id, result, received_at
FROM   scan_events
WHERE  subscriber_id    = $1
  AND  external_scan_id = $2;

-- name: ResolveMACSScanTicketBySystemID :one
-- The ticket MACS names by its integer id (macs export `id` =
-- tickets.system_ticket_id), locked for the admission stamp. has_ean13 tells
-- the legacy barcode fallback whether the ticket carries a stored EAN-13.
SELECT t.id, t.session_id, s.event_id, e.org_id, t.status, t.used_at,
       t.system_ticket_id,
       EXISTS (SELECT 1 FROM ticket_credentials tc
               WHERE tc.ticket_id = t.id AND tc.type = 'ean13') AS has_ean13
FROM   tickets  t
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  t.system_ticket_id = $1
FOR UPDATE OF t;

-- name: ResolveMACSScanTicketByBarcode :one
-- The ticket behind a 13-digit barcode MACS printed (macs export `barcode`).
-- Goes through barcodes.external_ref (indexed, migration 0106): the minted
-- EAN-13 credential is written there first with the same value, and an
-- imported legacy barcode lives only there.
SELECT t.id, t.session_id, s.event_id, e.org_id, t.status, t.used_at,
       t.system_ticket_id,
       EXISTS (SELECT 1 FROM ticket_credentials tc
               WHERE tc.ticket_id = t.id AND tc.type = 'ean13') AS has_ean13
FROM   barcodes b
JOIN   tickets  t ON t.id = b.ticket_id
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  b.external_ref = $1
  AND  b.ticket_id IS NOT NULL
ORDER  BY b.created_at, b.id
LIMIT  1
FOR UPDATE OF t;
