// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: hand-written (the SQL lives in the constants below only)
//
// Read side of the billing.collect job (billing wave B-A, task B2-2): six
// sources of billable events read from the sales tables, all in ONE row shape
// so the collector treats them alike. Every query is a keyset page over
// (occurred_at, subject_id) with a lower bound on the business time the
// sales-table index serves (migration 0158), ordered the same way, so a
// resumed pass never skips or repeats a row. Nothing here writes.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CollectCandidate is one row a collect source found: a billing fact in the
// making. Currency is the SALE currency ("" when the kind has none) and the
// money fields are in it, nil when they do not apply to the kind.
// VenueCountry is the country of the session's venue, nil when unknown.
// Attributes is a jsonb object (kind-specific facts).
type CollectCandidate struct {
	Kind               string
	SubjectID          uuid.UUID
	OccurredAt         time.Time
	OrgID              uuid.UUID
	EventID            *uuid.UUID
	SessionID          *uuid.UUID
	OrderID            *uuid.UUID
	TicketID           *uuid.UUID
	Seated             *bool
	Currency           string
	UnitPriceMinor     *int64
	DiscountMinor      *int64
	ServiceChargeMinor *int64
	VenueCountry       *string
	Attributes         []byte
}

// Every source below selects exactly these columns, in this order: kind,
// subject_id, occurred_at, org_id, event_id, session_id, order_id, ticket_id,
// seated, currency, unit_price, discount, charge, venue_country, attributes.
func (q *Queries) collectPage(ctx context.Context, query string, args ...any) ([]CollectCandidate, error) {
	rows, err := q.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CollectCandidate
	for rows.Next() {
		var c CollectCandidate
		var cur *string
		if err := rows.Scan(&c.Kind, &c.SubjectID, &c.OccurredAt, &c.OrgID, &c.EventID, &c.SessionID,
			&c.OrderID, &c.TicketID, &c.Seated, &cur, &c.UnitPriceMinor, &c.DiscountMinor,
			&c.ServiceChargeMinor, &c.VenueCountry, &c.Attributes); err != nil {
			return nil, err
		}
		if cur != nil {
			c.Currency = *cur
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const listCollectOrderItems = `-- name: ListCollectOrderItems :many
SELECT CASE WHEN o.source = 'complimentary' THEN 'ticket_complimentary' ELSE 'ticket_sold' END AS kind,
       oi.id AS subject_id, o.paid_at AS occurred_at, o.org_id,
       o.event_id, COALESCE(o.session_id, ss.session_id) AS session_id, o.id AS order_id, oi.ticket_id,
       COALESCE(ss.kind = 'seat', false) AS seated,
       o.currency::text AS currency, oi.unit_price, oi.discount, oi.charge,
       v.country::text AS venue_country,
       jsonb_build_object('order_source', o.source) AS attributes
FROM   orders o
JOIN   order_items oi ON oi.order_id = o.id AND oi.kind = 'ticket'
LEFT   JOIN session_seats ss ON ss.id = oi.session_seat_id
LEFT   JOIN sessions s ON s.id = COALESCE(o.session_id, ss.session_id)
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  o.paid_at IS NOT NULL
  AND  o.status IN ('paid', 'partially_refunded', 'refunded')
  AND  (o.source = 'complimentary') = $4
  AND  o.paid_at >= $1
  AND  (o.paid_at, oi.id) > ($1, $2)
ORDER BY o.paid_at, oi.id
LIMIT  $3`

// ListCollectOrderItems returns the ticket lines of paid orders, a page after
// (afterAt, afterID): ticket_sold lines, or - with complimentary - the
// ticket_complimentary lines of orders whose source is complimentary.
// occurred_at is orders.paid_at.
func (q *Queries) ListCollectOrderItems(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32, complimentary bool) ([]CollectCandidate, error) {
	return q.collectPage(ctx, listCollectOrderItems, afterAt, afterID, limit, complimentary)
}

const listCollectRefunds = `-- name: ListCollectRefunds :many
SELECT 'ticket_refunded'::text AS kind,
       r.id AS subject_id, COALESCE(r.settled_at, r.succeeded_at) AS occurred_at, r.org_id,
       COALESCE(o.event_id, ts.event_id) AS event_id, COALESCE(o.session_id, t.session_id) AS session_id,
       r.order_id, COALESCE(r.ticket_id, r.cancelled_ticket_id) AS ticket_id,
       NULL::boolean AS seated,
       r.currency::text AS currency, NULL::bigint AS unit_price, NULL::bigint AS discount, NULL::bigint AS charge,
       v.country::text AS venue_country,
       jsonb_build_object('refund_amount_minor', r.amount, 'cancel_ticket', r.cancel_ticket,
                          'settlement', r.settlement) AS attributes
FROM   refunds r
LEFT   JOIN orders o ON o.id = r.order_id
LEFT   JOIN tickets t ON t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
LEFT   JOIN sessions ts ON ts.id = t.session_id
LEFT   JOIN sessions s ON s.id = COALESCE(o.session_id, t.session_id)
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  r.state = 'succeeded'
  AND  (r.settled_at >= $1 OR (r.settled_at IS NULL AND r.succeeded_at >= $1))
  AND  (COALESCE(r.settled_at, r.succeeded_at), r.id) > ($1, $2)
ORDER BY COALESCE(r.settled_at, r.succeeded_at), r.id
LIMIT  $3`

// ListCollectRefunds returns succeeded refunds, a page after (afterAt,
// afterID). occurred_at is refunds.settled_at, falling back to succeeded_at
// for a row settled before settled_at existed.
func (q *Queries) ListCollectRefunds(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]CollectCandidate, error) {
	return q.collectPage(ctx, listCollectRefunds, afterAt, afterID, limit)
}

const listCollectCancelledTickets = `-- name: ListCollectCancelledTickets :many
SELECT 'ticket_cancelled'::text AS kind,
       t.id AS subject_id, t.cancelled_at AS occurred_at, e.org_id,
       s.event_id, t.session_id, t.order_id, t.id AS ticket_id,
       NULL::boolean AS seated,
       NULL::text AS currency, NULL::bigint AS unit_price, NULL::bigint AS discount, NULL::bigint AS charge,
       v.country::text AS venue_country,
       jsonb_build_object('status', t.status, 'reason', t.cancellation_reason) AS attributes
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events e ON e.id = s.event_id
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  t.status IN ('cancelled', 'revoked')
  AND  t.cancelled_at IS NOT NULL
  AND  t.refund_id IS NULL
  AND  t.cancelled_at >= $1
  AND  (t.cancelled_at, t.id) > ($1, $2)
ORDER BY t.cancelled_at, t.id
LIMIT  $3`

// ListCollectCancelledTickets returns cancelled or revoked tickets that no
// refund cancelled (a refund that cancels its ticket is one ticket_refunded
// event, not two facts), a page after (afterAt, afterID).
func (q *Queries) ListCollectCancelledTickets(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]CollectCandidate, error) {
	return q.collectPage(ctx, listCollectCancelledTickets, afterAt, afterID, limit)
}

const listCollectSessionCancellations = `-- name: ListCollectSessionCancellations :many
SELECT 'session_cancelled'::text AS kind,
       sc.id AS subject_id, sc.created_at AS occurred_at, sc.org_id,
       sc.event_id, sc.session_id, NULL::uuid AS order_id, NULL::uuid AS ticket_id,
       NULL::boolean AS seated,
       NULL::text AS currency, NULL::bigint AS unit_price, NULL::bigint AS discount, NULL::bigint AS charge,
       v.country::text AS venue_country,
       jsonb_build_object('orders_total', sc.orders_total, 'tickets_total', sc.tickets_total) AS attributes
FROM   session_changes sc
LEFT   JOIN sessions s ON s.id = sc.session_id
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  'cancelled' = ANY (sc.kinds)
  AND  sc.created_at >= $1
  AND  (sc.created_at, sc.id) > ($1, $2)
ORDER BY sc.created_at, sc.id
LIMIT  $3`

// ListCollectSessionCancellations returns the journal rows of cancelled
// sessions, a page after (afterAt, afterID).
func (q *Queries) ListCollectSessionCancellations(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]CollectCandidate, error) {
	return q.collectPage(ctx, listCollectSessionCancellations, afterAt, afterID, limit)
}

