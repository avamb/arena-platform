// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: holds_public.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PublicHoldRow is a reservations row read as a public seat hold (spec 67,
// migration 0226): the columns of the hold at the click on top of the plain
// reservation state.
type PublicHoldRow struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	ChannelID         uuid.UUID  `json:"channel_id"`
	SessionID         uuid.UUID  `json:"session_id"`
	Quantity          int32      `json:"quantity"`
	State             string     `json:"state"`
	ExpiresAt         time.Time  `json:"expires_at"`
	CreatedAt         time.Time  `json:"created_at"`
	CancelledAt       *time.Time `json:"cancelled_at"`
	ExpiredAt         *time.Time `json:"expired_at"`
	HoldTokenHash     []byte     `json:"hold_token_hash"`
	VisitorDeviceHash []byte     `json:"visitor_device_hash"`
	VisitorIPHash     []byte     `json:"visitor_ip_hash"`
	SelectDeadline    *time.Time `json:"select_deadline"`
	PaymentStartedAt  *time.Time `json:"payment_started_at"`
	ExtensionsUsed    int16      `json:"extensions_used"`
	PayingUntil       *time.Time `json:"paying_until"`
	ReholdOf          *uuid.UUID `json:"rehold_of"`
	ClosingStartedAt  *time.Time `json:"closing_started_at"`
	HoldPolicy        []byte     `json:"hold_policy"`
}

const publicHoldColumns = `id, org_id, channel_id, session_id, quantity, state, expires_at, created_at,
       cancelled_at, expired_at, hold_token_hash, visitor_device_hash, visitor_ip_hash,
       select_deadline, payment_started_at, extensions_used, paying_until, rehold_of,
       closing_started_at, hold_policy`

func scanPublicHoldRow(row interface{ Scan(dest ...any) error }) (PublicHoldRow, error) {
	var r PublicHoldRow
	err := row.Scan(&r.ID, &r.OrgID, &r.ChannelID, &r.SessionID, &r.Quantity, &r.State,
		&r.ExpiresAt, &r.CreatedAt, &r.CancelledAt, &r.ExpiredAt, &r.HoldTokenHash,
		&r.VisitorDeviceHash, &r.VisitorIPHash, &r.SelectDeadline, &r.PaymentStartedAt,
		&r.ExtensionsUsed, &r.PayingUntil, &r.ReholdOf, &r.ClosingStartedAt, &r.HoldPolicy)
	return r, err
}

const lockPublicHoldByTokenHash = `-- name: LockPublicHoldByTokenHash :one
SELECT ` + publicHoldColumns + `
FROM   reservations
WHERE  hold_token_hash = $1
FOR UPDATE`

// LockPublicHoldByTokenHash row-locks the hold with this token hash;
// pgx.ErrNoRows when no hold has it.
func (q *Queries) LockPublicHoldByTokenHash(ctx context.Context, tokenHash []byte) (PublicHoldRow, error) {
	return scanPublicHoldRow(q.db.QueryRow(ctx, lockPublicHoldByTokenHash, tokenHash))
}

const getPublicHoldByTokenHash = `-- name: GetPublicHoldByTokenHash :one
SELECT ` + publicHoldColumns + `
FROM   reservations
WHERE  hold_token_hash = $1`

// GetPublicHoldByTokenHash reads the hold with this token hash without a
// lock; pgx.ErrNoRows when no hold has it.
func (q *Queries) GetPublicHoldByTokenHash(ctx context.Context, tokenHash []byte) (PublicHoldRow, error) {
	return scanPublicHoldRow(q.db.QueryRow(ctx, getPublicHoldByTokenHash, tokenHash))
}

const lockOpenDeviceHoldOnSession = `-- name: LockOpenDeviceHoldOnSession :one
SELECT ` + publicHoldColumns + `
FROM   reservations
WHERE  session_id = $1
  AND  visitor_device_hash = $2
  AND  state IN ('draft', 'active')
  AND  hold_token_hash IS NOT NULL
  AND  expires_at > now()
  AND  closing_started_at IS NULL
ORDER  BY created_at DESC
LIMIT  1
FOR UPDATE`

// LockOpenDeviceHoldOnSession row-locks the device's newest open public hold
// on the session; pgx.ErrNoRows when it has none.
func (q *Queries) LockOpenDeviceHoldOnSession(ctx context.Context, sessionID uuid.UUID, deviceHash []byte) (PublicHoldRow, error) {
	return scanPublicHoldRow(q.db.QueryRow(ctx, lockOpenDeviceHoldOnSession, sessionID, deviceHash))
}

const countOpenPublicHoldsByDevice = `-- name: CountOpenPublicHoldsByDevice :one
SELECT count(*)::bigint
FROM   reservations
WHERE  visitor_device_hash = $1
  AND  state IN ('draft', 'active')
  AND  hold_token_hash IS NOT NULL
  AND  expires_at > now()`

// CountOpenPublicHoldsByDevice counts the device's open public holds on
// every session.
func (q *Queries) CountOpenPublicHoldsByDevice(ctx context.Context, deviceHash []byte) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countOpenPublicHoldsByDevice, deviceHash).Scan(&n)
	return n, err
}

