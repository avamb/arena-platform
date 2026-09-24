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
// SQL against the migrated schema: allowed-only subscriptions, the sales
// and refunds queries (joins, JSON category rollup, settle lag), and the
// chat-id / last-error bookkeeping. Needs DATABASE_URL.
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

	// The queries must run against the real schema; history may or may not
	// hold rows, so only the shape is asserted.
	sales, err := st.SalesSince(ctx, time.Now().Add(-365*24*time.Hour), "", 5)
	if err != nil {
		t.Fatalf("sales query: %v", err)
	}
	for _, s := range sales {
		if s.OrderNumber == 0 || s.OrgName == "" || s.EventName == "" {
			t.Fatalf("incomplete sale row: %+v", s)
		}
		if !s.At.Before(time.Now().Add(-9 * time.Second)) {
			t.Fatalf("settle lag not applied: %v", s.At)
		}
	}
	if _, err := st.RefundsSince(ctx, time.Now().Add(-365*24*time.Hour), "", 5); err != nil {
		t.Fatalf("refunds query: %v", err)
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
