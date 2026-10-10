// store.go — the engine's own SQL. It reads and writes the refunds columns
// migration 0138 added, which the shared gen.RefundRow scanner does not
// carry (widening that scanner would touch every query feeding it).
package refunds

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Refund is one refunds row as the engine sees it.
type Refund struct {
	ID                  uuid.UUID
	OrgID               uuid.UUID
	PaymentIntentID     uuid.UUID
	OrderID             *uuid.UUID
	TicketID            *uuid.UUID
	BatchID             *uuid.UUID
	Amount              int64
	Currency            string
	Reason              *string
	State               string
	Settlement          string
	Provider            *string
	ProviderRefundID    *string
	ProviderStatus      *string
	FailureCode         *string
	FailureReason       *string
	CancelTicket        bool
	ProviderAttempts    int32
	ProviderAttemptedAt *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Accepted reports whether the provider took the refund (the money is
// returned or on its way): the condition for cancelling the ticket.
func (r Refund) Accepted() bool {
	return r.State == StateSucceeded || (r.State == StateProviderPending && r.ProviderRefundID != nil)
}

const refundColumns = `id, org_id, payment_intent_id, order_id, ticket_id, batch_id, amount, currency, reason,
       state, settlement, provider, provider_refund_id, provider_status, failure_code, failure_reason,
       cancel_ticket, provider_attempts, provider_attempted_at, created_at, updated_at`

func scanRefund(row pgx.Row) (Refund, error) {
	var r Refund
	var pi *uuid.UUID
	err := row.Scan(&r.ID, &r.OrgID, &pi, &r.OrderID, &r.TicketID, &r.BatchID, &r.Amount, &r.Currency, &r.Reason,
		&r.State, &r.Settlement, &r.Provider, &r.ProviderRefundID, &r.ProviderStatus, &r.FailureCode, &r.FailureReason,
		&r.CancelTicket, &r.ProviderAttempts, &r.ProviderAttemptedAt, &r.CreatedAt, &r.UpdatedAt)
	if pi != nil {
		r.PaymentIntentID = *pi
	}
	return r, err
}

func scanRefunds(rows pgx.Rows) ([]Refund, error) {
	defer rows.Close()
	var out []Refund
	for rows.Next() {
		r, err := scanRefund(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Payment is the payment_intents row a refund goes back through.
type Payment struct {
	ID                uuid.UUID
	OrgID             uuid.UUID
	CheckoutSessionID *uuid.UUID
	Provider          string
	ProviderPaymentID *string
	ProviderChargeRef *string
	Amount            int64
	Currency          string
	State             string
}

const paymentColumns = `id, org_id, checkout_session_id, provider, provider_payment_id, provider_charge_ref, amount, currency, state`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.OrgID, &p.CheckoutSessionID, &p.Provider, &p.ProviderPaymentID, &p.ProviderChargeRef,
		&p.Amount, &p.Currency, &p.State)
	return p, err
}

func getRefund(ctx context.Context, q pgx.Tx, id uuid.UUID, forUpdate bool) (Refund, error) {
	sql := `SELECT ` + refundColumns + ` FROM refunds WHERE id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	return scanRefund(q.QueryRow(ctx, sql, id))
}

func getPayment(ctx context.Context, q pgx.Tx, id uuid.UUID) (Payment, error) {
	return scanPayment(q.QueryRow(ctx, `SELECT `+paymentColumns+` FROM payment_intents WHERE id = $1`, id))
}

// GetRefund reads one refund row with the engine's columns.
func (e *Engine) GetRefund(ctx context.Context, id uuid.UUID) (Refund, error) {
	var out Refund
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		r, err := getRefund(ctx, tx, id, false)
		out = r
		return err
	})
	return out, err
}

// inTx runs fn in a transaction and commits it when fn returns nil.
func (e *Engine) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	if e.db == nil {
		return errors.New("refunds: no database")
	}
	tx, err := e.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// truncate keeps an operator-facing text bounded.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
