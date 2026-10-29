//go:build integration

package httpserver

// shop_integration_test.go — the merchandise shop (spec 08_architecture/47,
// lane L1) through the real router and a live database: product CRUD, the
// storefront checkout with its stock hold and the 409 shop.out_of_stock
// refusal, the payment webhook paying the order, the expiry sweep releasing
// the hold, a partial and then a full refund through the refund engine, and
// organization isolation. Only Stripe is stubbed (newStubStripe).
//
// Run against a FRESH migrated and seeded database (AGENTS.md CI-Integration
// recipe), e.g. arena_shop_l1:
//
//	go test -tags integration -run TestShop ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

const shopTestBase = "https://shop.arena-integration.test"

type shopFixture struct {
	t      *testing.T
	srv    *Server
	pool   *pgxpool.Pool
	q      *gen.Queries
	stripe *stubStripe

	orgA, orgB        uuid.UUID
	orgASlug          string
	ownerA, ownerB    string
	keyB              string
	channelA, configA uuid.UUID
	webhookSecretA    string
	users             []uuid.UUID
	mediaA            uuid.UUID
}

func newShopFixture(t *testing.T) *shopFixture {
	t.Helper()
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_shop_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	srv, secret := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.PublicTicketsBaseURL = shopTestBase
		c.CORSAllowedOrigins = []string{shopTestBase}
		c.StripeAPIBaseURL = stripe.baseURL()
		c.WidgetPaymentWindowSeconds = 1860
		c.WidgetPaymentGraceSeconds = 120
	})
	f := &shopFixture{t: t, srv: srv, pool: srv.pgxPool, q: gen.New(srv.pgxPool), stripe: stripe}
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	f.orgASlug = "shop-a-" + suffix
	orgA, err := f.q.InsertOrganization(ctx, "Shop A "+suffix, f.orgASlug, "IL", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization A: %v", err)
	}
	orgB, err := f.q.InsertOrganization(ctx, "Shop B "+suffix, "shop-b-"+suffix, "IL", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization B: %v", err)
	}
	f.orgA, f.orgB = orgA.ID, orgB.ID
	t.Cleanup(f.cleanup)

	mkUser := func(org uuid.UUID) string {
		u, err := f.q.InsertUser(ctx, "shop-owner-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		f.users = append(f.users, u.ID)
		if _, err := f.q.InsertMembership(ctx, u.ID, org, "org_admin"); err != nil {
			t.Fatalf("InsertMembership: %v", err)
		}
		tok, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}
	f.ownerA, f.ownerB = mkUser(f.orgA), mkUser(f.orgB)

	keyOwner, err := f.q.InsertUser(ctx, "shop-keyowner-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	f.users = append(f.users, keyOwner.ID)
	_, f.keyB, err = apikeys.Issue(ctx, apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
		OrgID: f.orgB, Name: "shop-" + suffix, Scopes: []string{"shop.read", "shop.manage", "refund.create", "refund.approve"}, CreatedBy: keyOwner.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}

	f.channelA, f.configA, f.mediaA = uuid.New(), uuid.New(), uuid.New()
	f.webhookSecretA = "whsec_shop_" + suffix
	f.exec(`INSERT INTO sales_channels (id, org_id, name, provider) VALUES ($1, $2, 'Shop channel', 'stripe')`, f.channelA, f.orgA)
	f.exec(`INSERT INTO payment_provider_configs (id, org_id, provider, mode, secrets, status, is_active)
	        VALUES ($1, $2, 'stripe', 'test', $3::jsonb, 'configured', true)`,
		f.configA, f.orgA, fmt.Sprintf(`{"api_key":"sk_test_shop_%s","webhook_secret":%q}`, suffix, f.webhookSecretA))
	f.exec(`INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
	        VALUES ($1, $2, 'shop_product', 'local', $3, 'image/png', 10, repeat('a', 64))`, f.mediaA, f.orgA, "shop-test/"+suffix+".png")
	return f
}

func (f *shopFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("fixture %q: %v", sql, err)
	}
}

