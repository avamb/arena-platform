// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: sales_funnel.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const resolveFunnelTargetByFeedToken = `SELECT e.id, e.org_id,
       (
           SELECT v.timezone
           FROM   sessions s
           JOIN   venues v ON v.id = s.venue_id
           WHERE  s.event_id   = e.id
             AND  s.deleted_at IS NULL
             AND  s.status    <> 'cancelled'
           ORDER BY s.start_at ASC
           LIMIT 1
       ) AS timezone
FROM events e
JOIN event_publications ep ON ep.event_id = e.id
JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
WHERE ft.token     = $1
  AND ft.is_active = true
  AND e.id         = $2
  AND e.status     = 'published'
  AND e.deleted_at IS NULL
LIMIT 1`

// ResolveFunnelTargetByFeedToken finds the event a widget names (published,
// not deleted, actively published to the active feed token). pgx.ErrNoRows
// means the token or the event is not eligible.
func (q *Queries) ResolveFunnelTargetByFeedToken(ctx context.Context, feedToken string, eventID uuid.UUID) (PageTrackTarget, error) {
	var t PageTrackTarget
	err := q.db.QueryRow(ctx, resolveFunnelTargetByFeedToken, feedToken, eventID).Scan(&t.EventID, &t.OrgID, &t.Timezone)
	return t, err
}

const funnelSessionBelongsToEvent = `SELECT EXISTS (
    SELECT 1 FROM sessions s
    WHERE s.id = $2 AND s.event_id = $1 AND s.deleted_at IS NULL
)`

// FunnelSessionBelongsToEvent says whether the session is a live session of
// the event.
func (q *Queries) FunnelSessionBelongsToEvent(ctx context.Context, eventID, sessionID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, funnelSessionBelongsToEvent, eventID, sessionID).Scan(&ok)
	return ok, err
}

const recordSalesFunnelHit = `WITH pair AS (
    SELECT (
               ($6::text = '' AND $7::text = '')
               OR EXISTS (
                   SELECT 1 FROM sales_funnel_daily
                   WHERE event_id = $1 AND day = $4 AND utm_source = $6::text AND utm_campaign = $7::text
               )
               OR (
                   SELECT count(DISTINCT (utm_source, utm_campaign))
                   FROM   sales_funnel_daily
                   WHERE  event_id = $1 AND day = $4
                     AND (utm_source <> '' OR utm_campaign <> '')
               ) < $10::bigint
           ) AS keep
)
INSERT INTO sales_funnel_daily (org_id, event_id, session_id, day, step, surface, utm_source, utm_campaign, hits, suspect_hits)
SELECT $2, $1, $3::uuid, $4, $5::text, $11::text,
       CASE WHEN keep THEN $6::text ELSE '' END,
       CASE WHEN keep THEN $7::text ELSE '' END,
       $8::bigint, $9::bigint
FROM pair
ON CONFLICT (event_id, day, step, surface, session_key, utm_source, utm_campaign) DO UPDATE
   SET hits         = sales_funnel_daily.hits         + EXCLUDED.hits,
       suspect_hits = sales_funnel_daily.suspect_hits + EXCLUDED.suspect_hits`

// RecordSalesFunnelHitParams is one funnel beacon hit. SessionID is nil for
// the steps taken before a session is chosen. Suspect puts the hit in
// suspect_hits instead of hits. MaxUTMPairs bounds the different utm pairs one
// event keeps per day (see the SQL comment).
type RecordSalesFunnelHitParams struct {
	EventID     uuid.UUID
	OrgID       uuid.UUID
	SessionID   *uuid.UUID
	Day         time.Time
	Step        string
	Surface     string
	UTMSource   string
	UTMCampaign string
	Suspect     bool
	MaxUTMPairs int64
}

// RecordSalesFunnelHit adds one hit to the event's funnel counter for the day.
func (q *Queries) RecordSalesFunnelHit(ctx context.Context, p RecordSalesFunnelHitParams) error {
	var hits, suspect int64 = 1, 0
	if p.Suspect {
		hits, suspect = 0, 1
	}
	_, err := q.db.Exec(ctx, recordSalesFunnelHit,
		p.EventID, p.OrgID, p.SessionID, p.Day, p.Step, p.UTMSource, p.UTMCampaign,
		hits, suspect, p.MaxUTMPairs, p.Surface)
	return err
}

const listSalesFunnelClientRows = `SELECT step, utm_source, utm_campaign, sum(hits)::bigint, sum(suspect_hits)::bigint
FROM   sales_funnel_daily
WHERE  event_id = $1 AND day BETWEEN $2 AND $3
  AND  ($4::uuid IS NULL OR (step <> 'view' AND session_id = $4::uuid))
