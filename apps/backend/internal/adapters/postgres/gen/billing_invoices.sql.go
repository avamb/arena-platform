// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: billing_invoices.sql
//
// Platform invoices to organizers, their gapless numbering, payment fees,
// the platform Stripe webhook journal and incoming bank payments (billing
// tasks B3-3, B4-1, B4-2, migration 0160). Money is int64 minor units next
// to its currency, never a float.

package gen

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BillingInvoiceRow is an invoice with the columns of migration 0160.
type BillingInvoiceRow struct {
	ID                      uuid.UUID  `json:"id"`
	OrgID                   uuid.UUID  `json:"org_id"`
	AccountID               *uuid.UUID `json:"account_id"`
	IssuerID                *uuid.UUID `json:"issuer_id"`
	Kind                    string     `json:"kind"`
	State                   string     `json:"state"`
	BillingPeriod           string     `json:"billing_period"`
	Number                  *string    `json:"number"`
	NumberYear              *int32     `json:"number_year"`
	NumberSeq               *int32     `json:"number_seq"`
	RelatedInvoiceID        *uuid.UUID `json:"related_invoice_id"`
	IdempotencyKey          *string    `json:"idempotency_key"`
	PaymentMethod           *string    `json:"payment_method"`
	PaymentReference        *string    `json:"payment_reference"`
	PeriodFrom              *time.Time `json:"period_from"`
	PeriodTo                *time.Time `json:"period_to"`
	PackID                  *uuid.UUID `json:"pack_id"`
	Currency                string     `json:"currency"`
	SubtotalMinor           int64      `json:"subtotal_minor"`
	CardFeeMinor            int64      `json:"card_fee_minor"`
	TaxStatus               *string    `json:"tax_status"`
	TaxMinor                int64      `json:"tax_minor"`
	TotalAmountMinor        int64      `json:"total_amount_minor"`
	DueAt                   *time.Time `json:"due_at"`
	IssuedAt                *time.Time `json:"issued_at"`
	PaidAt                  *time.Time `json:"paid_at"`
	PaidAmountMinor         *int64     `json:"paid_amount_minor"`
	PaidVia                 *string    `json:"paid_via"`
	PaidReference           *string    `json:"paid_reference"`
	VoidedAt                *time.Time `json:"voided_at"`
	VoidReason              *string    `json:"void_reason"`
	StripeInvoiceID         *string    `json:"stripe_invoice_id"`
	StripeCheckoutSessionID *string    `json:"stripe_checkout_session_id"`
	StripePaymentIntentID   *string    `json:"stripe_payment_intent_id"`
	HostedPaymentURL        *string    `json:"hosted_payment_url"`
	CreatedBy               *string    `json:"created_by"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

const billingInvoiceColumns = `id, org_id, account_id, issuer_id, kind, state, billing_period,
       number, number_year, number_seq, related_invoice_id, idempotency_key, payment_method,
       payment_reference, period_from, period_to, pack_id, currency, subtotal_minor, card_fee_minor,
       tax_status, tax_minor, total_amount_minor, due_at, issued_at, paid_at, paid_amount_minor,
       paid_via, paid_reference, voided_at, void_reason, stripe_invoice_id,
       stripe_checkout_session_id, stripe_payment_intent_id, hosted_payment_url, created_by,
       created_at, updated_at`

func scanBillingInvoiceRow(row interface{ Scan(dest ...any) error }) (BillingInvoiceRow, error) {
	var r BillingInvoiceRow
	err := row.Scan(&r.ID, &r.OrgID, &r.AccountID, &r.IssuerID, &r.Kind, &r.State, &r.BillingPeriod,
		&r.Number, &r.NumberYear, &r.NumberSeq, &r.RelatedInvoiceID, &r.IdempotencyKey, &r.PaymentMethod,
		&r.PaymentReference, &r.PeriodFrom, &r.PeriodTo, &r.PackID, &r.Currency, &r.SubtotalMinor,
		&r.CardFeeMinor, &r.TaxStatus, &r.TaxMinor, &r.TotalAmountMinor, &r.DueAt, &r.IssuedAt, &r.PaidAt,
		&r.PaidAmountMinor, &r.PaidVia, &r.PaidReference, &r.VoidedAt, &r.VoidReason, &r.StripeInvoiceID,
		&r.StripeCheckoutSessionID, &r.StripePaymentIntentID, &r.HostedPaymentURL, &r.CreatedBy,
		&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func collectBillingInvoiceRows(rows pgx.Rows) ([]BillingInvoiceRow, error) {
	defer rows.Close()
	var out []BillingInvoiceRow
	for rows.Next() {
		r, err := scanBillingInvoiceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BillingInvoiceLineRow is one line of an invoice.
type BillingInvoiceLineRow struct {
	ID               uuid.UUID `json:"id"`
	InvoiceID        uuid.UUID `json:"invoice_id"`
	Kind             string    `json:"kind"`
	RuleType         *string   `json:"rule_type"`
	Description      string    `json:"description"`
	Quantity         int64     `json:"quantity"`
	UnitAmountMinor  int64     `json:"unit_amount_minor"`
	TotalAmountMinor int64     `json:"total_amount_minor"`
	Currency         string    `json:"currency"`
	CreatedAt        time.Time `json:"created_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Invoices
// ─────────────────────────────────────────────────────────────────────────────

// InsertBillingInvoiceParams are the columns of a new invoice (state draft
// or issued; a number is given separately by IssueBillingInvoice).
type InsertBillingInvoiceParams struct {
	OrgID            uuid.UUID
	AccountID        uuid.UUID
	IssuerID         uuid.UUID
	Kind             string
	BillingPeriod    string
	IdempotencyKey   *string
	PaymentMethod    *string
	PeriodFrom       *time.Time
	PeriodTo         *time.Time
	PackID           *uuid.UUID
	RelatedInvoiceID *uuid.UUID
	Currency         string
	TaxStatus        *string
	CreatedBy        *string
}

const insertBillingInvoice = `-- name: InsertBillingInvoice :one
INSERT INTO invoices (org_id, account_id, issuer_id, kind, state, billing_period, idempotency_key,
       payment_method, period_from, period_to, pack_id, related_invoice_id, currency, tax_status,
       created_by, subtotal_minor, card_fee_minor, tax_minor, total_amount_minor)
VALUES ($1, $2, $3, $4, 'draft', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 0, 0, 0, 0)
ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING ` + billingInvoiceColumns

// InsertBillingInvoice writes a draft invoice. With an idempotency key a
// repeat writes nothing and answers inserted=false.
func (q *Queries) InsertBillingInvoice(ctx context.Context, p InsertBillingInvoiceParams) (BillingInvoiceRow, bool, error) {
	r, err := scanBillingInvoiceRow(q.db.QueryRow(ctx, insertBillingInvoice,
		p.OrgID, p.AccountID, p.IssuerID, p.Kind, p.BillingPeriod, p.IdempotencyKey, p.PaymentMethod,
		p.PeriodFrom, p.PeriodTo, p.PackID, p.RelatedInvoiceID, p.Currency, p.TaxStatus, p.CreatedBy))
	if errors.Is(err, pgx.ErrNoRows) {
		return BillingInvoiceRow{}, false, nil
	}
	if err != nil {
		return BillingInvoiceRow{}, false, err
	}
	return r, true, nil
}

const getBillingInvoice = `-- name: GetBillingInvoice :one
SELECT ` + billingInvoiceColumns + ` FROM invoices WHERE id = $1`

// GetBillingInvoice returns one invoice.
func (q *Queries) GetBillingInvoice(ctx context.Context, id uuid.UUID) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, getBillingInvoice, id))
}