func (f *shopFixture) cleanup() {
	c := context.Background()
	orgs := []uuid.UUID{f.orgA, f.orgB}
	for _, st := range []string{
		// shop.order_email jobs name the order in their payload and have no
		// FK: a leftover makes a generic worker_jobs drain (the auth e-mail
		// test) fail with "no handler for job type".
		`DELETE FROM worker_jobs WHERE job_type = 'shop.order_email' AND payload->>'org_id' IN (
		   SELECT id::text FROM organizations WHERE id = ANY($1))`,
		`DELETE FROM sales_notification_subscriptions WHERE org_id = ANY($1)`,
		`DELETE FROM sales_notification_deliveries WHERE key IN (
		   SELECT 'shop_paid:' || id::text FROM shop_orders WHERE org_id = ANY($1)
		   UNION ALL SELECT 'shop_review:' || id::text FROM shop_orders WHERE org_id = ANY($1)
		   UNION ALL SELECT 'shop_refund:' || id::text || ':' || refunded_minor::text FROM shop_orders WHERE org_id = ANY($1))`,
		`DELETE FROM outbox_events WHERE aggregate_type = 'shop_order'
		   AND aggregate_id IN (SELECT id::text FROM shop_orders WHERE org_id = ANY($1))`,
		`DELETE FROM shop_stock_moves WHERE org_id = ANY($1)`,
		`DELETE FROM shop_order_items WHERE order_id IN (SELECT id FROM shop_orders WHERE org_id = ANY($1))`,
		`UPDATE shop_orders SET payment_intent_id = NULL WHERE org_id = ANY($1)`,
		`DELETE FROM refunds WHERE org_id = ANY($1)`,
		`DELETE FROM payment_intent_events WHERE payment_intent_id IN (SELECT id FROM payment_intents WHERE org_id = ANY($1))`,
		`DELETE FROM payment_intents WHERE org_id = ANY($1)`,
		`DELETE FROM shop_orders WHERE org_id = ANY($1)`,
		`DELETE FROM customer_org_links WHERE org_id = ANY($1)`,
		`DELETE FROM shop_product_images WHERE product_id IN (SELECT id FROM shop_products WHERE org_id = ANY($1))`,
		`DELETE FROM shop_variants WHERE product_id IN (SELECT id FROM shop_products WHERE org_id = ANY($1))`,
		`DELETE FROM shop_products WHERE org_id = ANY($1)`,
		`DELETE FROM shop_pages WHERE org_id = ANY($1)`,
		`DELETE FROM shop_settings WHERE org_id = ANY($1)`,
		`DELETE FROM media_objects WHERE org_id = ANY($1)`,
		`DELETE FROM payment_provider_configs WHERE org_id = ANY($1)`,
		`DELETE FROM api_keys WHERE org_id = ANY($1)`,
		`DELETE FROM sales_channels WHERE org_id = ANY($1)`,
		`DELETE FROM memberships WHERE org_id = ANY($1)`,
		`DELETE FROM organizations WHERE id = ANY($1)`,
	} {
		if _, err := f.pool.Exec(c, st, orgs); err != nil {
			fmt.Printf("shop cleanup %q: %v\n", st, err)
		}
	}
	if _, err := f.pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, f.users); err != nil {
		fmt.Printf("shop cleanup users: %v\n", err)
	}
}

// do sends one request through the real router.
func (f *shopFixture) do(method, path, token, body string) (int, map[string]any, http.Header) {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.srv.router.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(out) == 0 && len(raw) > 0 {
		out["_raw"] = string(raw)
	}
	return rec.Code, out, rec.Header()
}

func (f *shopFixture) orgPath(org uuid.UUID, rest string) string {
	return "/v1/organizations/" + org.String() + "/shop" + rest
}

func shopErrCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

// openShop saves settings, opens the storefront (the platform operator's
// switch, set directly) and creates one active product with one variant of
// the given price and stock; it returns the product id and the variant id.
func (f *shopFixture) openShop(price, stock int) (string, string) {
	f.t.Helper()
	code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA,
		`{"currency":"ILS","shipping_fee_minor":5000,"shipping_label":"Courier","country_iso2":"il"}`)
	if code != http.StatusOK {
		f.t.Fatalf("PUT settings = %d %v", code, body)
	}
	f.exec(`UPDATE shop_settings SET enabled = true WHERE org_id = $1`, f.orgA)
	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA, fmt.Sprintf(
		`{"title":"Rosé %s","status":"active","variants":[{"price_minor":%d,"stock":%d,"sku":"ROSE"}]}`,
		uuid.NewString()[:6], price, stock))
	if code != http.StatusCreated {
		f.t.Fatalf("POST product = %d %v", code, body)
	}
	vs := body["variants"].([]any)
	return body["id"].(string), vs[0].(map[string]any)["id"].(string)
}

