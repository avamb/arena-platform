// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: (inline) spec 67 sections 4.4, 4.5, 4.8: a seat hold of the click
// becomes an order (checkout/start with hold_token), the cancel that keeps
// the hold, and the "payment in progress" mark.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// HoldCheckoutRow is the part of a reservations row the checkout of a hold
// reads (migration 0226 columns included). HasHoldToken is false for a hold
// of the old path or of the gateway (hold_token_hash IS NULL).
type HoldCheckoutRow struct {
	ID               uuid.UUID  `json:"id"`
	OrgID            uuid.UUID  `json:"org_id"`
	ChannelID        uuid.UUID  `json:"channel_id"`
	SessionID        uuid.UUID  `json:"session_id"`
	State            string     `json:"state"`
	ExpiresAt        time.Time  `json:"expires_at"`
	HasHoldToken     bool       `json:"has_hold_token"`
	PaymentStartedAt *time.Time `json:"payment_started_at"`
	ClosingStartedAt *time.Time `json:"closing_started_at"`
	PayingUntil      *time.Time `json:"paying_until"`
	HoldPolicy       []byte     `json:"hold_policy"`
}

const holdCheckoutColumns = `id, org_id, channel_id, session_id, state, expires_at,
       hold_token_hash IS NOT NULL, payment_started_at, closing_started_at,
       paying_until, hold_policy`

func scanHoldCheckoutRow(row interface{ Scan(dest ...any) error }) (HoldCheckoutRow, error) {
	var r HoldCheckoutRow
	err := row.Scan(&r.ID, &r.OrgID, &r.ChannelID, &r.SessionID, &r.State, &r.ExpiresAt,
		&r.HasHoldToken, &r.PaymentStartedAt, &r.ClosingStartedAt, &r.PayingUntil, &r.HoldPolicy)
	return r, err
}

const lockHoldCheckoutByTokenHash = `-- name: LockHoldCheckoutByTokenHash :one
SELECT ` + holdCheckoutColumns + `
FROM   reservations
WHERE  hold_token_hash = $1
FOR UPDATE`

// LockHoldCheckoutByTokenHash locks the hold named by the SHA-256 of its
// key. The reservations row is the FIRST lock of any change of an existing
// hold (AGENTS.md). MUST run inside a transaction; pgx.ErrNoRows when no
// hold has that key.
func (q *Queries) LockHoldCheckoutByTokenHash(ctx context.Context, tokenHash []byte) (HoldCheckoutRow, error) {
	return scanHoldCheckoutRow(q.db.QueryRow(ctx, lockHoldCheckoutByTokenHash, tokenHash))
}

const lockHoldCheckout = `-- name: LockHoldCheckout :one
SELECT ` + holdCheckoutColumns + `
FROM   reservations
WHERE  id = $1
FOR UPDATE`

// LockHoldCheckout locks one reservation by id and reads its hold columns.
// MUST run inside a transaction; pgx.ErrNoRows when the row does not exist.
func (q *Queries) LockHoldCheckout(ctx context.Context, id uuid.UUID) (HoldCheckoutRow, error) {
	return scanHoldCheckoutRow(q.db.QueryRow(ctx, lockHoldCheckout, id))
}

const findOpenCheckoutOfHold = `-- name: FindOpenCheckoutOfHold :one
SELECT cs.id, cs.checkout_token
FROM   checkout_sessions cs
WHERE  cs.reservation_id = $1
  AND  (cs.state IN ('created', 'pricing_confirmed', 'payment_started', 'manual_review')
        OR EXISTS (SELECT 1 FROM orders o
                   WHERE o.checkout_session_id = cs.id
                     AND o.status = 'pending_payment'))
ORDER  BY cs.created_at DESC, cs.id DESC
LIMIT  1`

// FindOpenCheckoutOfHold returns the newest checkout of the hold that is
// still open (not completed, abandoned or expired, or with an order still
// waiting for its payment); pgx.ErrNoRows when the hold has none, which is
// the select step of spec 67 section 3.1.
func (q *Queries) FindOpenCheckoutOfHold(ctx context.Context, reservationID uuid.UUID) (uuid.UUID, string, error) {
	var id uuid.UUID
	var token *string
	err := q.db.QueryRow(ctx, findOpenCheckoutOfHold, reservationID).Scan(&id, &token)
	if token == nil {
		return id, "", err
	}
	return id, *token, err
}

const startHoldPaymentStep = `-- name: StartHoldPaymentStep :one
UPDATE reservations
SET    payment_started_at = $2,
       expires_at         = $3,
       updated_at         = now()
WHERE  id = $1
  AND  state IN ('draft', 'active')
  AND  payment_started_at IS NULL
RETURNING ` + holdCheckoutColumns

