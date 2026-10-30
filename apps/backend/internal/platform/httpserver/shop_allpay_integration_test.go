//go:build integration

package httpserver

// shop_allpay_integration_test.go — a merchandise shop order (spec 47) paid
// and refunded through AllPay end to end (audit SA-24), through the real
// router and a live database, only AllPay stubbed (stubAllPayServer from
// allpay_checkout_integration_test.go, a ledger of orders that signs and
// checks like allpay.to). The first shop client pays through AllPay, so this
// is the path that must hold: the storefront checkout creates an AllPay page
// for the shop order, the signed callback on the organization's per-config
// webhook route pays the order and takes the goods off the shelf, a partial
// refund reaches AllPay once and leaves the order partially_refunded.
//
// Run against a FRESH migrated and seeded database (AGENTS.md CI-Integration
// recipe):
//
//	go test -tags integration -run TestShopAllPay ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/allpay"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

const shopAllPayAPIBase = "https://api.arena-integration.test"

// shopAllPay is a shopFixture whose organization A pays through AllPay.
type shopAllPay struct {
	*shopFixture
	stub   *stubAllPayServer
	login  string
	apiKey string
}

func newShopAllPayFixture(t *testing.T) *shopAllPay {
	t.Helper()
	f := &shopAllPay{shopFixture: newShopFixture(t)}
	suffix := f.orgA.String()[:8]
	f.login, f.apiKey = "pp"+suffix, "allpay_key_shop_"+suffix
	f.stub = newStubAllPayServer(t)
	f.stub.account(f.login, f.apiKey)

	// The shop handler captures the payment starter and the refund engine when
	// the router is built, so the AllPay seam must be in the config before New:
	// a second server on the same database, with the same JWT secret the
	// fixture's owner tokens were signed with.
	f.srv, _ = productionIntegrationServerCfg(t, func(c *config.Config) {
		c.PublicTicketsBaseURL = shopTestBase
		c.CORSAllowedOrigins = []string{shopTestBase}
		c.AllPayAPIBaseURL = f.stub.baseURL()
		// AllPay takes the callback URL per page, built from the public API origin.
		c.APIPublicURL = shopAllPayAPIBase
		c.WidgetPaymentWindowSeconds = 1860
		c.WidgetPaymentGraceSeconds = 120
	})

	secrets, _ := json.Marshal(map[string]string{"merchant_id": f.login, "secret_key": f.apiKey})
	f.exec(`UPDATE sales_channels SET provider = 'allpay' WHERE id = $1`, f.channelA)
	f.exec(`UPDATE payment_provider_configs SET provider = 'allpay', secrets = $2::jsonb WHERE id = $1`, f.configA, string(secrets))
	return f
}

func (f *shopAllPay) webhookPath() string {
	return "/v1/payment-intents/webhook/" + f.configA.String()
}

// intentOf is the order and the payment intent behind the order token.
func (f *shopAllPay) intentOf(token string) (gen.ShopOrderRow, gen.PaymentIntentRow) {
	f.t.Helper()
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil || o.PaymentIntentID == nil {
		f.t.Fatalf("order of token: %v (payment %v)", err, o.PaymentIntentID)
	}
	pi, err := f.q.GetPaymentIntentByID(context.Background(), *o.PaymentIntentID)
	if err != nil || pi.ProviderPaymentID == nil {
		f.t.Fatalf("payment intent: %v", err)
	}
	return o, pi
}

