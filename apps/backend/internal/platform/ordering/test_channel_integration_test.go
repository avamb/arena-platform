//go:build integration

package ordering_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// TestOrderIsTest_FollowsChannelAndNeverChanges proves migration 0202 against
// a live database: an order born in a test channel carries orders.is_test, an
// order born in a live channel does not, and no UPDATE (nor a later change of
// the channel flag) moves the value.
func TestOrderIsTest_FollowsChannelAndNeverChanges(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set - skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newSweepFixture(t, ctx, pool)
	defer f.cleanup()
	q := gen.New(pool)
	exp := time.Now().UTC().Add(30 * time.Minute)

	live := f.seedOrder(t, ctx, "live", ordering.StatusPendingPayment, exp)

	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET is_test = true WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("flag channel: %v", err)
	}
	test := f.seedOrder(t, ctx, "test", ordering.StatusPendingPayment, exp)

	isTest := func(name string, id uuid.UUID) bool {
		t.Helper()
		var got bool
		if err := pool.QueryRow(ctx, `SELECT is_test FROM orders WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return got
	}
	if isTest("live", live) {
		t.Fatal("an order created in a live channel is marked test")
	}
	if !isTest("test", test) {
		t.Fatal("an order created in a test channel is not marked test")
	}

	// No update path: neither a direct UPDATE nor a flip of the channel moves it.
	if _, err := pool.Exec(ctx, `UPDATE orders SET is_test = false WHERE id = $1`, test); err != nil {
		t.Fatalf("update test order: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET is_test = true WHERE id = $1`, live); err != nil {
		t.Fatalf("update live order: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET is_test = false WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("unflag channel: %v", err)
	}
	if !isTest("test", test) || isTest("live", live) {
		t.Fatal("orders.is_test changed after creation")
	}

	row, err := q.GetOrderByID(ctx, test, f.orgID)
	if err != nil {
		t.Fatalf("GetOrderByID: %v", err)
	}
	if !row.IsTest {
		t.Fatal("OrderRow.IsTest is false for a test order")
	}
	ch, err := q.GetSalesChannelByID(ctx, f.channelID, f.orgID)
	if err != nil {
		t.Fatalf("GetSalesChannelByID: %v", err)
	}
	if ch.IsTest {
		t.Fatal("SalesChannelRow.IsTest should be false after the unflag")
	}
}
