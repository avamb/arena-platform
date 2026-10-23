// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: billing_credits.sql
//
// Credit lots and their journal (billing wave B-B, task B3-1, migration
// 0159). Money is int64 minor units of the account currency, never a float.
// credit_ledger is append-only: there is no update or delete of it here.
// Every write of a lot or the journal is made by internal/platform/billing
// inside a transaction that holds LockBillingAccount.

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

// CreditLotRow is one credit lot. Money is in Currency (the account's).
type CreditLotRow struct {
	ID                   uuid.UUID  `json:"id"`
	AccountID            uuid.UUID  `json:"account_id"`
	OrgID                uuid.UUID  `json:"org_id"`
	Currency             string     `json:"currency"`
	Kind                 string     `json:"kind"`
	Source               string     `json:"source"`
	SourceRef            *uuid.UUID `json:"source_ref"`
	SourceEventID        *uuid.UUID `json:"source_event_id"`
	Tier                 *int16     `json:"tier"`
	UnseatedRateMinor    *int64     `json:"unseated_rate_minor"`
	SeatedSurchargeMinor *int64     `json:"seated_surcharge_minor"`
	PriceListID          *uuid.UUID `json:"price_list_id"`
	PackID               *uuid.UUID `json:"pack_id"`
	PurchasedAt          time.Time  `json:"purchased_at"`
	FaceMinor            int64      `json:"face_minor"`
	RemainingMinor       int64      `json:"remaining_minor"`
	ExpiresAt            *time.Time `json:"expires_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

const creditLotColumns = `id, account_id, org_id, currency, kind, source, source_ref, source_event_id,
       tier, unseated_rate_minor, seated_surcharge_minor, price_list_id, pack_id,
       purchased_at, face_minor, remaining_minor, expires_at, created_at, updated_at`

func scanCreditLotRow(row interface{ Scan(dest ...any) error }) (CreditLotRow, error) {
	var r CreditLotRow
	err := row.Scan(&r.ID, &r.AccountID, &r.OrgID, &r.Currency, &r.Kind, &r.Source, &r.SourceRef,
		&r.SourceEventID, &r.Tier, &r.UnseatedRateMinor, &r.SeatedSurchargeMinor, &r.PriceListID,
		&r.PackID, &r.PurchasedAt, &r.FaceMinor, &r.RemainingMinor, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func collectCreditLotRows(rows pgx.Rows) ([]CreditLotRow, error) {
	defer rows.Close()
	var out []CreditLotRow
	for rows.Next() {
		r, err := scanCreditLotRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreditLedgerRow is one journal entry. AmountMinor is always positive, the
// sign follows EntryType; RemainingAfter is the lot's balance right after.
type CreditLedgerRow struct {
	ID             uuid.UUID  `json:"id"`
	Seq            int64      `json:"seq"`
	AccountID      uuid.UUID  `json:"account_id"`
	LotID          uuid.UUID  `json:"lot_id"`
	EntryType      string     `json:"entry_type"`
	AmountMinor    int64      `json:"amount_minor"`
	Currency       string     `json:"currency"`
	RemainingAfter int64      `json:"remaining_after"`
	ChargeID       *uuid.UUID `json:"charge_id"`
	AllocationID   *uuid.UUID `json:"allocation_id"`
	InvoiceID      *uuid.UUID `json:"invoice_id"`
	CauseEventID   *uuid.UUID `json:"cause_event_id"`
	ActorType      string     `json:"actor_type"`
	ActorID        string     `json:"actor_id"`
	Reason         *string    `json:"reason"`
	CreatedAt      time.Time  `json:"created_at"`
}

const creditLedgerColumns = `id, seq, account_id, lot_id, entry_type, amount_minor, currency, remaining_after,
       charge_id, allocation_id, invoice_id, cause_event_id, actor_type, actor_id, reason, created_at`

func scanCreditLedgerRow(row interface{ Scan(dest ...any) error }) (CreditLedgerRow, error) {
	var r CreditLedgerRow
	err := row.Scan(&r.ID, &r.Seq, &r.AccountID, &r.LotID, &r.EntryType, &r.AmountMinor, &r.Currency,
		&r.RemainingAfter, &r.ChargeID, &r.AllocationID, &r.InvoiceID, &r.CauseEventID,
		&r.ActorType, &r.ActorID, &r.Reason, &r.CreatedAt)
	return r, err
}

func collectCreditLedgerRows(rows pgx.Rows) ([]CreditLedgerRow, error) {
	defer rows.Close()
	var out []CreditLedgerRow
	for rows.Next() {
		r, err := scanCreditLedgerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Account lock
// ─────────────────────────────────────────────────────────────────────────────

const lockBillingAccount = `-- name: LockBillingAccount :one
SELECT id FROM billing_accounts WHERE id = $1 FOR UPDATE`

// LockBillingAccount takes the row lock every credit write runs under (spec
// 42 rule 10). It must be called on a transaction-bound Queries.
func (q *Queries) LockBillingAccount(ctx context.Context, accountID uuid.UUID) error {
	var id uuid.UUID
	return q.db.QueryRow(ctx, lockBillingAccount, accountID).Scan(&id)
}

// ─────────────────────────────────────────────────────────────────────────────
// Lots
// ─────────────────────────────────────────────────────────────────────────────

// InsertCreditLotParams are the columns of a new lot; RemainingMinor starts
// at FaceMinor.
type InsertCreditLotParams struct {
	AccountID            uuid.UUID
	OrgID                uuid.UUID
	Currency             string
	Kind                 string
	Source               string
	SourceRef            *uuid.UUID
	SourceEventID        *uuid.UUID
	Tier                 *int16
	UnseatedRateMinor    *int64
	SeatedSurchargeMinor *int64
	PriceListID          *uuid.UUID
	PackID               *uuid.UUID
	PurchasedAt          time.Time
	FaceMinor            int64
	ExpiresAt            *time.Time
}

const insertCreditLot = `-- name: InsertCreditLot :one
INSERT INTO credit_lots (account_id, org_id, currency, kind, source, source_ref, source_event_id,
       tier, unseated_rate_minor, seated_surcharge_minor, price_list_id, pack_id,
       purchased_at, face_minor, remaining_minor, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14, $15)
ON CONFLICT (account_id, source, source_ref) WHERE source_ref IS NOT NULL DO NOTHING
RETURNING ` + creditLotColumns

// InsertCreditLot writes a lot. With a SourceRef it is idempotent: a repeat
// writes nothing and answers inserted=false with a zero row.
func (q *Queries) InsertCreditLot(ctx context.Context, p InsertCreditLotParams) (CreditLotRow, bool, error) {
	r, err := scanCreditLotRow(q.db.QueryRow(ctx, insertCreditLot,
		p.AccountID, p.OrgID, p.Currency, p.Kind, p.Source, p.SourceRef, p.SourceEventID,
		p.Tier, p.UnseatedRateMinor, p.SeatedSurchargeMinor, p.PriceListID, p.PackID,
		p.PurchasedAt, p.FaceMinor, p.ExpiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return CreditLotRow{}, false, nil
	}
	if err != nil {
		return CreditLotRow{}, false, err
	}
	return r, true, nil
}

const getCreditLotBySource = `-- name: GetCreditLotBySource :one
SELECT ` + creditLotColumns + `
FROM   credit_lots
WHERE  account_id = $1 AND source = $2 AND source_ref = $3`

// GetCreditLotBySource returns the lot a source made (a purchase invoice, a
// cancellation), pgx.ErrNoRows when there is none.
func (q *Queries) GetCreditLotBySource(ctx context.Context, accountID uuid.UUID, source string, ref uuid.UUID) (CreditLotRow, error) {
	return scanCreditLotRow(q.db.QueryRow(ctx, getCreditLotBySource, accountID, source, ref))
}

const getCreditLot = `-- name: GetCreditLot :one
SELECT ` + creditLotColumns + ` FROM credit_lots WHERE id = $1`

// GetCreditLot returns one lot.
func (q *Queries) GetCreditLot(ctx context.Context, id uuid.UUID) (CreditLotRow, error) {
	return scanCreditLotRow(q.db.QueryRow(ctx, getCreditLot, id))
}

const listAvailableCreditLots = `-- name: ListAvailableCreditLots :many
SELECT ` + creditLotColumns + `
FROM   credit_lots
WHERE  account_id = $1
  AND  kind = $2
  AND  remaining_minor > 0
  AND  purchased_at <= $3
  AND  (expires_at IS NULL OR expires_at > $3)
ORDER  BY expires_at ASC NULLS LAST, purchased_at, id`

// ListAvailableCreditLots returns the lots of one kind that a charge at
// `at` may spend: bought (or granted) no later than at, not expired at at,
// with something left. Order: nearest to expiry first, then FIFO.
func (q *Queries) ListAvailableCreditLots(ctx context.Context, accountID uuid.UUID, kind string, at time.Time) ([]CreditLotRow, error) {
	rows, err := q.db.Query(ctx, listAvailableCreditLots, accountID, kind, at)
	if err != nil {
		return nil, err
	}
	return collectCreditLotRows(rows)
}

const setCreditLotRemaining = `-- name: SetCreditLotRemaining :exec
UPDATE credit_lots SET remaining_minor = $2, updated_at = now() WHERE id = $1`

// SetCreditLotRemaining stores a lot's new balance (written with its journal
// entry, under the account lock).
func (q *Queries) SetCreditLotRemaining(ctx context.Context, id uuid.UUID, remaining int64) error {
	_, err := q.db.Exec(ctx, setCreditLotRemaining, id, remaining)
	return err
}

const growCreditLot = `-- name: GrowCreditLot :one
UPDATE credit_lots
SET    face_minor = face_minor + $2, remaining_minor = remaining_minor + $2, updated_at = now()
WHERE  id = $1
RETURNING ` + creditLotColumns

// GrowCreditLot adds amount to both face and remaining of a lot (a
// cancellation bonus that grows with every reversed charge of its session).
func (q *Queries) GrowCreditLot(ctx context.Context, id uuid.UUID, amount int64) (CreditLotRow, error) {
	return scanCreditLotRow(q.db.QueryRow(ctx, growCreditLot, id, amount))
}

const listCreditLotsByAccount = `-- name: ListCreditLotsByAccount :many
SELECT ` + creditLotColumns + `
FROM   credit_lots
WHERE  account_id = $1
ORDER  BY purchased_at DESC, id DESC`

// ListCreditLotsByAccount returns every lot of an account, newest first.
func (q *Queries) ListCreditLotsByAccount(ctx context.Context, accountID uuid.UUID) ([]CreditLotRow, error) {
	rows, err := q.db.Query(ctx, listCreditLotsByAccount, accountID)
	if err != nil {
		return nil, err
	}
	return collectCreditLotRows(rows)
}

const listExpiringCreditLots = `-- name: ListExpiringCreditLots :many
SELECT ` + creditLotColumns + `
FROM   credit_lots
WHERE  expires_at IS NOT NULL AND expires_at <= $1 AND remaining_minor > 0
ORDER  BY expires_at, id
LIMIT  $2`

// ListExpiringCreditLots returns lots whose expiry has passed and still hold
// something, oldest expiry first.
func (q *Queries) ListExpiringCreditLots(ctx context.Context, now time.Time, limit int32) ([]CreditLotRow, error) {
	rows, err := q.db.Query(ctx, listExpiringCreditLots, now, limit)
	if err != nil {
		return nil, err
	}
	return collectCreditLotRows(rows)
}

// ─────────────────────────────────────────────────────────────────────────────
// Journal
// ─────────────────────────────────────────────────────────────────────────────

// AppendCreditLedgerParams are the columns of a journal entry.
type AppendCreditLedgerParams struct {
	AccountID      uuid.UUID
	LotID          uuid.UUID
	EntryType      string
	AmountMinor    int64
	Currency       string
	RemainingAfter int64
	ChargeID       *uuid.UUID
	AllocationID   *uuid.UUID
	InvoiceID      *uuid.UUID
	CauseEventID   *uuid.UUID
	ActorType      string
	ActorID        string
	Reason         *string
}

const appendCreditLedger = `-- name: AppendCreditLedger :one
INSERT INTO credit_ledger (account_id, lot_id, entry_type, amount_minor, currency, remaining_after,
       charge_id, allocation_id, invoice_id, cause_event_id, actor_type, actor_id, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE(NULLIF($11, ''), 'system'), $12, $13)
RETURNING ` + creditLedgerColumns

// AppendCreditLedger writes one journal entry.
func (q *Queries) AppendCreditLedger(ctx context.Context, p AppendCreditLedgerParams) (CreditLedgerRow, error) {
	return scanCreditLedgerRow(q.db.QueryRow(ctx, appendCreditLedger,
		p.AccountID, p.LotID, p.EntryType, p.AmountMinor, p.Currency, p.RemainingAfter,
		p.ChargeID, p.AllocationID, p.InvoiceID, p.CauseEventID, p.ActorType, p.ActorID, p.Reason))
}

const listCreditLedgerByAccount = `-- name: ListCreditLedgerByAccount :many
SELECT ` + creditLedgerColumns + `
FROM   credit_ledger
WHERE  account_id = $1 AND ($2::bigint IS NULL OR seq < $2)
ORDER  BY seq DESC
LIMIT  $3`

// ListCreditLedgerByAccount returns a page of an account's journal, newest
// first; beforeSeq (nil for the first page) is the keyset cursor.
func (q *Queries) ListCreditLedgerByAccount(ctx context.Context, accountID uuid.UUID, beforeSeq *int64, limit int32) ([]CreditLedgerRow, error) {
	rows, err := q.db.Query(ctx, listCreditLedgerByAccount, accountID, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	return collectCreditLedgerRows(rows)
}

// creditLedgerDelta is the signed effect of an entry on its lot. It is the
// one place the sign rule lives in SQL.
const creditLedgerDelta = `CASE l.entry_type
           WHEN 'purchase' THEN l.amount_minor
           WHEN 'bonus_grant' THEN l.amount_minor
           WHEN 'reversal' THEN l.amount_minor
           WHEN 'release' THEN l.amount_minor
           WHEN 'adjustment_credit' THEN l.amount_minor
           WHEN 'consume' THEN -l.amount_minor
           WHEN 'expire' THEN -l.amount_minor
           WHEN 'hold' THEN -l.amount_minor
           WHEN 'adjustment_debit' THEN -l.amount_minor
           ELSE 0 END`

// CreditLotMismatchRow is a lot whose remaining_minor differs from its
// journal, or whose last snapshot differs from it.
type CreditLotMismatchRow struct {
	LotID          uuid.UUID `json:"lot_id"`
	AccountID      uuid.UUID `json:"account_id"`
	OrgID          uuid.UUID `json:"org_id"`
	Currency       string    `json:"currency"`
	RemainingMinor int64     `json:"remaining_minor"`
	JournalMinor   int64     `json:"journal_minor"`
	SnapshotMinor  int64     `json:"snapshot_minor"`
}

const listCreditLotMismatches = `-- name: ListCreditLotMismatches :many
WITH j AS (
    SELECT l.lot_id, SUM(` + creditLedgerDelta + `)::bigint AS journal_minor
    FROM   credit_ledger l
    GROUP  BY l.lot_id
), last AS (
    SELECT DISTINCT ON (l.lot_id) l.lot_id, l.remaining_after
    FROM   credit_ledger l
    ORDER  BY l.lot_id, l.seq DESC
)
SELECT c.id, c.account_id, c.org_id, c.currency, c.remaining_minor,
       COALESCE(j.journal_minor, 0), COALESCE(last.remaining_after, 0)
FROM   credit_lots c
LEFT JOIN j    ON j.lot_id = c.id
LEFT JOIN last ON last.lot_id = c.id
WHERE  ($1::uuid[] IS NULL OR c.org_id = ANY($1))
  AND  (c.remaining_minor <> COALESCE(j.journal_minor, 0)
        OR c.remaining_minor <> COALESCE(last.remaining_after, 0))
ORDER  BY c.id
LIMIT  $2`

// ListCreditLotMismatches returns lots whose balance is not their journal
// (41 section 6.10). orgs (nil = every organization) scopes a test.
func (q *Queries) ListCreditLotMismatches(ctx context.Context, orgs []uuid.UUID, limit int32) ([]CreditLotMismatchRow, error) {
	rows, err := q.db.Query(ctx, listCreditLotMismatches, orgs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CreditLotMismatchRow
	for rows.Next() {
		var r CreditLotMismatchRow
		if err := rows.Scan(&r.LotID, &r.AccountID, &r.OrgID, &r.Currency, &r.RemainingMinor,
			&r.JournalMinor, &r.SnapshotMinor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ConsumeAllocationMismatchRow is a lot whose consume entries do not add up
// to the charge allocations that name it.
type ConsumeAllocationMismatchRow struct {
	LotID          uuid.UUID `json:"lot_id"`
	OrgID          uuid.UUID `json:"org_id"`
	Currency       string    `json:"currency"`
	ConsumedMinor  int64     `json:"consumed_minor"`
	AllocatedMinor int64     `json:"allocated_minor"`
	ReturnedMinor  int64     `json:"returned_minor"`
}

const listConsumeAllocationMismatches = `-- name: ListConsumeAllocationMismatches :many
WITH consumed AS (
    SELECT lot_id,
           SUM(amount_minor) FILTER (WHERE entry_type = 'consume')::bigint  AS consumed_minor,
           SUM(amount_minor) FILTER (WHERE entry_type = 'reversal')::bigint AS returned_minor
    FROM   credit_ledger
    WHERE  charge_id IS NOT NULL
    GROUP  BY lot_id
), allocated AS (
    SELECT lot_id, SUM(amount_minor)::bigint AS allocated_minor
    FROM   charge_allocations
    WHERE  lot_id IS NOT NULL
    GROUP  BY lot_id
)
SELECT c.id, c.org_id, c.currency,
       COALESCE(consumed.consumed_minor, 0), COALESCE(allocated.allocated_minor, 0),
       COALESCE(consumed.returned_minor, 0)
FROM   credit_lots c
LEFT JOIN consumed  ON consumed.lot_id  = c.id
LEFT JOIN allocated ON allocated.lot_id = c.id
WHERE  ($1::uuid[] IS NULL OR c.org_id = ANY($1))
  AND  COALESCE(consumed.consumed_minor, 0) <> COALESCE(allocated.allocated_minor, 0)
ORDER  BY c.id
LIMIT  $2`

// ListConsumeAllocationMismatches returns lots whose consume entries differ
// from the allocations that name them (41 section 6.10: sum consume = sum
// charge_allocations by lot).
func (q *Queries) ListConsumeAllocationMismatches(ctx context.Context, orgs []uuid.UUID, limit int32) ([]ConsumeAllocationMismatchRow, error) {
	rows, err := q.db.Query(ctx, listConsumeAllocationMismatches, orgs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConsumeAllocationMismatchRow
	for rows.Next() {
		var r ConsumeAllocationMismatchRow
		if err := rows.Scan(&r.LotID, &r.OrgID, &r.Currency, &r.ConsumedMinor, &r.AllocatedMinor,
			&r.ReturnedMinor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Balance
// ─────────────────────────────────────────────────────────────────────────────

// CreditBalanceRow is the balance of an account at `at`, by lot kind.
type CreditBalanceRow struct {
	Currency           string     `json:"currency"`
	PurchasedMinor     int64      `json:"purchased_minor"`
	BonusMinor         int64      `json:"bonus_minor"`
	NextBonusExpiresAt *time.Time `json:"next_bonus_expires_at"`
}

const getCreditBalance = `-- name: GetCreditBalance :one
SELECT a.currency,
       COALESCE(SUM(c.remaining_minor) FILTER (WHERE c.kind = 'purchased'), 0)::bigint,
       COALESCE(SUM(c.remaining_minor) FILTER (WHERE c.kind = 'bonus'), 0)::bigint,
       MIN(c.expires_at) FILTER (WHERE c.kind = 'bonus')
FROM   billing_accounts a
LEFT JOIN credit_lots c
       ON c.account_id = a.id AND c.remaining_minor > 0
      AND (c.expires_at IS NULL OR c.expires_at > $2)
WHERE  a.id = $1
GROUP  BY a.currency`

// GetCreditBalance returns what an account can spend at `at`.
func (q *Queries) GetCreditBalance(ctx context.Context, accountID uuid.UUID, at time.Time) (CreditBalanceRow, error) {
	var r CreditBalanceRow
	err := q.db.QueryRow(ctx, getCreditBalance, accountID, at).Scan(
		&r.Currency, &r.PurchasedMinor, &r.BonusMinor, &r.NextBonusExpiresAt)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Charges for the settler
// ─────────────────────────────────────────────────────────────────────────────

const listCreditAllocationsByCharge = `-- name: ListCreditAllocationsByCharge :many
SELECT id, charge_id, kind, lot_id, invoice_line_id, amount_minor, currency, created_at
FROM   charge_allocations
WHERE  charge_id = $1
ORDER  BY created_at, id
FOR UPDATE`

// ListCreditAllocationsByCharge returns (and row-locks) the allocations of a
// charge, the input of a reversal.
func (q *Queries) ListCreditAllocationsByCharge(ctx context.Context, chargeID uuid.UUID) ([]ChargeAllocationRow, error) {
	rows, err := q.db.Query(ctx, listCreditAllocationsByCharge, chargeID)
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
