// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: hold_close.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// GetExpiredReservationsHoldAware
// ─────────────────────────────────────────────────────────────────────────────

const getExpiredReservationsHoldAware = `-- name: GetExpiredReservationsHoldAware :many
SELECT r.id, r.org_id, r.channel_id, r.session_id, r.tier_id, r.user_id, r.quantity, r.state,
       r.expires_at, r.created_at, r.updated_at, r.cancelled_at, r.converted_at, r.expired_at
FROM   reservations r
WHERE  r.state IN ('draft', 'active')
  AND  r.expires_at < now()
  AND  NOT EXISTS (
           SELECT 1
           FROM   checkout_sessions cs
           JOIN   payment_intents pi ON pi.checkout_session_id = cs.id
           WHERE  cs.reservation_id = r.id
             AND  pi.state IN ('created', 'requires_action')
             AND  (
                      (pi.page_expires_at IS NOT NULL
                       AND pi.page_expires_at + make_interval(secs => $2::float8) > now())
                   OR (pi.page_expires_at IS NULL
                       AND r.hold_token_hash IS NOT NULL
                       AND pi.provider = ANY($3::text[])
                       AND pi.provider_payment_id IS NOT NULL
                       AND r.expires_at
                           + make_interval(secs => GREATEST(
                                 $4::float8,
                                 COALESCE((r.hold_policy ->> 'payment_seconds')::float8, 0)
                                 + COALESCE((r.hold_policy ->> 'extend_seconds')::float8, 0)
                                   * COALESCE((r.hold_policy ->> 'extend_count')::float8, 0)
                                 + COALESCE((r.hold_policy ->> 'paying_seconds')::float8, 0)))
                           + make_interval(secs => $2::float8) > now())
                  )
       )
  AND  NOT (
           r.hold_token_hash IS NOT NULL
       AND EXISTS (
               SELECT 1
               FROM   checkout_sessions cs
               WHERE  cs.reservation_id = r.id
                 AND  (cs.state = 'manual_review'
                       OR EXISTS (SELECT 1 FROM payment_intents pi
                                  WHERE  pi.checkout_session_id = cs.id
                                    AND  pi.state = 'succeeded'))
           )
       )
ORDER BY r.expires_at ASC
LIMIT  $1
FOR UPDATE OF r SKIP LOCKED`

