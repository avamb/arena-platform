//go:build integration

// shop_fulfillment_0199_integration_test.go — migration 0199 (spec 61, M5):
// the backfill of the delivery step and the Down section. Like the 0198 test,
// it runs Down inside a transaction (the database is already at 0199), inserts
// orders of the old shape, runs Up again and checks what it made. Everything
// is rolled back.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func section0199(t *testing.T, which string) string {
	t.Helper()
	body, err := os.ReadFile("sql/0199_shop_fulfillment_steps.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(body), "-- +goose Down")
	if !ok {
		t.Fatal("no Down section")
	}
	_, up, ok = strings.Cut(up, "-- +goose Up")
	if !ok {
		t.Fatal("no Up section")
	}
	if which == "up" {
		return up
	}
	return down
}

func TestMigration0199_BackfillStepsAndDown(t *testing.T) {
	ctx := context.Background()
	conn := connectDB(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, section0199(t, "down")); err != nil {
		t.Fatalf("Down of 0199: %v", err)
	}
	var hasCol bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'shop_orders' AND column_name = 'fulfillment_step')`).Scan(&hasCol); err != nil || hasCol {
		t.Fatalf("Down left fulfillment_step behind: %v %v", hasCol, err)
	}

	var org string
	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name, slug) VALUES ('mig0199', $1) RETURNING id::text`,
		"mig0199-"+time.Now().Format("150405.000000000")).Scan(&org); err != nil {
		t.Fatal(err)
	}
	mk := func(status, kind string, paid, shipped bool) string {
		t.Helper()
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO shop_orders (org_id, order_token, currency, subtotal, total, status, delivery_kind, paid_at, shipped_at)
			VALUES ($1, md5(random()::text) || md5(random()::text), 'EUR', 100, 100, $2, $3,
			        CASE WHEN $4 THEN now() END, CASE WHEN $5 THEN now() END) RETURNING id::text`,
			org, status, kind, paid, shipped).Scan(&id)
		if err != nil {
			t.Fatalf("order %s: %v", status, err)
		}
		return id
	}
	shipped := mk("shipped", "shipping", true, true)
	paid := mk("paid", "shipping", true, false)
	refundedUnshipped := mk("refunded", "pickup", true, false)
	review := mk("manual_review", "shipping", true, false)
	digital := mk("paid", "none", true, false)
	unpaid := mk("pending_payment", "shipping", false, false)

	if _, err := tx.Exec(ctx, section0199(t, "up")); err != nil {
		t.Fatalf("Up of 0199: %v", err)
	}
	want := map[string]*string{shipped: ptr0199("handed_over"), paid: ptr0199("waiting"), refundedUnshipped: ptr0199("waiting"),
		review: nil, digital: nil, unpaid: nil}
	for id, w := range want {
		var got *string
		if err := tx.QueryRow(ctx, `SELECT fulfillment_step FROM shop_orders WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (got == nil) != (w == nil) || (got != nil && *got != *w) {
			t.Errorf("order %s: step %v, want %v", id, deref0199(got), deref0199(w))
		}
	}
	// A digital order can never carry a step.
	if _, err := tx.Exec(ctx, `SAVEPOINT s`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE shop_orders SET fulfillment_step = 'packed' WHERE id = $1`, digital); err == nil {
		t.Error("a digital order took a step")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`); err != nil {
		t.Fatal(err)
	}

	// Down again: a deleted chat comes out switched off.
	if _, err := tx.Exec(ctx, `INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds, deleted_at)
		VALUES ($1, 'gone', 'mig0199-chat', '{shop_delivery}', now())`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, section0199(t, "down")); err != nil {
		t.Fatalf("second Down of 0199: %v", err)
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT allowed FROM sales_notification_subscriptions WHERE chat_id = 'mig0199-chat'`).Scan(&allowed); err != nil || allowed {
		t.Fatalf("deleted chat after Down: allowed=%v %v", allowed, err)
	}
	if _, err := tx.Exec(ctx, section0199(t, "up")); err != nil {
		t.Fatalf("Up after the second Down: %v", err)
	}
}

func ptr0199(s string) *string { return &s }

func deref0199(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
