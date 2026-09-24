package salesnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func (s *PGStore) SaleByOrder(ctx context.Context, orderID string) (Sale, error) {
	id, err := uuid.Parse(orderID)
	if err != nil {
		return Sale{}, ErrNotFound
	}
	var sale Sale
	var cats []byte
	err = s.pool.QueryRow(ctx, `
		SELECT o.org_id::text, org.name, ev.name,
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
		 WHERE o.id = $1`, id).Scan(&sale.OrgID, &sale.OrgName, &sale.EventName,
		&sale.VenueName, &sale.StartAt, &sale.TimeZone, &sale.OrderNumber, &sale.Source,
		&sale.ChannelName, &sale.Currency, &sale.Total, &sale.PromoCode, &cats)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sale{}, ErrNotFound
	}
	if err != nil {
		return Sale{}, err
	}
	if err := json.Unmarshal(cats, &sale.Categories); err != nil {
		return Sale{}, fmt.Errorf("decode categories: %w", err)
	}
	return sale, nil
}

func (s *PGStore) RefundByTicket(ctx context.Context, ticketID string) (Refund, error) {
	id, err := uuid.Parse(ticketID)
	if err != nil {
		return Refund{}, ErrNotFound
	}
	// The amount is the ticket's own refund_price (written with the
	// cancellation), else a succeeded external refund naming the ticket.
	var r Refund
	err = s.pool.QueryRow(ctx, `
		SELECT ev.org_id::text, org.name, ev.name, COALESCE(v.name, ''), s.start_at, COALESCE(v.timezone, ''),
		       COALESCE(o.system_id, 0), t.system_ticket_id, COALESCE(o.currency, ''),
		       COALESCE(t.refund_price,
		                (SELECT rf.amount FROM refunds rf
		                  WHERE rf.ticket_id = t.id AND rf.state = 'succeeded'
		                  ORDER BY rf.succeeded_at DESC LIMIT 1), 0)
		  FROM tickets t
		  JOIN sessions s ON s.id = t.session_id
		  JOIN events ev ON ev.id = s.event_id
		  JOIN organizations org ON org.id = ev.org_id
		  LEFT JOIN venues v ON v.id = s.venue_id
		  LEFT JOIN orders o ON o.id = t.order_id
		 WHERE t.id = $1`, id).Scan(&r.OrgID, &r.OrgName, &r.EventName, &r.VenueName,
		&r.StartAt, &r.TimeZone, &r.OrderNumber, &r.TicketNumber, &r.Currency, &r.Amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Refund{}, ErrNotFound
	}
	return r, err
}

func (s *PGStore) Claim(ctx context.Context, key string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO sales_notification_deliveries (key) VALUES ($1) ON CONFLICT (key) DO NOTHING`, key)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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
