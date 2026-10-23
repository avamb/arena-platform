// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: hand-written (the SQL lives in the constants below only)
//
// Read side of the organizer's billing usage screen (billing wave B-A, task
// B2-6): the charges of ONE organization, newest first, with per-currency
// sums, and the charge summary of one event. Every query filters on
// billable_events.org_id, so a row of another organization is never read.
// Money is int64 minor units with the currency next to it. Nothing here
// writes.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrgChargeRow is one charge of an organization joined with the event it
// rates. A reversal row (ReversalOf set) belongs to the ORIGINAL event, so
// the charges of one event net to zero when it is reversed; OccurredAt is the
// event's time. UnitPriceMinor is in SaleCurrency, AmountMinor in Currency.
type OrgChargeRow struct {
	ChargeID         uuid.UUID  `json:"charge_id"`
	BillableEventID  uuid.UUID  `json:"billable_event_id"`
	Kind             string     `json:"kind"`
	OccurredAt       time.Time  `json:"occurred_at"`
	RatedAt          time.Time  `json:"rated_at"`
	EventID          *uuid.UUID `json:"event_id"`
	SessionID        *uuid.UUID `json:"session_id"`
	OrderID          *uuid.UUID `json:"order_id"`
	Currency         string     `json:"currency"`
	AmountMinor      int64      `json:"amount_minor"`
	RuleType         string     `json:"rule_type"`
	Reason           *string    `json:"reason"`
	ReversalOf       *uuid.UUID `json:"reversal_of"`
	PriceListVersion *int32     `json:"price_list_version"`
	PricingClass     *string    `json:"pricing_class"`
	Seated           *bool      `json:"seated"`
	SaleCurrency     *string    `json:"sale_currency"`
	UnitPriceMinor   *int64     `json:"unit_price_minor"`
}

// ChargeTotalRow is the sum of charges in one currency. ChargedMinor sums the
// direct charges, ReversedMinor the reversals (zero or negative), NetMinor is
// their sum. Count is the number of direct charges.
type ChargeTotalRow struct {
	Currency      string `json:"currency"`
	ChargedMinor  int64  `json:"charged_minor"`
	ReversedMinor int64  `json:"reversed_minor"`
	NetMinor      int64  `json:"net_minor"`
	Count         int64  `json:"count"`
}

// ChargeRuleRow is the net charge of one rule type (a "line" of the future
// invoice) in one currency. Count is the number of direct charges.
type ChargeRuleRow struct {
	RuleType string  `json:"rule_type"`
	Reason   *string `json:"reason"`
	Currency string  `json:"currency"`
	NetMinor int64   `json:"net_minor"`
	Count    int64   `json:"count"`
}

// ChargeSessionRow is the charge summary of one session of an event in one
// currency. SessionStartAt is nil when the session row is gone.
type ChargeSessionRow struct {
	SessionID      *uuid.UUID `json:"session_id"`
	SessionStartAt *time.Time `json:"session_start_at"`
	Currency       string     `json:"currency"`
	ChargedMinor   int64      `json:"charged_minor"`
	ReversedMinor  int64      `json:"reversed_minor"`
	NetMinor       int64      `json:"net_minor"`
	Count          int64      `json:"count"`
}

// EventKindCountRow counts the billing events of one kind.
type EventKindCountRow struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

const listOrgChargesSQL = `-- name: ListOrgCharges :many
SELECT c.id, c.billable_event_id, be.kind, be.occurred_at, c.rated_at,
       be.event_id, be.session_id, be.order_id,
       c.currency::text, c.amount_minor, c.rule_type, c.reason, c.reversal_of,
       c.price_list_version, be.pricing_class, be.seated,
       be.sale_currency::text, be.unit_price_minor
FROM   charges c
JOIN   billable_events be ON be.id = c.billable_event_id
WHERE  be.org_id = $1
  AND  ($2::timestamptz IS NULL OR be.occurred_at >= $2)
  AND  ($3::timestamptz IS NULL OR be.occurred_at <  $3)
  AND  ($4::uuid IS NULL OR be.event_id = $4)
  AND  ($5::timestamptz IS NULL OR (be.occurred_at, c.id) < ($5, $6::uuid))
ORDER BY be.occurred_at DESC, c.id DESC
LIMIT  $7`

