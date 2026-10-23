-- ticket_scans.sql — what the order card and the session ticket list show
-- about entry at the door (task M2): the scans of a ticket, and the tickets
-- of a session with or without an entry. A scan row is read only inside the
-- organization it was recorded for (scan_events.org_id), so a ticket cannot
-- surface a scan of another organization.
--
-- A ticket is "entered" when it carries used_at (the first admission, set by
-- the arena scanner and by MACS alike), an admitted scan row of its
-- organization, or a scanned barcode (the legacy import) — the same reading
-- the refund guard uses (refunds/scanned.go). entered_at is the first of them.

-- name: ListTicketScans :many
-- The last $3 scans (any result) of each of the tickets $2 of organization
-- $1, newest first within a ticket.
SELECT ticket_id, scanned_at, result, source, scanner_label, gate
FROM (
    SELECT se.ticket_id, se.scanned_at, se.result, se.source, se.scanner_label, se.gate,
           row_number() OVER (PARTITION BY se.ticket_id ORDER BY se.scanned_at DESC, se.id DESC) AS rn
    FROM   scan_events se
    WHERE  se.org_id = $1 AND se.ticket_id = ANY($2::uuid[])
) x
WHERE  rn <= $3
ORDER  BY ticket_id, scanned_at DESC;

-- name: CountSessionTicketsForEntry :one
-- Tickets of session $1 in organization $2. $3: NULL = every ticket,
-- false = active tickets nobody entered with, true = tickets with an entry.
WITH base AS (
    SELECT t.id AS ticket_id,
           COALESCE(t.used_at,
                    (SELECT min(se.scanned_at) FROM scan_events se
                     WHERE se.ticket_id = t.id AND se.result = 'admitted' AND se.org_id = $2),
                    (SELECT max(b.scanned_at) FROM barcodes b WHERE b.ticket_id = t.id)) AS entered_at
    FROM   tickets t
    WHERE  t.session_id = $1
)
SELECT count(*)
FROM   base
JOIN   tickets t ON t.id = base.ticket_id
WHERE  ($3::boolean IS NULL
        OR ($3::boolean AND base.entered_at IS NOT NULL)
        OR (NOT $3::boolean AND base.entered_at IS NULL AND t.status = 'active'));

-- name: ListSessionTicketsForEntry :many
-- The same filter, one page ($4 rows after $5), ordered by ticket number. The
-- last scan is the newest scan row of any result, entered_at the first
-- admission.
WITH base AS (
    SELECT t.id AS ticket_id,
           COALESCE(t.used_at,
                    (SELECT min(se.scanned_at) FROM scan_events se
                     WHERE se.ticket_id = t.id AND se.result = 'admitted' AND se.org_id = $2),
                    (SELECT max(b.scanned_at) FROM barcodes b WHERE b.ticket_id = t.id)) AS entered_at
    FROM   tickets t
    WHERE  t.session_id = $1
)
SELECT t.id, t.system_ticket_id, t.status, t.seat_sector, t.seat_row, t.seat_number,
       tt.name AS tier_name,
       o.id AS order_id, o.system_id AS order_number,
       o.buyer_name, COALESCE(o.buyer_email, t.holder_email) AS buyer_email,
       base.entered_at,
       ls.scanned_at AS last_scanned_at, ls.result AS last_result, ls.source AS last_source,
       ls.scanner_label AS last_scanner_label, ls.gate AS last_gate
FROM   base
JOIN   tickets t ON t.id = base.ticket_id
LEFT JOIN orders o ON o.id = t.order_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN LATERAL (
    SELECT se.scanned_at, se.result, se.source, se.scanner_label, se.gate
    FROM   scan_events se
    WHERE  se.ticket_id = t.id AND se.org_id = $2
    ORDER  BY se.scanned_at DESC, se.id DESC
    LIMIT  1
) ls ON true
WHERE  ($3::boolean IS NULL
        OR ($3::boolean AND base.entered_at IS NOT NULL)
        OR (NOT $3::boolean AND base.entered_at IS NULL AND t.status = 'active'))
ORDER  BY t.system_ticket_id
LIMIT  $4 OFFSET $5;
