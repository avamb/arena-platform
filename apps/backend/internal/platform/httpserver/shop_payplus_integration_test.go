//go:build integration

package httpserver

// shop_payplus_integration_test.go — a merchandise shop order (spec 47) paid
// through PayPlus end to end, through the real router and a live database,
// only PayPlus stubbed (stubPayPlusServer from payplus_checkout_integration_test.go).
// The first shop client pays through PayPlus, so this is the path that must
// hold: the storefront checkout creates a PayPlus link, the signed callback
// on the organization's per-config webhook route pays the order and takes the
// goods off the shelf, a replay changes nothing, and a callback that arrives
// after the order was cancelled is parked when the goods are gone or paid
// when they are still there.
//
// Run against a FRESH migrated and seeded database (AGENTS.md CI-Integration
// recipe), e.g. arena_shop_l2:
//
//	go test -tags integration -run TestShopPayPlus ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// shopPayPlus is a shopFixture whose organization A pays through PayPlus.
type shopPayPlus struct {
	*shopFixture
	stub              *stubPayPlusServer
	apiKey, secretKey string
	pageUID           string
}

func newShopPayPlusFixture(t *testing.T) *shopPayPlus {
	t.Helper()
	f := &shopPayPlus{shopFixture: newShopFixture(t)}
	f.apiKey = uuid.NewString()
	f.secretKey = "pp_shop_" + uuid.NewString()[:12]
	f.pageUID = uuid.NewString()
	f.stub = newStubPayPlusServer(t, f.apiKey, f.secretKey)

	// The shop handler captures the payment starter when the router is
	// built, so the PayPlus seam must be in the config before New: a second
	// server on the same database, with the same JWT secret the fixture's
	// owner tokens were signed with.
	f.srv, _ = productionIntegrationServerCfg(t, func(c *config.Config) {
		c.PublicTicketsBaseURL = shopTestBase
		c.CORSAllowedOrigins = []string{shopTestBase}
		c.PayPlusAPIBaseURL = f.stub.baseURL()
		// PayPlus takes the callback URL per link, built from the public API
		// origin.
		c.APIPublicURL = shopPayPlusAPIBase
		c.WidgetPaymentWindowSeconds = 1860
		c.WidgetPaymentGraceSeconds = 120
	})

	secrets, _ := json.Marshal(map[string]string{"api_key": f.apiKey, "secret_key": f.secretKey, "payment_page_uid": f.pageUID})
	f.exec(`UPDATE sales_channels SET provider = 'payplus' WHERE id = $1`, f.channelA)
	f.exec(`UPDATE payment_provider_configs SET provider = 'payplus', secrets = $2::jsonb WHERE id = $1`, f.configA, string(secrets))
	return f
}

const shopPayPlusAPIBase = "https://api.arena-integration.test"

func (f *shopPayPlus) webhookPath() string {
	return "/v1/payment-intents/webhook/" + f.configA.String()
}

