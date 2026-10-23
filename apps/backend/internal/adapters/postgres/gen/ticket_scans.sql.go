// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: ticket_scans.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// ListTicketScans
// ─────────────────────────────────────────────────────────────────────────────

const listTicketScans = `-- name: ListTicketScans :many
SELECT ticket_id, scanned_at, result, source, scanner_label, gate
FROM (
    SELECT se.ticket_id, se.scanned_at, se.result, se.source, se.scanner_label, se.gate,
           row_number() OVER (PARTITION BY se.ticket_id ORDER BY se.scanned_at DESC, se.id DESC) AS rn
    FROM   scan_events se
    WHERE  se.org_id = $1 AND se.ticket_id = ANY($2::uuid[])
) x
WHERE  rn <= $3
ORDER  BY ticket_id, scanned_at DESC`

// TicketScanRow is one scan of a ticket at a door, as the order card prints
// it. Source is arena_scanner or macs; ScannerLabel is the reporting system's
// own name for the device (MACS only) and Gate the gate it stood at.
type TicketScanRow struct {
	TicketID     uuid.UUID `json:"ticket_id"`
	ScannedAt    time.Time `json:"scanned_at"`
	Result       string    `json:"result"`
	Source       string    `json:"source"`
	ScannerLabel string    `json:"scanner_label"`
	Gate         string    `json:"gate"`
}

// ListTicketScans returns, for each of the given tickets, its last perTicket
// scans (any result), newest first, among the scan rows recorded for the
// organization.
func (q *Queries) ListTicketScans(ctx context.Context, orgID uuid.UUID, ticketIDs []uuid.UUID, perTicket int32) ([]TicketScanRow, error) {
	rows, err := q.db.Query(ctx, listTicketScans, orgID, ticketIDs, perTicket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TicketScanRow
	for rows.Next() {
		var i TicketScanRow
		if err := rows.Scan(&i.TicketID, &i.ScannedAt, &i.Result, &i.Source, &i.ScannerLabel, &i.Gate); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// CountSessionTicketsForEntry / ListSessionTicketsForEntry
// ─────────────────────────────────────────────────────────────────────────────

// sessionTicketsEntryBase computes, per ticket of session $1, when it was
// first entered: used_at, else the first admitted scan row of organization
// $2, else the newest scanned barcode (the legacy import's). NULL = not
// entered.
const sessionTicketsEntryBase = `WITH base AS (
    SELECT t.id AS ticket_id,
           COALESCE(t.used_at,
                    (SELECT min(se.scanned_at) FROM scan_events se
                     WHERE se.ticket_id = t.id AND se.result = 'admitted' AND se.org_id = $2),
                    (SELECT max(b.scanned_at) FROM barcodes b WHERE b.ticket_id = t.id)) AS entered_at
    FROM   tickets t
    WHERE  t.session_id = $1
)
`

// sessionTicketsEntryFilter keeps every ticket ($3 NULL), the entered ones
// ($3 true) or the active ones nobody entered with ($3 false).
const sessionTicketsEntryFilter = `WHERE  ($3::boolean IS NULL
        OR ($3::boolean AND base.entered_at IS NOT NULL)
        OR (NOT $3::boolean AND base.entered_at IS NULL AND t.status = 'active'))`

const countSessionTicketsForEntry = `-- name: CountSessionTicketsForEntry :one
` + sessionTicketsEntryBase + `SELECT count(*)
FROM   base
JOIN   tickets t ON t.id = base.ticket_id
` + sessionTicketsEntryFilter

// CountSessionTicketsForEntry counts the tickets of the session a
// ListSessionTicketsForEntry with the same filter pages through. entered:
// nil = every ticket, false = active tickets nobody entered with, true =
// tickets with an entry.
func (q *Queries) CountSessionTicketsForEntry(ctx context.Context, sessionID, orgID uuid.UUID, entered *bool) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countSessionTicketsForEntry, sessionID, orgID, entered).Scan(&n)
	return n, err
}

const listSessionTicketsForEntry = `-- name: ListSessionTicketsForEntry :many
` + sessionTicketsEntryBase + `SELECT t.id, t.system_ticket_id, t.status, t.seat_sector, t.seat_row, t.seat_number,
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
` + sessionTicketsEntryFilter + `
ORDER  BY t.system_ticket_id
LIMIT  $4 OFFSET $5`

// SessionTicketEntryRow is one ticket of a session with its order, buyer,
// first entry and last scan. Every order-side field is nil for an invitation
// (no order); the last-scan fields are nil for a ticket nobody scanned.
type SessionTicketEntryRow struct {
	TicketID         uuid.UUID  `json:"ticket_id"`
	SystemTicketID   int64      `json:"system_ticket_id"`
	Status           string     `json:"status"`
	SeatSector       *string    `json:"seat_sector"`
	SeatRow          *string    `json:"seat_row"`
	SeatNumber       *string    `json:"seat_number"`
	TierName         *string    `json:"tier_name"`
	OrderID          *uuid.UUID `json:"order_id"`
	OrderNumber      *int64     `json:"order_number"`
	BuyerName        *string    `json:"buyer_name"`
	BuyerEmail       *string    `json:"buyer_email"`
	EnteredAt        *time.Time `json:"entered_at"`
	LastScannedAt    *time.Time `json:"last_scanned_at"`
	LastResult       *string    `json:"last_result"`
	LastSource       *string    `json:"last_source"`
	LastScannerLabel *string    `json:"last_scanner_label"`
	LastGate         *string    `json:"last_gate"`
}

// ListSessionTicketsForEntry returns one page of the session's tickets in
// ticket-number order, filtered by entry (see CountSessionTicketsForEntry).
func (q *Queries) ListSessionTicketsForEntry(ctx context.Context, sessionID, orgID uuid.UUID, entered *bool, limit, offset int32) ([]SessionTicketEntryRow, error) {
	rows, err := q.db.Query(ctx, listSessionTicketsForEntry, sessionID, orgID, entered, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionTicketEntryRow
	for rows.Next() {
		var i SessionTicketEntryRow
		if err := rows.Scan(
			&i.TicketID, &i.SystemTicketID, &i.Status, &i.SeatSector, &i.SeatRow, &i.SeatNumber,
			&i.TierName, &i.OrderID, &i.OrderNumber, &i.BuyerName, &i.BuyerEmail,
			&i.EnteredAt, &i.LastScannedAt, &i.LastResult, &i.LastSource, &i.LastScannerLabel, &i.LastGate,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