const listCollectEventPublications = `-- name: ListCollectEventPublications :many
SELECT 'event_published'::text AS kind,
       e.id AS subject_id, p.first_at AS occurred_at, e.org_id,
       e.id AS event_id, NULL::uuid AS session_id, NULL::uuid AS order_id, NULL::uuid AS ticket_id,
       NULL::boolean AS seated,
       NULL::text AS currency, NULL::bigint AS unit_price, NULL::bigint AS discount, NULL::bigint AS charge,
       fv.country::text AS venue_country,
       '{}'::jsonb AS attributes
FROM   (SELECT event_id, min(published_at) AS first_at
        FROM   event_publications
        GROUP  BY event_id
        HAVING min(published_at) >= $1) p
JOIN   events e ON e.id = p.event_id
LEFT   JOIN LATERAL (SELECT v.country
                     FROM   sessions s
                     JOIN   venues v ON v.id = s.venue_id
                     WHERE  s.event_id = e.id AND s.deleted_at IS NULL
                     ORDER  BY s.start_at
                     LIMIT  1) fv ON true
WHERE  (p.first_at, e.id) > ($1, $2)
ORDER BY p.first_at, e.id
LIMIT  $3`

// ListCollectEventPublications returns one row per event, timed at its
// FIRST publication (the minimum of event_publications.published_at), a page
// after (afterAt, afterID). events has no published_at column of its own.
func (q *Queries) ListCollectEventPublications(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]CollectCandidate, error) {
	return q.collectPage(ctx, listCollectEventPublications, afterAt, afterID, limit)
}