// StartHoldPaymentStep gives the hold its payment step ONCE (spec 67
// section 3.3): payment_started_at and the new expires_at. pgx.ErrNoRows
// when the hold is closed or its payment step was already given.
func (q *Queries) StartHoldPaymentStep(ctx context.Context, id uuid.UUID, startedAt, expiresAt time.Time) (HoldCheckoutRow, error) {
	return scanHoldCheckoutRow(q.db.QueryRow(ctx, startHoldPaymentStep, id, startedAt, expiresAt))
}

const setHoldPayingUntil = `-- name: SetHoldPayingUntil :one
UPDATE reservations
SET    paying_until = $2,
       updated_at   = now()
WHERE  id = $1
  AND  state IN ('draft', 'active')
  AND  paying_until IS NULL
  AND  closing_started_at IS NULL
RETURNING ` + holdCheckoutColumns

// SetHoldPayingUntil writes the "payment in progress" mark ONCE (spec 67
// section 4.8). pgx.ErrNoRows when the hold is closed, closing, or already
// marked.
func (q *Queries) SetHoldPayingUntil(ctx context.Context, id uuid.UUID, payingUntil time.Time) (HoldCheckoutRow, error) {
	return scanHoldCheckoutRow(q.db.QueryRow(ctx, setHoldPayingUntil, id, payingUntil))
}

const extendPendingOrderExpiry = `-- name: ExtendPendingOrderExpiry :execrows
UPDATE orders
SET    expires_at = GREATEST(COALESCE(expires_at, $2), $2),
       updated_at = now()
WHERE  id = $1
  AND  status = 'pending_payment'`

// ExtendPendingOrderExpiry moves a waiting order's expires_at to at least
// until, never earlier (spec 67 section 4.8). Returns the rows changed.
func (q *Queries) ExtendPendingOrderExpiry(ctx context.Context, orderID uuid.UUID, until time.Time) (int64, error) {
	ct, err := q.db.Exec(ctx, extendPendingOrderExpiry, orderID, until)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

const setPaymentIntentPageExpiresAt = `-- name: SetPaymentIntentPageExpiresAt :execrows
UPDATE payment_intents
SET    page_expires_at = $2,
       updated_at      = now()
WHERE  id = $1`

// SetPaymentIntentPageExpiresAt records when the provider page of the
// payment attempt expires (migration 0226, spec 67 section 3.2).
func (q *Queries) SetPaymentIntentPageExpiresAt(ctx context.Context, id uuid.UUID, pageExpiresAt time.Time) (int64, error) {
	ct, err := q.db.Exec(ctx, setPaymentIntentPageExpiresAt, id, pageExpiresAt)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// CheckoutHoldWindowRow is the hold of one checkout with the page expiry of
// its newest payment attempt that has one.
type CheckoutHoldWindowRow struct {
	ReservationID    uuid.UUID  `json:"reservation_id"`
	ReservationState string     `json:"reservation_state"`
	HasHoldToken     bool       `json:"has_hold_token"`
	ExpiresAt        time.Time  `json:"expires_at"`
	PaymentStartedAt *time.Time `json:"payment_started_at"`
	ClosingStartedAt *time.Time `json:"closing_started_at"`
	PayingUntil      *time.Time `json:"paying_until"`
	HoldPolicy       []byte     `json:"hold_policy"`
	PageExpiresAt    *time.Time `json:"page_expires_at"`
}

const getCheckoutHoldWindow = `-- name: GetCheckoutHoldWindow :one
SELECT r.id, r.state, r.hold_token_hash IS NOT NULL, r.expires_at,
       r.payment_started_at, r.closing_started_at, r.paying_until, r.hold_policy,
       (SELECT pi.page_expires_at FROM payment_intents pi
         WHERE pi.checkout_session_id = cs.id AND pi.page_expires_at IS NOT NULL
         ORDER BY pi.created_at DESC, pi.id DESC LIMIT 1)
FROM   checkout_sessions cs
JOIN   reservations r ON r.id = cs.reservation_id
WHERE  cs.id = $1`

// GetCheckoutHoldWindow reads the hold behind a checkout and the page expiry
// of its newest payment attempt that has one (nil on the old path, spec 67
// section 3.2). pgx.ErrNoRows when the checkout does not exist.
func (q *Queries) GetCheckoutHoldWindow(ctx context.Context, checkoutSessionID uuid.UUID) (CheckoutHoldWindowRow, error) {
	var r CheckoutHoldWindowRow
	err := q.db.QueryRow(ctx, getCheckoutHoldWindow, checkoutSessionID).Scan(
		&r.ReservationID, &r.ReservationState, &r.HasHoldToken, &r.ExpiresAt,
		&r.PaymentStartedAt, &r.ClosingStartedAt, &r.PayingUntil, &r.HoldPolicy, &r.PageExpiresAt)
	return r, err
}