const lockBillingInvoice = `-- name: LockBillingInvoice :one
SELECT ` + billingInvoiceColumns + ` FROM invoices WHERE id = $1 FOR UPDATE`

// LockBillingInvoice returns one invoice under a row lock.
func (q *Queries) LockBillingInvoice(ctx context.Context, id uuid.UUID) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, lockBillingInvoice, id))
}

const getBillingInvoiceByIdempotencyKey = `-- name: GetBillingInvoiceByIdempotencyKey :one
SELECT ` + billingInvoiceColumns + ` FROM invoices WHERE idempotency_key = $1`

// GetBillingInvoiceByIdempotencyKey returns the invoice a key made.
func (q *Queries) GetBillingInvoiceByIdempotencyKey(ctx context.Context, key string) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, getBillingInvoiceByIdempotencyKey, key))
}

const getBillingInvoiceByPaymentReference = `-- name: GetBillingInvoiceByPaymentReference :one
SELECT ` + billingInvoiceColumns + ` FROM invoices WHERE payment_reference = $1`

// GetBillingInvoiceByPaymentReference returns the invoice of a payment
// reference.
func (q *Queries) GetBillingInvoiceByPaymentReference(ctx context.Context, ref string) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, getBillingInvoiceByPaymentReference, ref))
}

const getBillingInvoiceByCheckoutSession = `-- name: GetBillingInvoiceByCheckoutSession :one
SELECT ` + billingInvoiceColumns + ` FROM invoices WHERE stripe_checkout_session_id = $1`