func (f *shopFixture) checkout(variantID string, qty int) (int, map[string]any) {
	f.t.Helper()
	code, body, _ := f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(`{
		"items":[{"variant_id":%q,"qty":%d}],
		"buyer":{"name":"Anna","email":"anna-%s@example.test","phone":"+97250%07d"},
		"ship":{"address1":"Herzl 1","city":"Haifa","postal":"3100000","country":"IL"},
		"delivery_note":"call before","locale":"ru","return_url":"%s/checkout"}`,
		variantID, qty, uuid.NewString()[:6], rand.IntN(10_000_000), shopTestBase))
	return code, body
}

// pay posts a signed checkout.session.completed for the order's hosted page
// to the organization's per-config webhook route.
func (f *shopFixture) pay(orderToken string) {
	f.t.Helper()
	o, err := f.q.GetShopOrderByToken(context.Background(), orderToken)
	if err != nil || o.PaymentIntentID == nil {
		f.t.Fatalf("order of token: %v (payment %v)", err, o.PaymentIntentID)
	}
	pi, err := f.q.GetPaymentIntentByID(context.Background(), *o.PaymentIntentID)
	if err != nil || pi.ProviderPaymentID == nil {
		f.t.Fatalf("payment intent: %v", err)
	}
	event := sessionCompletedEvent(*pi.ProviderPaymentID, "pi_shop_"+uuid.NewString()[:10], "paid")
	code, body := postSignedWebhookTo(f.t, f.srv, "/v1/payment-intents/webhook/"+f.configA.String(), event, f.webhookSecretA)
	if code != http.StatusOK || !strings.Contains(string(body), `"shop_order_updated":true`) {
		f.t.Fatalf("paid webhook = %d %s; want 200 with shop_order_updated", code, body)
	}
}

func (f *shopFixture) variant(id string) gen.ShopVariantRow {
	f.t.Helper()
	var productID uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT product_id FROM shop_variants WHERE id = $1`, id).Scan(&productID); err != nil {
		f.t.Fatalf("variant product: %v", err)
	}
	vs, err := f.q.ListShopVariants(context.Background(), []uuid.UUID{productID})
	if err != nil {
		f.t.Fatalf("ListShopVariants: %v", err)
	}
	for _, v := range vs {
		if v.ID.String() == id {
			return v
		}
	}
	f.t.Fatalf("variant %s not found", id)
	return gen.ShopVariantRow{}
}

func (f *shopFixture) outboxCount(eventType string, orderID uuid.UUID) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events
		WHERE event_type = $1 AND aggregate_type = 'shop_order' AND aggregate_id = $2::text`, eventType, orderID).Scan(&n); err != nil {
		f.t.Fatalf("outbox count: %v", err)
	}
	return n
}

