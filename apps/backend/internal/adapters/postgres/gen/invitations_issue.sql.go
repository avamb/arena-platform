// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: invitations_issue.sql

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InvitationSessionTarget is the session a staff invitation is issued for
// (spec 46, section 3.2).
type InvitationSessionTarget struct {
	ID      uuid.UUID
	EventID uuid.UUID
	OrgID   uuid.UUID
	Status  string
	// Deleted is true when the session or its event is soft-deleted.
	Deleted bool
	// Timezone is the venue's IANA zone, UTC for a session without a venue.
	Timezone string
}

const getInvitationSessionTarget = `-- name: GetInvitationSessionTarget :one
SELECT s.id, s.event_id, e.org_id, s.status, (s.deleted_at IS NOT NULL OR e.deleted_at IS NOT NULL) AS deleted,
       COALESCE(v.timezone, 'UTC') AS timezone
FROM   sessions s
JOIN   events e ON e.id = s.event_id
LEFT JOIN venues v ON v.id = s.venue_id
WHERE  s.id = $1`

// GetInvitationSessionTarget answers the session with its organization and
// the status / soft-delete facts the issue transaction gates on.
func (q *Queries) GetInvitationSessionTarget(ctx context.Context, sessionID uuid.UUID) (InvitationSessionTarget, error) {
	var t InvitationSessionTarget
	err := q.db.QueryRow(ctx, getInvitationSessionTarget, sessionID).Scan(
		&t.ID, &t.EventID, &t.OrgID, &t.Status, &t.Deleted, &t.Timezone,
	)
	return t, err
}

const lockInvitationGAUnits = `-- name: LockInvitationGAUnits :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
FROM   session_seats
WHERE  session_id = $1
  AND  kind       = 'ga_unit'
  AND  tier_id    = $2::uuid
  AND  status IN ('available', 'unavailable')
ORDER  BY (status = 'unavailable'), seat_key
LIMIT  $3
FOR UPDATE SKIP LOCKED`