// GetBillingInvoiceByCheckoutSession returns the invoice a Stripe Checkout
// Session pays.
func (q *Queries) GetBillingInvoiceByCheckoutSession(ctx context.Context, sessionID string) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, getBillingInvoiceByCheckoutSession, sessionID))
}

const getBillingInvoiceByStripeInvoice = `-- name: GetBillingInvoiceByStripeInvoice :one
SELECT ` + billingInvoiceColumns + ` FROM invoices WHERE stripe_invoice_id = $1`

// GetBillingInvoiceByStripeInvoice returns the invoice a Stripe invoice
// collects.
func (q *Queries) GetBillingInvoiceByStripeInvoice(ctx context.Context, stripeInvoiceID string) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, getBillingInvoiceByStripeInvoice, stripeInvoiceID))
}

const listBillingInvoicesByOrg = `-- name: ListBillingInvoicesByOrg :many
SELECT ` + billingInvoiceColumns + `
FROM   invoices
WHERE  org_id = $1 AND account_id IS NOT NULL AND ($2::text = '' OR state = $2)
ORDER  BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4`

// ListBillingInvoicesByOrg returns an organization's billing-account
// invoices, newest first; state "" is every state.
func (q *Queries) ListBillingInvoicesByOrg(ctx context.Context, orgID uuid.UUID, state string, limit, offset int32) ([]BillingInvoiceRow, error) {
	rows, err := q.db.Query(ctx, listBillingInvoicesByOrg, orgID, state, limit, offset)
	if err != nil {
		return nil, err
	}
	return collectBillingInvoiceRows(rows)
}

const listOpenBillingInvoices = `-- name: ListOpenBillingInvoices :many
SELECT ` + billingInvoiceColumns + `
FROM   invoices
WHERE  account_id IS NOT NULL AND state = $1
ORDER  BY COALESCE(due_at, created_at), id
LIMIT  $2`

// ListOpenBillingInvoices returns billing-account invoices in one state,
// oldest due first (the operator's queue).
func (q *Queries) ListOpenBillingInvoices(ctx context.Context, state string, limit int32) ([]BillingInvoiceRow, error) {
	rows, err := q.db.Query(ctx, listOpenBillingInvoices, state, limit)
	if err != nil {
		return nil, err
	}
	return collectBillingInvoiceRows(rows)
}

const countBillingInvoicesForPeriod = `-- name: CountBillingInvoicesForPeriod :one
SELECT count(*) FROM invoices
WHERE  account_id = $1 AND kind = $2 AND billing_period = $3`

// CountBillingInvoicesForPeriod counts an account's invoices of a kind for
// a period, void ones included (the next idempotency sequence).
func (q *Queries) CountBillingInvoicesForPeriod(ctx context.Context, accountID uuid.UUID, kind, period string) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countBillingInvoicesForPeriod, accountID, kind, period).Scan(&n)
	return n, err
}

const setBillingInvoiceTotals = `-- name: SetBillingInvoiceTotals :one
UPDATE invoices
SET    subtotal_minor = $2, card_fee_minor = $3, tax_minor = $4,
       total_amount_minor = $2::bigint + $3::bigint + $4::bigint, updated_at = now()
WHERE  id = $1 AND state = 'draft'
RETURNING ` + billingInvoiceColumns

// SetBillingInvoiceTotals stores the money of a DRAFT invoice; total is
// subtotal + card fee + tax.
func (q *Queries) SetBillingInvoiceTotals(ctx context.Context, id uuid.UUID, subtotal, cardFee, tax int64) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, setBillingInvoiceTotals, id, subtotal, cardFee, tax))
}

// IssueBillingInvoiceParams are what issuing writes.
type IssueBillingInvoiceParams struct {
	ID               uuid.UUID
	Number           string
	NumberYear       int32
	NumberSeq        int32
	PaymentMethod    string
	PaymentReference string
	DueAt            *time.Time
	IssuedAt         time.Time
}

const issueBillingInvoice = `-- name: IssueBillingInvoice :one
UPDATE invoices
SET    state = 'issued', number = $2, number_year = $3, number_seq = $4, payment_method = $5,
       payment_reference = $6, due_at = $7, issued_at = $8, updated_at = now()
WHERE  id = $1 AND state = 'draft'
RETURNING ` + billingInvoiceColumns

// IssueBillingInvoice moves a draft to issued with its number.
func (q *Queries) IssueBillingInvoice(ctx context.Context, p IssueBillingInvoiceParams) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, issueBillingInvoice, p.ID, p.Number, p.NumberYear,
		p.NumberSeq, p.PaymentMethod, p.PaymentReference, p.DueAt, p.IssuedAt))
}

