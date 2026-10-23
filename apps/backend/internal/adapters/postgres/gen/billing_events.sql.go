// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: billing_events.sql
//
// Billing event store (billing wave B-A, task B2-1, migration 0158):
// billable_events, charges, charge_reversals, charge_allocations and the
// collect/rate job cursors. Money is int64 minor units with the currency
// next to it, never a float. billable_events, charges and charge_reversals
// are append-only: there is no update or delete of them here.

package gen

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Row types and scanners
// ─────────────────────────────────────────────────────────────────────────────

// BillableEventRow is one billing fact. Currency is the SALE currency
// (billable_events.sale_currency) and the money fields are in it, nil when
// they do not apply to the kind. Attributes is the raw jsonb.
type BillableEventRow struct {
	ID                 uuid.UUID  `json:"id"`
	Kind               string     `json:"kind"`
	SubjectID          uuid.UUID  `json:"subject_id"`
	OccurredAt         time.Time  `json:"occurred_at"`
	OrgID              uuid.UUID  `json:"org_id"`
	AccountID          uuid.UUID  `json:"account_id"`
	EventID            *uuid.UUID `json:"event_id"`
	SessionID          *uuid.UUID `json:"session_id"`
	OrderID            *uuid.UUID `json:"order_id"`
	TicketID           *uuid.UUID `json:"ticket_id"`
	Seated             *bool      `json:"seated"`
	PricingClass       *string    `json:"pricing_class"`
	Currency           *string    `json:"sale_currency"`
	UnitPriceMinor     *int64     `json:"unit_price_minor"`
	DiscountMinor      *int64     `json:"discount_minor"`
	ServiceChargeMinor *int64     `json:"service_charge_minor"`
	VenueCountry       *string    `json:"venue_country"`
	Attributes         []byte     `json:"attributes"`
	Source             string     `json:"source"`
	CollectedAt        time.Time  `json:"collected_at"`
}

const billableEventColumns = `id, kind, subject_id, occurred_at, org_id, account_id,
       event_id, session_id, order_id, ticket_id, seated, pricing_class,
       sale_currency, unit_price_minor, discount_minor, service_charge_minor,
       venue_country, attributes, source, collected_at`

const billableEventColumnsE = `e.id, e.kind, e.subject_id, e.occurred_at, e.org_id, e.account_id,
       e.event_id, e.session_id, e.order_id, e.ticket_id, e.seated, e.pricing_class,
       e.sale_currency, e.unit_price_minor, e.discount_minor, e.service_charge_minor,
       e.venue_country, e.attributes, e.source, e.collected_at`

func scanBillableEventRow(row interface{ Scan(dest ...any) error }) (BillableEventRow, error) {
	var r BillableEventRow
	err := row.Scan(&r.ID, &r.Kind, &r.SubjectID, &r.OccurredAt, &r.OrgID, &r.AccountID,
		&r.EventID, &r.SessionID, &r.OrderID, &r.TicketID, &r.Seated, &r.PricingClass,
		&r.Currency, &r.UnitPriceMinor, &r.DiscountMinor, &r.ServiceChargeMinor,
		&r.VenueCountry, &r.Attributes, &r.Source, &r.CollectedAt)
	return r, err
}

