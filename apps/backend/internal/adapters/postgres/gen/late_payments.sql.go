// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: late_payments.sql

package gen

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// ReopenExpiredOrderForLatePayment
// ─────────────────────────────────────────────────────────────────────────────

const reopenExpiredOrderForLatePayment = `-- name: ReopenExpiredOrderForLatePayment :execrows
UPDATE orders
   SET status = 'pending_payment', cancelled_at = NULL, updated_at = now()
 WHERE id = $1
   AND status = 'expired'`

// ReopenExpiredOrderForLatePayment moves an EXPIRED order back to
// pending_payment so a payment that was made in time but notified late
// (spec 62 П-15) can pay it on the same transaction that re-acquires its
// places. Only the webhook's late-payment path calls it, and only together
// with the re-acquisition; false means the order is no longer expired.
func (q *Queries) ReopenExpiredOrderForLatePayment(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, reopenExpiredOrderForLatePayment, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// LatestLatePaymentOutcome
// ─────────────────────────────────────────────────────────────────────────────

const latestLatePaymentOutcome = `-- name: LatestLatePaymentOutcome :one
SELECT outcome FROM late_payments
 WHERE checkout_session_id = $1
 ORDER BY created_at DESC
 LIMIT 1`

// LatestLatePaymentOutcome is the outcome of the newest late payment of a
// checkout (migration 0205); "" and no error when there is none.
func (q *Queries) LatestLatePaymentOutcome(ctx context.Context, checkoutSessionID uuid.UUID) (string, error) {
	var outcome string
	err := q.db.QueryRow(ctx, latestLatePaymentOutcome, checkoutSessionID).Scan(&outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return outcome, err
}

// ─────────────────────────────────────────────────────────────────────────────
// LatePaymentLetter
// ─────────────────────────────────────────────────────────────────────────────

// LatePaymentLetterRow is what the buyer's late-payment letter needs.
type LatePaymentLetterRow struct {
	PaymentIntentID uuid.UUID
	OrgID           uuid.UUID
	OrderID         *uuid.UUID
	LateCase        string
	Outcome         string
	Amount          int64
	Currency        string
	BuyerNotifiedAt *time.Time
	BuyerEmail      *string
	BuyerName       *string
	OrderNumber     *int64
	SessionID       *uuid.UUID
	BuyerLocale     *string
	OrgLocale       string
}

const getLatePaymentLetter = `-- name: GetLatePaymentLetter :one
SELECT lp.payment_intent_id, lp.org_id, lp.order_id, lp.late_case, lp.outcome,
       lp.amount, lp.currency, lp.buyer_notified_at,
       o.buyer_email, o.buyer_name, o.system_id, o.session_id,
       cs.buyer_locale, COALESCE(org.default_locale, '')
  FROM late_payments lp
  JOIN checkout_sessions cs ON cs.id = lp.checkout_session_id
  JOIN organizations org    ON org.id = lp.org_id
  LEFT JOIN orders o        ON o.id = lp.order_id
 WHERE lp.payment_intent_id = $1`

// GetLatePaymentLetter loads one late payment for its letter.
func (q *Queries) GetLatePaymentLetter(ctx context.Context, paymentIntentID uuid.UUID) (LatePaymentLetterRow, error) {
	var r LatePaymentLetterRow
	err := q.db.QueryRow(ctx, getLatePaymentLetter, paymentIntentID).Scan(
		&r.PaymentIntentID, &r.OrgID, &r.OrderID, &r.LateCase, &r.Outcome,
		&r.Amount, &r.Currency, &r.BuyerNotifiedAt,
		&r.BuyerEmail, &r.BuyerName, &r.OrderNumber, &r.SessionID,
		&r.BuyerLocale, &r.OrgLocale,
	)
	return r, err
}

const markLatePaymentNotified = `-- name: MarkLatePaymentNotified :execrows
UPDATE late_payments SET buyer_notified_at = now()
 WHERE payment_intent_id = $1 AND buyer_notified_at IS NULL`

// MarkLatePaymentNotified claims the letter of a late payment, once.
func (q *Queries) MarkLatePaymentNotified(ctx context.Context, paymentIntentID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, markLatePaymentNotified, paymentIntentID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const unmarkLatePaymentNotified = `-- name: UnmarkLatePaymentNotified :exec
UPDATE late_payments SET buyer_notified_at = NULL WHERE payment_intent_id = $1`

// UnmarkLatePaymentNotified gives the claim back after a refused send, so
// the job's retry sends the letter.
func (q *Queries) UnmarkLatePaymentNotified(ctx context.Context, paymentIntentID uuid.UUID) error {
	_, err := q.db.Exec(ctx, unmarkLatePaymentNotified, paymentIntentID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Reconciliation of unfinished hosted payments (П-15 §5.4 п. 5)
// ─────────────────────────────────────────────────────────────────────────────

// UnsettledHostedPaymentRow is one hosted payment with a page that has not
// been confirmed either way.
type UnsettledHostedPaymentRow struct {
	PaymentIntentID   uuid.UUID
	OrgID             uuid.UUID
	Provider          string
	ProviderPaymentID string
	CheckoutSessionID uuid.UUID
}

const listUnsettledHostedPayments = `-- name: ListUnsettledHostedPayments :many
SELECT pi.id, pi.org_id, pi.provider, pi.provider_payment_id, cs.id
  FROM payment_intents pi
  JOIN checkout_sessions cs ON cs.id = pi.checkout_session_id
  JOIN reservations r      ON r.id = cs.reservation_id
 WHERE pi.provider = $1
   AND pi.state IN ('created', 'requires_action', 'processing', 'authorized')
   AND pi.hosted_checkout_url IS NOT NULL
   AND pi.provider_payment_id IS NOT NULL
   AND pi.created_at < $2
   AND (pi.provider_checked_at IS NULL OR pi.provider_checked_at < $3)
   AND cs.state IN ('pricing_confirmed', 'payment_started', 'expired', 'abandoned')
   AND r.expires_at > $4
 ORDER BY pi.provider_checked_at NULLS FIRST, pi.created_at
 LIMIT $5`

// ListUnsettledHostedPayments lists hosted payments of one provider whose
// page was handed out and that no notification settled: older than
// createdBefore, not asked about since checkedBefore, whose hold ended after
// holdEndedAfter (the job looks until the end of the window plus a margin).
// An expired or abandoned checkout stays on the list: a payment that still
// lands there is a late payment the webhook state machine settles.
func (q *Queries) ListUnsettledHostedPayments(ctx context.Context, provider string, createdBefore, checkedBefore, holdEndedAfter time.Time, limit int32) ([]UnsettledHostedPaymentRow, error) {
	rows, err := q.db.Query(ctx, listUnsettledHostedPayments, provider, createdBefore, checkedBefore, holdEndedAfter, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnsettledHostedPaymentRow
	for rows.Next() {
		var r UnsettledHostedPaymentRow
		if err := rows.Scan(&r.PaymentIntentID, &r.OrgID, &r.Provider, &r.ProviderPaymentID, &r.CheckoutSessionID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const markPaymentIntentProviderChecked = `-- name: MarkPaymentIntentProviderChecked :exec
UPDATE payment_intents SET provider_checked_at = now() WHERE id = $1`

// MarkPaymentIntentProviderChecked stamps the reconciliation's last question.
func (q *Queries) MarkPaymentIntentProviderChecked(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, markPaymentIntentProviderChecked, id)
	return err
}
