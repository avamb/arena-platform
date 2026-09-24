package salesnotify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// settleLag keeps the cursor behind rows whose transaction may still be
// committing: paid_at / succeeded_at are stamped inside the transaction, so
// a slow commit could otherwise land a row behind a cursor that already
// moved past it.
const settleLag = "10 seconds"

// PGStore is the production Store.
type PGStore struct{ pool *pgxpool.Pool }

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) Subscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, COALESCE(org_id::text, ''), name, chat_id, on_order_paid, on_ticket_refunded
		  FROM sales_notification_subscriptions
		 WHERE allowed
		 ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.OrgID, &sub.Name, &sub.ChatID, &sub.OnOrderPaid, &sub.OnTicketRefunded); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *PGStore) SalesSince(ctx context.Context, ts time.Time, id string, limit int) ([]Sale, error) {
	// Every order that was ever paid counts, even if it is already refunded
	// by the time the job looks — the sale did happen.
	rows, err := s.pool.Query(ctx, `
		SELECT o.id::text, o.paid_at, o.org_id::text, org.name, ev.name,
		       COALESCE(v.name, ''), s.start_at, COALESCE(v.timezone, ''),
		       o.system_id, o.source, COALESCE(ch.name, ''), o.currency, o.total,
		       COALESCE(pc.code, ''),
		       COALESCE((SELECT json_agg(json_build_object('name', x.name, 'n', x.n) ORDER BY x.n DESC, x.name)
		                   FROM (SELECT tt.name, count(*)::int AS n
		                           FROM order_items oi
		                           JOIN ticket_tiers tt ON tt.id = oi.tier_id
		                          WHERE oi.order_id = o.id
		                          GROUP BY tt.name) x), '[]'::json)
		  FROM orders o
		  JOIN organizations org ON org.id = o.org_id
		  JOIN events ev ON ev.id = o.event_id
		  JOIN sessions s ON s.id = o.session_id
		  LEFT JOIN venues v ON v.id = s.venue_id
		  LEFT JOIN sales_channels ch ON ch.id = o.channel_id
		  LEFT JOIN promo_codes pc ON pc.id = o.promo_code_id
		 WHERE o.paid_at IS NOT NULL
		   AND o.paid_at <= now() - interval '`+settleLag+`'
		   AND (o.paid_at > $1 OR (o.paid_at = $1 AND o.id::text > $2))
		 ORDER BY o.paid_at, o.id
		 LIMIT $3`, ts, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sale
	for rows.Next() {
		var sale Sale
		var cats []byte
		if err := rows.Scan(&sale.ID, &sale.At, &sale.OrgID, &sale.OrgName, &sale.EventName,
			&sale.VenueName, &sale.StartAt, &sale.TimeZone, &sale.OrderNumber, &sale.Source,
			&sale.ChannelName, &sale.Currency, &sale.Total, &sale.PromoCode, &cats); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cats, &sale.Categories); err != nil {
			return nil, fmt.Errorf("decode categories: %w", err)
		}
		out = append(out, sale)
	}
	return out, rows.Err()
}

func (s *PGStore) RefundsSince(ctx context.Context, ts time.Time, id string, limit int) ([]Refund, error) {
	// An external refund (a selling site's REFUND_TICKET) names its order and
	// ticket; a provider refund only its payment intent, whose checkout
	// session identifies the order.
	rows, err := s.pool.Query(ctx, `
		SELECT r.id::text, r.succeeded_at, r.org_id::text, org.name,
		       COALESCE(ev.name, ''), COALESCE(v.name, ''), s.start_at, COALESCE(v.timezone, ''),
		       COALESCE(o.system_id, 0), COALESCE(t.system_ticket_id, 0), r.currency, r.amount
		  FROM refunds r
		  JOIN organizations org ON org.id = r.org_id
		  LEFT JOIN tickets t ON t.id = r.ticket_id
		  LEFT JOIN payment_intents pi ON pi.id = r.payment_intent_id
		  LEFT JOIN orders o ON o.id = COALESCE(r.order_id, t.order_id,
		        (SELECT o2.id FROM orders o2 WHERE o2.checkout_session_id = pi.checkout_session_id))
		  LEFT JOIN events ev ON ev.id = o.event_id
		  LEFT JOIN sessions s ON s.id = o.session_id
		  LEFT JOIN venues v ON v.id = s.venue_id
		 WHERE r.state = 'succeeded'
		   AND r.succeeded_at IS NOT NULL
		   AND r.succeeded_at <= now() - interval '`+settleLag+`'
		   AND (r.succeeded_at > $1 OR (r.succeeded_at = $1 AND r.id::text > $2))
		 ORDER BY r.succeeded_at, r.id
		 LIMIT $3`, ts, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Refund
	for rows.Next() {
		var r Refund
		if err := rows.Scan(&r.ID, &r.At, &r.OrgID, &r.OrgName, &r.EventName, &r.VenueName,
			&r.StartAt, &r.TimeZone, &r.OrderNumber, &r.TicketNumber, &r.Currency, &r.Amount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PGStore) UpdateChatID(ctx context.Context, subscriptionID, chatID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sales_notification_subscriptions SET chat_id = $2, updated_at = now() WHERE id = $1`,
		subscriptionID, chatID)
	return err
}

func (s *PGStore) RecordDelivery(ctx context.Context, subscriptionID, lastError string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sales_notification_subscriptions
		   SET last_error = NULLIF($2, ''), updated_at = now()
		 WHERE id = $1 AND last_error IS DISTINCT FROM NULLIF($2, '')`,
		subscriptionID, lastError)
	return err
}

// PGScheduler enqueues the next run into worker_jobs.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)`, JobType, at)
	return err
}

// ScheduleInitialJob enqueues the first run unless one is already queued.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var n int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`,
		JobType).Scan(&n); err != nil {
		return fmt.Errorf("salesnotify: check initial job: %w", err)
	}
	if n > 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobType)
	return err
}