// MarkBillingInvoicePaidParams are what a payment writes.
type MarkBillingInvoicePaidParams struct {
	ID              uuid.UUID
	PaidAt          time.Time
	PaidAmountMinor int64
	Currency        string
	PaidVia         string
	PaidReference   string
	PaymentIntentID *string
}

const markBillingInvoicePaid = `-- name: MarkBillingInvoicePaid :one
UPDATE invoices
SET    state = 'paid', paid_at = $2, paid_amount_minor = $3, paid_via = $5, paid_reference = $6,
       stripe_payment_intent_id = COALESCE($7, stripe_payment_intent_id), updated_at = now()
WHERE  id = $1 AND state = 'issued' AND currency = $4
RETURNING ` + billingInvoiceColumns

// MarkBillingInvoicePaid moves an issued invoice to paid.
func (q *Queries) MarkBillingInvoicePaid(ctx context.Context, p MarkBillingInvoicePaidParams) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, markBillingInvoicePaid, p.ID, p.PaidAt, p.PaidAmountMinor,
		p.Currency, p.PaidVia, p.PaidReference, p.PaymentIntentID))
}

const voidBillingInvoice = `-- name: VoidBillingInvoice :one
UPDATE invoices
SET    state = 'void', voided_at = $2, void_reason = $3, updated_at = now()
WHERE  id = $1 AND state IN ('draft', 'issued')
RETURNING ` + billingInvoiceColumns

// VoidBillingInvoice voids a draft or issued invoice (never a paid one).
func (q *Queries) VoidBillingInvoice(ctx context.Context, id uuid.UUID, at time.Time, reason string) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, voidBillingInvoice, id, at, reason))
}

const setBillingInvoiceCheckout = `-- name: SetBillingInvoiceCheckout :one
UPDATE invoices
SET    stripe_checkout_session_id = $2, hosted_payment_url = $3, updated_at = now()
WHERE  id = $1
RETURNING ` + billingInvoiceColumns

// SetBillingInvoiceCheckout stores the Stripe Checkout Session of an
// invoice and its payment page.
func (q *Queries) SetBillingInvoiceCheckout(ctx context.Context, id uuid.UUID, sessionID, url string) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, setBillingInvoiceCheckout, id, sessionID, url))
}

const setBillingInvoiceStripeInvoice = `-- name: SetBillingInvoiceStripeInvoice :one
UPDATE invoices
SET    stripe_invoice_id = $2, hosted_payment_url = COALESCE($3, hosted_payment_url), updated_at = now()
WHERE  id = $1
RETURNING ` + billingInvoiceColumns

// SetBillingInvoiceStripeInvoice stores the Stripe invoice that collects an
// invoice and its hosted page.
func (q *Queries) SetBillingInvoiceStripeInvoice(ctx context.Context, id uuid.UUID, stripeInvoiceID string, url *string) (BillingInvoiceRow, error) {
	return scanBillingInvoiceRow(q.db.QueryRow(ctx, setBillingInvoiceStripeInvoice, id, stripeInvoiceID, url))
}

// ─────────────────────────────────────────────────────────────────────────────
// Lines
// ─────────────────────────────────────────────────────────────────────────────

// InsertBillingInvoiceLineParams are the columns of a line.
type InsertBillingInvoiceLineParams struct {
	InvoiceID        uuid.UUID
	Kind             string
	RuleType         *string
	Description      string
	Quantity         int64
	UnitAmountMinor  int64
	TotalAmountMinor int64
	Currency         string
}

const insertBillingInvoiceLine = `-- name: InsertBillingInvoiceLine :one
INSERT INTO invoice_lines (invoice_id, kind, rule_type, description, quantity, unit_amount_minor,
       total_amount_minor, currency)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, invoice_id, kind, rule_type, description, quantity, unit_amount_minor,
          total_amount_minor, currency, created_at`

// InsertBillingInvoiceLine writes one line.
func (q *Queries) InsertBillingInvoiceLine(ctx context.Context, p InsertBillingInvoiceLineParams) (BillingInvoiceLineRow, error) {
	var r BillingInvoiceLineRow
	err := q.db.QueryRow(ctx, insertBillingInvoiceLine, p.InvoiceID, p.Kind, p.RuleType, p.Description,
		p.Quantity, p.UnitAmountMinor, p.TotalAmountMinor, p.Currency).Scan(&r.ID, &r.InvoiceID, &r.Kind,
		&r.RuleType, &r.Description, &r.Quantity, &r.UnitAmountMinor, &r.TotalAmountMinor, &r.Currency, &r.CreatedAt)
	return r, err
}

