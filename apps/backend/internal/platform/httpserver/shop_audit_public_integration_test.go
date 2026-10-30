//go:build integration

package httpserver

// shop_audit_public_integration_test.go — the public half of the shop audit
// (SA-05 live unpaid orders per address, SA-27 what the catalog discloses).
//
// Run against a FRESH migrated and seeded database:
//
//	go test -tags integration -run 'TestShopAudit' ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// TestShopAudit_LiveUnpaidOrdersPerAddress: random e-mail addresses get past
// the per-buyer cap, so one client address (an IPv6 address counts by its /64)
// may hold at most shop.MaxPendingPerClient unpaid orders in a shop. The count
// is read from the database, only live holds count, and the address itself is
// never stored.
func TestShopAudit_LiveUnpaidOrdersPerAddress(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 100)

	// Two different addresses of one /64 are one client.
	f.remote = "[2001:db8:1:2::1]:4000"
	for i := 0; i < shop.MaxPendingPerClient; i++ {
		if code, body := f.checkout(variant, 1); code != http.StatusCreated {
			t.Fatalf("order %d of the client = %d %v; want 201", i+1, code, body)
		}
		f.remote = fmt.Sprintf("[2001:db8:1:2::%x]:4000", i+2)
	}
	code, body := f.checkout(variant, 1)
	if code != http.StatusConflict || shopErrCode(body) != "shop.too_many_pending" {
		t.Fatalf("order over the address cap = %d %v; want 409 shop.too_many_pending", code, body)
	}

	// Another /64, and another IPv4 address, are other clients.
	for _, remote := range []string{"[2001:db8:1:3::1]:4000", "203.0.113.7:4000"} {
		f.remote = remote
		if code, body := f.checkout(variant, 1); code != http.StatusCreated {
			t.Fatalf("order from %s = %d %v; want 201", remote, code, body)
		}
	}

	// The hash is stored, not the address.
	var stored int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM shop_orders WHERE org_id = $1 AND buyer_ip_hash ~ '^[0-9a-f]{64}$'`, f.orgA).Scan(&stored); err != nil || stored != shop.MaxPendingPerClient+2 {
		t.Fatalf("orders with a client hash = %d (%v); want %d", stored, err, shop.MaxPendingPerClient+2)
	}

	// A hold that ended stops counting.
	f.remote = "[2001:db8:1:2::99]:4000"
	f.exec(`UPDATE shop_orders SET expires_at = now() - interval '1 second'
	        WHERE id = (SELECT id FROM shop_orders WHERE org_id = $1 AND status = 'pending_payment' ORDER BY number LIMIT 1)`, f.orgA)
	if code, body := f.checkout(variant, 1); code != http.StatusCreated {
		t.Fatalf("order after one hold ended = %d %v; want 201", code, body)
	}
}

// TestShopAudit_CatalogDoesNotDiscloseTheShelf: the storefront gets no article
// number and no stock figure beyond what one order can hold.
func TestShopAudit_CatalogDoesNotDiscloseTheShelf(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 100)
	lineCap := int(shop.DefaultMaxLineQty)

	check := func(wantAvailable int, wantInStock bool) {
		t.Helper()
		code, body, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug, "", "")
		if code != http.StatusOK {
			t.Fatalf("public catalog = %d %v", code, body)
		}
		pv := body["products"].([]any)[0].(map[string]any)["variants"].([]any)[0].(map[string]any)
		if _, leaked := pv["sku"]; leaked {
			t.Fatalf("the catalog publishes the article number: %v", pv)
		}
		if got, _ := pv["available"].(float64); int(got) != wantAvailable || pv["in_stock"] != wantInStock {
			t.Fatalf("variant = %v; want available %d in_stock %v", pv, wantAvailable, wantInStock)
		}
	}
	check(lineCap, true) // 100 on the shelf, one order holds at most the cap
	f.exec(`UPDATE shop_variants SET stock = 5 WHERE id = $1`, uuid.MustParse(variant))
	check(5, true)
	f.exec(`UPDATE shop_variants SET stock = 0 WHERE id = $1`, uuid.MustParse(variant))
	check(0, false)

	// The product route says the same.
	f.exec(`UPDATE shop_variants SET stock = 100 WHERE id = $1`, uuid.MustParse(variant))
	var slug string
	if err := f.pool.QueryRow(context.Background(), `SELECT slug FROM shop_products WHERE org_id = $1`, f.orgA).Scan(&slug); err != nil {
		t.Fatalf("product slug: %v", err)
	}
	code, body, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug+"/products/"+slug, "", "")
	if code != http.StatusOK {
		t.Fatalf("public product = %d %v", code, body)
	}
	pv := body["product"].(map[string]any)["variants"].([]any)[0].(map[string]any)
	if _, leaked := pv["sku"]; leaked || int(pv["available"].(float64)) != lineCap {
		t.Fatalf("public product variant = %v; want no sku and available %d", pv, lineCap)
	}
}