GROUP BY step, utm_source, utm_campaign`

// SalesFunnelClientRow is the sum of one client step for one utm pair.
type SalesFunnelClientRow struct {
	Step        string
	UTMSource   string
	UTMCampaign string
	Hits        int64
	SuspectHits int64
}

// ListSalesFunnelClientRows sums the client steps of the event over [from, to].
// A non-nil sessionID reads only the steps taken inside that session.
func (q *Queries) ListSalesFunnelClientRows(ctx context.Context, eventID uuid.UUID, from, to time.Time, sessionID *uuid.UUID) ([]SalesFunnelClientRow, error) {
	rows, err := q.db.Query(ctx, listSalesFunnelClientRows, eventID, from, to, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SalesFunnelClientRow
	for rows.Next() {
		var r SalesFunnelClientRow
		if err := rows.Scan(&r.Step, &r.UTMSource, &r.UTMCampaign, &r.Hits, &r.SuspectHits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listEventFunnelSessions = `SELECT s.id, s.start_at, s.status, v.timezone
FROM      sessions s
LEFT JOIN venues   v ON v.id = s.venue_id
WHERE  s.event_id = $1
  AND  s.deleted_at IS NULL
ORDER  BY s.start_at, s.id`

// EventFunnelSession is a live session of an event with the IANA zone of its
// venue (nil when the venue has none on record).
type EventFunnelSession struct {
	ID       uuid.UUID
	StartAt  time.Time
	Status   string
	Timezone *string
}

// ListEventFunnelSessions lists the live sessions of the event in start order.
func (q *Queries) ListEventFunnelSessions(ctx context.Context, eventID uuid.UUID) ([]EventFunnelSession, error) {
	rows, err := q.db.Query(ctx, listEventFunnelSessions, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventFunnelSession
	for rows.Next() {
		var s EventFunnelSession
		if err := rows.Scan(&s.ID, &s.StartAt, &s.Status, &s.Timezone); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const listFunnelServerRows = `WITH x AS (
    SELECT COALESCE(cs.attribution ->> 'utm_source', '')   AS utm_source,
           COALESCE(cs.attribution ->> 'utm_campaign', '') AS utm_campaign,
           o.sales_link_id,
           o.currency::text AS currency,
           COALESCE(o.customer_id::text, lower(btrim(o.buyer_email)), o.id::text)
               || '|' || o.session_id::text
               || '|' || ((o.created_at AT TIME ZONE $5::text)::date)::text AS buyer,
           (o.status IN ('paid', 'partially_refunded', 'refunded')) AS paid,
           EXISTS (SELECT 1 FROM payment_intents pi
                   WHERE pi.checkout_session_id = o.checkout_session_id) AS has_intent,
           o.total - rf.amount AS net
    FROM   orders o
    JOIN   checkout_sessions cs ON cs.id = o.checkout_session_id
    LEFT JOIN LATERAL (
        SELECT COALESCE(sum(r.amount), 0)::bigint AS amount
        FROM  (
                  SELECT r1.id FROM refunds r1 WHERE r1.order_id = o.id
                  UNION
                  SELECT r2.id FROM refunds r2 JOIN tickets t ON t.id = r2.ticket_id WHERE t.order_id = o.id
                  UNION
                  SELECT r3.id FROM refunds r3 JOIN payment_intents p3 ON p3.id = r3.payment_intent_id
                  WHERE  p3.checkout_session_id = o.checkout_session_id
              ) cand
        JOIN   refunds r ON r.id = cand.id
        WHERE  r.state = 'succeeded'
    ) rf ON true
    WHERE  o.event_id = $1 AND o.org_id = $2
      AND  o.source <> 'complimentary'
      AND  o.created_at >= $3::date - 1 AND o.created_at < $4::date + 2
      AND  (o.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
      AND  ($6::uuid IS NULL OR o.session_id = $6::uuid)
      AND  NOT EXISTS (
               SELECT 1 FROM sales_channels sc
               WHERE  sc.id = o.channel_id
                 AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
                       OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
           )
)
SELECT x.utm_source, x.utm_campaign, x.sales_link_id,
       sl.code  AS sales_link_code,
       sl.label AS sales_link_label,
       x.currency,
       count(DISTINCT x.buyer)::bigint                                  AS started,
       (count(DISTINCT x.buyer) FILTER (WHERE x.paid OR x.has_intent))::bigint AS payment_open,
       (count(DISTINCT x.buyer) FILTER (WHERE x.paid))::bigint          AS paid,
       COALESCE(sum(x.net) FILTER (WHERE x.paid), 0)::bigint            AS revenue
FROM   x
LEFT JOIN sales_links sl ON sl.id = x.sales_link_id
GROUP BY 1, 2, 3, 4, 5, 6`

// FunnelServerRow is the server-side steps of the orders of one source: a utm
// pair, a sales link and a currency. The steps count buyers (a buyer who
// retried checkout is one), Revenue is in minor units, net of succeeded refunds.
type FunnelServerRow struct {
	UTMSource      string
	UTMCampaign    string
	SalesLinkID    *uuid.UUID
	SalesLinkCode  *string
	SalesLinkLabel *string
	Currency       string
	Started        int64
	PaymentOpen    int64
	Paid           int64
	Revenue        int64
}

// FunnelWindow is the window every server-side funnel read uses: the event and
// organization, the first and last day, the venue's IANA zone (the day of an
// order is its creation day there) and an optional session.
type FunnelWindow struct {
	EventID   uuid.UUID
	OrgID     uuid.UUID
	From      time.Time
	To        time.Time
	Zone      string
	SessionID *uuid.UUID
}

// ListFunnelServerRows counts started, payment-opened and paid buyers of the
// non-gateway channels of the event, per source.
func (q *Queries) ListFunnelServerRows(ctx context.Context, w FunnelWindow) ([]FunnelServerRow, error) {
	rows, err := q.db.Query(ctx, listFunnelServerRows, w.EventID, w.OrgID, w.From, w.To, w.Zone, w.SessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FunnelServerRow
	for rows.Next() {
		var r FunnelServerRow
		if err := rows.Scan(&r.UTMSource, &r.UTMCampaign, &r.SalesLinkID, &r.SalesLinkCode, &r.SalesLinkLabel,
			&r.Currency, &r.Started, &r.PaymentOpen, &r.Paid, &r.Revenue); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getFunnelServerTotals = `WITH x AS (
    SELECT COALESCE(o.customer_id::text, lower(btrim(o.buyer_email)), o.id::text)
               || '|' || o.session_id::text
               || '|' || ((o.created_at AT TIME ZONE $5::text)::date)::text AS buyer,
           (o.status IN ('paid', 'partially_refunded', 'refunded')) AS paid,
           EXISTS (SELECT 1 FROM payment_intents pi
                   WHERE pi.checkout_session_id = o.checkout_session_id) AS has_intent
    FROM   orders o
    WHERE  o.event_id = $1 AND o.org_id = $2
      AND  o.source <> 'complimentary'
      AND  o.created_at >= $3::date - 1 AND o.created_at < $4::date + 2
      AND  (o.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
      AND  ($6::uuid IS NULL OR o.session_id = $6::uuid)
      AND  NOT EXISTS (
               SELECT 1 FROM sales_channels sc
               WHERE  sc.id = o.channel_id
                 AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
                       OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
           )
)
SELECT count(DISTINCT buyer)::bigint,
       (count(DISTINCT buyer) FILTER (WHERE paid OR has_intent))::bigint,
       (count(DISTINCT buyer) FILTER (WHERE paid))::bigint
FROM   x`

// FunnelServerTotals are the buyers of the window counted once, whatever the
// source, link or currency they appear under.
type FunnelServerTotals struct {
	Started     int64
	PaymentOpen int64
	Paid        int64
}

// GetFunnelServerTotals counts the distinct buyers of the whole window.
func (q *Queries) GetFunnelServerTotals(ctx context.Context, w FunnelWindow) (FunnelServerTotals, error) {
	var t FunnelServerTotals
	err := q.db.QueryRow(ctx, getFunnelServerTotals, w.EventID, w.OrgID, w.From, w.To, w.Zone, w.SessionID).
		Scan(&t.Started, &t.PaymentOpen, &t.Paid)
	return t, err
}

const countFunnelGatewayCarts = `SELECT count(*)::bigint
FROM   reservations r
JOIN   sessions s        ON s.id = r.session_id
JOIN   sales_channels sc ON sc.id = r.channel_id
WHERE  s.event_id = $1 AND r.org_id = $2
  AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
        OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
  AND  r.created_at >= $3::date - 1 AND r.created_at < $4::date + 2
  AND  (r.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
  AND  ($6::uuid IS NULL OR r.session_id = $6::uuid)`

// CountFunnelGatewayCarts counts the carts taken through channels that carry a
// gateway credential.
func (q *Queries) CountFunnelGatewayCarts(ctx context.Context, w FunnelWindow) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countFunnelGatewayCarts, w.EventID, w.OrgID, w.From, w.To, w.Zone, w.SessionID).Scan(&n)
	return n, err
}

const listFunnelGatewayOrders = `SELECT o.currency::text AS currency,
       count(*)::bigint AS started,
       (count(*) FILTER (WHERE o.status IN ('paid', 'partially_refunded', 'refunded')))::bigint AS paid,
       COALESCE(sum(o.total - rf.amount) FILTER (WHERE o.status IN ('paid', 'partially_refunded', 'refunded')), 0)::bigint AS revenue
FROM   orders o
JOIN   sales_channels sc ON sc.id = o.channel_id
LEFT JOIN LATERAL (
    SELECT COALESCE(sum(r.amount), 0)::bigint AS amount
    FROM  (
              SELECT r1.id FROM refunds r1 WHERE r1.order_id = o.id
              UNION
              SELECT r2.id FROM refunds r2 JOIN tickets t ON t.id = r2.ticket_id WHERE t.order_id = o.id
              UNION
              SELECT r3.id FROM refunds r3 JOIN payment_intents p3 ON p3.id = r3.payment_intent_id
              WHERE  p3.checkout_session_id = o.checkout_session_id
          ) cand
    JOIN   refunds r ON r.id = cand.id
    WHERE  r.state = 'succeeded'
) rf ON true
WHERE  o.event_id = $1 AND o.org_id = $2
  AND  o.source <> 'complimentary'
  AND  (sc.settings #>> '{gateway,token_hash}' IS NOT NULL
        OR sc.settings ->> 'gateway_token_hash' IS NOT NULL)
  AND  o.created_at >= $3::date - 1 AND o.created_at < $4::date + 2
  AND  (o.created_at AT TIME ZONE $5::text)::date BETWEEN $3 AND $4
  AND  ($6::uuid IS NULL OR o.session_id = $6::uuid)
GROUP BY o.currency`

// FunnelGatewayOrderRow is the orders of the gateway channels in one currency.
type FunnelGatewayOrderRow struct {
	Currency string
	Started  int64
	Paid     int64
	Revenue  int64
}

// ListFunnelGatewayOrders counts the orders of the gateway channels per currency.
func (q *Queries) ListFunnelGatewayOrders(ctx context.Context, w FunnelWindow) ([]FunnelGatewayOrderRow, error) {
	rows, err := q.db.Query(ctx, listFunnelGatewayOrders, w.EventID, w.OrgID, w.From, w.To, w.Zone, w.SessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FunnelGatewayOrderRow
	for rows.Next() {
		var r FunnelGatewayOrderRow
		if err := rows.Scan(&r.Currency, &r.Started, &r.Paid, &r.Revenue); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