// GetExpiredReservationsHoldAware is GetExpiredReservations for
// reservation.expire_sweep since spec 67 section 5.2: a reservation with an
// open payment attempt whose provider page lives until page_expires_at is
// skipped until page_expires_at + grace (the hard limit); hold.close_payment
// closes the page first.
//
// A hold of the click (hold_token_hash) with an open attempt of a provider
// in pageClosingProviders that has NO page_expires_at (a page whose expiry
// was never recorded) is skipped as well, up to expires_at + the longest
// page the hold's policy can make (max(minPageLifetime, payment + extend x
// extend_count + paying)) + grace, so it can never stick forever. A hold of
// the click with a checkout in manual_review or a succeeded payment attempt
// is never a candidate: its seats belong to a payment (same guard as
// holdclose's Release). Rows of the old path behave as before. Must be
// called inside a transaction (FOR UPDATE SKIP LOCKED).
func (q *Queries) GetExpiredReservationsHoldAware(ctx context.Context, limit int32, grace time.Duration, pageClosingProviders []string, minPageLifetime time.Duration) ([]ReservationRow, error) {
	if pageClosingProviders == nil {
		pageClosingProviders = []string{}
	}
	rows, err := q.db.Query(ctx, getExpiredReservationsHoldAware, limit, grace.Seconds(), pageClosingProviders, minPageLifetime.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reservations []ReservationRow
	for rows.Next() {
		r, err := scanReservationRow(rows)
		if err != nil {
			return nil, err
		}
		reservations = append(reservations, r)
	}
	return reservations, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListHoldCloseCandidates
// ─────────────────────────────────────────────────────────────────────────────

const listHoldCloseCandidates = `-- name: ListHoldCloseCandidates :many
SELECT r.id, r.expires_at
FROM   reservations r
WHERE  r.state IN ('draft', 'active')
  AND  r.hold_token_hash IS NOT NULL
  AND  r.expires_at < now()
  AND  (r.paying_until IS NULL OR r.paying_until <= now())
  AND  (r.closing_started_at IS NULL OR r.closing_started_at < $2)
  AND  EXISTS (
           SELECT 1
           FROM   checkout_sessions cs
           JOIN   payment_intents pi ON pi.checkout_session_id = cs.id
           WHERE  cs.reservation_id = r.id
             AND  pi.state IN ('created', 'requires_action')
             AND  pi.provider = ANY($1::text[])
             AND  pi.provider_payment_id IS NOT NULL
       )
ORDER BY r.expires_at ASC
LIMIT  $3`

// HoldCloseCandidate is one hold whose provider payment page
// hold.close_payment should close.
type HoldCloseCandidate struct {
	ReservationID uuid.UUID
	ExpiresAt     time.Time
}

// ListHoldCloseCandidates lists the holds of the click path past expires_at
// with an open attempt of one of providers, oldest expires_at first. A
// closing mark older than staleBefore no longer hides a hold.
func (q *Queries) ListHoldCloseCandidates(ctx context.Context, providers []string, staleBefore time.Time, limit int32) ([]HoldCloseCandidate, error) {
	rows, err := q.db.Query(ctx, listHoldCloseCandidates, providers, staleBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HoldCloseCandidate
	for rows.Next() {
		var c HoldCloseCandidate
		if err := rows.Scan(&c.ReservationID, &c.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ClaimHoldForClose / ClearHoldCloseMark
// ─────────────────────────────────────────────────────────────────────────────

const claimHoldForClose = `-- name: ClaimHoldForClose :one
WITH c AS (
    SELECT id
    FROM   reservations
    WHERE  id = $1
      AND  state IN ('draft', 'active')
      AND  expires_at < now()
      AND  (paying_until IS NULL OR paying_until <= now())
      AND  (closing_started_at IS NULL OR closing_started_at < $2)
    FOR UPDATE SKIP LOCKED
)
UPDATE reservations r
SET    closing_started_at = now()
FROM   c
WHERE  r.id = c.id
RETURNING r.closing_started_at, r.expires_at`

// ClaimHoldForClose locks the reservation row (SKIP LOCKED), re-checks it
// and writes closing_started_at, in one statement. pgx.ErrNoRows: the hold
// is locked by someone else, no longer open, extended, paying, or claimed.
func (q *Queries) ClaimHoldForClose(ctx context.Context, reservationID uuid.UUID, staleBefore time.Time) (mark, expiresAt time.Time, err error) {
	err = q.db.QueryRow(ctx, claimHoldForClose, reservationID, staleBefore).Scan(&mark, &expiresAt)
	return mark, expiresAt, err
}

const clearHoldCloseMark = `-- name: ClearHoldCloseMark :exec
UPDATE reservations
SET    closing_started_at = NULL
WHERE  id = $1
  AND  closing_started_at = $2`

// ClearHoldCloseMark removes the closing mark this claim wrote.
func (q *Queries) ClearHoldCloseMark(ctx context.Context, reservationID uuid.UUID, mark time.Time) error {
	_, err := q.db.Exec(ctx, clearHoldCloseMark, reservationID, mark)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListOpenPageIntents
// ─────────────────────────────────────────────────────────────────────────────

const listOpenPageIntents = `-- name: ListOpenPageIntents :many
SELECT pi.id, pi.provider, pi.provider_payment_id, cs.id, cs.org_id, cs.channel_id
FROM   checkout_sessions cs
JOIN   payment_intents pi ON pi.checkout_session_id = cs.id
WHERE  cs.reservation_id = $1
  AND  pi.state IN ('created', 'requires_action')
  AND  pi.provider = ANY($2::text[])
  AND  pi.provider_payment_id IS NOT NULL
ORDER BY pi.created_at ASC`

// OpenPageIntent is one open payment attempt of a hold with a provider page.
type OpenPageIntent struct {
	PaymentIntentID   uuid.UUID
	Provider          string
	ProviderSessionID string
	CheckoutSessionID uuid.UUID
	OrgID             uuid.UUID
	ChannelID         uuid.UUID
}

// ListOpenPageIntents lists the open attempts of reservationID whose provider
// is one of providers, oldest first.
func (q *Queries) ListOpenPageIntents(ctx context.Context, reservationID uuid.UUID, providers []string) ([]OpenPageIntent, error) {
	rows, err := q.db.Query(ctx, listOpenPageIntents, reservationID, providers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenPageIntent
	for rows.Next() {
		var p OpenPageIntent
		if err := rows.Scan(&p.PaymentIntentID, &p.Provider, &p.ProviderSessionID, &p.CheckoutSessionID, &p.OrgID, &p.ChannelID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// GetLatePaymentWindowBounds
// ─────────────────────────────────────────────────────────────────────────────

const getLatePaymentWindowBounds = `-- name: GetLatePaymentWindowBounds :one
SELECT (SELECT paying_until    FROM reservations    WHERE id = $2),
       (SELECT page_expires_at FROM payment_intents WHERE id = $1)`

// GetLatePaymentWindowBounds returns the reservation's paying_until and the
// payment intent's page_expires_at (nil when unset or the row is missing).
func (q *Queries) GetLatePaymentWindowBounds(ctx context.Context, paymentIntentID, reservationID uuid.UUID) (payingUntil, pageExpiresAt *time.Time, err error) {
	err = q.db.QueryRow(ctx, getLatePaymentWindowBounds, paymentIntentID, reservationID).Scan(&payingUntil, &pageExpiresAt)
	return payingUntil, pageExpiresAt, err
}

// ─────────────────────────────────────────────────────────────────────────────
// PurgeVisitorHashes
// ─────────────────────────────────────────────────────────────────────────────

const purgeVisitorHashes = `-- name: PurgeVisitorHashes :execrows
UPDATE reservations
SET    visitor_device_hash = NULL,
       visitor_ip_hash     = NULL
WHERE  id IN (
           SELECT id
           FROM   reservations
           WHERE  (visitor_device_hash IS NOT NULL OR visitor_ip_hash IS NOT NULL)
             AND  state IN ('expired', 'cancelled', 'converted')
             AND  COALESCE(expired_at, cancelled_at, converted_at, updated_at) < $1
           LIMIT  $2
       )`

// PurgeVisitorHashes erases the visitor hashes of at most limit holds that
// closed before closedBefore, and reports how many rows it changed.
func (q *Queries) PurgeVisitorHashes(ctx context.Context, closedBefore time.Time, limit int32) (int64, error) {
	tag, err := q.db.Exec(ctx, purgeVisitorHashes, closedBefore, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
