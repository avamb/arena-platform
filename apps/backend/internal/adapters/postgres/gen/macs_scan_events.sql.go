// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: scan_events.sql (MACS section, migration 0164)
//
// M1 — scans MACS posts back to arena through POST /v1/macs/scan-events.

package gen

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// InsertMACSScanEvent
// ─────────────────────────────────────────────────────────────────────────────

const insertMACSScanEvent = `-- name: InsertMACSScanEvent :one
INSERT INTO scan_events (
    org_id, event_id, session_id, ticket_id,
    credential_code, scanned_at, gate, device_id, result,
    source, scanner_label, external_scan_id, subscriber_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'macs', $10, $11, $12)
ON CONFLICT DO NOTHING
RETURNING id, org_id, event_id, session_id, ticket_id,
          credential_code, scanned_at, gate, device_id, result, received_at`

// InsertMACSScanEventParams is one scan reported by MACS. TicketID,
// SessionID and EventID are always set: MACS scans are recorded only for a
// ticket that was resolved inside the subscriber's organization.
type InsertMACSScanEventParams struct {
	OrgID          uuid.UUID
	EventID        uuid.UUID
	SessionID      uuid.UUID
	TicketID       uuid.UUID
	CredentialCode string
	ScannedAt      time.Time
	Gate           string
	DeviceID       string
	Result         string
	ScannerLabel   string
	ExternalScanID string
	SubscriberID   uuid.UUID
}

// InsertMACSScanEvent inserts one MACS scan with source='macs'. inserted is
// false when either unique index already holds the scan
// ((subscriber_id, external_scan_id) or (credential_code, scanned_at)); the
// caller then reads the existing row with GetScanEventBySubscriberScan or
// GetScanEventByCredential. Nothing is written in that case.
func (q *Queries) InsertMACSScanEvent(ctx context.Context, p InsertMACSScanEventParams) (row ScanEventRow, inserted bool, err error) {
	r := q.db.QueryRow(ctx, insertMACSScanEvent,
		p.OrgID, p.EventID, p.SessionID, p.TicketID,
		p.CredentialCode, p.ScannedAt, p.Gate, p.DeviceID, p.Result,
		p.ScannerLabel, p.ExternalScanID, p.SubscriberID,
	)
	row, err = scanScanEventRow(r)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanEventRow{}, false, nil
	}
	if err != nil {
		return ScanEventRow{}, false, err
	}
	return row, true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetScanEventBySubscriberScan
// ─────────────────────────────────────────────────────────────────────────────

const getScanEventBySubscriberScan = `-- name: GetScanEventBySubscriberScan :one
SELECT id, org_id, event_id, session_id, ticket_id,
       credential_code, scanned_at, gate, device_id, result, received_at
FROM   scan_events
WHERE  subscriber_id    = $1
  AND  external_scan_id = $2`

// GetScanEventBySubscriberScan reads the scan a subscriber already delivered
// under its own scan id. Returns pgx.ErrNoRows when there is none.
func (q *Queries) GetScanEventBySubscriberScan(ctx context.Context, subscriberID uuid.UUID, externalScanID string) (ScanEventRow, error) {
	return scanScanEventRow(q.db.QueryRow(ctx, getScanEventBySubscriberScan, subscriberID, externalScanID))
}

// ─────────────────────────────────────────────────────────────────────────────
// ResolveMACSScanTicket*
// ─────────────────────────────────────────────────────────────────────────────

// MACSScanTicketRow is the ticket a MACS scan names, with the lineage the
// scan row needs and the facts the handler decides on. The row is locked
// (FOR UPDATE OF t) until the batch transaction ends.
type MACSScanTicketRow struct {
	TicketID       uuid.UUID  `json:"ticket_id"`
	SessionID      uuid.UUID  `json:"session_id"`
	EventID        uuid.UUID  `json:"event_id"`
	OrgID          uuid.UUID  `json:"org_id"`
	Status         string     `json:"status"`
	UsedAt         *time.Time `json:"used_at"`
	SystemTicketID int64      `json:"system_ticket_id"`
}

func scanMACSScanTicketRow(row pgx.Row) (MACSScanTicketRow, error) {
	var r MACSScanTicketRow
	err := row.Scan(&r.TicketID, &r.SessionID, &r.EventID, &r.OrgID,
		&r.Status, &r.UsedAt, &r.SystemTicketID)
	return r, err
}

const resolveMACSScanTicketBySystemID = `-- name: ResolveMACSScanTicketBySystemID :one
SELECT t.id, t.session_id, s.event_id, e.org_id, t.status, t.used_at,
       t.system_ticket_id
FROM   tickets  t
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  t.system_ticket_id = $1
  AND  e.org_id = $2
FOR UPDATE OF t`

// ResolveMACSScanTicketBySystemID resolves MACS's integer ticket id
// (tickets.system_ticket_id) inside orgID, the subscriber's organization.
// Returns pgx.ErrNoRows when unknown or another organization's: a foreign
// ticket is never locked.
func (q *Queries) ResolveMACSScanTicketBySystemID(ctx context.Context, systemTicketID int64, orgID uuid.UUID) (MACSScanTicketRow, error) {
	return scanMACSScanTicketRow(q.db.QueryRow(ctx, resolveMACSScanTicketBySystemID, systemTicketID, orgID))
}

const resolveMACSScanTicketByBarcode = `-- name: ResolveMACSScanTicketByBarcode :one
SELECT t.id, t.session_id, s.event_id, e.org_id, t.status, t.used_at,
       t.system_ticket_id
FROM   barcodes b
JOIN   tickets  t ON t.id = b.ticket_id
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  b.external_ref = $1
  AND  b.ticket_id IS NOT NULL
  AND  e.org_id = $2
ORDER  BY b.created_at, b.id
LIMIT  1
FOR UPDATE OF t`

// ResolveMACSScanTicketByBarcode resolves a 13-digit barcode through
// barcodes.external_ref (any authority: the minted EAN-13 credential is
// written there with the same value, an imported legacy barcode lives only
// there) inside orgID. Returns pgx.ErrNoRows when no ticket of orgID
// carries the code.
func (q *Queries) ResolveMACSScanTicketByBarcode(ctx context.Context, barcode string, orgID uuid.UUID) (MACSScanTicketRow, error) {
	return scanMACSScanTicketRow(q.db.QueryRow(ctx, resolveMACSScanTicketByBarcode, barcode, orgID))
}
