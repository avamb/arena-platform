// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: session_summary.sql
//
// Every aggregate here runs over a SET of session ids and tags its rows with
// the session they belong to: the session summary passes one id, the event
// summary (EC-06) every session of the event, and horders folds both with
// the same assembly.

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
// GetEventSummaryHeader
// ─────────────────────────────────────────────────────────────────────────────

const getEventSummaryHeader = `-- name: GetEventSummaryHeader :one
SELECT e.id, e.org_id, e.name, e.status, e.first_session_at, e.last_session_at
FROM   events e
WHERE  e.id = $1
  AND  e.org_id = $2
  AND  e.deleted_at IS NULL`

// EventSummaryHeaderRow names the event an event summary is about.
type EventSummaryHeaderRow struct {
	ID             uuid.UUID  `json:"id"`
	OrgID          uuid.UUID  `json:"org_id"`
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	FirstSessionAt *time.Time `json:"first_session_at"`
	LastSessionAt  *time.Time `json:"last_session_at"`
}

// GetEventSummaryHeader returns the event scoped to the organization.
// pgx.ErrNoRows means the event does not exist in that organization.
func (q *Queries) GetEventSummaryHeader(ctx context.Context, eventID, orgID uuid.UUID) (EventSummaryHeaderRow, error) {
	var i EventSummaryHeaderRow
	err := q.db.QueryRow(ctx, getEventSummaryHeader, eventID, orgID).Scan(
		&i.ID, &i.OrgID, &i.Name, &i.Status, &i.FirstSessionAt, &i.LastSessionAt,
	)
	return i, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListEventSummarySessions
// ─────────────────────────────────────────────────────────────────────────────

const listEventSummarySessions = `-- name: ListEventSummarySessions :many
SELECT s.id, s.start_at, s.end_at, s.status, s.capacity_total,
       s.seating_plan_version_id, v.name AS venue_name, v.timezone AS venue_timezone
FROM      sessions s
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.event_id = $1
  AND  s.deleted_at IS NULL
ORDER  BY s.start_at, s.id`

// EventSummarySessionRow is one session of an event summary, cancelled ones
// included (a cancelled session may carry refunded orders).
type EventSummarySessionRow struct {
	ID                   uuid.UUID  `json:"id"`
	StartAt              time.Time  `json:"start_at"`
	EndAt                time.Time  `json:"end_at"`
	Status               string     `json:"status"`
	CapacityTotal        int32      `json:"capacity_total"`
	SeatingPlanVersionID *uuid.UUID `json:"seating_plan_version_id"`
	VenueName            *string    `json:"venue_name"`
	VenueTimezone        *string    `json:"venue_timezone"`
}

// ListEventSummarySessions returns every non-deleted session of an event in
// start order.
func (q *Queries) ListEventSummarySessions(ctx context.Context, eventID uuid.UUID) ([]EventSummarySessionRow, error) {
	rows, err := q.db.Query(ctx, listEventSummarySessions, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []EventSummarySessionRow
	for rows.Next() {
		var i EventSummarySessionRow
		if err := rows.Scan(&i.ID, &i.StartAt, &i.EndAt, &i.Status, &i.CapacityTotal,
			&i.SeatingPlanVersionID, &i.VenueName, &i.VenueTimezone); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryPlaces
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryPlaces = `-- name: ListSessionSummaryPlaces :many
SELECT ss.session_id, ss.tier_id, ss.kind,
       count(*) FILTER (WHERE ss.status = 'available')   AS available,
       count(*) FILTER (WHERE ss.status = 'held')        AS held,
       count(*) FILTER (WHERE ss.status = 'sold')        AS sold,
       count(*) FILTER (WHERE ss.status = 'sold' AND ss.reservation_id IS NULL) AS sold_upstream,
       count(*) FILTER (WHERE ss.status = 'unavailable') AS unavailable
FROM   session_seats ss
WHERE  ss.session_id = ANY($1::uuid[])
GROUP  BY ss.session_id, ss.tier_id, ss.kind`

// SessionSummaryPlacesRow counts the places of one (session, category, kind)
// group by status. SoldUpstream is the part of Sold that was sold in the
// system the session was imported from — no ticket or order stands behind it.
type SessionSummaryPlacesRow struct {
	SessionID    uuid.UUID  `json:"session_id"`
	TierID       *uuid.UUID `json:"tier_id"`
	Kind         string     `json:"kind"`
	Available    int64      `json:"available"`
	Held         int64      `json:"held"`
	Sold         int64      `json:"sold"`
	SoldUpstream int64      `json:"sold_upstream"`
	Unavailable  int64      `json:"unavailable"`
}

// ListSessionSummaryPlaces returns the place counters of the given sessions.
func (q *Queries) ListSessionSummaryPlaces(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryPlacesRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryPlaces, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryPlacesRow
	for rows.Next() {
		var i SessionSummaryPlacesRow
		if err := rows.Scan(&i.SessionID, &i.TierID, &i.Kind, &i.Available, &i.Held, &i.Sold, &i.SoldUpstream, &i.Unavailable); err != nil {
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
SELECT tt.session_id, tt.id, tt.name, tt.price_amount, tt.currency, tt.capacity,
       tt.is_open, tt.sort_order,
       COALESCE(sales.items, 0)   AS paid_items,
       COALESCE(sales.revenue, 0)::bigint AS paid_revenue
FROM   ticket_tiers tt
LEFT JOIN (
    SELECT oi.tier_id, count(*) AS items, sum(oi.total) AS revenue
    FROM   order_items oi
    JOIN   orders o ON o.id = oi.order_id
    WHERE  o.session_id = ANY($1::uuid[])
      AND  o.status IN ('paid', 'partially_refunded', 'refunded')
    GROUP  BY oi.tier_id
) sales ON sales.tier_id = tt.id
WHERE  tt.session_id = ANY($1::uuid[])
  AND  tt.deleted_at IS NULL
ORDER  BY tt.session_id, tt.sort_order, tt.name`

// SessionSummaryTierRow is one category with what was paid for it. PaidRevenue
// sums order_items.total (minor units) over paid orders — the price actually
// paid, not today's list price.
type SessionSummaryTierRow struct {
	SessionID   uuid.UUID `json:"session_id"`
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

// ListSessionSummaryTiers returns the categories of the given sessions with
// their paid item count and revenue.
func (q *Queries) ListSessionSummaryTiers(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryTierRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryTiers, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryTierRow
	for rows.Next() {
		var i SessionSummaryTierRow
		if err := rows.Scan(&i.SessionID, &i.ID, &i.Name, &i.PriceAmount, &i.Currency, &i.Capacity,
			&i.IsOpen, &i.SortOrder, &i.PaidItems, &i.PaidRevenue); err != nil {
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
SELECT o.session_id, o.status, o.source, o.currency,
       count(*)                     AS orders,
       COALESCE(sum(o.subtotal), 0)::bigint AS subtotal,
       COALESCE(sum(o.discount), 0)::bigint AS discount,
       COALESCE(sum(o.charge), 0)::bigint   AS charge,
       COALESCE(sum(o.total), 0)::bigint    AS total
FROM   orders o
WHERE  o.session_id = ANY($1::uuid[])
GROUP  BY o.session_id, o.status, o.source, o.currency`

// SessionSummaryOrdersRow totals the orders of one (session, status, source,
// currency) group. Money is in minor units.
type SessionSummaryOrdersRow struct {
	SessionID uuid.UUID `json:"session_id"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	Currency  string    `json:"currency"`
	Orders    int64     `json:"orders"`
	Subtotal  int64     `json:"subtotal"`
	Discount  int64     `json:"discount"`
	Charge    int64     `json:"charge"`
	Total     int64     `json:"total"`
}

// ListSessionSummaryOrders returns the order totals of the given sessions.
func (q *Queries) ListSessionSummaryOrders(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryOrdersRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryOrders, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryOrdersRow
	for rows.Next() {
		var i SessionSummaryOrdersRow
		if err := rows.Scan(&i.SessionID, &i.Status, &i.Source, &i.Currency, &i.Orders,
			&i.Subtotal, &i.Discount, &i.Charge, &i.Total); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryTickets
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryTickets = `-- name: ListSessionSummaryTickets :many
SELECT t.session_id,
       count(*) FILTER (WHERE t.status = 'active')      AS active,
       count(*) FILTER (WHERE t.status = 'cancelled')   AS cancelled,
       count(*) FILTER (WHERE t.status = 'transferred') AS transferred,
       count(*) FILTER (WHERE t.status = 'active' AND t.used_at IS NOT NULL) AS used,
       count(*) FILTER (WHERE t.status = 'active' AND t.complimentary_issuance_id IS NOT NULL) AS complimentary
FROM   tickets t
WHERE  t.session_id = ANY($1::uuid[])
GROUP  BY t.session_id`

// SessionSummaryTicketsRow counts the tickets of one session. Used and
// Complimentary are subsets of Active. A session without tickets has no row.
type SessionSummaryTicketsRow struct {
	SessionID     uuid.UUID `json:"session_id"`
	Active        int64     `json:"active"`
	Cancelled     int64     `json:"cancelled"`
	Transferred   int64     `json:"transferred"`
	Used          int64     `json:"used"`
	Complimentary int64     `json:"complimentary"`
}

// ListSessionSummaryTickets returns the ticket counters of the given sessions.
func (q *Queries) ListSessionSummaryTickets(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryTicketsRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryTickets, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryTicketsRow
	for rows.Next() {
		var i SessionSummaryTicketsRow
		if err := rows.Scan(&i.SessionID, &i.Active, &i.Cancelled, &i.Transferred, &i.Used, &i.Complimentary); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryRefunds
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryRefunds = `-- name: ListSessionSummaryRefunds :many
SELECT COALESCE(ro.session_id, rt.session_id, po.session_id) AS session_id,
       r.settlement, r.state, r.currency,
       count(*)                   AS refunds,
       COALESCE(sum(r.amount), 0)::bigint AS amount
FROM   refunds r
LEFT JOIN orders          ro ON ro.id = r.order_id
LEFT JOIN tickets         rt ON rt.id = r.ticket_id
LEFT JOIN payment_intents pi ON pi.id = r.payment_intent_id
LEFT JOIN orders          po ON po.checkout_session_id = pi.checkout_session_id
WHERE  COALESCE(ro.session_id, rt.session_id, po.session_id) = ANY($1::uuid[])
GROUP  BY COALESCE(ro.session_id, rt.session_id, po.session_id), r.settlement, r.state, r.currency`

// SessionSummaryRefundsRow totals the refunds of one (session, settlement,
// state, currency) group. Amount is in minor units.
type SessionSummaryRefundsRow struct {
	SessionID  uuid.UUID `json:"session_id"`
	Settlement string    `json:"settlement"`
	State      string    `json:"state"`
	Currency   string    `json:"currency"`
	Refunds    int64     `json:"refunds"`
	Amount     int64     `json:"amount"`
}

// ListSessionSummaryRefunds returns the refund totals of the given sessions.
func (q *Queries) ListSessionSummaryRefunds(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryRefundsRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryRefunds, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryRefundsRow
	for rows.Next() {
		var i SessionSummaryRefundsRow
		if err := rows.Scan(&i.SessionID, &i.Settlement, &i.State, &i.Currency, &i.Refunds, &i.Amount); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryPromos
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryPromos = `-- name: ListSessionSummaryPromos :many
SELECT o.session_id, p.id AS promo_code_id, p.code, o.currency,
       count(*)::bigint                     AS orders,
       count(r.id)::bigint                  AS redemptions,
       COALESCE(sum(o.discount), 0)::bigint AS discount
FROM   orders o
JOIN   promo_codes p ON p.id = o.promo_code_id
LEFT JOIN promo_code_redemptions r ON r.order_id = o.id AND r.promo_code_id = p.id
WHERE  o.session_id = ANY($1::uuid[])
  AND  o.status IN ('paid', 'partially_refunded', 'refunded')
GROUP  BY o.session_id, p.id, p.code, o.currency`

// SessionSummaryPromoRow is one promo code's share of a session's paid
// orders: how many used it, how many of those carry a recorded redemption,
// and the discount they took, in minor units.
type SessionSummaryPromoRow struct {
	SessionID   uuid.UUID `json:"session_id"`
	PromoCodeID uuid.UUID `json:"promo_code_id"`
	Code        string    `json:"code"`
	Currency    string    `json:"currency"`
	Orders      int64     `json:"orders"`
	Redemptions int64     `json:"redemptions"`
	Discount    int64     `json:"discount"`
}

// ListSessionSummaryPromos returns the promo codes used by the paid orders of
// the given sessions.
func (q *Queries) ListSessionSummaryPromos(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryPromoRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryPromos, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryPromoRow
	for rows.Next() {
		var i SessionSummaryPromoRow
		if err := rows.Scan(&i.SessionID, &i.PromoCodeID, &i.Code, &i.Currency, &i.Orders, &i.Redemptions, &i.Discount); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSummaryComplimentary
// ─────────────────────────────────────────────────────────────────────────────

const listSessionSummaryComplimentary = `-- name: ListSessionSummaryComplimentary :many
SELECT sid.id AS session_id,
       (SELECT count(*)
          FROM   orders o
          WHERE  o.session_id = sid.id
            AND  o.source = 'complimentary'
            AND  o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders,
       (SELECT count(*)
          FROM   tickets t
          LEFT JOIN orders o ON o.id = t.order_id
          WHERE  t.session_id = sid.id
            AND  t.status = 'active'
            AND  (t.complimentary_issuance_id IS NOT NULL OR o.source = 'complimentary'))::bigint AS tickets
FROM   unnest($1::uuid[]) AS sid(id)`

// SessionSummaryComplimentaryRow counts the invitations of one session:
// paid orders written with source='complimentary' and the active tickets of
// either invitation path (such orders, or the admin complimentary flow).
type SessionSummaryComplimentaryRow struct {
	SessionID uuid.UUID `json:"session_id"`
	Orders    int64     `json:"orders"`
	Tickets   int64     `json:"tickets"`
}

// ListSessionSummaryComplimentary returns one row per requested session.
func (q *Queries) ListSessionSummaryComplimentary(ctx context.Context, sessionIDs []uuid.UUID) ([]SessionSummaryComplimentaryRow, error) {
	rows, err := q.db.Query(ctx, listSessionSummaryComplimentary, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SessionSummaryComplimentaryRow
	for rows.Next() {
		var i SessionSummaryComplimentaryRow
		if err := rows.Scan(&i.SessionID, &i.Orders, &i.Tickets); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