// callback builds AllPay's signed payment webhook for the order.
func (f *shopAllPay) callback(orderID string, status int, totalMinor int64, key string) []byte {
	f.t.Helper()
	price := json.Number(allpay.MinorToMajor(totalMinor))
	m := map[string]any{
		"order_id":         orderID,
		"status":           json.Number(fmt.Sprint(status)),
		"amount":           price,
		"currency":         "ILS",
		"currency_display": "ILS",
		"currency_payment": "ILS",
		"currency_course":  json.Number("1"),
		"name":             "Shop order",
		"items":            []any{map[string]any{"name": "Shop order", "qty": json.Number("1"), "price": price, "vat": json.Number("0")}},
		"inst":             json.Number("1"),
		"card_mask":        "455743******3431",
		"card_brand":       "Visa",
		"foreign_card":     json.Number("0"),
		"client_email":     "anna@example.test",
	}
	m["sign"] = allpay.Sign(key, m)
	raw, err := json.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

// refund posts a refund of the order with the Idempotency-Key header every
// refund must carry.
func (f *shopAllPay) refund(orderID uuid.UUID, body string) (int, map[string]any) {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, f.orgPath(f.orgA, "/orders/"+orderID.String()+"/refund"), strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+f.ownerA)
	req.Header.Set("Idempotency-Key", "shop-allpay-"+uuid.NewString())
	rec := httptest.NewRecorder()
	f.srv.router.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return rec.Code, out
}