func TestShop_ProductCRUD(t *testing.T) {
	f := newShopFixture(t)

	code, body, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/settings"), f.ownerA, "")
	if code != http.StatusOK || body["configured"] != false || body["currency"] != "EUR" {
		t.Fatalf("GET settings before saving = %d %v; want the unconfigured defaults", code, body)
	}
	// The storefront is closed until the settings are saved.
	if code, body, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug, "", ""); code != http.StatusNotFound || shopErrCode(body) != "shop.not_found" {
		t.Fatalf("public catalog of a closed shop = %d %v; want 404 shop.not_found", code, body)
	}
	// Only the platform operator sets the domain.
	if code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA,
		`{"currency":"ILS","shop_domain":"shop-a.example.test"}`); code != http.StatusForbidden || shopErrCode(body) != "shop.domain_superadmin_only" {
		t.Fatalf("owner setting the domain = %d %v; want 403", code, body)
	}

	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA,
		`{"title":"Cabernet Sauvignon 2021","body_html":"<p>Dry red</p>","variants":[{"title":"750 ml","price_minor":12000,"stock":5},{"title":"Magnum","price_minor":25000,"stock":1,"track_stock":true}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST product = %d %v", code, body)
	}
	id := body["id"].(string)
	if body["slug"] != "cabernet-sauvignon-2021" || body["status"] != "draft" || len(body["variants"].([]any)) != 2 {
		t.Fatalf("created product = %v; want the derived slug, draft, two variants", body)
	}
	// Creating a product opens the settings row.
	if code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/settings"), f.ownerA, ""); body["configured"] != true {
		t.Fatalf("settings after the first product = %d %v; want configured", code, body)
	}
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA,
		`{"title":"Another","slug":"cabernet-sauvignon-2021"}`); code != http.StatusConflict || shopErrCode(body) != "shop.duplicate_slug" {
		t.Fatalf("duplicate slug = %d %v; want 409 shop.duplicate_slug", code, body)
	}

	code, body, _ = f.do(http.MethodPatch, f.orgPath(f.orgA, "/products/"+id), f.ownerA, `{"title":"Cabernet 2021","status":"active"}`)
	if code != http.StatusOK || body["title"] != "Cabernet 2021" || body["status"] != "active" || body["slug"] != "cabernet-sauvignon-2021" {
		t.Fatalf("PATCH product = %d %v", code, body)
	}

	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, "/products/"+id+"/images"), f.ownerA, fmt.Sprintf(`{"media_id":%q,"alt":"bottle"}`, f.mediaA))
	if code != http.StatusOK || len(body["images"].([]any)) != 1 {
		t.Fatalf("POST image = %d %v", code, body)
	}
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products/"+id+"/images"), f.ownerA, fmt.Sprintf(`{"media_id":%q}`, uuid.New())); code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_media" {
		t.Fatalf("POST unknown image = %d %v; want 422", code, body)
	}

	// Replace the variants: keep the first with new stock, drop the magnum,
	// add a gift box.
	first := body["variants"].([]any)[0].(map[string]any)["id"].(string)
	code, body, _ = f.do(http.MethodPut, f.orgPath(f.orgA, "/products/"+id+"/variants"), f.ownerA, fmt.Sprintf(
		`{"variants":[{"id":%q,"title":"750 ml","price_minor":11000,"stock":8},{"title":"Gift box","price_minor":30000,"stock":2}]}`, first))
	if code != http.StatusOK {
		t.Fatalf("PUT variants = %d %v", code, body)
	}
	vs := body["variants"].([]any)
	if len(vs) != 2 || vs[0].(map[string]any)["id"] != first || vs[0].(map[string]any)["stock"].(float64) != 8 {
		t.Fatalf("variants after replace = %v", vs)
	}
	var manual int
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(sum(delta), 0) FROM shop_stock_moves
		WHERE variant_id = $1 AND reason = 'manual'`, first).Scan(&manual); err != nil || manual != 8 {
		t.Fatalf("manual stock journal of the first variant = %d (%v); want 5 + 3 = 8", manual, err)
	}

	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/products"), f.ownerA, "")
	if code != http.StatusOK || len(body["products"].([]any)) != 1 {
		t.Fatalf("GET products = %d %v", code, body)
	}

	if code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/pages/about"), f.ownerA, `{"title":"About us","body_html":"<p>Winery</p>"}`); code != http.StatusOK {
		t.Fatalf("PUT page = %d %v", code, body)
	}
	f.exec(`UPDATE shop_settings SET enabled = true WHERE org_id = $1`, f.orgA)
	code, body, hdr := f.do(http.MethodGet, "/v1/public/shop/pages/"+f.orgASlug, "", "")
	if code != http.StatusOK || len(body["pages"].([]any)) != 1 || hdr.Get("Cache-Control") != "public, max-age=30" {
		t.Fatalf("public pages = %d %v %v", code, body, hdr)
	}

	// A page answers its old address too, and always names the canonical slug.
	f.exec(`UPDATE shop_pages SET legacy_slug = 'о-нас' WHERE org_id = $1 AND slug = 'about'`, f.orgA)
	for _, key := range []string{"about", "%D0%BE-%D0%BD%D0%B0%D1%81", "%D0%9E-%D0%9D%D0%90%D0%A1"} {
		code, body, hdr = f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug+"/pages/"+key, "", "")
		page, _ := body["page"].(map[string]any)
		if code != http.StatusOK || page["slug"] != "about" || page["legacy_slug"] != "о-нас" || page["title"] != "About us" || hdr.Get("Cache-Control") != "public, max-age=30" {
			t.Fatalf("public page %q = %d %v %v", key, code, body, hdr)
		}
	}
	if code, body, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug+"/pages/nope", "", ""); code != http.StatusNotFound || shopErrCode(body) != "shop.page_not_found" {
		t.Fatalf("unknown public page = %d %v", code, body)
	}
	// The slug wins when another page uses the same word as an old address.
	if code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/pages/%D0%BE-%D0%BD%D0%B0%D1%81"), f.ownerA, `{"title":"Real slug","body_html":"<p>x</p>"}`); code != http.StatusOK {
		t.Fatalf("PUT page = %d %v", code, body)
	}
	code, body, _ = f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug+"/pages/%D0%BE-%D0%BD%D0%B0%D1%81", "", "")
	if page, _ := body["page"].(map[string]any); code != http.StatusOK || page["title"] != "Real slug" {
		t.Fatalf("slug must win over an alias = %d %v", code, body)
	}

	code, body, hdr = f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug+"/products/cabernet-sauvignon-2021", "", "")
	if code != http.StatusOK || !strings.Contains(strings.Join(hdr.Values("Vary"), ","), "Origin") {
		t.Fatalf("public product = %d %v %v", code, body, hdr)
	}

	if code, _, _ := f.do(http.MethodDelete, f.orgPath(f.orgA, "/products/"+id), f.ownerA, ""); code != http.StatusNoContent {
		t.Fatalf("DELETE product = %d", code)
	}
	if code, body, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/products/"+id), f.ownerA, ""); code != http.StatusNotFound || shopErrCode(body) != "shop.product_not_found" {
		t.Fatalf("GET deleted product = %d %v", code, body)
	}
}