// ListOrgCharges returns a page of the organization's charges, newest event
// first. from is inclusive, to exclusive, eventID filters on the arena event
// (all nil = no filter); (cursorAt, cursorID) is the last row of the previous
// page (both nil = first page).
func (q *Queries) ListOrgCharges(ctx context.Context, orgID uuid.UUID, from, to *time.Time, eventID *uuid.UUID, cursorAt *time.Time, cursorID *uuid.UUID, limit int32) ([]OrgChargeRow, error) {
	rows, err := q.db.Query(ctx, listOrgChargesSQL, orgID, from, to, eventID, cursorAt, cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgChargeRow
	for rows.Next() {
		var r OrgChargeRow
		if err := rows.Scan(&r.ChargeID, &r.BillableEventID, &r.Kind, &r.OccurredAt, &r.RatedAt,
			&r.EventID, &r.SessionID, &r.OrderID,
			&r.Currency, &r.AmountMinor, &r.RuleType, &r.Reason, &r.ReversalOf,
			&r.PriceListVersion, &r.PricingClass, &r.Seated,
			&r.SaleCurrency, &r.UnitPriceMinor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const sumOrgChargesSQL = `-- name: SumOrgCharges :many
SELECT c.currency::text,
       COALESCE(sum(c.amount_minor) FILTER (WHERE c.reversal_of IS NULL), 0)::bigint,
       COALESCE(sum(c.amount_minor) FILTER (WHERE c.reversal_of IS NOT NULL), 0)::bigint,
       COALESCE(sum(c.amount_minor), 0)::bigint,
       (count(*) FILTER (WHERE c.reversal_of IS NULL))::bigint
FROM   charges c
JOIN   billable_events be ON be.id = c.billable_event_id
WHERE  be.org_id = $1
  AND  ($2::timestamptz IS NULL OR be.occurred_at >= $2)
  AND  ($3::timestamptz IS NULL OR be.occurred_at <  $3)
  AND  ($4::uuid IS NULL OR be.event_id = $4)
GROUP  BY c.currency
ORDER  BY c.currency`

// SumOrgCharges returns the per-currency sums over the same filter as
// ListOrgCharges (the whole filter, not one page).
func (q *Queries) SumOrgCharges(ctx context.Context, orgID uuid.UUID, from, to *time.Time, eventID *uuid.UUID) ([]ChargeTotalRow, error) {
	rows, err := q.db.Query(ctx, sumOrgChargesSQL, orgID, from, to, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChargeTotalRow
	for rows.Next() {
		var r ChargeTotalRow
		if err := rows.Scan(&r.Currency, &r.ChargedMinor, &r.ReversedMinor, &r.NetMinor, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getBillingEventOrgIDSQL = `-- name: GetBillingEventOrgID :one
SELECT org_id FROM events WHERE id = $1`

// GetBillingEventOrgID returns the organization of an arena event, deleted or
// not (its charges outlive it), or pgx.ErrNoRows.
func (q *Queries) GetBillingEventOrgID(ctx context.Context, eventID uuid.UUID) (uuid.UUID, error) {
	var org uuid.UUID
	err := q.db.QueryRow(ctx, getBillingEventOrgIDSQL, eventID).Scan(&org)
	return org, err
}

const chargesByRuleSQL = `-- name: ListEventChargesByRule :many
SELECT c.rule_type, c.reason, c.currency::text,
       COALESCE(sum(c.amount_minor), 0)::bigint,
       (count(*) FILTER (WHERE c.reversal_of IS NULL))::bigint
FROM   charges c
JOIN   billable_events be ON be.id = c.billable_event_id
WHERE  be.org_id = $1 AND be.event_id = $2
GROUP  BY c.rule_type, c.reason, c.currency
ORDER  BY c.currency, c.rule_type, c.reason NULLS FIRST`

// ListEventChargesByRule returns the net charge of one event per rule type
// (and reason) and currency: the lines of its future invoice.
func (q *Queries) ListEventChargesByRule(ctx context.Context, orgID, eventID uuid.UUID) ([]ChargeRuleRow, error) {
	rows, err := q.db.Query(ctx, chargesByRuleSQL, orgID, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChargeRuleRow
	for rows.Next() {
		var r ChargeRuleRow
		if err := rows.Scan(&r.RuleType, &r.Reason, &r.Currency, &r.NetMinor, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const chargesBySessionSQL = `-- name: ListEventChargesBySession :many
SELECT be.session_id, s.start_at, c.currency::text,
       COALESCE(sum(c.amount_minor) FILTER (WHERE c.reversal_of IS NULL), 0)::bigint,
       COALESCE(sum(c.amount_minor) FILTER (WHERE c.reversal_of IS NOT NULL), 0)::bigint,
       COALESCE(sum(c.amount_minor), 0)::bigint,
       (count(*) FILTER (WHERE c.reversal_of IS NULL))::bigint
FROM   charges c
JOIN   billable_events be ON be.id = c.billable_event_id
LEFT   JOIN sessions s ON s.id = be.session_id
WHERE  be.org_id = $1 AND be.event_id = $2
GROUP  BY be.session_id, s.start_at, c.currency
ORDER  BY s.start_at NULLS LAST, be.session_id, c.currency`

// ListEventChargesBySession returns the charge summary of one event per
// session and currency, earliest session first.
func (q *Queries) ListEventChargesBySession(ctx context.Context, orgID, eventID uuid.UUID) ([]ChargeSessionRow, error) {
	rows, err := q.db.Query(ctx, chargesBySessionSQL, orgID, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChargeSessionRow
	for rows.Next() {
		var r ChargeSessionRow
		if err := rows.Scan(&r.SessionID, &r.SessionStartAt, &r.Currency,
			&r.ChargedMinor, &r.ReversedMinor, &r.NetMinor, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const eventKindCountsSQL = `-- name: CountEventBillingKinds :many
SELECT kind, count(*)::bigint
FROM   billable_events
WHERE  org_id = $1 AND event_id = $2
GROUP  BY kind
ORDER  BY kind`

// CountEventBillingKinds counts the billing events of one event by kind.
func (q *Queries) CountEventBillingKinds(ctx context.Context, orgID, eventID uuid.UUID) ([]EventKindCountRow, error) {
	rows, err := q.db.Query(ctx, eventKindCountsSQL, orgID, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventKindCountRow
	for rows.Next() {
		var r EventKindCountRow
		if err := rows.Scan(&r.Kind, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