func TestShopAllPay_CheckoutCallbackAndPartialRefund(t *testing.T) {
	f := newShopAllPayFixture(t)
	ctx := context.Background()
	_, variant := f.openShop(4500, 3)

	// ── 1. Checkout → an AllPay page for the shop order.
	code, body := f.checkout(variant, 2)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	if body["status"] != "pending" || body["total"].(float64) != 2*4500+5000 {
		t.Fatalf("checkout answer = %v; want pending, total 14000", body)
	}
	if !strings.HasPrefix(fmt.Sprint(body["payment_url"]), "https://allpay.test/") {
		t.Fatalf("payment_url = %v; want the AllPay page of the stub", body["payment_url"])
	}

	// What AllPay was asked for: the shop order's id and the total in shekels.
	o, pi := f.intentOf(token)
	ord := f.stub.last(t)
	if ord["login"] != f.login || ord["order_id"] != o.ID.String() || ord["currency_display"] != "ILS" {
		t.Fatalf("getpayment login %v order_id %v currency %v; want %s, the shop order %s, ILS", ord["login"], ord["order_id"], ord["currency_display"], f.login, o.ID)
	}
	items, _ := ord["items"].([]any)
	if len(items) != 1 || fmt.Sprint(items[0].(map[string]any)["price"]) != allpay.MinorToMajor(o.Total) {
		t.Fatalf("getpayment items = %v; want one line of %s", ord["items"], allpay.MinorToMajor(o.Total))
	}
	if ord["webhook_url"] != shopAllPayAPIBase+f.webhookPath() {
		t.Fatalf("webhook_url = %v; want %s", ord["webhook_url"], shopAllPayAPIBase+f.webhookPath())
	}
	if pi.Provider != "allpay" || pi.State != "created" || pi.Amount != o.Total || *pi.ProviderPaymentID != o.ID.String() {
		t.Fatalf("payment intent = %s %s %d ref %s; want allpay created %d keyed by the shop order id", pi.Provider, pi.State, pi.Amount, *pi.ProviderPaymentID, o.Total)
	}
	if v := f.variant(variant); v.Held != 2 || v.Stock != 3 {
		t.Fatalf("after checkout stock %d held %d; want 3 and 2", v.Stock, v.Held)
	}

	// ── 2. A callback signed with another key moves nothing, and a paid
	// callback AllPay's own books do not confirm yet asks for a redelivery.
	if code, _ := postAllPayWebhook(t, f.srv, f.webhookPath(), f.callback(o.ID.String(), 1, o.Total, "not-the-key")); code != http.StatusUnauthorized {
		t.Fatalf("callback with the wrong key = %d; want 401", code)
	}
	good := f.callback(o.ID.String(), 1, o.Total, f.apiKey)
	if code, resp := postAllPayWebhook(t, f.srv, f.webhookPath(), good); code != http.StatusServiceUnavailable {
		t.Fatalf("paid callback while AllPay reports the order unpaid = %d %s; want 503", code, resp)
	}
	if got, _ := f.q.GetShopOrder(ctx, o.ID, f.orgA); got.Status != shop.StatusPendingPayment {
		t.Fatalf("an unconfirmed callback moved the order to %s", got.Status)
	}

	// ── 3. The buyer paid: the signed callback pays the order.
	f.stub.markPaid(t, o.ID.String())
	code, resp := postAllPayWebhook(t, f.srv, f.webhookPath(), good)
	if code != http.StatusOK || !strings.Contains(string(resp), `"shop_order_updated":true`) {
		t.Fatalf("paid callback = %d %s; want 200 with shop_order_updated", code, resp)
	}
	got, _ := f.q.GetShopOrder(ctx, o.ID, f.orgA)
	if got.Status != shop.StatusPaid || got.PaidAt == nil {
		t.Fatalf("order = %s paid_at %v; want paid", got.Status, got.PaidAt)
	}
	if v := f.variant(variant); v.Stock != 1 || v.Held != 0 {
		t.Fatalf("after payment stock %d held %d; want 1 and 0", v.Stock, v.Held)
	}
	piAfter, _ := f.q.GetPaymentIntentByID(ctx, pi.ID)
	if piAfter.State != "succeeded" || piAfter.ProviderChargeRef == nil || *piAfter.ProviderChargeRef != o.ID.String() {
		t.Fatalf("payment intent = %s charge ref %v; want succeeded with the order id (AllPay refunds by it)", piAfter.State, piAfter.ProviderChargeRef)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("v1.shop.order.paid events = %d; want 1", n)
	}

	// AllPay retries webhooks: a replay is an acknowledged no-op.
	if code, resp := postAllPayWebhook(t, f.srv, f.webhookPath(), good); code != http.StatusOK {
		t.Fatalf("replayed callback = %d %s; want 200", code, resp)
	}
	if v := f.variant(variant); v.Stock != 1 || v.Held != 0 {
		t.Fatalf("after the replay stock %d held %d; want 1 and 0", v.Stock, v.Held)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("v1.shop.order.paid events after the replay = %d; want 1", n)
	}
	code, pub, _ := f.do(http.MethodGet, "/v1/public/shop/checkout/"+token, "", "")
	if code != http.StatusOK || pub["status"] != "paid" {
		t.Fatalf("public status = %d %v; want paid", code, pub)
	}

	// ── 4. A partial refund goes to AllPay once, for that amount.
	code, out := f.refund(o.ID, `{"amount_minor":5000,"reason":"one bottle broken"}`)
	if code != http.StatusOK {
		t.Fatalf("partial refund = %d %v", code, out)
	}
	refund, order := out["refund"].(map[string]any), out["order"].(map[string]any)
	if refund["state"] != "succeeded" || order["status"] != shop.StatusPartiallyRefunded || order["refunded_minor"].(float64) != 5000 {
		t.Fatalf("after the partial refund refund %v order status %v refunded %v", refund, order["status"], order["refunded_minor"])
	}
	if got := f.stub.refundsOf(o.ID.String()); len(got) != 1 || got[0] != 5000 {
		t.Fatalf("AllPay refunded %v; want [5000]", got)
	}
	if n := f.outboxCount(shop.EventOrderRefunded, o.ID); n != 1 {
		t.Fatalf("refunded events = %d; want 1", n)
	}
	if v := f.variant(variant); v.Stock != 1 {
		t.Fatalf("stock after the refund = %d; want 1 (never put back automatically)", v.Stock)
	}

	// ── 5. The rest of the money goes back, and a refunded order takes no
	// more.
	code, out = f.refund(o.ID, ``)
	if code != http.StatusOK {
		t.Fatalf("refund of the rest = %d %v", code, out)
	}
	order = out["order"].(map[string]any)
	if order["status"] != shop.StatusRefunded || order["refunded_minor"].(float64) != 14000 {
		t.Fatalf("after the full refund status %v refunded %v", order["status"], order["refunded_minor"])
	}
	if got := f.stub.refundsOf(o.ID.String()); len(got) != 2 || got[1] != 9000 {
		t.Fatalf("AllPay refunded %v; want [5000 9000]", got)
	}
	if code, out := f.refund(o.ID, ``); code != http.StatusConflict {
		t.Fatalf("refund of a refunded order = %d %v; want 409", code, out)
	}
}