func TestShop_CheckoutHoldsStockAndWebhookPaysTheOrder(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)

	code, body, hdr := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug, "", "")
	if code != http.StatusOK || hdr.Get("Cache-Control") != "public, max-age=30" {
		t.Fatalf("public catalog = %d %v %v", code, body, hdr)
	}
	if ps := body["products"].([]any); len(ps) != 1 {
		t.Fatalf("catalog products = %v", ps)
	}

	code, body = f.checkout(variant, 2)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	if !strings.HasPrefix(body["payment_url"].(string), "https://checkout.stripe.test/") {
		t.Fatalf("payment_url = %v; want the provider-hosted page", body["payment_url"])
	}
	if body["total"].(float64) != 2*4500+5000 || body["status"] != "pending" {
		t.Fatalf("checkout answer = %v; want total 14000 pending", body)
	}
	form := f.stripe.lastForm(t)
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if form.Get("client_reference_id") != o.ID.String() || form.Get("line_items[0][price_data][unit_amount]") != "14000" {
		t.Fatalf("stripe got reference %q amount %q; want the shop order id and 14000",
			form.Get("client_reference_id"), form.Get("line_items[0][price_data][unit_amount]"))
	}
	if v := f.variant(variant); v.Held != 2 || shop.Available(v) != 1 {
		t.Fatalf("after checkout held %d available %d; want 2 and 1", v.Held, shop.Available(v))
	}

	// The second cart cannot be served and holds nothing.
	code, body = f.checkout(variant, 2)
	if code != http.StatusConflict || shopErrCode(body) != "shop.out_of_stock" {
		t.Fatalf("second checkout = %d %v; want 409 shop.out_of_stock", code, body)
	}
	details := body["error"].(map[string]any)["details"].(map[string]any)["items"].([]any)
	if len(details) != 1 || details[0].(map[string]any)["available"].(float64) != 1 {
		t.Fatalf("out-of-stock details = %v", details)
	}

	code, body, _ = f.do(http.MethodGet, "/v1/public/shop/checkout/"+token, "", "")
	if code != http.StatusOK || body["status"] != "pending" || body["payment_url"] == "" {
		t.Fatalf("status before paying = %d %v", code, body)
	}

	f.pay(token)
	f.pay2xIdempotent(token)

	code, body, _ = f.do(http.MethodGet, "/v1/public/shop/checkout/"+token, "", "")
	if code != http.StatusOK || body["status"] != "paid" || body["payment_url"] != "" {
		t.Fatalf("status after paying = %d %v", code, body)
	}
	v := f.variant(variant)
	if v.Stock != 1 || v.Held != 0 {
		t.Fatalf("after payment stock %d held %d; want 1 and 0", v.Stock, v.Held)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("v1.shop.order.paid events = %d; want exactly 1", n)
	}
	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/orders/"+o.ID.String()), f.ownerA, "")
	if code != http.StatusOK || body["status"] != "paid" || body["payment"] == nil {
		t.Fatalf("org order = %d %v", code, body)
	}
	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/orders?status=paid"), f.ownerA, "")
	if code != http.StatusOK || len(body["orders"].([]any)) != 1 {
		t.Fatalf("order list = %d %v", code, body)
	}
	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/courier-text"), f.ownerA, "")
	if code != http.StatusOK || !strings.Contains(body["_raw"].(string), "Herzl 1") {
		t.Fatalf("courier text = %d %v", code, body)
	}
	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship"), f.ownerA, `{"tracking_note":"TRK1"}`)
	if code != http.StatusOK || body["status"] != "shipped" || f.outboxCount(shop.EventOrderShipped, o.ID) != 1 {
		t.Fatalf("ship = %d %v", code, body)
	}
	// A paid order is refunded, never cancelled.
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/cancel"), f.ownerA, ""); code != http.StatusConflict {
		t.Fatalf("cancel of a shipped order = %d %v; want 409", code, body)
	}
}