// intentOf is the order's payment intent and the PayPlus link id behind it.
func (f *shopPayPlus) intentOf(token string) (gen.ShopOrderRow, gen.PaymentIntentRow) {
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

// payCallback posts PayPlus's signed charge callback for the order and
// returns the transaction uid it carried.
func (f *shopPayPlus) payCallback(token string) (string, int, []byte) {
	f.t.Helper()
	o, pi := f.intentOf(token)
	txUID := "pp-shop-" + uuid.NewString()
	body := payplusCallback(f.t, *pi.ProviderPaymentID, txUID, "000", o.Total, o.ID.String())
	code, resp := postPayPlusCallback(f.t, f.srv, f.webhookPath(), body, f.secretKey)
	return txUID, code, resp
}

func TestShopPayPlus_CheckoutAndSignedCallbackPayTheOrder(t *testing.T) {
	f := newShopPayPlusFixture(t)
	_, variant := f.openShop(4500, 3)

	code, body := f.checkout(variant, 2)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	if body["status"] != "pending" || body["total"].(float64) != 2*4500+5000 {
		t.Fatalf("checkout answer = %v; want pending, total 14000", body)
	}
	if !strings.HasPrefix(body["payment_url"].(string), "https://paymentsdev.payplus.test/") {
		t.Fatalf("payment_url = %v; want the PayPlus page of the stub", body["payment_url"])
	}

	// What PayPlus was asked for: the shop order, in major units.
	o, pi := f.intentOf(token)
	link := f.stub.lastLink(t)
	if link["amount"] != json.Number("140") || link["currency_code"] != "ILS" || link["more_info"] != o.ID.String() {
		t.Fatalf("generateLink amount %v currency %v more_info %v; want 140 ILS and the shop order id %s",
			link["amount"], link["currency_code"], link["more_info"], o.ID)
	}
	if link["payment_page_uid"] != f.pageUID || link["refURL_callback"] != shopPayPlusAPIBase+f.webhookPath() {
		t.Fatalf("generateLink page %v callback %v", link["payment_page_uid"], link["refURL_callback"])
	}
	if pi.Provider != "payplus" || pi.State != "created" || pi.Amount != o.Total {
		t.Fatalf("payment intent = %s %s %d; want payplus created %d", pi.Provider, pi.State, pi.Amount, o.Total)
	}
	if v := f.variant(variant); v.Held != 2 || v.Stock != 3 {
		t.Fatalf("after checkout stock %d held %d; want 3 and 2", v.Stock, v.Held)
	}

	// A callback signed with another key moves nothing.
	bad := payplusCallback(t, *pi.ProviderPaymentID, "pp-forged", "000", o.Total, o.ID.String())
	if code, _ := postPayPlusCallback(t, f.srv, f.webhookPath(), bad, "not-the-secret"); code != http.StatusUnauthorized {
		t.Fatalf("callback with the wrong key = %d; want 401", code)
	}

	txUID, code, resp := f.payCallback(token)
	if code != http.StatusOK || !strings.Contains(string(resp), `"shop_order_updated":true`) {
		t.Fatalf("paid callback = %d %s; want 200 with shop_order_updated", code, resp)
	}
	got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if got.Status != shop.StatusPaid || got.PaidAt == nil {
		t.Fatalf("order = %s paid_at %v; want paid", got.Status, got.PaidAt)
	}
	if v := f.variant(variant); v.Stock != 1 || v.Held != 0 {
		t.Fatalf("after payment stock %d held %d; want 1 and 0", v.Stock, v.Held)
	}
	piAfter, _ := f.q.GetPaymentIntentByID(context.Background(), pi.ID)
	if piAfter.State != "succeeded" || piAfter.ProviderChargeRef == nil || *piAfter.ProviderChargeRef != txUID {
		t.Fatalf("payment intent = %s charge ref %v; want succeeded with transaction uid %s", piAfter.State, piAfter.ProviderChargeRef, txUID)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("v1.shop.order.paid events = %d; want 1", n)
	}

	// PayPlus delivers again: nothing changes.
	o2, pi2 := f.intentOf(token)
	replay := payplusCallback(t, *pi2.ProviderPaymentID, txUID, "000", o2.Total, o2.ID.String())
	code, resp = postPayPlusCallback(t, f.srv, f.webhookPath(), replay, f.secretKey)
	if code != http.StatusOK || strings.Contains(string(resp), `"processed":true`) {
		t.Fatalf("replayed callback = %d %s; want an acknowledged no-op", code, resp)
	}
	if v := f.variant(variant); v.Stock != 1 || v.Held != 0 {
		t.Fatalf("after the replay stock %d held %d; want 1 and 0", v.Stock, v.Held)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("v1.shop.order.paid events after the replay = %d; want 1", n)
	}
	code, body, _ = f.do(http.MethodGet, "/v1/public/shop/checkout/"+token, "", "")
	if code != http.StatusOK || body["status"] != "paid" {
		t.Fatalf("public status = %d %v; want paid", code, body)
	}
}

// TestShopPayPlus_LateCallbackAfterCancel: the operator cancels a pending
// order (its hold ends), and the buyer's PayPlus payment arrives afterwards.
func TestShopPayPlus_LateCallbackAfterCancel(t *testing.T) {
	cancel := func(f *shopPayPlus, id uuid.UUID) {
		t.Helper()
		code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+id.String()+"/cancel"), f.ownerA, "")
		if code != http.StatusOK || body["status"] != shop.StatusCancelled {
			t.Fatalf("cancel = %d %v", code, body)
		}
	}

	t.Run("goods gone: parked for review", func(t *testing.T) {
		f := newShopPayPlusFixture(t)
		_, variant := f.openShop(4500, 1)
		late := f.placed(variant, 1)
		cancel(f, late.ID)
		// Another buyer takes the last bottle and pays for it.
		other := f.placed(variant, 1)
		if _, code, resp := f.payCallback(other.OrderToken); code != http.StatusOK {
			t.Fatalf("other buyer's callback = %d %s", code, resp)
		}

		_, code, resp := f.payCallback(late.OrderToken)
		if code != http.StatusOK {
			t.Fatalf("late callback = %d %s", code, resp)
		}
		got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA)
		if got.Status != shop.StatusManualReview || got.PaidAt == nil {
			t.Fatalf("late order = %s paid_at %v; want manual_review with the payment recorded", got.Status, got.PaidAt)
		}
		if v := f.variant(variant); v.Stock != 0 || v.Held != 0 {
			t.Fatalf("stock %d held %d after the late payment; want 0 and 0 (never oversold)", v.Stock, v.Held)
		}
		if n := f.outboxCount(shop.EventOrderManualReview, late.ID); n != 1 {
			t.Fatalf("manual_review events = %d; want 1", n)
		}
		if n := f.outboxCount(shop.EventOrderPaid, late.ID); n != 0 {
			t.Fatalf("paid events of the parked order = %d; want 0", n)
		}
		_, pi := f.intentOf(late.OrderToken)
		if pi.State != "succeeded" {
			t.Fatalf("late payment intent = %s; want succeeded (the money arrived)", pi.State)
		}
	})

	t.Run("goods still free: paid", func(t *testing.T) {
		f := newShopPayPlusFixture(t)
		_, variant := f.openShop(4500, 2)
		late := f.placed(variant, 1)
		cancel(f, late.ID)

		_, code, resp := f.payCallback(late.OrderToken)
		if code != http.StatusOK || !strings.Contains(string(resp), `"shop_order_updated":true`) {
			t.Fatalf("late callback = %d %s", code, resp)
		}
		got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA)
		if got.Status != shop.StatusPaid {
			t.Fatalf("late order = %s; want paid", got.Status)
		}
		if !strings.Contains(strings.ReplaceAll(string(got.Metadata), " ", ""), `"paid_after":"cancelled"`) {
			t.Fatalf("metadata = %s; want paid_after cancelled", got.Metadata)
		}
		if v := f.variant(variant); v.Stock != 1 || v.Held != 0 {
			t.Fatalf("stock %d held %d; want 1 and 0", v.Stock, v.Held)
		}
	})
}
