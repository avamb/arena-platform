package salesnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// PGStore is the production Store.
type PGStore struct{ pool *pgxpool.Pool }

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (s *PGStore) Subscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, COALESCE(org_id::text, ''), name, chat_id, on_order_paid, on_ticket_refunded, on_event_changes, kinds
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
		if err := rows.Scan(&sub.ID, &sub.OrgID, &sub.Name, &sub.ChatID, &sub.OnOrderPaid, &sub.OnTicketRefunded, &sub.OnEventChanges, &sub.Kinds); err != nil {
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
		                          GROUP BY tt.name) x), '[]'::json),
		       COALESCE(NULLIF(o.buyer_name, ''), c.display_name, ''),
		       COALESCE(NULLIF(o.buyer_email, ''), (SELECT ci.value_normalized FROM customer_identities ci
		                  WHERE ci.customer_id = o.customer_id AND ci.kind = 'email'
		                  ORDER BY ci.last_seen_at DESC NULLS LAST LIMIT 1), ''),
		       COALESCE(NULLIF(o.buyer_phone, ''), (SELECT ci.value_normalized FROM customer_identities ci
		                  WHERE ci.customer_id = o.customer_id AND ci.kind = 'phone'
		                  ORDER BY ci.last_seen_at DESC NULLS LAST LIMIT 1), '')
		  FROM orders o
		  JOIN organizations org ON org.id = o.org_id
		  JOIN events ev ON ev.id = o.event_id
		  JOIN sessions s ON s.id = o.session_id
		  LEFT JOIN venues v ON v.id = s.venue_id
		  LEFT JOIN sales_channels ch ON ch.id = o.channel_id
		  LEFT JOIN promo_codes pc ON pc.id = o.promo_code_id
		  LEFT JOIN customers c ON c.id = o.customer_id
		 WHERE o.id = $1`, id).Scan(&sale.OrgID, &sale.OrgName, &sale.EventName,
		&sale.VenueName, &sale.StartAt, &sale.TimeZone, &sale.OrderNumber, &sale.Source,
		&sale.ChannelName, &sale.Currency, &sale.Total, &sale.PromoCode, &cats,
		&sale.Buyer.Name, &sale.Buyer.Email, &sale.Buyer.Phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sale{}, ErrNotFound
	}
	if err != nil {
		return Sale{}, err
	}
	if err := json.Unmarshal(cats, &sale.Categories); err != nil {
		return Sale{}, fmt.Errorf("decode categories: %w", err)
	}
	if sale.Source == "complimentary" {
		if sale.IssuedBy, _, err = s.invitationStaff(ctx, id); err != nil {
			return Sale{}, err
		}
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
	var orderID *uuid.UUID
	err = s.pool.QueryRow(ctx, `
		SELECT ev.org_id::text, org.name, ev.name, COALESCE(v.name, ''), s.start_at, COALESCE(v.timezone, ''),
		       COALESCE(o.system_id, 0), t.system_ticket_id, COALESCE(o.currency, ''),
		       COALESCE(t.refund_price,
		                (SELECT rf.amount FROM refunds rf
		                  WHERE rf.ticket_id = t.id AND rf.state = 'succeeded'
		                  ORDER BY rf.succeeded_at DESC LIMIT 1), 0),
		       COALESCE(NULLIF(o.buyer_name, ''), c.display_name, ''),
		       COALESCE(NULLIF(o.buyer_email, ''), (SELECT ci.value_normalized FROM customer_identities ci
		                  WHERE ci.customer_id = o.customer_id AND ci.kind = 'email'
		                  ORDER BY ci.last_seen_at DESC NULLS LAST LIMIT 1), ''),
		       COALESCE(NULLIF(o.buyer_phone, ''), (SELECT ci.value_normalized FROM customer_identities ci
		                  WHERE ci.customer_id = o.customer_id AND ci.kind = 'phone'
		                  ORDER BY ci.last_seen_at DESC NULLS LAST LIMIT 1), ''),
		       t.order_id
		  FROM tickets t
		  JOIN sessions s ON s.id = t.session_id
		  JOIN events ev ON ev.id = s.event_id
		  JOIN organizations org ON org.id = ev.org_id
		  LEFT JOIN venues v ON v.id = s.venue_id
		  LEFT JOIN orders o ON o.id = t.order_id
		  LEFT JOIN customers c ON c.id = o.customer_id
		 WHERE t.id = $1`, id).Scan(&r.OrgID, &r.OrgName, &r.EventName, &r.VenueName,
		&r.StartAt, &r.TimeZone, &r.OrderNumber, &r.TicketNumber, &r.Currency, &r.Amount,
		&r.Buyer.Name, &r.Buyer.Email, &r.Buyer.Phone, &orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Refund{}, ErrNotFound
	}
	if err != nil {
		return Refund{}, err
	}
	if orderID != nil {
		if _, r.CancelledBy, err = s.invitationStaff(ctx, *orderID); err != nil {
			return Refund{}, err
		}
	}
	return r, nil
}

// invitationStaff reads who issued a staff invitation and who cancelled it
// last (spec 46 section 3.7, decision R-11). Only an order with source
// 'complimentary' and issued_via set is an invitation of staff: any other
// order answers two zero values, so an ordinary sale and an old gateway
// invitation print no extra line. The issuer is orders.issued_by (nil for an
// API key, whose name hinvitations recorded in metadata.invitation); the
// canceller is metadata.invitation.cancelled_by, written by the cancel
// transaction.
func (s *PGStore) invitationStaff(ctx context.Context, orderID uuid.UUID) (issued, cancelled Staff, err error) {
	var (
		orgID       uuid.UUID
		issuedBy    *uuid.UUID
		apiKey      string
		cancelledBy []byte
	)
	err = s.pool.QueryRow(ctx, `
		SELECT o.org_id, o.issued_by,
		       COALESCE(o.metadata->'invitation'->>'issued_by_api_key', ''),
		       o.metadata->'invitation'->'cancelled_by'
		  FROM orders o
		 WHERE o.id = $1 AND o.source = 'complimentary' AND o.issued_via IS NOT NULL`,
		orderID).Scan(&orgID, &issuedBy, &apiKey, &cancelledBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Staff{}, Staff{}, nil
	}
	if err != nil {
		return Staff{}, Staff{}, err
	}
	if issuedBy != nil {
		if issued, err = s.staffOfUser(ctx, orgID, *issuedBy); err != nil {
			return Staff{}, Staff{}, err
		}
	} else {
		issued = Staff{Role: RoleAPIKey, Label: apiKey}
	}
	var cb struct {
		UserID string `json:"user_id"`
		APIKey string `json:"api_key"`
	}
	if len(cancelledBy) > 0 && json.Unmarshal(cancelledBy, &cb) == nil {
		if uid, perr := uuid.Parse(cb.UserID); perr == nil {
			if cancelled, err = s.staffOfUser(ctx, orgID, uid); err != nil {
				return Staff{}, Staff{}, err
			}
		} else {
			cancelled = Staff{Role: RoleAPIKey, Label: cb.APIKey}
		}
	}
	return issued, cancelled, nil
}

// Role names of a Staff.
const (
	RoleOwner    = "Owner"
	RoleManager  = "Manager"
	RoleOperator = "Platform operator"
	RoleAPIKey   = "API key"
)

// staffOfUser names a user as the organization knows them: the membership
// role in THAT organization (org_admin is the Owner, organizer the Manager),
// else Platform operator for platform staff acting without a membership.
// A user who left the organization (no membership, not staff) keeps the
// e-mail and no role.
func (s *PGStore) staffOfUser(ctx context.Context, orgID, userID uuid.UUID) (Staff, error) {
	var email, role string
	var staff bool
	err := s.pool.QueryRow(ctx, `
		SELECT u.email,
		       COALESCE((SELECT m.role FROM memberships m
		                  WHERE m.user_id = u.id AND m.org_id = $2
		                  ORDER BY (m.status = 'active') DESC,
		                           CASE m.role WHEN 'org_admin' THEN 0 WHEN 'organizer' THEN 1 ELSE 2 END
		                  LIMIT 1), ''),
		       EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		                WHERE ur.user_id = u.id AND `+gen.PlatformRoleRowSQL+`)
		  FROM users u WHERE u.id = $1`, userID, orgID).Scan(&email, &role, &staff)
	if errors.Is(err, pgx.ErrNoRows) {
		return Staff{}, nil
	}
	if err != nil {
		return Staff{}, err
	}
	out := Staff{Email: email}
	switch role {
	case "org_admin":
		out.Role = RoleOwner
	case "organizer":
		out.Role = RoleManager
	case "":
		if staff {
			out.Role = RoleOperator
		}
	default:
		out.Role = role
	}
	return out, nil
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
