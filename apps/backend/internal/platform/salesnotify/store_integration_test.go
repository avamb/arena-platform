//go:build integration

package salesnotify

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPGStore_SubscriptionsQueriesAndBookkeeping_LiveDB proves the store's
// SQL against the migrated schema: allowed-only subscriptions, the order and
// ticket lookups, the once-only claim and the chat-id / last-error
// bookkeeping. Needs DATABASE_URL.
func TestPGStore_SubscriptionsQueriesAndBookkeeping_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	nonce := fmt.Sprintf("%d", time.Now().UnixNano())
	var orgID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id::text`,
		"SN Org "+nonce, "sn-org-"+nonce).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM sales_notification_subscriptions WHERE chat_id LIKE $1`, "sn-"+nonce+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	}()

	var subID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, on_ticket_refunded)
		VALUES ($1, 'client', $2, false) RETURNING id::text`, orgID, "sn-"+nonce+"-a").Scan(&subID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, allowed)
		VALUES ($1, 'disabled', $2, false)`, orgID, "sn-"+nonce+"-b"); err != nil {
		t.Fatalf("seed disabled subscription: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sales_notification_subscriptions (org_id, name, chat_id)
		VALUES ($1, 'duplicate', $2)`, orgID, "sn-"+nonce+"-a"); err == nil {
		t.Fatal("the same chat subscribed twice for one org must be refused")
	}

	st := NewPGStore(pool)
	subs, err := st.Subscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mine []Subscription
	for _, s := range subs {
		if strings.HasPrefix(s.ChatID, "sn-"+nonce) {
			mine = append(mine, s)
		}
	}
	if len(mine) != 1 || mine[0].OrgID != orgID || !mine[0].OnOrderPaid || mine[0].OnTicketRefunded {
		t.Fatalf("allowed subscriptions = %+v", mine)
	}

	// The lookups run against whatever the database holds; a fresh one has
	// no orders, which only skips the shape checks.
	var orderID, ticketID string
	_ = pool.QueryRow(ctx, `SELECT id::text FROM orders WHERE paid_at IS NOT NULL ORDER BY paid_at DESC LIMIT 1`).Scan(&orderID)
	_ = pool.QueryRow(ctx, `SELECT id::text FROM tickets ORDER BY issued_at DESC LIMIT 1`).Scan(&ticketID)
	if orderID != "" {
		sale, err := st.SaleByOrder(ctx, orderID)
		if err != nil || sale.OrderNumber == 0 || sale.OrgName == "" || sale.EventName == "" || sale.Tickets() == 0 {
			t.Fatalf("SaleByOrder(%s) = %+v, %v", orderID, sale, err)
		}
	}
	if ticketID != "" {
		r, err := st.RefundByTicket(ctx, ticketID)
		if err != nil || r.TicketNumber == 0 || r.OrgID == "" {
			t.Fatalf("RefundByTicket(%s) = %+v, %v", ticketID, r, err)
		}
	}
	if _, err := st.SaleByOrder(ctx, "01a0cdf2-0000-7000-8000-000000000000"); err != ErrNotFound {
		t.Fatalf("unknown order: %v, want ErrNotFound", err)
	}
	if _, err := st.RefundByTicket(ctx, "not-a-uuid"); err != ErrNotFound {
		t.Fatalf("malformed ticket id: %v, want ErrNotFound", err)
	}

	key := "paid:test-" + nonce
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM sales_notification_deliveries WHERE key = $1`, key) }()
	if first, err := st.Claim(ctx, key); err != nil || !first {
		t.Fatalf("first claim = %v, %v", first, err)
	}
	if again, err := st.Claim(ctx, key); err != nil || again {
		t.Fatalf("second claim = %v, %v — a retry must not post twice", again, err)
	}

	if err := st.RecordDelivery(ctx, subID, "telegram 403: Forbidden"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateChatID(ctx, subID, "sn-"+nonce+"-super"); err != nil {
		t.Fatal(err)
	}
	var chat, lastErr string
	if err := pool.QueryRow(ctx, `SELECT chat_id, COALESCE(last_error, '') FROM sales_notification_subscriptions WHERE id = $1`,
		subID).Scan(&chat, &lastErr); err != nil {
		t.Fatal(err)
	}
	if chat != "sn-"+nonce+"-super" || lastErr != "telegram 403: Forbidden" {
		t.Fatalf("bookkeeping: chat=%q last_error=%q", chat, lastErr)
	}
	if err := st.RecordDelivery(ctx, subID, ""); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(last_error, '') FROM sales_notification_subscriptions WHERE id = $1`,
		subID).Scan(&lastErr); err != nil || lastErr != "" {
		t.Fatalf("a success must clear last_error, got %q (%v)", lastErr, err)
	}
}
