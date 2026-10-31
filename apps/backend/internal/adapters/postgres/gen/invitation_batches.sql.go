// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: invitation_batches.sql

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// InvitationPlanSeat is one assigned seat of a session as a CSV file
// addresses it.
type InvitationPlanSeat struct {
	ID         uuid.UUID
	SeatKey    string
	SectorName *string
	RowName    *string
	SeatNumber *string
	TierID     *uuid.UUID
	Status     string
}

const listInvitationPlanSeats = `-- name: ListInvitationPlanSeats :many
SELECT id, seat_key, sector_name, row_name, seat_number, tier_id, status
FROM   session_seats
WHERE  session_id = $1
  AND  kind <> 'ga_unit'`

// ListInvitationPlanSeats reads every assigned seat of the session, not
// locked.
func (q *Queries) ListInvitationPlanSeats(ctx context.Context, sessionID uuid.UUID) ([]InvitationPlanSeat, error) {
	rows, err := q.db.Query(ctx, listInvitationPlanSeats, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationPlanSeat
	for rows.Next() {
		var s InvitationPlanSeat
		if err := rows.Scan(&s.ID, &s.SeatKey, &s.SectorName, &s.RowName, &s.SeatNumber, &s.TierID, &s.Status); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// InvitationGACount is how many places of one General Admission category an
// invitation may take.
type InvitationGACount struct {
	TierID    uuid.UUID
	Available int64
	Reserve   int64
}

const listInvitationGACounts = `-- name: ListInvitationGACounts :many
SELECT tier_id,
       count(*) FILTER (WHERE status = 'available')   AS available,
       count(*) FILTER (WHERE status = 'unavailable') AS reserve
FROM   session_seats
WHERE  session_id = $1
  AND  kind = 'ga_unit'
  AND  tier_id IS NOT NULL
GROUP  BY tier_id`

// ListInvitationGACounts counts, per category, the free and reserved
// General Admission places.
func (q *Queries) ListInvitationGACounts(ctx context.Context, sessionID uuid.UUID) ([]InvitationGACount, error) {
	rows, err := q.db.Query(ctx, listInvitationGACounts, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationGACount
	for rows.Next() {
		var c InvitationGACount
		if err := rows.Scan(&c.TierID, &c.Available, &c.Reserve); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// InvitationBatchRow is one invitation_batches row (migration 0174).
type InvitationBatchRow struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	SessionID   uuid.UUID
	CreatedBy   *uuid.UUID
	FileSHA256  string
	RequestID   uuid.UUID
	Rows        int32
	Seats       int32
	FromReserve int32
	Status      string
	ErrorReport json.RawMessage
	CreatedAt   time.Time
}

const invitationBatchColumns = `id, org_id, session_id, created_by, file_sha256, request_id, rows, seats,
       from_reserve, status, error_report, created_at`

func scanInvitationBatch(row interface{ Scan(dest ...any) error }) (InvitationBatchRow, error) {
	var b InvitationBatchRow
	err := row.Scan(&b.ID, &b.OrgID, &b.SessionID, &b.CreatedBy, &b.FileSHA256, &b.RequestID,
		&b.Rows, &b.Seats, &b.FromReserve, &b.Status, &b.ErrorReport, &b.CreatedAt)
	return b, err
}

const insertInvitationBatch = `-- name: InsertInvitationBatch :one
INSERT INTO invitation_batches (org_id, session_id, created_by, file_sha256, request_id, rows, seats)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING ` + invitationBatchColumns

// InsertInvitationBatch writes the batch row an upload's orders point at.
func (q *Queries) InsertInvitationBatch(ctx context.Context, orgID, sessionID uuid.UUID, createdBy *uuid.UUID, fileSHA256 string, requestID uuid.UUID, rows, seats int32) (InvitationBatchRow, error) {
	return scanInvitationBatch(q.db.QueryRow(ctx, insertInvitationBatch,
		orgID, sessionID, createdBy, fileSHA256, requestID, rows, seats))
}

const getInvitationBatchByRequest = `-- name: GetInvitationBatchByRequest :one
SELECT ` + invitationBatchColumns + `
FROM   invitation_batches
WHERE  org_id = $1
  AND  request_id = $2`

// GetInvitationBatchByRequest finds the batch an earlier upload with the
// same request_id wrote; pgx.ErrNoRows when there is none.
func (q *Queries) GetInvitationBatchByRequest(ctx context.Context, orgID, requestID uuid.UUID) (InvitationBatchRow, error) {
	return scanInvitationBatch(q.db.QueryRow(ctx, getInvitationBatchByRequest, orgID, requestID))
}

const getInvitationBatch = `-- name: GetInvitationBatch :one
SELECT ` + invitationBatchColumns + `
FROM   invitation_batches
WHERE  id = $1
  AND  org_id = $2`

// GetInvitationBatch loads a batch of the organization; pgx.ErrNoRows for
// any other, including another organization's.
func (q *Queries) GetInvitationBatch(ctx context.Context, id, orgID uuid.UUID) (InvitationBatchRow, error) {
	return scanInvitationBatch(q.db.QueryRow(ctx, getInvitationBatch, id, orgID))
}

const setInvitationBatchFromReserve = `-- name: SetInvitationBatchFromReserve :exec
UPDATE invitation_batches SET from_reserve = $2 WHERE id = $1`

// SetInvitationBatchFromReserve records how many places came from the
// organizer's reserve, known once the places are locked.
func (q *Queries) SetInvitationBatchFromReserve(ctx context.Context, id uuid.UUID, fromReserve int32) error {
	_, err := q.db.Exec(ctx, setInvitationBatchFromReserve, id, fromReserve)
	return err
}

const setOrderInvitationBatch = `-- name: SetOrderInvitationBatch :exec
UPDATE orders
SET    issued_via          = $2,
       issued_by           = $3,
       invitation_batch_id = $4,
       updated_at          = now()
WHERE  id = $1`

// SetOrderInvitationBatch stamps a bulk invitation order: how it was
// issued, by whom and in which batch.
func (q *Queries) SetOrderInvitationBatch(ctx context.Context, orderID uuid.UUID, issuedVia string, issuedBy *uuid.UUID, batchID uuid.UUID) error {
	_, err := q.db.Exec(ctx, setOrderInvitationBatch, orderID, issuedVia, issuedBy, batchID)
	return err
}

// InvitationBatchOrder is one order of a batch.
type InvitationBatchOrder struct {
	ID         uuid.UUID
	SystemID   int64
	Status     string
	BuyerName  *string
	BuyerEmail *string
	Metadata   json.RawMessage
}

const listInvitationBatchOrders = `-- name: ListInvitationBatchOrders :many
SELECT o.id, o.system_id, o.status, o.buyer_name, o.buyer_email, o.metadata
FROM   orders o
WHERE  o.invitation_batch_id = $1
  AND  o.org_id = $2
ORDER  BY COALESCE((o.metadata->'invitation'->>'batch_seq')::int, 0), o.created_at, o.id`

// ListInvitationBatchOrders lists the orders of a batch in file order.
func (q *Queries) ListInvitationBatchOrders(ctx context.Context, batchID, orgID uuid.UUID) ([]InvitationBatchOrder, error) {
	rows, err := q.db.Query(ctx, listInvitationBatchOrders, batchID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationBatchOrder
	for rows.Next() {
		var o InvitationBatchOrder
		if err := rows.Scan(&o.ID, &o.SystemID, &o.Status, &o.BuyerName, &o.BuyerEmail, &o.Metadata); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

const holdSessionSeatsForInvitation = `-- name: HoldSessionSeatsForInvitation :many
UPDATE session_seats
SET    status         = 'held',
       reservation_id = $2,
       status_version = $3,
       updated_at     = now()
WHERE  id = ANY($1::uuid[])
  AND  status IN ('available', 'unavailable')
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind`

// HoldSessionSeatsForInvitation moves the named available or unavailable
// seats to held for the invitation's reservation in ONE statement and
// returns the seats it moved, in no particular order.
func (q *Queries) HoldSessionSeatsForInvitation(ctx context.Context, seatIDs []uuid.UUID, reservationID uuid.UUID, statusVersion int64) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, holdSessionSeatsForInvitation, seatIDs, reservationID, statusVersion)
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

const insertReservationSeats = `-- name: InsertReservationSeats :exec
INSERT INTO reservation_seats (reservation_id, session_seat_id)
SELECT $1, unnest($2::uuid[])`

// InsertReservationSeats links every seat to the reservation in ONE
// statement. A duplicate raises 23505 like InsertReservationSeat.
func (q *Queries) InsertReservationSeats(ctx context.Context, reservationID uuid.UUID, seatIDs []uuid.UUID) error {
	_, err := q.db.Exec(ctx, insertReservationSeats, reservationID, seatIDs)
	return err
}

const getInvitationSessionCapacity = `-- name: GetInvitationSessionCapacity :one
SELECT capacity_total, capacity_held + capacity_sold AS used
FROM   inventory_ledger
WHERE  session_id = $1
  AND  tier_id IS NULL`

// GetInvitationSessionCapacity answers the session ledger's capacity_total
// (nil: unlimited) and the places held or sold; pgx.ErrNoRows when the
// session has no ledger row, which ReserveCapacity also refuses.
func (q *Queries) GetInvitationSessionCapacity(ctx context.Context, sessionID uuid.UUID) (total *int32, used int32, err error) {
	err = q.db.QueryRow(ctx, getInvitationSessionCapacity, sessionID).Scan(&total, &used)
	return total, used, err
}
