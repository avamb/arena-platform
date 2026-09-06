// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: superadmin.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// ListAllCheckoutSessions
// ─────────────────────────────────────────────────────────────────────────────

const listAllCheckoutSessions = `-- name: ListAllCheckoutSessions :many
SELECT id, org_id, channel_id, reservation_id, user_id, state,
       subtotal, discount, platform_fee, provider_fee, tax, total, currency,
       promo_code_id, payment_intent_id, payment_provider,
       completed_at, abandoned_at, expired_at, created_at, updated_at
FROM   checkout_sessions
WHERE  ($1::uuid IS NULL OR org_id = $1)
  AND  ($2::text  IS NULL OR state  = $2)
ORDER BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4`

// ListAllCheckoutSessions returns checkout sessions across all organizations.
// Pass nil for orgID to return sessions from all orgs.
// Pass nil for stateFilter to return sessions in any state.
// Use limit and offset for pagination (e.g. limit=50 offset=0).
func (q *Queries) ListAllCheckoutSessions(
	ctx context.Context,
	orgID *uuid.UUID,
	stateFilter *string,
	limit int32,
	offset int32,
) ([]CheckoutSessionRow, error) {
	rows, err := q.db.Query(ctx, listAllCheckoutSessions, orgID, stateFilter, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []CheckoutSessionRow
	for rows.Next() {
		r, err := scanCheckoutSessionRow(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, r)
	}
	return sessions, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListAllTickets
// ─────────────────────────────────────────────────────────────────────────────

const listAllTickets = `-- name: ListAllTickets :many
SELECT t.id, t.checkout_session_id, t.session_id, t.tier_id,
       t.holder_email, t.status, t.issued_at, t.created_at, t.updated_at,
       t.seat_key, t.seat_sector, t.seat_row, t.seat_number, t.ordinal,
       t.cancelled_at, t.cancellation_reason, t.refund_mode, t.refund_id,
       t.refund_date, t.refund_price, t.review_hold, t.review_hold_reason, t.system_ticket_id
FROM   tickets t
JOIN   checkout_sessions cs ON cs.id = t.checkout_session_id
WHERE  ($1::uuid IS NULL OR cs.org_id = $1)
  AND  ($2::text  IS NULL OR t.status = $2)
ORDER BY t.issued_at DESC, t.id DESC
LIMIT  $3 OFFSET $4`

// ListAllTickets returns tickets across all organizations (joined through the
// owning checkout session for org-level scoping).
// Pass nil for orgID to return tickets from all orgs.
// Pass nil for statusFilter to return tickets in any status.
// Use limit and offset for pagination (e.g. limit=50 offset=0).
func (q *Queries) ListAllTickets(
	ctx context.Context,
	orgID *uuid.UUID,
	statusFilter *string,
	limit int32,
	offset int32,
) ([]TicketRow, error) {
	rows, err := q.db.Query(ctx, listAllTickets, orgID, statusFilter, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []TicketRow
	for rows.Next() {
		r, err := scanTicketRow(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, r)
	}
	return tickets, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListAllOrders
// ─────────────────────────────────────────────────────────────────────────────

const listAllOrders = `-- name: ListAllOrders :many
SELECT id, system_id, org_id, channel_id, event_id, session_id, customer_id,
       checkout_session_id, reservation_id, external_ref, source, status,
       currency, subtotal, discount, charge, total, charge_percent_bp,
       promo_code_id, buyer_name, buyer_email, buyer_phone, payment_method,
       paid_at, cancelled_at, expires_at, metadata, created_at, updated_at
FROM   orders
WHERE  ($1::uuid IS NULL OR org_id = $1)
  AND  ($2::text  IS NULL OR status = $2)
ORDER BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4`

// ListAllOrders returns orders across all organizations (W1-A6d, feature
// #489, spec §14.2 — GET /v1/admin/orders reads the `orders` aggregate table
// instead of checkout_sessions).
// Pass nil for orgID to return orders from all orgs.
// Pass nil for stateFilter to return orders in any status.
// Use limit and offset for pagination (e.g. limit=50 offset=0).
func (q *Queries) ListAllOrders(
	ctx context.Context,
	orgID *uuid.UUID,
	stateFilter *string,
	limit int32,
	offset int32,
) ([]OrderRow, error) {
	rows, err := q.db.Query(ctx, listAllOrders, orgID, stateFilter, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []OrderRow
	for rows.Next() {
		o, err := scanOrderRow(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListAllRefunds
// ─────────────────────────────────────────────────────────────────────────────

const listAllRefunds = `-- name: ListAllRefunds :many
SELECT id, payment_intent_id, org_id, amount, currency, reason, requested_by,
       state, provider_refund_id, failure_reason,
       requested_at, approved_at, succeeded_at, failed_at, created_at, updated_at
FROM   refunds
WHERE  ($1::uuid IS NULL OR org_id = $1)
  AND  ($2::text  IS NULL OR state  = $2)
ORDER BY created_at DESC, id DESC
LIMIT  $3 OFFSET $4`

// ListAllRefunds returns refunds across all organizations.
// Pass nil for orgID to return refunds from all orgs.
// Pass nil for stateFilter to return refunds in any state.
// Use limit and offset for pagination (e.g. limit=50 offset=0).
func (q *Queries) ListAllRefunds(
	ctx context.Context,
	orgID *uuid.UUID,
	stateFilter *string,
	limit int32,
	offset int32,
) ([]RefundRow, error) {
	rows, err := q.db.Query(ctx, listAllRefunds, orgID, stateFilter, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refunds []RefundRow
	for rows.Next() {
		r, err := scanRefundRow(rows)
		if err != nil {
			return nil, err
		}
		refunds = append(refunds, r)
	}
	return refunds, rows.Err()
}