const listBillingInvoiceLines = `-- name: ListBillingInvoiceLines :many
SELECT id, invoice_id, kind, rule_type, description, quantity, unit_amount_minor,
       total_amount_minor, currency, created_at
FROM   invoice_lines WHERE invoice_id = $1 ORDER BY created_at, id`

// ListBillingInvoiceLines returns the lines of an invoice in order.
func (q *Queries) ListBillingInvoiceLines(ctx context.Context, invoiceID uuid.UUID) ([]BillingInvoiceLineRow, error) {
	rows, err := q.db.Query(ctx, listBillingInvoiceLines, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BillingInvoiceLineRow
	for rows.Next() {
		var r BillingInvoiceLineRow
		if err := rows.Scan(&r.ID, &r.InvoiceID, &r.Kind, &r.RuleType, &r.Description, &r.Quantity,
			&r.UnitAmountMinor, &r.TotalAmountMinor, &r.Currency, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const deleteBillingInvoiceCardFeeLines = `-- name: DeleteBillingInvoiceCardFeeLines :exec
DELETE FROM invoice_lines WHERE invoice_id = $1 AND kind = 'card_fee'`

// DeleteBillingInvoiceCardFeeLines removes the card fee line of a draft
// before it is recomputed.
func (q *Queries) DeleteBillingInvoiceCardFeeLines(ctx context.Context, invoiceID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteBillingInvoiceCardFeeLines, invoiceID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Usage: charges not yet invoiced
// ─────────────────────────────────────────────────────────────────────────────

// UninvoicedChargeRow is a direct charge with something left to bill.
type UninvoicedChargeRow struct {
	ChargeID       uuid.UUID `json:"charge_id"`
	RuleType       string    `json:"rule_type"`
	Currency       string    `json:"currency"`
	AmountMinor    int64     `json:"amount_minor"`
	UncoveredMinor int64     `json:"uncovered_minor"`
}

const listUninvoicedCharges = `-- name: ListUninvoicedCharges :many
SELECT c.id, c.rule_type, c.currency, c.amount_minor,
       (c.amount_minor - COALESCE((SELECT SUM(a.amount_minor) FROM charge_allocations a WHERE a.charge_id = c.id), 0))::bigint
FROM   charges c
JOIN   billable_events be ON be.id = c.billable_event_id
WHERE  c.account_id = $1
  AND  c.reversal_of IS NULL
  AND  c.amount_minor > 0
  AND  be.occurred_at >= $2 AND be.occurred_at < $3
  AND  NOT EXISTS (SELECT 1 FROM charges rv WHERE rv.reversal_of = c.id)
  AND  c.amount_minor > COALESCE((SELECT SUM(a.amount_minor) FROM charge_allocations a WHERE a.charge_id = c.id), 0)
ORDER  BY c.rule_type, be.occurred_at, c.id`

// ListUninvoicedCharges returns the direct charges of an account whose event
// is in [from, to), that were not reversed and still have an uncovered
// amount (charge minus lot, discount and earlier invoice allocations).
func (q *Queries) ListUninvoicedCharges(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]UninvoicedChargeRow, error) {
	rows, err := q.db.Query(ctx, listUninvoicedCharges, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UninvoicedChargeRow
	for rows.Next() {
		var r UninvoicedChargeRow
		if err := rows.Scan(&r.ChargeID, &r.RuleType, &r.Currency, &r.AmountMinor, &r.UncoveredMinor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const deleteInvoiceLineAllocations = `-- name: DeleteInvoiceLineAllocations :execrows
DELETE FROM charge_allocations
WHERE  kind = 'invoice_line'
  AND  invoice_line_id IN (SELECT id FROM invoice_lines WHERE invoice_id = $1)`

// DeleteInvoiceLineAllocations releases the charges a voided invoice had
// taken, so the next invoice bills them again.
func (q *Queries) DeleteInvoiceLineAllocations(ctx context.Context, invoiceID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteInvoiceLineAllocations, invoiceID)
	return tag.RowsAffected(), err
}

// ─────────────────────────────────────────────────────────────────────────────
// Numbering
// ─────────────────────────────────────────────────────────────────────────────

const nextInvoiceNumber = `-- name: NextInvoiceNumber :one
INSERT INTO invoice_number_series (issuer_id, year, last_seq)
VALUES ($1, $2, 1)
ON CONFLICT (issuer_id, year) DO UPDATE SET last_seq = invoice_number_series.last_seq + 1, updated_at = now()
RETURNING last_seq`

// NextInvoiceNumber takes the next number of an issuer's series for a year.
// Called in the transaction that issues the invoice: the row lock of the
// series serializes issuers, and a rollback gives the number back.
func (q *Queries) NextInvoiceNumber(ctx context.Context, issuerID uuid.UUID, year int32) (int32, error) {
	var seq int32
	err := q.db.QueryRow(ctx, nextInvoiceNumber, issuerID, year).Scan(&seq)
	return seq, err
}

const getBillingIssuer = `-- name: GetBillingIssuer :one
SELECT id, code, name, country, tax_id, invoice_series, timezone FROM billing_issuers WHERE id = $1`

// BillingIssuerBrief is what invoicing needs of an issuer.
type BillingIssuerBrief struct {
	ID            uuid.UUID
	Code          string
	Name          string
	Country       string
	TaxID         *string
	InvoiceSeries *string
	Timezone      string
}

// GetBillingIssuer returns an issuer.
func (q *Queries) GetBillingIssuer(ctx context.Context, id uuid.UUID) (BillingIssuerBrief, error) {
	var r BillingIssuerBrief
	err := q.db.QueryRow(ctx, getBillingIssuer, id).Scan(&r.ID, &r.Code, &r.Name, &r.Country, &r.TaxID,
		&r.InvoiceSeries, &r.Timezone)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Packs
// ─────────────────────────────────────────────────────────────────────────────

const getPriceListPackByID = `-- name: GetPriceListPackByID :one
SELECT p.id, p.price_list_id, pl.currency, p.tier, p.face_minor, p.unseated_rate_minor,
       p.seated_surcharge_minor, p.active, p.sort_order
FROM   price_list_packs p JOIN price_lists pl ON pl.id = p.price_list_id
WHERE  p.id = $1`

// GetPriceListPackByID returns one pack with its list's currency.
func (q *Queries) GetPriceListPackByID(ctx context.Context, id uuid.UUID) (PriceListPackRow, error) {
	var r PriceListPackRow
	err := q.db.QueryRow(ctx, getPriceListPackByID, id).Scan(&r.ID, &r.PriceListID, &r.Currency, &r.Tier,
		&r.FaceMinor, &r.UnseatedRateMinor, &r.SeatedSurchargeMinor, &r.Active, &r.SortOrder)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Payment fees
// ─────────────────────────────────────────────────────────────────────────────

// BillingPaymentFeeRow is the average fee of a payment method in a currency.
type BillingPaymentFeeRow struct {
	ID            uuid.UUID `json:"id"`
	Method        string    `json:"method"`
	Currency      string    `json:"currency"`
	PctBp         int32     `json:"pct_bp"`
	FixedMinor    int64     `json:"fixed_minor"`
	EffectiveFrom time.Time `json:"effective_from"`
	CreatedBy     *string   `json:"created_by"`
	Reason        *string   `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
}

const billingPaymentFeeColumns = `id, method, currency, pct_bp, fixed_minor, effective_from, created_by, reason, created_at`

func scanBillingPaymentFee(row interface{ Scan(dest ...any) error }) (BillingPaymentFeeRow, error) {
	var r BillingPaymentFeeRow
	err := row.Scan(&r.ID, &r.Method, &r.Currency, &r.PctBp, &r.FixedMinor, &r.EffectiveFrom, &r.CreatedBy,
		&r.Reason, &r.CreatedAt)
	return r, err
}

const getBillingPaymentFeeAt = `-- name: GetBillingPaymentFeeAt :one
SELECT ` + billingPaymentFeeColumns + `
FROM   billing_payment_fees
WHERE  method = $1 AND currency = $2 AND effective_from <= $3
ORDER  BY effective_from DESC
LIMIT  1`

// GetBillingPaymentFeeAt returns the fee of a method in force at `at`,
// pgx.ErrNoRows when none.
func (q *Queries) GetBillingPaymentFeeAt(ctx context.Context, method, currency string, at time.Time) (BillingPaymentFeeRow, error) {
	return scanBillingPaymentFee(q.db.QueryRow(ctx, getBillingPaymentFeeAt, method, currency, at))
}

// InsertBillingPaymentFeeParams are the columns of a new fee version.
type InsertBillingPaymentFeeParams struct {
	Method        string
	Currency      string
	PctBp         int32
	FixedMinor    int64
	EffectiveFrom time.Time
	CreatedBy     *string
	Reason        *string
}

const insertBillingPaymentFee = `-- name: InsertBillingPaymentFee :one
INSERT INTO billing_payment_fees (method, currency, pct_bp, fixed_minor, effective_from, created_by, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING ` + billingPaymentFeeColumns

// InsertBillingPaymentFee writes a fee version.
func (q *Queries) InsertBillingPaymentFee(ctx context.Context, p InsertBillingPaymentFeeParams) (BillingPaymentFeeRow, error) {
	return scanBillingPaymentFee(q.db.QueryRow(ctx, insertBillingPaymentFee, p.Method, p.Currency, p.PctBp,
		p.FixedMinor, p.EffectiveFrom, p.CreatedBy, p.Reason))
}

const listBillingPaymentFees = `-- name: ListBillingPaymentFees :many
SELECT ` + billingPaymentFeeColumns + ` FROM billing_payment_fees ORDER BY method, currency, effective_from DESC`

// ListBillingPaymentFees returns every fee version.
func (q *Queries) ListBillingPaymentFees(ctx context.Context) ([]BillingPaymentFeeRow, error) {
	rows, err := q.db.Query(ctx, listBillingPaymentFees)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BillingPaymentFeeRow
	for rows.Next() {
		r, err := scanBillingPaymentFee(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Stripe webhook journal
// ─────────────────────────────────────────────────────────────────────────────

const insertBillingStripeEvent = `-- name: InsertBillingStripeEvent :one
INSERT INTO billing_stripe_events (event_id, event_type, invoice_id, outcome)
VALUES ($1, $2, $3, $4)
ON CONFLICT (event_id) DO NOTHING
RETURNING event_id`

// InsertBillingStripeEvent records an applied event; false when it was
// already applied.
func (q *Queries) InsertBillingStripeEvent(ctx context.Context, eventID, eventType string, invoiceID *uuid.UUID, outcome string) (bool, error) {
	var id string
	err := q.db.QueryRow(ctx, insertBillingStripeEvent, eventID, eventType, invoiceID, outcome).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Stripe customers per account
// ─────────────────────────────────────────────────────────────────────────────

const getStripeCustomerIDByAccount = `-- name: GetStripeCustomerIDByAccount :one
SELECT stripe_customer_id FROM stripe_customers WHERE account_id = $1`

// GetStripeCustomerIDByAccount returns the Stripe customer of a billing
// account, pgx.ErrNoRows when none.
func (q *Queries) GetStripeCustomerIDByAccount(ctx context.Context, accountID uuid.UUID) (string, error) {
	var id string
	err := q.db.QueryRow(ctx, getStripeCustomerIDByAccount, accountID).Scan(&id)
	return id, err
}

const insertStripeCustomerForAccount = `-- name: InsertStripeCustomerForAccount :exec
INSERT INTO stripe_customers (org_id, account_id, stripe_customer_id)
VALUES ($1, $2, $3)
ON CONFLICT (stripe_customer_id) DO NOTHING`

// InsertStripeCustomerForAccount stores the Stripe customer of an account.
func (q *Queries) InsertStripeCustomerForAccount(ctx context.Context, orgID, accountID uuid.UUID, stripeCustomerID string) error {
	_, err := q.db.Exec(ctx, insertStripeCustomerForAccount, orgID, accountID, stripeCustomerID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Bank transactions
// ─────────────────────────────────────────────────────────────────────────────

// BillingBankTransactionRow is one incoming bank payment.
type BillingBankTransactionRow struct {
	ID               uuid.UUID  `json:"id"`
	Provider         string     `json:"provider"`
	ExternalID       string     `json:"external_id"`
	BookedAt         time.Time  `json:"booked_at"`
	AmountMinor      int64      `json:"amount_minor"`
	Currency         string     `json:"currency"`
	Reference        *string    `json:"reference"`
	CounterpartyName *string    `json:"counterparty_name"`
	Status           string     `json:"status"`
	UnmatchedReason  *string    `json:"unmatched_reason"`
	InvoiceID        *uuid.UUID `json:"invoice_id"`
	MatchedAt        *time.Time `json:"matched_at"`
	MatchedBy        *string    `json:"matched_by"`
	ReceivedAt       time.Time  `json:"received_at"`
}

const bankTxColumns = `id, provider, external_id, booked_at, amount_minor, currency, reference, counterparty_name,
       status, unmatched_reason, invoice_id, matched_at, matched_by, received_at`

func scanBankTx(row interface{ Scan(dest ...any) error }) (BillingBankTransactionRow, error) {
	var r BillingBankTransactionRow
	err := row.Scan(&r.ID, &r.Provider, &r.ExternalID, &r.BookedAt, &r.AmountMinor, &r.Currency, &r.Reference,
		&r.CounterpartyName, &r.Status, &r.UnmatchedReason, &r.InvoiceID, &r.MatchedAt, &r.MatchedBy, &r.ReceivedAt)
	return r, err
}

// InsertBankTransactionParams are the columns of an incoming payment.
type InsertBankTransactionParams struct {
	Provider         string
	ExternalID       string
	BookedAt         time.Time
	AmountMinor      int64
	Currency         string
	Reference        *string
	CounterpartyName *string
}

const insertBankTransaction = `-- name: InsertBankTransaction :one
INSERT INTO billing_bank_transactions (provider, external_id, booked_at, amount_minor, currency, reference,
       counterparty_name)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider, external_id) DO NOTHING
RETURNING ` + bankTxColumns

// InsertBankTransaction stores an incoming payment; a repeat (same provider
// and external id) writes nothing and answers inserted=false.
func (q *Queries) InsertBankTransaction(ctx context.Context, p InsertBankTransactionParams) (BillingBankTransactionRow, bool, error) {
	r, err := scanBankTx(q.db.QueryRow(ctx, insertBankTransaction, p.Provider, p.ExternalID, p.BookedAt,
		p.AmountMinor, p.Currency, p.Reference, p.CounterpartyName))
	if errors.Is(err, pgx.ErrNoRows) {
		return BillingBankTransactionRow{}, false, nil
	}
	if err != nil {
		return BillingBankTransactionRow{}, false, err
	}
	return r, true, nil
}

const lockBankTransaction = `-- name: LockBankTransaction :one
SELECT ` + bankTxColumns + ` FROM billing_bank_transactions WHERE id = $1 FOR UPDATE`

// LockBankTransaction returns one payment under a row lock.
func (q *Queries) LockBankTransaction(ctx context.Context, id uuid.UUID) (BillingBankTransactionRow, error) {
	return scanBankTx(q.db.QueryRow(ctx, lockBankTransaction, id))
}

const matchBankTransaction = `-- name: MatchBankTransaction :one
UPDATE billing_bank_transactions
SET    status = 'matched', invoice_id = $2, matched_at = $3, matched_by = $4, unmatched_reason = NULL
WHERE  id = $1 AND status <> 'matched'
RETURNING ` + bankTxColumns

// MatchBankTransaction ties a payment to the invoice it paid.
func (q *Queries) MatchBankTransaction(ctx context.Context, id, invoiceID uuid.UUID, at time.Time, by string) (BillingBankTransactionRow, error) {
	return scanBankTx(q.db.QueryRow(ctx, matchBankTransaction, id, invoiceID, at, by))
}

const setBankTransactionStatus = `-- name: SetBankTransactionStatus :one
UPDATE billing_bank_transactions
SET    status = $2, unmatched_reason = $3
WHERE  id = $1 AND status <> 'matched'
RETURNING ` + bankTxColumns

// SetBankTransactionStatus marks an unmatched payment (with the reason) or
// an ignored one.
func (q *Queries) SetBankTransactionStatus(ctx context.Context, id uuid.UUID, status string, reason *string) (BillingBankTransactionRow, error) {
	return scanBankTx(q.db.QueryRow(ctx, setBankTransactionStatus, id, status, reason))
}

const listBankTransactions = `-- name: ListBankTransactions :many
SELECT ` + bankTxColumns + `
FROM   billing_bank_transactions
WHERE  ($1::text = '' OR status = $1)
ORDER  BY booked_at DESC, id DESC
LIMIT  $2`

// ListBankTransactions returns payments, newest first; status "" is all.
func (q *Queries) ListBankTransactions(ctx context.Context, status string, limit int32) ([]BillingBankTransactionRow, error) {
	rows, err := q.db.Query(ctx, listBankTransactions, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BillingBankTransactionRow
	for rows.Next() {
		r, err := scanBankTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const invoiceHasBillingAccount = `-- name: InvoiceHasBillingAccount :one
SELECT account_id IS NOT NULL FROM invoices WHERE id = $1`

// InvoiceHasBillingAccount says whether an invoice belongs to the billing
// accounts (migration 0160) rather than the legacy tariff ledger: the legacy
// routes must not move it, or a credit pack would be paid without its lot.
func (q *Queries) InvoiceHasBillingAccount(ctx context.Context, id uuid.UUID) (bool, error) {
	var has bool
	err := q.db.QueryRow(ctx, invoiceHasBillingAccount, id).Scan(&has)
	return has, err
}

const updateBillingAccountsPayer = `-- name: UpdateBillingAccountsPayer :execrows
UPDATE billing_accounts SET payer = $2, updated_at = now() WHERE org_id = $1`

// UpdateBillingAccountsPayer stores the payer details on every billing
// account of an organization.
func (q *Queries) UpdateBillingAccountsPayer(ctx context.Context, orgID uuid.UUID, payer []byte) (int64, error) {
	tag, err := q.db.Exec(ctx, updateBillingAccountsPayer, orgID, payer)
	return tag.RowsAffected(), err
}