// pay2xIdempotent replays the same kind of event for an already paid order:
// the payment intent is terminal, nothing moves twice.
func (f *shopFixture) pay2xIdempotent(token string) {
	f.t.Helper()
	o, _ := f.q.GetShopOrderByToken(context.Background(), token)
	pi, _ := f.q.GetPaymentIntentByID(context.Background(), *o.PaymentIntentID)
	event := sessionCompletedEvent(*pi.ProviderPaymentID, "pi_shop_replay", "paid")
	code, body := postSignedWebhookTo(f.t, f.srv, "/v1/payment-intents/webhook/"+f.configA.String(), event, f.webhookSecretA)
	if code != http.StatusOK || strings.Contains(string(body), `"processed":true`) {
		f.t.Fatalf("replayed webhook = %d %s; want an acknowledged no-op", code, body)
	}
}

func TestShop_ExpirySweepReleasesTheHold(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	code, body := f.checkout(variant, 3)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	o, _ := f.q.GetShopOrderByToken(context.Background(), token)
	if a := shop.Available(f.variant(variant)); a != 0 {
		t.Fatalf("available while held = %d; want 0", a)
	}
	f.exec(`UPDATE shop_orders SET expires_at = now() - interval '1 minute' WHERE id = $1`, o.ID)

	// The worker handler, as arena-worker registers it (without a scheduler).
	handler := shop.NewSweepHandler(shop.SweepOptions{Service: shop.New(f.pool, nil)})
	if err := handler(context.Background(), nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if got.Status != shop.StatusExpired {
		t.Fatalf("status after sweep = %s; want expired", got.Status)
	}
	if v := f.variant(variant); v.Stock != 3 || shop.Available(v) != 3 {
		t.Fatalf("after sweep stock %d available %d; want 3 and 3", v.Stock, shop.Available(v))
	}
	var released int
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(sum(delta), 0) FROM shop_stock_moves
		WHERE order_id = $1 AND reason = 'release'`, o.ID).Scan(&released); err != nil || released != 3 {
		t.Fatalf("release journal = %d (%v); want 3", released, err)
	}
	code, body, _ = f.do(http.MethodGet, "/v1/public/shop/checkout/"+token, "", "")
	if code != http.StatusOK || body["status"] != "expired" {
		t.Fatalf("status after sweep = %d %v", code, body)
	}
	// A pending order can be cancelled; an expired one cannot.
	if code, _, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/cancel"), f.ownerA, ""); code != http.StatusConflict {
		t.Fatalf("cancel of an expired order = %d; want 409", code)
	}
}

func TestShop_PartialThenFullRefund(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	code, body := f.checkout(variant, 2)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	f.pay(token)
	o, _ := f.q.GetShopOrderByToken(context.Background(), token)
	path := f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund")

	if code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"amount_minor":999999}`); code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_refund_amount" {
		t.Fatalf("too large refund = %d %v; want 422", code, body)
	}
	code, body, _ = f.do(http.MethodPost, path, f.ownerA, `{"amount_minor":5000,"reason":"one bottle broken"}`)
	if code != http.StatusOK {
		t.Fatalf("partial refund = %d %v", code, body)
	}
	refund := body["refund"].(map[string]any)
	order := body["order"].(map[string]any)
	if refund["state"] != "succeeded" || order["status"] != shop.StatusPartiallyRefunded || order["refunded_minor"].(float64) != 5000 {
		t.Fatalf("after partial refund refund %v order status %v refunded %v", refund, order["status"], order["refunded_minor"])
	}
	if n := f.outboxCount(shop.EventOrderRefunded, o.ID); n != 1 {
		t.Fatalf("refunded events = %d; want 1", n)
	}
	if v := f.variant(variant); v.Stock != 1 {
		t.Fatalf("stock after refund = %d; want 1 (never put back automatically)", v.Stock)
	}

	code, body, _ = f.do(http.MethodPost, path, f.ownerA, ``)
	if code != http.StatusOK {
		t.Fatalf("refund of the rest = %d %v", code, body)
	}
	order = body["order"].(map[string]any)
	if order["status"] != shop.StatusRefunded || order["refunded_minor"].(float64) != 14000 {
		t.Fatalf("after full refund status %v refunded %v", order["status"], order["refunded_minor"])
	}
	if code, body, _ := f.do(http.MethodPost, path, f.ownerA, ``); code != http.StatusConflict {
		t.Fatalf("refund of a refunded order = %d %v; want 409", code, body)
	}
	var refunded int64
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount), 0) FROM refunds
		WHERE payment_intent_id = $1 AND state = 'succeeded'`, *o.PaymentIntentID).Scan(&refunded); err != nil || refunded != 14000 {
		t.Fatalf("succeeded refunds = %d (%v); want 14000", refunded, err)
	}
}

func TestShop_OrganizationIsolation(t *testing.T) {
	f := newShopFixture(t)
	product, variant := f.openShop(4500, 3)
	code, body := f.checkout(variant, 1)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	o, _ := f.q.GetShopOrderByToken(context.Background(), body["order_token"].(string))

	// Organization B's key, on its own organization's routes, cannot see or
	// touch A's rows: each is the route's own 404.
	for _, c := range []struct{ method, path, body, code string }{
		{http.MethodGet, f.orgPath(f.orgB, "/products/"+product), "", "shop.product_not_found"},
		{http.MethodPatch, f.orgPath(f.orgB, "/products/"+product), `{"title":"stolen"}`, "shop.product_not_found"},
		{http.MethodPut, f.orgPath(f.orgB, "/products/"+product+"/variants"), `{"variants":[{"price_minor":1,"stock":1}]}`, "shop.product_not_found"},
		{http.MethodGet, f.orgPath(f.orgB, "/orders/"+o.ID.String()), "", "shop.order_not_found"},
		{http.MethodPost, f.orgPath(f.orgB, "/orders/"+o.ID.String()+"/ship"), "", "shop.order_not_found"},
		{http.MethodPost, f.orgPath(f.orgB, "/orders/"+o.ID.String()+"/cancel"), "", "shop.order_not_found"},
		{http.MethodPost, f.orgPath(f.orgB, "/orders/"+o.ID.String()+"/refund"), "", "shop.order_not_found"},
	} {
		code, body, _ := f.do(c.method, c.path, f.keyB, c.body)
		if code != http.StatusNotFound || shopErrCode(body) != c.code {
			t.Errorf("%s %s with org B's key = %d %v; want 404 %s", c.method, c.path, code, body, c.code)
		}
	}
	// On A's own routes B's key and B's owner are refused outright.
	for _, tok := range []string{f.keyB, f.ownerB} {
		if code, _, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/products/"+product), tok, ""); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("A's product through another organization's credential = %d; want 403 or 404", code)
		}
	}
	// B's list sees nothing of A's.
	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgB, "/orders"), f.keyB, "")
	if code != http.StatusOK || len(body["orders"].([]any)) != 0 {
		t.Fatalf("B's order list = %d %v; want empty", code, body)
	}
	// Nothing moved.
	got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if got.Status != shop.StatusPendingPayment {
		t.Fatalf("A's order status = %s; want pending_payment", got.Status)
	}
}