const countOpenPublicHoldsByIP = `-- name: CountOpenPublicHoldsByIP :one
SELECT count(*)::bigint
FROM   reservations
WHERE  visitor_ip_hash = $1
  AND  state IN ('draft', 'active')
  AND  hold_token_hash IS NOT NULL
  AND  expires_at > now()`

// CountOpenPublicHoldsByIP counts the address's open public holds on every
// session.
func (q *Queries) CountOpenPublicHoldsByIP(ctx context.Context, ipHash []byte) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countOpenPublicHoldsByIP, ipHash).Scan(&n)
	return n, err
}

const holdAdvisoryXactLock = `-- name: HoldAdvisoryXactLock :exec
SELECT pg_advisory_xact_lock($1::bigint)`

// HoldAdvisoryXactLock takes a transaction-scoped advisory lock. Only
// meaningful inside a transaction.
func (q *Queries) HoldAdvisoryXactLock(ctx context.Context, key int64) error {
	_, err := q.db.Exec(ctx, holdAdvisoryXactLock, key)
	return err
}

const publicHoldHasOpenOrder = `-- name: PublicHoldHasOpenOrder :one
SELECT EXISTS (
    SELECT 1 FROM orders
    WHERE  reservation_id = $1
      AND  status = 'pending_payment'
)`

// PublicHoldHasOpenOrder reports whether the hold has an order waiting for
// payment: the payment step of spec 67 section 3.1.
func (q *Queries) PublicHoldHasOpenOrder(ctx context.Context, reservationID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, publicHoldHasOpenOrder, reservationID).Scan(&ok)
	return ok, err
}

const getPublicHoldCheckoutToken = `-- name: GetPublicHoldCheckoutToken :one
SELECT cs.checkout_token
FROM   orders o
JOIN   checkout_sessions cs ON cs.id = o.checkout_session_id
WHERE  o.reservation_id = $1
  AND  o.status = 'pending_payment'
ORDER  BY o.created_at DESC
LIMIT  1`

// GetPublicHoldCheckoutToken returns the checkout token of the hold's order
// waiting for payment; pgx.ErrNoRows when it has none.
func (q *Queries) GetPublicHoldCheckoutToken(ctx context.Context, reservationID uuid.UUID) (*string, error) {
	var tok *string
	err := q.db.QueryRow(ctx, getPublicHoldCheckoutToken, reservationID).Scan(&tok)
	return tok, err
}

// PausedSeatKeyRow is one seat of an expired public hold and when the last
// such hold expired.
type PausedSeatKeyRow struct {
	SeatKey   string    `json:"seat_key"`
	ExpiredAt time.Time `json:"expired_at"`
}

const listPausedSeatKeysByDevice = `-- name: ListPausedSeatKeysByDevice :many
SELECT k.seat_key, max(r.expired_at)::timestamptz AS expired_at
FROM   reservations r,
       jsonb_array_elements_text(COALESCE(r.hold_policy -> 'seat_keys', '[]'::jsonb)) AS k(seat_key)
WHERE  r.state = 'expired'
  AND  r.visitor_device_hash = $1
  AND  r.session_id = $2
  AND  r.expired_at > $3
GROUP  BY k.seat_key`

// ListPausedSeatKeysByDevice lists the seats of the device's public holds on
// the session that expired after since.
func (q *Queries) ListPausedSeatKeysByDevice(ctx context.Context, deviceHash []byte, sessionID uuid.UUID, since time.Time) ([]PausedSeatKeyRow, error) {
	return q.listPausedSeatKeys(ctx, listPausedSeatKeysByDevice, deviceHash, sessionID, since)
}

const listPausedSeatKeysByIP = `-- name: ListPausedSeatKeysByIP :many
SELECT k.seat_key, max(r.expired_at)::timestamptz AS expired_at
FROM   reservations r,
       jsonb_array_elements_text(COALESCE(r.hold_policy -> 'seat_keys', '[]'::jsonb)) AS k(seat_key)
WHERE  r.state = 'expired'
  AND  r.visitor_ip_hash = $1
  AND  r.session_id = $2
  AND  r.expired_at > $3
GROUP  BY k.seat_key`

// ListPausedSeatKeysByIP lists the seats of the address's public holds on
// the session that expired after since.
func (q *Queries) ListPausedSeatKeysByIP(ctx context.Context, ipHash []byte, sessionID uuid.UUID, since time.Time) ([]PausedSeatKeyRow, error) {
	return q.listPausedSeatKeys(ctx, listPausedSeatKeysByIP, ipHash, sessionID, since)
}

