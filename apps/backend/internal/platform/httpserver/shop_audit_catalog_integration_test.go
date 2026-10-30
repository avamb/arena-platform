//go:build integration

package httpserver

// shop_audit_catalog_integration_test.go — the catalog findings of the shop
// audit of 2026-10-30 (SA-04, SA-12, SA-13, SA-18): a screen that did not
// touch the shelf cannot overwrite a sale, a stale list cannot delete a
// colleague's variant, stale settings are refused, and the currency of a shop
// with goods cannot change.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// SA-04: a row without expected_stock leaves the stock alone, even when the
// body carries a stock figure.
func TestShop_VariantsWithoutExpectedStockKeepTheShelf(t *testing.T) {
	f := newShopFixture(t)
	product, variant := f.openShop(4500, 5)
	path := f.orgPath(f.orgA, "/products/"+product+"/variants")

	f.pay(f.placed(variant, 2).OrderToken) // a sale: stock 5 -> 3
	code, body, _ := f.do(http.MethodPut, path, f.ownerA,
		`{"variants":[{"id":"`+variant+`","price_minor":4700,"stock":5}]}`)
	if code != http.StatusOK {
		t.Fatalf("price change without expected_stock = %d %v", code, body)
	}
	if v := f.variant(variant); v.Stock != 3 || v.PriceMinor != 4700 {
		t.Fatalf("variant after a price-only write: stock %d price %d; want 3 and 4700", v.Stock, v.PriceMinor)
	}
	var manual int
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(sum(delta), 0) FROM shop_stock_moves
		WHERE variant_id = $1 AND reason = 'manual'`, variant).Scan(&manual); err != nil || manual != 5 {
		t.Fatalf("manual stock journal = %d (%v); want only the 5 of the first write", manual, err)
	}
}

// SA-13: expected_variant_ids refuses a list written against another set.
func TestShop_VariantsRefuseAChangedSet(t *testing.T) {
	f := newShopFixture(t)
	product, variant := f.openShop(4500, 5)
	path := f.orgPath(f.orgA, "/products/"+product+"/variants")

	// A colleague adds a variant.
	code, body, _ := f.do(http.MethodPut, path, f.ownerA,
		`{"variants":[{"id":"`+variant+`","price_minor":4500},{"title":"Gift box","price_minor":9000,"stock":1}]}`)
	if code != http.StatusOK {
		t.Fatalf("colleague adds a variant = %d %v", code, body)
	}
	// The stale screen still holds one variant and saves: it would delete the box.
	code, body, _ = f.do(http.MethodPut, path, f.ownerA,
		`{"expected_variant_ids":["`+variant+`"],"variants":[{"id":"`+variant+`","price_minor":5000}]}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.variants_changed" {
		t.Fatalf("stale variant set = %d %v; want 409 shop.variants_changed", code, body)
	}
	var live int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM shop_variants
		WHERE product_id = $1 AND deleted_at IS NULL`, product).Scan(&live); err != nil || live != 2 {
		t.Fatalf("live variants after the refused write = %d (%v); want 2", live, err)
	}
}

// SA-12: expected_updated_at refuses stale settings.
func TestShop_SettingsRefuseAStaleVersion(t *testing.T) {
	f := newShopFixture(t)
	f.openShop(4500, 1)
	code, body, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/settings"), f.ownerA, "")
	if code != http.StatusOK {
		t.Fatalf("GET settings = %d %v", code, body)
	}
	loaded := body["updated_at"].(string)

	// Written with the loaded version: fine, and it moves the version on.
	code, body, _ = f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA,
		fmt.Sprintf(`{"currency":"ILS","shipping_fee_minor":6000,"expected_updated_at":%q}`, loaded))
	if code != http.StatusOK || body["updated_at"].(string) == loaded {
		t.Fatalf("PUT with the loaded version = %d %v; want 200 and a new updated_at", code, body)
	}
	// The same, stale version again: refused, nothing written.
	code, body, _ = f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA,
		fmt.Sprintf(`{"currency":"ILS","shipping_fee_minor":7000,"expected_updated_at":%q}`, loaded))
	if code != http.StatusConflict || shopErrCode(body) != "shop.settings_changed" {
		t.Fatalf("PUT with a stale version = %d %v; want 409 shop.settings_changed", code, body)
	}
	if _, got, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/settings"), f.ownerA, ""); got["shipping_fee_minor"].(float64) != 6000 {
		t.Fatalf("shipping fee after the refused write = %v; want 6000", got["shipping_fee_minor"])
	}
	if code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA,
		`{"currency":"ILS","expected_updated_at":"yesterday"}`); code != http.StatusBadRequest {
		t.Fatalf("PUT with a malformed version = %d %v; want 400", code, body)
	}
}

// SA-18: the currency of a shop with products is locked, and a provider that
// cannot charge in it refuses it.
func TestShop_CurrencyIsLockedAndChecked(t *testing.T) {
	f := newShopFixture(t)
	f.openShop(4500, 1) // ILS, one product

	code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA, `{"currency":"EUR"}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.currency_locked" {
		t.Fatalf("currency change with products = %d %v; want 409 shop.currency_locked", code, body)
	}
	// Repeating the stored currency is fine.
	if code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA, `{"currency":"ILS"}`); code != http.StatusOK {
		t.Fatalf("saving the same currency = %d %v", code, body)
	}

	// A shop without goods may change currency, but not to one its AllPay
	// channel cannot charge.
	g := newShopFixture(t)
	g.exec(`UPDATE sales_channels SET provider = 'allpay' WHERE id = $1`, g.channelA)
	if code, body, _ := g.do(http.MethodPut, g.orgPath(g.orgA, "/settings"), g.ownerA,
		fmt.Sprintf(`{"currency":"GBP","sales_channel_id":%q}`, g.channelA)); code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.currency_unsupported" {
		t.Fatalf("GBP on an AllPay channel = %d %v; want 422 shop.currency_unsupported", code, body)
	}
	if code, body, _ := g.do(http.MethodPut, g.orgPath(g.orgA, "/settings"), g.ownerA,
		fmt.Sprintf(`{"currency":"ILS","sales_channel_id":%q}`, g.channelA)); code != http.StatusOK {
		t.Fatalf("ILS on an AllPay channel = %d %v; want 200", code, body)
	}
}

