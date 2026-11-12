//go:build integration

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

var orderFileHeader = []string{
	"Name", "Email", "Financial Status", "Paid at", "Fulfillment Status", "Fulfilled at", "Currency", "Subtotal", "Shipping",
	"Taxes", "Total", "Discount Code", "Discount Amount", "Created at", "Lineitem quantity", "Lineitem name", "Lineitem price",
	"Cancelled at", "Refunded Amount", "Billing Name", "Shipping Address1", "Shipping City", "Shipping Country", "Phone", "Id",
}

// TestShopImport_OrderHistory drives the real CLI with an orders file: the
// orders arrive as records (money, dates, lines matched to the shop's
// products, a customer), a re-run duplicates nothing and refreshes a changed
// order, a dry run writes nothing, and the shop refuses to ship, e-mail or
// refund an imported order and never offers it under "to ship".
func TestShopImport_OrderHistory(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set - skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	orgID := uuid.New()
	nonce := strings.ReplaceAll(orgID.String(), "-", "")[:12]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Order import "+nonce, "order-import-"+nonce); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupOrg(pool, orgID, t) })

	dir := t.TempDir()
	t.Setenv("JWT_SIGNING_SECRET", "integration-test-secret")
	productsCSV := filepath.Join(dir, "products.csv")
	writeCSV(t, productsCSV, [][]string{
		productHeader,
		{"red-wine-" + nonce, "Red Wine", "<p>Dry</p>", "Winery", "", "", "Size", "0.75", "W-1", "shopify", "12", "deny", "200.00", "", "", "", "", "active"},
		{"red-wine-" + nonce, "", "", "", "", "", "", "1.5", "W-2", "shopify", "4", "deny", "400.00", "", "", "", "", ""},
		{"gift-" + nonce, "Gift Set", "<p>Set</p>", "Winery", "", "", "Title", "Default Title", "", "shopify", "40", "deny", "550.00", "", "", "", "", "active"},
	})
	if err := run([]string{"--org", orgID.String(), "--db-url", dsn, "--products", productsCSV, "--skip-images", "--skip-customers"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("products: %v", err)
	}
	stockBefore := scalar[int](t, pool, `SELECT sum(v.stock) FROM shop_variants v JOIN shop_products p ON p.id = v.product_id WHERE p.org_id = $1`, orgID)

	email := "buyer." + nonce + "@example.com"
	write := func(rows ...[]string) string {
		p := filepath.Join(dir, "orders.csv")
		writeCSV(t, p, append([][]string{orderFileHeader}, rows...))
		return p
	}
	// #1001 shipped, #1002 paid but never marked fulfilled, #1003 fully refunded, #1004 partly refunded with a discount,
	// #1005 not paid (skipped), #1006 whose total does not add up (fails).
	row := func(name, fin, ful, fulAt, sub, ship, tot, disc, qty, line, price, refunded string) []string {
		return []string{name, email, fin, "2026-10-17 12:00:01 +0300", ful, fulAt, "ILS", sub, ship, "0", tot, "", disc, "2026-10-17 12:00:00 +0300",
			qty, line, price, "", refunded, "Ann Buyer", "1 Main St", "Haifa", "IL", "+972501112233", "9" + name[1:]}
	}
	orders := write(
		row("#1001", "paid", "fulfilled", "2026-10-18 09:00:00 +0300", "400.00", "50.00", "450.00", "0.00", "1", "Red Wine - 1.5", "400.00", "0.00"),
		row("#1002", "paid", "unfulfilled", "", "550.00", "0.00", "550.00", "0.00", "1", "Gift Set", "550.00", "0.00"),
		row("#1003", "refunded", "fulfilled", "2026-10-18 09:00:00 +0300", "200.00", "0.00", "200.00", "0.00", "1", "Red Wine - 0.75", "200.00", "200.00"),
		row("#1004", "partially_refunded", "fulfilled", "2026-10-18 09:00:00 +0300", "270.00", "50.00", "320.00", "30.00", "3", "Old Product", "100.00", "40.00"),
		row("#1005", "pending", "unfulfilled", "", "200.00", "0.00", "200.00", "0.00", "1", "Red Wine - 0.75", "200.00", "0.00"),
		row("#1006", "paid", "fulfilled", "2026-10-18 09:00:00 +0300", "200.00", "0.00", "999.00", "0.00", "1", "Red Wine - 0.75", "200.00", "0.00"),
	)

	runOrders := func(extra ...string) (string, error) {
		var out bytes.Buffer
		err := run(append([]string{"--org", orgID.String(), "--db-url", dsn, "--orders", orders}, extra...), &out)
		return out.String(), err
	}
	count := func(sql string) int { return scalar[int](t, pool, sql, orgID) }

	// A dry run classifies and writes nothing.
	out, err := runOrders("--dry-run")
	if err == nil || !strings.Contains(out, "4 would be created") || !strings.Contains(out, "1 would fail") || !strings.Contains(out, "1 skipped") {
		t.Fatalf("dry run: err=%v\n%s", err, out)
	}
	if n := count(`SELECT count(*) FROM shop_orders WHERE org_id = $1`); n != 0 {
		t.Fatalf("a dry run wrote %d orders", n)
	}

	// The real run: four orders, one skipped, one failed (so the run reports a failure).
	out, err = runOrders()
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("the order that cannot be stored must fail the run, got err=%v\n%s", err, out)
	}
	if !strings.Contains(out, "4 created") || !strings.Contains(out, "order lines: 3 matched") || !strings.Contains(out, "1 kept as text only") {
		t.Fatalf("report:\n%s", out)
	}
	if n := count(`SELECT count(*) FROM shop_orders WHERE org_id = $1 AND source = 'shopify'`); n != 4 {
		t.Fatalf("orders = %d, want 4", n)
	}

	type got struct {
		status                                    string
		subtotal, discount, ship, total, refunded int64
		kind, createdAt, shippedAt, step          string
		hasCustomer                               bool
	}
	read := func(ref string) got {
		t.Helper()
		var g got
		err := pool.QueryRow(ctx, `SELECT status, subtotal, discount, shipping_fee, total, refunded_minor, delivery_kind,
			to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'), COALESCE(to_char(shipped_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'), ''),
			COALESCE(fulfillment_step, ''), customer_id IS NOT NULL
			FROM shop_orders WHERE org_id = $1 AND source = 'shopify' AND external_ref = $2`, orgID, ref).
			Scan(&g.status, &g.subtotal, &g.discount, &g.ship, &g.total, &g.refunded, &g.kind, &g.createdAt, &g.shippedAt, &g.step, &g.hasCustomer)
		if err != nil {
			t.Fatalf("read %s: %v", ref, err)
		}
		return g
	}
	if g := read("#1001"); g.status != "shipped" || g.total != 45000 || g.ship != 5000 || g.kind != "shipping" ||
		g.createdAt != "2026-10-17 09:00:00" || g.shippedAt != "2026-10-18 06:00:00" || g.step != "" || !g.hasCustomer {
		t.Fatalf("#1001: %+v", g)
	}
	if g := read("#1002"); g.status != "shipped" || g.shippedAt != "2026-10-17 09:00:01" { // never marked fulfilled in Shopify: shipped at the time of payment
		t.Fatalf("#1002: %+v", g)
	}
	if g := read("#1003"); g.status != "refunded" || g.refunded != 20000 {
		t.Fatalf("#1003: %+v", g)
	}
	if g := read("#1004"); g.status != "partially_refunded" || g.refunded != 4000 || g.subtotal != 30000 || g.discount != 3000 || g.total != 32000 {
		t.Fatalf("#1004: %+v", g)
	}
	// Lines: the matched ones carry the shop's variant, the old product stays text.
	if n := count(`SELECT count(*) FROM shop_order_items i JOIN shop_orders o ON o.id = i.order_id WHERE o.org_id = $1 AND i.variant_id IS NOT NULL`); n != 3 {
		t.Fatalf("matched lines = %d, want 3", n)
	}
	if n := count(`SELECT count(*) FROM shop_order_items i JOIN shop_orders o ON o.id = i.order_id
		WHERE o.org_id = $1 AND i.variant_id IS NULL AND i.product_title = 'Old Product' AND i.qty = 3 AND i.total_minor = 30000`); n != 1 {
		t.Fatalf("the unmatched line must keep its name, quantity and total (%d)", n)
	}
	// An import is a record: stock and the holds journal are untouched, nothing is queued to the buyer.
	if after := scalar[int](t, pool, `SELECT sum(v.stock) FROM shop_variants v JOIN shop_products p ON p.id = v.product_id WHERE p.org_id = $1`, orgID); after != stockBefore {
		t.Fatalf("stock moved from %d to %d", stockBefore, after)
	}
	if n := count(`SELECT count(*) FROM shop_stock_moves WHERE org_id = $1 AND order_id IS NOT NULL`); n != 0 {
		t.Fatalf("an imported order wrote %d stock moves", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM worker_jobs WHERE payload::text LIKE '%' || $1 || '%' AND job_type LIKE 'shop.%'`, orgID.String()); n != 0 {
		t.Fatalf("an imported order queued %d shop jobs", n)
	}

	// A second run duplicates nothing.
	out, _ = runOrders()
	if !strings.Contains(out, "4 unchanged") || count(`SELECT count(*) FROM shop_orders WHERE org_id = $1 AND source = 'shopify'`) != 4 {
		t.Fatalf("second run:\n%s", out)
	}

	// A new export reuses the name #1001 for another order (other Shopify id,
	// total): the run says so and leaves the stored order alone (F-SH-01).
	other := row("#1001", "paid", "fulfilled", "2026-10-18 09:00:00 +0300", "800.00", "50.00", "850.00", "0.00", "2", "Red Wine - 1.5", "400.00", "0.00")
	other[len(other)-1] = "9999999999"
	orders = write(other)
	out, err = runOrders()
	if err != nil || !strings.Contains(out, "1 in conflict with the file") ||
		!strings.Contains(out, "orders: #1001: already stored, but the file has another Shopify id, total;") {
		t.Fatalf("conflict run: err=%v\n%s", err, out)
	}
	if strings.Contains(out, "1 unchanged") {
		t.Fatalf("a conflicting order must not count as unchanged:\n%s", out)
	}
	if g := read("#1001"); g.total != 45000 {
		t.Fatalf("the stored order was changed by a conflicting file: %+v", g)
	}

	// Shopify says #1002 was fulfilled meanwhile: the re-run brings the known order up to date.
	orders = write(
		row("#1002", "paid", "fulfilled", "2026-10-20 10:00:00 +0300", "550.00", "0.00", "550.00", "0.00", "1", "Gift Set", "550.00", "0.00"),
	)
	out, _ = runOrders()
	if !strings.Contains(out, "1 updated") {
		t.Fatalf("third run:\n%s", out)
	}
	if g := read("#1002"); g.status != "shipped" || g.shippedAt != "2026-10-20 07:00:00" {
		t.Fatalf("#1002 after refresh: %+v", g)
	}

	// The shop treats them as history.
	svc := shop.New(pool, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	counts, err := svc.CountOrders(ctx, orgID)
	if err != nil || counts.All != 4 || counts.ToShip != 0 {
		t.Fatalf("counts: %+v err=%v (an imported order is never under \"to ship\")", counts, err)
	}
	list, err := svc.ListOrders(ctx, orgID, shop.OrderFilter{Query: "#1003"})
	if err != nil || len(list) != 1 || list[0].ExternalRef == nil || *list[0].ExternalRef != "#1003" {
		t.Fatalf("search by the old number: %d orders err=%v", len(list), err)
	}
	if list, err = svc.ListOrders(ctx, orgID, shop.OrderFilter{Query: "1004"}); err != nil || len(list) != 1 {
		t.Fatalf("search by the old number without #: %d orders err=%v", len(list), err)
	}
	id := list[0].ID
	refuse := func(what string, err error) {
		t.Helper()
		if e, ok := shop.AsError(err); !ok || e.Code != "shop.order_historic" {
			t.Fatalf("%s of an imported order must answer shop.order_historic, got %v", what, err)
		}
	}
	_, err = svc.Ship(ctx, orgID, id, "", shop.StepInput{ActorName: "test"})
	refuse("shipping", err)
	refuse("a letter", svc.ResendEmail(ctx, orgID, id, shop.EmailRefunded))
	_, err = svc.Refund(ctx, shop.RefundInput{OrgID: orgID, OrderID: id, IdempotencyKey: "k-" + nonce})
	refuse("a refund", err)
}

// scalar reads one value of a query that takes the organization as $1.
func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%q: %v", sql, err)
	}
	return v
}
