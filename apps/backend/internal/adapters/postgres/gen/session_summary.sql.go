// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: session_summary.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSummaryHeader
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSummaryHeader = `-- name: GetSessionSummaryHeader :one
SELECT s.id, s.event_id, e.org_id, e.name AS event_name, s.start_at,
       s.status, s.capacity_total, s.seating_plan_version_id,
       v.name AS venue_name, v.timezone AS venue_timezone
FROM      sessions s
JOIN      events   e ON e.id = s.event_id
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.id = $1
  AND  e.org_id = $2
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL`

// SessionSummaryHeaderRow names the session a summary is about.
type SessionSummaryHeaderRow struct {
	ID                   uuid.UUID  `json:"id"`
	EventID              uuid.UUID  `json:"event_id"`
	OrgID                uuid.UUID  `json:"org_id"`
	EventName            string     `json:"event_name"`
	StartAt              time.Time  `json:"start_at"`
	Status               string     `json:"status"`
	CapacityTotal        int32      `json:"capacity_total"`
	SeatingPlanVersionID *uuid.UUID `json:"seating_plan_version_id"`
	VenueName            *string    `json:"venue_name"`
	VenueTimezone        *string    `json:"venue_timezone"`
}

// GetSessionSummaryHeader returns the session with its event and venue,
// scoped to the organization. pgx.ErrNoRows means the session does not exist
// in that organization (or is soft-deleted).
func (q *Queries) GetSessionSummaryHeader(ctx context.Context, sessionID, orgID uuid.UUID) (SessionSummaryHeaderRow, error) {
	var i SessionSummaryHeaderRow
	err := q.db.QueryRow(ctx, getSessionSummaryHeader, sessionID, orgID).Scan(
		&i.ID, &i.EventID, &i.OrgID, &i.EventName, &i.StartAt,
		&i.Status, &i.CapacityTotal, &i.SeatingPlanVersionID,
		&i.VenueName, &i.VenueTimezone,
	)
	return i, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryPlaces
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryPlaces = `-- name: ListSessionSummaryPlaces :many
SELECT ss.tier_id, ss.kind,
       count(*) FILTER (WHERE ss.status = 'available')   AS available,
       count(*) FILTER (WHERE ss.status = 'held')        AS held,
       count(*) FILTER (WHERE ss.status = 'sold')        AS sold,
       count(*) FILTER (WHERE ss.status = 'sold' AND ss.reservation_id IS NULL) AS sold_upstream,
       count(*) FILTER (WHERE ss.status = 'unavailable') AS unavailable
FROM   session_seats ss
WHERE  ss.session_id = $1
GROUP  BY ss.tier_id, ss.kind`

// SessionSummaryPlacesRow counts the places of one (category, kind) pair by
// status. SoldUpstream is the part of Sold that was sold in the system the
// session was imported from — no ticket or order stands behind it.
type SessionSummaryPlacesRow struct {
	TierID       *uuid.UUID `json:"tier_id"`
	Kind         string     `json:"kind"`
	Available    int64      `json:"available"`
	Held         int64      `json:"held"`
	Sold         int64      `json:"sold"`
	SoldUpstream int64      `json:"sold_upstream"`
	Unavailable  int64      `json:"unavailable"`
}

// ListSessionSummaryPlaces returns the place counters of a session.
func (q *Queries) ListSessionSummaryPlaces(ctx context.Context, sessionID uuid.UUID) ([]SessionSummaryPlacesRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryPlaces, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryPlacesRow
	for rows.Next() {
		var i SessionSummaryPlacesRow
		if err := rows.Scan(&i.TierID, &i.Kind, &i.Available, &i.Held, &i.Sold, &i.SoldUpstream, &i.Unavailable); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryTiers
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryTiers = `-- name: ListSessionSummaryTiers :many
SELECT tt.id, tt.name, tt.price_amount, tt.currency, tt.capacity, tt.is_open,
       tt.sort_order,
       COALESCE(sales.items, 0)   AS paid_items,
       COALESCE(sales.revenue, 0)::bigint AS paid_revenue
FROM   ticket_tiers tt
LEFT JOIN (
    SELECT oi.tier_id, count(*) AS items, sum(oi.total) AS revenue
    FROM   order_items oi
    JOIN   orders o ON o.id = oi.order_id
    WHERE  o.session_id = $1
      AND  o.status IN ('paid', 'partially_refunded', 'refunded')
    GROUP  BY oi.tier_id
) sales ON sales.tier_id = tt.id
WHERE  tt.session_id = $1
  AND  tt.deleted_at IS NULL
ORDER  BY tt.sort_order, tt.name`

// SessionSummaryTierRow is one category with what was paid for it. PaidRevenue
// sums order_items.total (minor units) over paid orders — the price actually
// paid, not today's list price.
type SessionSummaryTierRow struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	PriceAmount int64     `json:"price_amount"`
	Currency    string    `json:"currency"`
	Capacity    *int32    `json:"capacity"`
	IsOpen      bool      `json:"is_open"`
	SortOrder   int32     `json:"sort_order"`
	PaidItems   int64     `json:"paid_items"`
	PaidRevenue int64     `json:"paid_revenue"`
}

// ListSessionSummaryTiers returns the categories of a session with their paid
// item count and revenue.
func (q *Queries) ListSessionSummaryTiers(ctx context.Context, sessionID uuid.UUID) ([]SessionSummaryTierRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryTiers, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryTierRow
	for rows.Next() {
		var i SessionSummaryTierRow
		if err := rows.Scan(&i.ID, &i.Name, &i.PriceAmount, &i.Currency, &i.Capacity, &i.IsOpen,
			&i.SortOrder, &i.PaidItems, &i.PaidRevenue); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryOrders
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryOrders = `-- name: ListSessionSummaryOrders :many
SELECT o.status, o.source, o.currency,
       count(*)                     AS orders,
       COALESCE(sum(o.subtotal), 0)::bigint AS subtotal,
       COALESCE(sum(o.discount), 0)::bigint AS discount,
       COALESCE(sum(o.charge), 0)::bigint   AS charge,
       COALESCE(sum(o.total), 0)::bigint    AS total
FROM   orders o
WHERE  o.session_id = $1
GROUP  BY o.status, o.source, o.currency`

// SessionSummaryOrdersRow totals the orders of one (status, source, currency)
// group. Money is in minor units.
type SessionSummaryOrdersRow struct {
	Status   string `json:"status"`
	Source   string `json:"source"`
	Currency string `json:"currency"`
	Orders   int64  `json:"orders"`
	Subtotal int64  `json:"subtotal"`
	Discount int64  `json:"discount"`
	Charge   int64  `json:"charge"`
	Total    int64  `json:"total"`
}

// ListSessionSummaryOrders returns the order totals of a session.
func (q *Queries) ListSessionSummaryOrders(ctx context.Context, sessionID uuid.UUID) ([]SessionSummaryOrdersRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryOrders, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryOrdersRow
	for rows.Next() {
		var i SessionSummaryOrdersRow
		if err := rows.Scan(&i.Status, &i.Source, &i.Currency, &i.Orders,
			&i.Subtotal, &i.Discount, &i.Charge, &i.Total); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSummaryTickets
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSummaryTickets = `-- name: GetSessionSummaryTickets :one
SELECT count(*) FILTER (WHERE t.status = 'active')      AS active,
       count(*) FILTER (WHERE t.status = 'cancelled')   AS cancelled,
       count(*) FILTER (WHERE t.status = 'transferred') AS transferred,
       count(*) FILTER (WHERE t.status = 'active' AND t.used_at IS NOT NULL) AS used,
       count(*) FILTER (WHERE t.status = 'active' AND t.complimentary_issuance_id IS NOT NULL) AS complimentary
FROM   tickets t
WHERE  t.session_id = $1`

// SessionSummaryTicketsRow counts the tickets of a session. Used and
// Complimentary are subsets of Active.
type SessionSummaryTicketsRow struct {
	Active        int64 `json:"active"`
	Cancelled     int64 `json:"cancelled"`
	Transferred   int64 `json:"transferred"`
	Used          int64 `json:"used"`
	Complimentary int64 `json:"complimentary"`
}

// GetSessionSummaryTickets returns the ticket counters of a session.
func (q *Queries) GetSessionSummaryTickets(ctx context.Context, sessionID uuid.UUID) (SessionSummaryTicketsRow, error) {
	var i SessionSummaryTicketsRow
	err := q.db.QueryRow(ctx, getSessionSummaryTickets, sessionID).Scan(
		&i.Active, &i.Cancelled, &i.Transferred, &i.Used, &i.Complimentary,
	)
	return i, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryRefunds
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryRefunds = `-- name: ListSessionSummaryRefunds :many
SELECT r.settlement, r.state, r.currency,
       count(*)                   AS refunds,
       COALESCE(sum(r.amount), 0)::bigint AS amount
FROM   refunds r
LEFT JOIN orders          ro ON ro.id = r.order_id
LEFT JOIN tickets         rt ON rt.id = r.ticket_id
LEFT JOIN payment_intents pi ON pi.id = r.payment_intent_id
LEFT JOIN orders          po ON po.checkout_session_id = pi.checkout_session_id
WHERE  COALESCE(ro.session_id, rt.session_id, po.session_id) = $1
GROUP  BY r.settlement, r.state, r.currency`

// SessionSummaryRefundsRow totals the refunds of one (settlement, state,
// currency) group. Amount is in minor units.
type SessionSummaryRefundsRow struct {
	Settlement string `json:"settlement"`
	State      string `json:"state"`
	Currency   string `json:"currency"`
	Refunds    int64  `json:"refunds"`
	Amount     int64  `json:"amount"`
}

// ListSessionSummaryRefunds returns the refund totals of a session.
func (q *Queries) ListSessionSummaryRefunds(ctx context.Context, sessionID uuid.UUID) ([]SessionSummaryRefundsRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryRefunds, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryRefundsRow
	for rows.Next() {
		var i SessionSummaryRefundsRow
		if err := rows.Scan(&i.Settlement, &i.State, &i.Currency, &i.Refunds, &i.Amount); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

const listSessionSummaryPromos = `-- name: ListSessionSummaryPromos :many
SELECT p.id AS promo_code_id, p.code, o.currency,
       count(*)::bigint                     AS orders,
       COALESCE(sum(o.discount), 0)::bigint AS discount
FROM   orders o
JOIN   promo_codes p ON p.id = o.promo_code_id
WHERE  o.session_id = $1
  AND  o.status IN ('paid', 'partially_refunded', 'refunded')
GROUP  BY p.id, p.code, o.currency`

// SessionSummaryPromoRow is one promo code's share of a session's paid
// orders: how many used it and the discount they took, in minor units.
type SessionSummaryPromoRow struct {
	PromoCodeID uuid.UUID `json:"promo_code_id"`
	Code        string    `json:"code"`
	Currency    string    `json:"currency"`
	Orders      int64     `json:"orders"`
	Discount    int64     `json:"discount"`
}

// ListSessionSummaryPromos returns the promo codes used by a session's paid
// orders.
func (q *Queries) ListSessionSummaryPromos(ctx context.Context, sessionID uuid.UUID) ([]SessionSummaryPromoRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryPromos, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryPromoRow
	for rows.Next() {
		var i SessionSummaryPromoRow
		if err := rows.Scan(&i.PromoCodeID, &i.Code, &i.Currency, &i.Orders, &i.Discount); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
