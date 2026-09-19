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
SELECT o.id, o.system_id, o.org_id, o.channel_id, o.event_id, o.session_id, o.customer_id,
       o.checkout_session_id, o.reservation_id, o.external_ref, o.source, o.status,
       o.currency, o.subtotal, o.discount, o.charge, o.total, o.charge_percent_bp,
       o.promo_code_id, o.buyer_name, o.buyer_email, o.buyer_phone, o.payment_method,
       o.paid_at, o.cancelled_at, o.expires_at, o.metadata, o.created_at, o.updated_at,
       COALESCE(org.name, '') AS org_name, COALESCE(ev.name, '') AS event_name
FROM   orders o
LEFT JOIN organizations org ON org.id = o.org_id
LEFT JOIN events        ev  ON ev.id  = o.event_id
WHERE  ($1::uuid IS NULL OR o.org_id = $1)
  AND  ($2::text  IS NULL OR o.status = $2)
  AND  ($3::text  IS NULL
        OR o.system_id::text = $3
        OR o.external_ref = $3
        OR strpos(lower(COALESCE(o.buyer_email, '')), lower($3)) > 0
        OR strpos(lower(COALESCE(o.buyer_name,  '')), lower($3)) > 0
        OR strpos(COALESCE(o.buyer_phone, ''), $3) > 0)
ORDER BY o.created_at DESC, o.id DESC
LIMIT  $4 OFFSET $5`

// AdminOrderRow is an orders row with the names support reads it by.
type AdminOrderRow struct {
	OrderRow
	OrgName   string `json:"org_name"`
	EventName string `json:"event_name"`
}

// ListAllOrders returns orders across all organizations (W1-A6d, feature
// #489, spec §14.2 — GET /v1/admin/orders reads the `orders` aggregate table
// instead of checkout_sessions).
// Pass nil for orgID to return orders from all orgs.
// Pass nil for stateFilter to return orders in any status.
// Pass nil for search to skip it; otherwise it matches the order number or
// the site's reference exactly, or part of the buyer's email, name or phone.
// Use limit and offset for pagination (e.g. limit=50 offset=0).
func (q *Queries) ListAllOrders(
	ctx context.Context,
	orgID *uuid.UUID,
	stateFilter *string,
	search *string,
	limit int32,
	offset int32,
) ([]AdminOrderRow, error) {
	rows, err := q.db.Query(ctx, listAllOrders, orgID, stateFilter, search, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []AdminOrderRow
	for rows.Next() {
		var a AdminOrderRow
		o := &a.OrderRow
		if err := rows.Scan(
			&o.ID, &o.SystemID, &o.OrgID, &o.ChannelID, &o.EventID, &o.SessionID, &o.CustomerID,
			&o.CheckoutSessionID, &o.ReservationID, &o.ExternalRef, &o.Source, &o.Status,
			&o.Currency, &o.Subtotal, &o.Discount, &o.Charge, &o.Total, &o.ChargePercentBP,
			&o.PromoCodeID, &o.BuyerName, &o.BuyerEmail, &o.BuyerPhone, &o.PaymentMethod,
			&o.PaidAt, &o.CancelledAt, &o.ExpiresAt, &o.Metadata, &o.CreatedAt, &o.UpdatedAt,
			&a.OrgName, &a.EventName,
		); err != nil {
			return nil, err
		}
		orders = append(orders, a)
	}
	return orders, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListAllRefunds
// ─────────────────────────────────────────────────────────────────────────────

const listAllRefunds = `-- name: ListAllRefunds :many
SELECT id, payment_intent_id, org_id, amount, currency, reason, requested_by,
       state, provider_refund_id, failure_reason,
       requested_at, approved_at, succeeded_at, failed_at, created_at, updated_at, settlement, order_id, ticket_id
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