// LockInvitationGAUnits locks up to limit places of one GA category for a
// staff invitation, free places first and then the organizer's reserve.
// Each row carries its status BEFORE the invitation takes it.
func (q *Queries) LockInvitationGAUnits(ctx context.Context, sessionID, tierID uuid.UUID, limit int32) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, lockInvitationGAUnits, sessionID, tierID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSeatRow
	for rows.Next() {
		s, err := scanSessionSeatRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const holdSessionSeatForInvitation = `-- name: HoldSessionSeatForInvitation :one
UPDATE session_seats
SET    status         = 'held',
       reservation_id = $2,
       status_version = $3,
       updated_at     = now()
WHERE  id     = $1
  AND  status IN ('available', 'unavailable')
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

// HoldSessionSeatForInvitation moves an available OR unavailable seat to
// held for the invitation's reservation. pgx.ErrNoRows when the seat is in
// any other state.
func (q *Queries) HoldSessionSeatForInvitation(ctx context.Context, id, reservationID uuid.UUID, statusVersion int64) (SessionSeatRow, error) {
	row := q.db.QueryRow(ctx, holdSessionSeatForInvitation, id, reservationID, statusVersion)
	return scanSessionSeatRow(row)
}

const setOrderStaffIssuance = `-- name: SetOrderStaffIssuance :exec
UPDATE orders
SET    issued_via = $2,
       issued_by  = $3,
       updated_at = now()
WHERE  id = $1`

// SetOrderStaffIssuance stamps how and by whom a staff invitation order was
// issued (migration 0176). issuedBy is nil for an organization API key.
func (q *Queries) SetOrderStaffIssuance(ctx context.Context, orderID uuid.UUID, issuedVia string, issuedBy *uuid.UUID) error {
	_, err := q.db.Exec(ctx, setOrderStaffIssuance, orderID, issuedVia, issuedBy)
	return err
}

const setInvitationCancelledBy = `-- name: SetInvitationCancelledBy :exec
UPDATE orders
SET    metadata   = COALESCE(metadata, '{}'::jsonb)
                    || jsonb_build_object('invitation',
                         COALESCE(metadata->'invitation', '{}'::jsonb)
                         || jsonb_build_object('cancelled_by', $2::jsonb)),
       updated_at = now()
WHERE  id = $1
  AND  issued_via IS NOT NULL`

// SetInvitationCancelledBy records who cancelled a staff invitation in
// orders.metadata.invitation.cancelled_by (spec 46 section 3.7). cancelledBy
// is {"user_id": ...} or {"api_key": name}; the last cancellation wins.
func (q *Queries) SetInvitationCancelledBy(ctx context.Context, orderID uuid.UUID, cancelledBy []byte) error {
	_, err := q.db.Exec(ctx, setInvitationCancelledBy, orderID, string(cancelledBy))
	return err
}

const setInvitationOrderCustomer = `-- name: SetInvitationOrderCustomer :exec
UPDATE orders
SET    customer_id = $2,
       updated_at  = now()
WHERE  id = $1
  AND  status = 'paid'
  AND  issued_via IS NOT NULL`

// SetInvitationOrderCustomer links a staff invitation to the guest's
// customer row AFTER the order is paid, on the issue transaction. Linking
// it while the order is still pending_payment would collide with
// orders_one_pending_per_customer_session_uq when the guest has an open
// purchase of their own on the same session. pgx.ErrNoRows when the order
// is not a paid staff invitation.
func (q *Queries) SetInvitationOrderCustomer(ctx context.Context, orderID, customerID uuid.UUID) error {
	tag, err := q.db.Exec(ctx, setInvitationOrderCustomer, orderID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

const setOrderItemsSeatOrigin = `-- name: SetOrderItemsSeatOrigin :execrows
UPDATE order_items oi
SET    seat_origin = v.origin
FROM   unnest($2::uuid[], $3::text[]) AS v(seat_id, origin)
WHERE  oi.order_id = $1
  AND  oi.session_seat_id = v.seat_id`

// SetOrderItemsSeatOrigin records, per line, the status each seat had
// before the invitation took it. seatIDs and origins are parallel slices.
// It answers the number of lines updated: the caller compares it with the
// number of places, a shortfall means a line lost its seat link.
func (q *Queries) SetOrderItemsSeatOrigin(ctx context.Context, orderID uuid.UUID, seatIDs []uuid.UUID, origins []string) (int64, error) {
	tag, err := q.db.Exec(ctx, setOrderItemsSeatOrigin, orderID, seatIDs, origins)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// InvitationRequestRef is the order an earlier request with the same
// request_id wrote.
type InvitationRequestRef struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	// Fingerprint is orders.metadata.invitation.fingerprint, the digest of
	// the request body; nil for an order written before it was stored.
	Fingerprint *string
}

const getInvitationOrderByRequest = `-- name: GetInvitationOrderByRequest :one
SELECT id, session_id, metadata->'invitation'->>'fingerprint' AS fingerprint
FROM   orders
WHERE  org_id = $1
  AND  external_ref = $2
  AND  issued_via IS NOT NULL`

// GetInvitationOrderByRequest finds a staff invitation by its request
// reference; pgx.ErrNoRows when the request has not been seen.
func (q *Queries) GetInvitationOrderByRequest(ctx context.Context, orgID uuid.UUID, externalRef string) (InvitationRequestRef, error) {
	var r InvitationRequestRef
	err := q.db.QueryRow(ctx, getInvitationOrderByRequest, orgID, externalRef).Scan(&r.ID, &r.SessionID, &r.Fingerprint)
	return r, err
}

// InvitationOrderRow is one staff invitation order.
type InvitationOrderRow struct {
	ID                uuid.UUID
	SystemID          int64
	SessionID         uuid.UUID
	CheckoutSessionID uuid.UUID
	Status            string
	BuyerName         *string
	Metadata          json.RawMessage
	IssuedVia         string
	IssuedBy          *uuid.UUID
	CreatedAt         time.Time
}

const getInvitationOrder = `-- name: GetInvitationOrder :one
SELECT o.id, o.system_id, o.session_id, o.checkout_session_id, o.status,
       o.buyer_name, o.metadata, o.issued_via, o.issued_by, o.created_at
FROM   orders o
WHERE  o.id = $1
  AND  o.org_id = $2
  AND  o.source = 'complimentary'
  AND  o.issued_via IS NOT NULL`

// GetInvitationOrder loads one staff invitation of the organization;
// pgx.ErrNoRows for any other order, including another organization's.
func (q *Queries) GetInvitationOrder(ctx context.Context, orderID, orgID uuid.UUID) (InvitationOrderRow, error) {
	var o InvitationOrderRow
	err := q.db.QueryRow(ctx, getInvitationOrder, orderID, orgID).Scan(
		&o.ID, &o.SystemID, &o.SessionID, &o.CheckoutSessionID, &o.Status,
		&o.BuyerName, &o.Metadata, &o.IssuedVia, &o.IssuedBy, &o.CreatedAt,
	)
	return o, err
}

// SessionInvitationRow is one row of the per-session invitation list.
type SessionInvitationRow struct {
	ID          uuid.UUID
	SystemID    int64
	Status      string
	BuyerName   *string
	Metadata    json.RawMessage
	IssuedVia   string
	IssuedBy    *uuid.UUID
	IssuerEmail *string
	IssuerFirst *string
	IssuerLast  *string
	CreatedAt   time.Time
	// BatchID is the CSV batch the invitation came from (migration 0175).
	BatchID       *uuid.UUID
	TicketsTotal  int64
	TicketsActive int64
}

const listSessionInvitations = `-- name: ListSessionInvitations :many
SELECT o.id, o.system_id, o.status, o.buyer_name, o.metadata, o.issued_via,
       o.issued_by, u.email, u.first_name, u.last_name, o.created_at, o.invitation_batch_id,
       (SELECT count(*) FROM tickets t WHERE t.checkout_session_id = o.checkout_session_id) AS tickets_total,
       (SELECT count(*) FROM tickets t WHERE t.checkout_session_id = o.checkout_session_id AND t.status = 'active') AS tickets_active
FROM   orders o
LEFT JOIN users u ON u.id = o.issued_by
WHERE  o.org_id = $1
  AND  o.session_id = $2
  AND  o.source = 'complimentary'
  AND  o.issued_via IS NOT NULL
ORDER  BY o.created_at DESC, o.id DESC
LIMIT  $3`

// ListSessionInvitations lists the staff invitations of a session, newest
// first, up to limit rows.
func (q *Queries) ListSessionInvitations(ctx context.Context, orgID, sessionID uuid.UUID, limit int32) ([]SessionInvitationRow, error) {
	rows, err := q.db.Query(ctx, listSessionInvitations, orgID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInvitationRow
	for rows.Next() {
		var r SessionInvitationRow
		if err := rows.Scan(
			&r.ID, &r.SystemID, &r.Status, &r.BuyerName, &r.Metadata, &r.IssuedVia,
			&r.IssuedBy, &r.IssuerEmail, &r.IssuerFirst, &r.IssuerLast, &r.CreatedAt, &r.BatchID,
			&r.TicketsTotal, &r.TicketsActive,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InvitationOrderLineRow is one place of a staff invitation order. The seat
// columns are nil for a line without a session_seats row.
type InvitationOrderLineRow struct {
	OrderID    uuid.UUID
	Ordinal    int32
	TicketID   *uuid.UUID
	UnitPrice  int64
	SeatOrigin *string
	SeatKey    *string
	SectorName *string
	RowName    *string
	SeatNumber *string
	Kind       *string
	TierID     uuid.UUID
	TierName   string
	// TicketStatus is tickets.status of the place's ticket, nil until it is issued.
	TicketStatus *string
	// VIP is the organizer's private VIP mark of the place's ticket, false until it is issued.
	VIP bool
	// Scanned is true once the place's ticket was used at the door (the test
	// refunds.ticketScannedSQL applies); false while it has no ticket.
	Scanned bool
}

const listInvitationOrderLines = `-- name: ListInvitationOrderLines :many
SELECT oi.order_id, oi.ordinal, oi.ticket_id, oi.unit_price, oi.seat_origin,
       ss.seat_key, ss.sector_name, ss.row_name, ss.seat_number, ss.kind,
       tt.id AS tier_id, tt.name AS tier_name, t.status AS ticket_status,
       coalesce(t.vip, false) AS vip,
       -- Used at the door: the same test as refunds.ticketScannedSQL, which
       -- refuses to cancel such a ticket.
       (t.used_at IS NOT NULL
        OR EXISTS (SELECT 1 FROM barcodes sb WHERE sb.ticket_id = t.id AND (sb.status = 'scanned' OR sb.scanned_at IS NOT NULL))
        OR EXISTS (SELECT 1 FROM scan_events se
                   JOIN sessions sx ON sx.id = t.session_id
                   JOIN events ev ON ev.id = sx.event_id
                   WHERE se.ticket_id = t.id AND se.result = 'admitted' AND se.org_id = ev.org_id)) AS scanned
FROM   order_items oi
JOIN   ticket_tiers tt ON tt.id = oi.tier_id
LEFT JOIN session_seats ss ON ss.id = oi.session_seat_id
LEFT JOIN tickets t ON t.id = oi.ticket_id
WHERE  oi.order_id = ANY($1::uuid[])
ORDER  BY oi.order_id, oi.ordinal`

// ListInvitationOrderLines answers the places of the given orders.
func (q *Queries) ListInvitationOrderLines(ctx context.Context, orderIDs []uuid.UUID) ([]InvitationOrderLineRow, error) {
	rows, err := q.db.Query(ctx, listInvitationOrderLines, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationOrderLineRow
	for rows.Next() {
		var r InvitationOrderLineRow
		if err := rows.Scan(
			&r.OrderID, &r.Ordinal, &r.TicketID, &r.UnitPrice, &r.SeatOrigin,
			&r.SeatKey, &r.SectorName, &r.RowName, &r.SeatNumber, &r.Kind,
			&r.TierID, &r.TierName, &r.TicketStatus, &r.VIP, &r.Scanned,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OrgInvitationRow is one row of the organization-wide invitation list: the
// same facts as SessionInvitationRow plus the session the invitation is for.
type OrgInvitationRow struct {
	SessionInvitationRow
	SessionID       uuid.UUID
	SessionStartAt  time.Time
	EventID         uuid.UUID
	EventName       string
	VenueName       *string
	SessionTimezone string
}

// OrgInvitationFilter narrows ListOrgInvitations. State is "active",
// "cancelled" or "all"; CursorAt/CursorID are the last row of the previous
// page (both nil = first page).
type OrgInvitationFilter struct {
	SessionID *uuid.UUID
	EventID   *uuid.UUID
	State     string
	CursorAt  *time.Time
	CursorID  *uuid.UUID
	Limit     int32
}

const listOrgInvitations = `-- name: ListOrgInvitations :many
SELECT o.id, o.system_id, o.status, o.buyer_name, o.metadata, o.issued_via,
       o.issued_by, u.email, u.first_name, u.last_name, o.created_at, o.invitation_batch_id,
       tc.total, tc.active,
       s.id, s.start_at, s.event_id, e.name, v.name, COALESCE(v.timezone, 'UTC')
FROM   orders o
JOIN   sessions s ON s.id = o.session_id
JOIN   events e ON e.id = s.event_id
LEFT JOIN venues v ON v.id = s.venue_id
LEFT JOIN users u ON u.id = o.issued_by
CROSS JOIN LATERAL (
    SELECT count(*) AS total, count(*) FILTER (WHERE t.status = 'active') AS active
    FROM   tickets t WHERE t.checkout_session_id = o.checkout_session_id
) tc
WHERE  o.org_id = $1
  AND  o.source = 'complimentary'
  AND  o.issued_via IS NOT NULL
  AND  ($2::uuid IS NULL OR o.session_id = $2)
  AND  ($3::uuid IS NULL OR s.event_id = $3)
  AND  ($4::text = 'all'
        OR ($4::text = 'cancelled' AND tc.total > 0 AND tc.active = 0)
        OR ($4::text = 'active' AND NOT (tc.total > 0 AND tc.active = 0)))
  AND  ($5::timestamptz IS NULL OR (o.created_at, o.id) < ($5, $6::uuid))
ORDER  BY o.created_at DESC, o.id DESC
LIMIT  $7`

// ListOrgInvitations lists the staff invitations of the whole organization,
// newest first, scoped by org_id (spec 46 section 6.1).
func (q *Queries) ListOrgInvitations(ctx context.Context, orgID uuid.UUID, f OrgInvitationFilter) ([]OrgInvitationRow, error) {
	rows, err := q.db.Query(ctx, listOrgInvitations, orgID, f.SessionID, f.EventID, f.State, f.CursorAt, f.CursorID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgInvitationRow
	for rows.Next() {
		var r OrgInvitationRow
		if err := rows.Scan(
			&r.ID, &r.SystemID, &r.Status, &r.BuyerName, &r.Metadata, &r.IssuedVia,
			&r.IssuedBy, &r.IssuerEmail, &r.IssuerFirst, &r.IssuerLast, &r.CreatedAt, &r.BatchID,
			&r.TicketsTotal, &r.TicketsActive,
			&r.SessionID, &r.SessionStartAt, &r.EventID, &r.EventName, &r.VenueName, &r.SessionTimezone,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InvitationLetterRow is the delivery_jobs of one invitation order folded into
// counts by status (see ListInvitationLetters).
type InvitationLetterRow struct {
	OrderID  uuid.UUID
	Jobs     int64
	Failed   int64
	Pending  int64
	Sent     int64
	Skipped  int64
	Disabled int64
	// SentAt is the latest sent_at among the sent jobs, nil when none was sent.
	SentAt *time.Time
	// LastError is the latest error text of a job that did not go out; it is
	// internal and never leaves the API as it is.
	LastError *string
}

const listInvitationLetters = `-- name: ListInvitationLetters :many
SELECT o.id,
       count(dj.id),
       count(*) FILTER (WHERE dj.status = 'failed'),
       count(*) FILTER (WHERE dj.status IN ('pending', 'processing')),
       count(*) FILTER (WHERE dj.status = 'sent'),
       count(*) FILTER (WHERE dj.status = 'skipped'),
       count(*) FILTER (WHERE dj.status = 'disabled'),
       max(dj.sent_at) FILTER (WHERE dj.status = 'sent'),
       (array_agg(dj.last_error ORDER BY dj.updated_at DESC)
            FILTER (WHERE dj.status IN ('failed', 'skipped', 'disabled') AND dj.last_error IS NOT NULL))[1]
FROM   orders o
JOIN   tickets t ON t.checkout_session_id = o.checkout_session_id
JOIN   delivery_jobs dj ON dj.ticket_id = t.id
WHERE  o.id = ANY($1::uuid[])
  AND  (t.status = 'active'
        OR NOT EXISTS (SELECT 1 FROM tickets a
                       WHERE a.checkout_session_id = o.checkout_session_id AND a.status = 'active'))
GROUP  BY o.id`

// ListInvitationLetters answers the letter state of the given orders in one
// statement. An order without delivery jobs has no row.
func (q *Queries) ListInvitationLetters(ctx context.Context, orderIDs []uuid.UUID) ([]InvitationLetterRow, error) {
	rows, err := q.db.Query(ctx, listInvitationLetters, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationLetterRow
	for rows.Next() {
		var r InvitationLetterRow
		if err := rows.Scan(
			&r.OrderID, &r.Jobs, &r.Failed, &r.Pending, &r.Sent, &r.Skipped, &r.Disabled,
			&r.SentAt, &r.LastError,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