func (q *Queries) listPausedSeatKeys(ctx context.Context, sql string, hash []byte, sessionID uuid.UUID, since time.Time) ([]PausedSeatKeyRow, error) {
	rows, err := q.db.Query(ctx, sql, hash, sessionID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PausedSeatKeyRow
	for rows.Next() {
		var r PausedSeatKeyRow
		if err := rows.Scan(&r.SeatKey, &r.ExpiredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getInheritableSelectDeadline = `-- name: GetInheritableSelectDeadline :one
SELECT select_deadline, extensions_used
FROM   reservations
WHERE  session_id = $1
  AND  visitor_device_hash = $2
  AND  hold_token_hash IS NOT NULL
  AND  state IN ('cancelled', 'expired')
  AND  COALESCE(cancelled_at, expired_at) > $3
  AND  select_deadline > now()
ORDER  BY COALESCE(cancelled_at, expired_at) DESC
LIMIT  1`

// GetInheritableSelectDeadline returns the select deadline and the used
// extensions of the device's latest public hold on the session closed after
// since whose deadline is still ahead; pgx.ErrNoRows when there is none.
func (q *Queries) GetInheritableSelectDeadline(ctx context.Context, sessionID uuid.UUID, deviceHash []byte, since time.Time) (time.Time, int16, error) {
	var deadline time.Time
	var used int16
	err := q.db.QueryRow(ctx, getInheritableSelectDeadline, sessionID, deviceHash, since).Scan(&deadline, &used)
	return deadline, used, err
}

const countReholdsOf = `-- name: CountReholdsOf :one
SELECT count(*)::bigint FROM reservations WHERE rehold_of = $1`

// CountReholdsOf counts the holds made by "hold again" of this hold.
func (q *Queries) CountReholdsOf(ctx context.Context, reservationID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countReholdsOf, reservationID).Scan(&n)
	return n, err
}

const setPublicHoldFields = `-- name: SetPublicHoldFields :exec
UPDATE reservations
SET    hold_token_hash     = $2,
       visitor_device_hash = $3,
       visitor_ip_hash     = $4,
       select_deadline     = $5,
       extensions_used     = $6,
       rehold_of           = $7,
       hold_policy         = $8::jsonb,
       updated_at          = now()
WHERE  id = $1`

// SetPublicHoldFieldsParams are the public-hold columns written once, right
// after the reservation is inserted.
type SetPublicHoldFieldsParams struct {
	ID                uuid.UUID
	HoldTokenHash     []byte
	VisitorDeviceHash []byte
	VisitorIPHash     []byte
	SelectDeadline    time.Time
	ExtensionsUsed    int16
	ReholdOf          *uuid.UUID
	HoldPolicy        []byte
}

// SetPublicHoldFields turns a fresh reservation into a public hold.
func (q *Queries) SetPublicHoldFields(ctx context.Context, arg SetPublicHoldFieldsParams) error {
	_, err := q.db.Exec(ctx, setPublicHoldFields, arg.ID, arg.HoldTokenHash, arg.VisitorDeviceHash,
		arg.VisitorIPHash, arg.SelectDeadline, arg.ExtensionsUsed, arg.ReholdOf, string(arg.HoldPolicy))
	return err
}

const setPublicHoldSeatKeys = `-- name: SetPublicHoldSeatKeys :exec
UPDATE reservations
SET    hold_policy = jsonb_set(COALESCE(hold_policy, '{}'::jsonb), '{seat_keys}', to_jsonb($2::text[])),
       updated_at  = now()
WHERE  id = $1`

// SetPublicHoldSeatKeys records the seats the hold holds now in
// hold_policy->'seat_keys'.
func (q *Queries) SetPublicHoldSeatKeys(ctx context.Context, reservationID uuid.UUID, seatKeys []string) error {
	if seatKeys == nil {
		seatKeys = []string{}
	}
	_, err := q.db.Exec(ctx, setPublicHoldSeatKeys, reservationID, seatKeys)
	return err
}

const extendPublicHold = `-- name: ExtendPublicHold :one
UPDATE reservations
SET    expires_at      = $2,
       extensions_used = extensions_used + 1,
       select_deadline = CASE WHEN $3::boolean THEN $2 ELSE select_deadline END,
       updated_at      = now()
WHERE  id = $1
  AND  state IN ('draft', 'active')
RETURNING ` + publicHoldColumns

// ExtendPublicHold moves the hold's expiry to expiresAt and counts one
// extension; withSelect also moves the select deadline (select step).
// pgx.ErrNoRows when the hold is no longer open.
func (q *Queries) ExtendPublicHold(ctx context.Context, reservationID uuid.UUID, expiresAt time.Time, withSelect bool) (PublicHoldRow, error) {
	return scanPublicHoldRow(q.db.QueryRow(ctx, extendPublicHold, reservationID, expiresAt, withSelect))
}

const setPendingOrderExpiryForReservation = `-- name: SetPendingOrderExpiryForReservation :execrows
UPDATE orders
SET    expires_at = GREATEST(expires_at, $2),
       updated_at = now()
WHERE  reservation_id = $1
  AND  status = 'pending_payment'`

// SetPendingOrderExpiryForReservation gives the hold's order waiting for
// payment the hold's new expiry, never an earlier one than it has; returns
// the rows changed.
func (q *Queries) SetPendingOrderExpiryForReservation(ctx context.Context, reservationID uuid.UUID, expiresAt time.Time) (int64, error) {
	ct, err := q.db.Exec(ctx, setPendingOrderExpiryForReservation, reservationID, expiresAt)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