const listCollectComplimentaryTickets = `-- name: ListCollectComplimentaryTickets :many
SELECT 'ticket_complimentary'::text AS kind,
       t.id AS subject_id, t.issued_at AS occurred_at, e.org_id,
       s.event_id, t.session_id, NULL::uuid AS order_id, t.id AS ticket_id,
       COALESCE(t.seat_key NOT LIKE 'ga|%', false) AS seated,
       NULL::text AS currency, NULL::bigint AS unit_price, NULL::bigint AS discount, NULL::bigint AS charge,
       v.country::text AS venue_country,
       '{}'::jsonb AS attributes
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events e ON e.id = s.event_id
LEFT   JOIN venues v ON v.id = s.venue_id
WHERE  t.complimentary_issuance_id IS NOT NULL
  AND  t.order_id IS NULL
  AND  t.issued_at >= $1
  AND  (t.issued_at, t.id) > ($1, $2)
ORDER BY t.issued_at, t.id
LIMIT  $3`

// ListCollectComplimentaryTickets returns tickets an operator issued as
// invitations (a complimentary issuance and no order), a page after
// (afterAt, afterID). A ticket with a seat key that does not start with
// "ga|" is a seated one.
func (q *Queries) ListCollectComplimentaryTickets(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]CollectCandidate, error) {
	return q.collectPage(ctx, listCollectComplimentaryTickets, afterAt, afterID, limit)
}

const getOrgBillingCountry = `-- name: GetOrgBillingCountry :one
SELECT COALESCE(NULLIF(btrim(legal_address_country), ''), NULLIF(btrim(country), ''), '')::text
FROM   organizations
WHERE  id = $1`

// GetOrgBillingCountry returns the country a sale without a venue is routed
// by: the organization's legal_address_country, else its country, else "".
func (q *Queries) GetOrgBillingCountry(ctx context.Context, orgID uuid.UUID) (string, error) {
	var c string
	err := q.db.QueryRow(ctx, getOrgBillingCountry, orgID).Scan(&c)
	return c, err
}