func collectBillableEventRows(rows pgx.Rows) ([]BillableEventRow, error) {
	defer rows.Close()
	var out []BillableEventRow
	for rows.Next() {
		r, err := scanBillableEventRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChargeRow is the result of rating one billable event. AmountMinor is in
// Currency, the account currency: >= 0 for a direct charge, <= 0 for a
// reversal (ReversalOf set).
type ChargeRow struct {
	ID               uuid.UUID  `json:"id"`
	BillableEventID  uuid.UUID  `json:"billable_event_id"`
	AccountID        uuid.UUID  `json:"account_id"`
	Currency         string     `json:"currency"`
	AmountMinor      int64      `json:"amount_minor"`
	RuleType         string     `json:"rule_type"`
	RuleID           *uuid.UUID `json:"rule_id"`
	PriceListID      *uuid.UUID `json:"price_list_id"`
	PriceListVersion *int32     `json:"price_list_version"`
	OverrideID       *uuid.UUID `json:"override_id"`
	Inputs           []byte     `json:"inputs"`
	Reason           *string    `json:"reason"`
	ReversalOf       *uuid.UUID `json:"reversal_of"`
	RatedAt          time.Time  `json:"rated_at"`
}

const chargeColumnsC = `c.id, c.billable_event_id, c.account_id, c.currency, c.amount_minor, c.rule_type, c.rule_id,
       c.price_list_id, c.price_list_version, c.override_id, c.inputs, c.reason, c.reversal_of, c.rated_at`

func scanChargeRow(row interface{ Scan(dest ...any) error }) (ChargeRow, error) {
	var r ChargeRow
	err := row.Scan(&r.ID, &r.BillableEventID, &r.AccountID, &r.Currency, &r.AmountMinor,
		&r.RuleType, &r.RuleID, &r.PriceListID, &r.PriceListVersion, &r.OverrideID,
		&r.Inputs, &r.Reason, &r.ReversalOf, &r.RatedAt)
	return r, err
}

func collectChargeRows(rows pgx.Rows) ([]ChargeRow, error) {
	defer rows.Close()
	var out []ChargeRow
	for rows.Next() {
		r, err := scanChargeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChargeReversalRow is one entry of the reversal journal: the charge, the
// negative charge that cancels it and the event that caused it.
type ChargeReversalRow struct {
	ID               uuid.UUID `json:"id"`
	ChargeID         uuid.UUID `json:"charge_id"`
	ReversalChargeID uuid.UUID `json:"reversal_charge_id"`
	CauseEventID     uuid.UUID `json:"cause_event_id"`
	Reason           string    `json:"reason"`
	ActorType        string    `json:"actor_type"`
	ActorID          string    `json:"actor_id"`
	CreatedAt        time.Time `json:"created_at"`
}

func scanChargeReversalRow(row interface{ Scan(dest ...any) error }) (ChargeReversalRow, error) {
	var r ChargeReversalRow
	err := row.Scan(&r.ID, &r.ChargeID, &r.ReversalChargeID, &r.CauseEventID,
		&r.Reason, &r.ActorType, &r.ActorID, &r.CreatedAt)
	return r, err
}

// ChargeAllocationRow is what closed (part of) a charge. AmountMinor is in
// Currency and always positive.
type ChargeAllocationRow struct {
	ID            uuid.UUID  `json:"id"`
	ChargeID      uuid.UUID  `json:"charge_id"`
	Kind          string     `json:"kind"`
	LotID         *uuid.UUID `json:"lot_id"`
	InvoiceLineID *uuid.UUID `json:"invoice_line_id"`
	AmountMinor   int64      `json:"amount_minor"`
	Currency      string     `json:"currency"`
	CreatedAt     time.Time  `json:"created_at"`
}

func scanChargeAllocationRow(row interface{ Scan(dest ...any) error }) (ChargeAllocationRow, error) {
	var r ChargeAllocationRow
	err := row.Scan(&r.ID, &r.ChargeID, &r.Kind, &r.LotID, &r.InvoiceLineID,
		&r.AmountMinor, &r.Currency, &r.CreatedAt)
	return r, err
}

// ChargeSumRow is the net amount of one currency over a period.
// AmountMinor is the sum of the charges and the negative reversals.
type ChargeSumRow struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	ChargeCount int64  `json:"charge_count"`
}

// BillingJobCursorRow is the high-water mark of one billing job.
type BillingJobCursorRow struct {
	Key       string    `json:"key"`
	CursorAt  time.Time `json:"cursor_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Billable events
// ─────────────────────────────────────────────────────────────────────────────

// InsertBillableEventParams are the columns of a new billable event. Empty
// Source means "collect". Currency is the sale currency; the money fields are
// in it. Attributes nil means an empty object.
type InsertBillableEventParams struct {
	Kind               string
	SubjectID          uuid.UUID
	OccurredAt         time.Time
	OrgID              uuid.UUID
	AccountID          uuid.UUID
	EventID            *uuid.UUID
	SessionID          *uuid.UUID
	OrderID            *uuid.UUID
	TicketID           *uuid.UUID
	Seated             *bool
	PricingClass       *string
	Currency           *string
	UnitPriceMinor     *int64
	DiscountMinor      *int64
	ServiceChargeMinor *int64
	VenueCountry       *string
	Attributes         []byte
	Source             string
}

const insertBillableEvent = `-- name: InsertBillableEvent :one
INSERT INTO billable_events (
    kind, subject_id, occurred_at, org_id, account_id,
    event_id, session_id, order_id, ticket_id, seated, pricing_class,
    sale_currency, unit_price_minor, discount_minor, service_charge_minor,
    venue_country, attributes, source)
VALUES ($1, $2, $3, $4, $5,
        $6, $7, $8, $9, $10, $11,
        $12, $13, $14, $15,
        $16, COALESCE($17::jsonb, '{}'::jsonb), $18)
ON CONFLICT (kind, subject_id) DO NOTHING
RETURNING id`

// InsertBillableEvent writes a billing fact, idempotently by (Kind,
// SubjectID): inserted is false and the id is the zero UUID when the event was
// already there (nothing is written). Any other failure is returned as is.
func (q *Queries) InsertBillableEvent(ctx context.Context, p InsertBillableEventParams) (id uuid.UUID, inserted bool, err error) {
	source := p.Source
	if source == "" {
		source = "collect"
	}
	err = q.db.QueryRow(ctx, insertBillableEvent,
		p.Kind, p.SubjectID, p.OccurredAt, p.OrgID, p.AccountID,
		p.EventID, p.SessionID, p.OrderID, p.TicketID, p.Seated, p.PricingClass,
		p.Currency, p.UnitPriceMinor, p.DiscountMinor, p.ServiceChargeMinor,
		p.VenueCountry, p.Attributes, source).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

const getBillableEvent = `-- name: GetBillableEvent :one
SELECT ` + billableEventColumns + `
FROM   billable_events
WHERE  kind = $1 AND subject_id = $2`

// GetBillableEvent returns the event with that kind and subject, pgx.ErrNoRows
// when there is none.
func (q *Queries) GetBillableEvent(ctx context.Context, kind string, subjectID uuid.UUID) (BillableEventRow, error) {
	return scanBillableEventRow(q.db.QueryRow(ctx, getBillableEvent, kind, subjectID))
}

const listUnratedBillableEvents = `-- name: ListUnratedBillableEvents :many
SELECT ` + billableEventColumnsE + `
FROM   billable_events e
WHERE  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL)
ORDER BY e.occurred_at, e.id
LIMIT  $1`

// ListUnratedBillableEvents returns up to limit events that have no DIRECT
// charge yet, oldest occurred_at first. An event that costs nothing is rated
// with a zero-amount direct charge, so "unrated" is exactly "no direct
// charge".
func (q *Queries) ListUnratedBillableEvents(ctx context.Context, limit int32) ([]BillableEventRow, error) {
	rows, err := q.db.Query(ctx, listUnratedBillableEvents, limit)
	if err != nil {
		return nil, err
	}
	return collectBillableEventRows(rows)
}

const listUnratedBillableEventsSince = `-- name: ListUnratedBillableEventsSince :many
SELECT ` + billableEventColumnsE + `
FROM   billable_events e
WHERE  e.occurred_at >= $1
  AND  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL)
ORDER BY e.occurred_at, e.id
LIMIT  $2`

// ListUnratedBillableEventsSince is ListUnratedBillableEvents limited to
// events that occurred at or after since, so a rate job that keeps a
// watermark does not walk the whole rated past.
func (q *Queries) ListUnratedBillableEventsSince(ctx context.Context, since time.Time, limit int32) ([]BillableEventRow, error) {
	rows, err := q.db.Query(ctx, listUnratedBillableEventsSince, since, limit)
	if err != nil {
		return nil, err
	}
	return collectBillableEventRows(rows)
}

const listBillableEventsForSession = `-- name: ListBillableEventsForSession :many
SELECT ` + billableEventColumns + `
FROM   billable_events
WHERE  session_id = $1 AND kind = $2
ORDER BY occurred_at, id`

// ListBillableEventsForSession returns the events of one kind of a session,
// oldest first.
func (q *Queries) ListBillableEventsForSession(ctx context.Context, sessionID uuid.UUID, kind string) ([]BillableEventRow, error) {
	rows, err := q.db.Query(ctx, listBillableEventsForSession, sessionID, kind)
	if err != nil {
		return nil, err
	}
	return collectBillableEventRows(rows)
}

const countUnratedBillableEventsForSession = `-- name: CountUnratedBillableEventsForSession :one
SELECT count(*)
FROM   billable_events e
WHERE  e.session_id = $1 AND e.kind = $2
  AND  NOT EXISTS (SELECT 1 FROM charges c
                   WHERE  c.billable_event_id = e.id AND c.reversal_of IS NULL)`

// CountUnratedBillableEventsForSession counts the events of one kind of a
// session that still have no direct charge: a session_cancelled event waits
// while its session's ticket_sold events are not all rated.
func (q *Queries) CountUnratedBillableEventsForSession(ctx context.Context, sessionID uuid.UUID, kind string) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countUnratedBillableEventsForSession, sessionID, kind).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Charges
// ─────────────────────────────────────────────────────────────────────────────

// InsertChargeParams are the columns of a new charge. A reversal sets
// ReversalOf and carries a negative AmountMinor. Inputs nil means an empty
// object. Currency is the account currency.
type InsertChargeParams struct {
	BillableEventID  uuid.UUID
	AccountID        uuid.UUID
	Currency         string
	AmountMinor      int64
	RuleType         string
	RuleID           *uuid.UUID
	PriceListID      *uuid.UUID
	PriceListVersion *int32
	OverrideID       *uuid.UUID
	Inputs           []byte
	Reason           *string
	ReversalOf       *uuid.UUID
}

const insertCharge = `-- name: InsertCharge :one
INSERT INTO charges (
    billable_event_id, account_id, currency, amount_minor, rule_type, rule_id,
    price_list_id, price_list_version, override_id, inputs, reason, reversal_of)
VALUES ($1, $2, $3, $4, $5, $6,
        $7, $8, $9, COALESCE($10::jsonb, '{}'::jsonb), $11, $12)
RETURNING id, billable_event_id, account_id, currency, amount_minor, rule_type, rule_id,
          price_list_id, price_list_version, override_id, inputs, reason, reversal_of, rated_at`

// InsertCharge writes the result of rating one event. A second DIRECT charge
// for the same event, or a second reversal of the same charge, fails with
// SQLSTATE 23505 (charges_one_direct_per_event_uq,
// charges_one_reversal_per_charge_uq): a rate job treats that as "already
// rated".
func (q *Queries) InsertCharge(ctx context.Context, p InsertChargeParams) (ChargeRow, error) {
	return scanChargeRow(q.db.QueryRow(ctx, insertCharge,
		p.BillableEventID, p.AccountID, p.Currency, p.AmountMinor, p.RuleType, p.RuleID,
		p.PriceListID, p.PriceListVersion, p.OverrideID, p.Inputs, p.Reason, p.ReversalOf))
}

const getChargeByEvent = `-- name: GetChargeByEvent :one
SELECT id, billable_event_id, account_id, currency, amount_minor, rule_type, rule_id,
       price_list_id, price_list_version, override_id, inputs, reason, reversal_of, rated_at
FROM   charges
WHERE  billable_event_id = $1 AND reversal_of IS NULL`

// GetChargeByEvent returns the DIRECT charge of an event, pgx.ErrNoRows when
// the event is not rated yet.
func (q *Queries) GetChargeByEvent(ctx context.Context, billableEventID uuid.UUID) (ChargeRow, error) {
	return scanChargeRow(q.db.QueryRow(ctx, getChargeByEvent, billableEventID))
}

const listReversibleChargesForSession = `-- name: ListReversibleChargesForSession :many
SELECT ` + chargeColumnsC + `
FROM   charges c
JOIN   billable_events e ON e.id = c.billable_event_id
WHERE  e.session_id = $1 AND e.kind = 'ticket_sold'
  AND  c.reversal_of IS NULL
  AND  NOT EXISTS (SELECT 1 FROM charges r WHERE r.reversal_of = c.id)
ORDER BY e.occurred_at, c.id`

// ListReversibleChargesForSession returns the direct charges of the
// ticket_sold events of a session that have no reversal yet: what a
// session_cancelled event reverses.
func (q *Queries) ListReversibleChargesForSession(ctx context.Context, sessionID uuid.UUID) ([]ChargeRow, error) {
	rows, err := q.db.Query(ctx, listReversibleChargesForSession, sessionID)
	if err != nil {
		return nil, err
	}
	return collectChargeRows(rows)
}

// InsertChargeReversalParams are the columns of a reversal journal entry.
// Empty ActorType means "system".
type InsertChargeReversalParams struct {
	ChargeID         uuid.UUID
	ReversalChargeID uuid.UUID
	CauseEventID     uuid.UUID
	Reason           string
	ActorType        string
	ActorID          string
}

const insertChargeReversal = `-- name: InsertChargeReversal :one
INSERT INTO charge_reversals (charge_id, reversal_charge_id, cause_event_id, reason, actor_type, actor_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, charge_id, reversal_charge_id, cause_event_id, reason, actor_type, actor_id, created_at`

// InsertChargeReversal journals a reversal. A charge is reversed at most once:
// a second entry for the same charge, or for the same reversal charge, fails
// with SQLSTATE 23505.
func (q *Queries) InsertChargeReversal(ctx context.Context, p InsertChargeReversalParams) (ChargeReversalRow, error) {
	actorType := p.ActorType
	if actorType == "" {
		actorType = "system"
	}
	return scanChargeReversalRow(q.db.QueryRow(ctx, insertChargeReversal,
		p.ChargeID, p.ReversalChargeID, p.CauseEventID, p.Reason, actorType, p.ActorID))
}

const getChargeReversalByCharge = `-- name: GetChargeReversalByCharge :one
SELECT id, charge_id, reversal_charge_id, cause_event_id, reason, actor_type, actor_id, created_at
FROM   charge_reversals
WHERE  charge_id = $1`

// GetChargeReversalByCharge returns the journal entry that reversed a charge,
// pgx.ErrNoRows when it was not reversed.
func (q *Queries) GetChargeReversalByCharge(ctx context.Context, chargeID uuid.UUID) (ChargeReversalRow, error) {
	return scanChargeReversalRow(q.db.QueryRow(ctx, getChargeReversalByCharge, chargeID))
}

const listChargesByAccountPeriod = `-- name: ListChargesByAccountPeriod :many
SELECT ` + chargeColumnsC + `
FROM   billable_events e
JOIN   charges c ON c.billable_event_id = e.id
WHERE  e.account_id = $1 AND e.occurred_at >= $2 AND e.occurred_at < $3
ORDER BY e.occurred_at, c.rated_at, c.id`

// ListChargesByAccountPeriod returns the charges and reversals of an account
// whose EVENT occurred in [from, to). The period is the period of the sale: a
// reversal counts in the period of the charge it reverses.
func (q *Queries) ListChargesByAccountPeriod(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]ChargeRow, error) {
	rows, err := q.db.Query(ctx, listChargesByAccountPeriod, accountID, from, to)
	if err != nil {
		return nil, err
	}
	return collectChargeRows(rows)
}

const sumChargesByAccountPeriod = `-- name: SumChargesByAccountPeriod :many
SELECT c.currency, COALESCE(sum(c.amount_minor), 0)::bigint AS amount_minor, count(*) AS charge_count
FROM   billable_events e
JOIN   charges c ON c.billable_event_id = e.id
WHERE  e.account_id = $1 AND e.occurred_at >= $2 AND e.occurred_at < $3
GROUP BY c.currency
ORDER BY c.currency`

// SumChargesByAccountPeriod returns the net amount (charges plus the negative
// reversals) and the number of charge rows per currency, for the same period
// as ListChargesByAccountPeriod.
func (q *Queries) SumChargesByAccountPeriod(ctx context.Context, accountID uuid.UUID, from, to time.Time) ([]ChargeSumRow, error) {
	rows, err := q.db.Query(ctx, sumChargesByAccountPeriod, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChargeSumRow
	for rows.Next() {
		var r ChargeSumRow
		if err := rows.Scan(&r.Currency, &r.AmountMinor, &r.ChargeCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Charge allocations
// ─────────────────────────────────────────────────────────────────────────────

// InsertChargeAllocationParams are the columns of an allocation. Kind
// purchased_lot or bonus_lot needs LotID, invoice_line needs InvoiceLineID,
// agreement_discount needs neither; the table's CHECK refuses the rest.
// AmountMinor must be positive and is in Currency.
type InsertChargeAllocationParams struct {
	ChargeID      uuid.UUID
	Kind          string
	LotID         *uuid.UUID
	InvoiceLineID *uuid.UUID
	AmountMinor   int64
	Currency      string
}

const insertChargeAllocation = `-- name: InsertChargeAllocation :one
INSERT INTO charge_allocations (charge_id, kind, lot_id, invoice_line_id, amount_minor, currency)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, charge_id, kind, lot_id, invoice_line_id, amount_minor, currency, created_at`

// InsertChargeAllocation records what closed (part of) a charge.
func (q *Queries) InsertChargeAllocation(ctx context.Context, p InsertChargeAllocationParams) (ChargeAllocationRow, error) {
	return scanChargeAllocationRow(q.db.QueryRow(ctx, insertChargeAllocation,
		p.ChargeID, p.Kind, p.LotID, p.InvoiceLineID, p.AmountMinor, p.Currency))
}

const listChargeAllocations = `-- name: ListChargeAllocations :many
SELECT id, charge_id, kind, lot_id, invoice_line_id, amount_minor, currency, created_at
FROM   charge_allocations
WHERE  charge_id = $1
ORDER BY created_at, id`

// ListChargeAllocations returns the allocations of one charge, oldest first.
func (q *Queries) ListChargeAllocations(ctx context.Context, chargeID uuid.UUID) ([]ChargeAllocationRow, error) {
	rows, err := q.db.Query(ctx, listChargeAllocations, chargeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChargeAllocationRow
	for rows.Next() {
		r, err := scanChargeAllocationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Job cursors
// ─────────────────────────────────────────────────────────────────────────────

const getBillingJobCursor = `-- name: GetBillingJobCursor :one
SELECT key, cursor_at, updated_at
FROM   billing_job_cursors
WHERE  key = $1`

// GetBillingJobCursor returns the cursor of a billing job, pgx.ErrNoRows when
// the job has not run yet.
func (q *Queries) GetBillingJobCursor(ctx context.Context, key string) (BillingJobCursorRow, error) {
	var r BillingJobCursorRow
	err := q.db.QueryRow(ctx, getBillingJobCursor, key).Scan(&r.Key, &r.CursorAt, &r.UpdatedAt)
	return r, err
}

const upsertBillingJobCursor = `-- name: UpsertBillingJobCursor :exec
INSERT INTO billing_job_cursors (key, cursor_at, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET cursor_at = EXCLUDED.cursor_at, updated_at = now()`

// UpsertBillingJobCursor sets the cursor of a billing job to at, creating it
// when missing. It is the one in-place update of this store: the cursor is
// bookkeeping, not billing history.
func (q *Queries) UpsertBillingJobCursor(ctx context.Context, key string, at time.Time) error {
	_, err := q.db.Exec(ctx, upsertBillingJobCursor, key, at)
	return err
}