// SA-17: POST .../orders/search answers what GET .../orders answers, the filters
// in the body (RFC 3339 instants for the period).
func TestShop_OrdersSearchReadsTheFiltersFromTheBody(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 5)
	o := f.placed(variant, 1)

	path := f.orgPath(f.orgA, "/orders/search")
	body := fmt.Sprintf(`{"q":%q,"from":%q,"to":%q,"limit":10}`, fmt.Sprint(o.Number),
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	code, got, _ := f.do(http.MethodPost, path, f.ownerA, body)
	if code != http.StatusOK || len(got["orders"].([]any)) != 1 {
		t.Fatalf("search by number inside the period = %d %v; want the one order", code, got)
	}
	// Outside the period: nothing.
	body = fmt.Sprintf(`{"from":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if code, got, _ := f.do(http.MethodPost, path, f.ownerA, body); code != http.StatusOK || len(got["orders"].([]any)) != 0 {
		t.Fatalf("search after the order = %d %v; want no orders", code, got)
	}
	if code, got, _ := f.do(http.MethodPost, path, f.ownerA, `{"from":"yesterday"}`); code != http.StatusBadRequest || shopErrCode(got) != "shop.invalid_filter" {
		t.Fatalf("malformed period = %d %v; want 400 shop.invalid_filter", code, got)
	}
	// Another organization's owner finds nothing of this one.
	if code, got, _ := f.do(http.MethodPost, f.orgPath(f.orgB, "/orders/search"), f.ownerB, `{}`); code != http.StatusOK || len(got["orders"].([]any)) != 0 {
		t.Fatalf("search in another organization = %d %v; want its own (empty) list", code, got)
	}
}
