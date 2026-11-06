//go:build integration

package httpserver

// shop_module_integration_test.go — the shop as a paid module (spec
// 08_architecture/61, stage M1) through the real router and a live database:
// the module gate on the organization shop routes (full, orders_only, none,
// for the owner, the manager and a platform superadmin alike), the public
// storefront closing with the module while an existing order's page keeps
// working, the organization's own storefront switch with its blockers, the
// module fields of /v1/me and of the organization list, and the superadmin's
// module routes with their audit rows.
//
// Run against a FRESH migrated and seeded scratch database:
//
//	go test -tags integration -count=1 -p 1 -run 'TestShopModule|TestOrgModules' ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// shopModuleEnv is a shop fixture plus a manager of organization A and a
// platform superadmin.
type shopModuleEnv struct {
	*shopFixture
	managerA string
	super    string
}

func newShopModuleEnv(t *testing.T) *shopModuleEnv {
	t.Helper()
	return newShopModuleEnvWith(t, nil)
}

// newShopModuleEnvWith is newShopModuleEnv with one more change to the
// server's configuration.
func newShopModuleEnvWith(t *testing.T, extra func(*config.Config)) *shopModuleEnv {
	t.Helper()
	f := newShopFixtureWith(t, extra)
	e := &shopModuleEnv{shopFixture: f}
	ctx := context.Background()
	// The secret productionIntegrationServerCfg signs with.
	const secret = "integration-test-secret-32-bytes!!"
	mint := func(id uuid.UUID) string {
		tok, _, err := auth.IssueJWT(secret, id, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}
	mgr, err := f.q.InsertUser(ctx, "shop-mgr-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	f.users = append(f.users, mgr.ID)
	if _, err := f.q.InsertMembership(ctx, mgr.ID, f.orgA, "organizer"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	e.managerA = mint(mgr.ID)
	sup, err := f.q.InsertUser(ctx, "shop-super-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	f.users = append(f.users, sup.ID)
	f.exec(`INSERT INTO user_roles (user_id, role_id, org_id)
	        SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, sup.ID)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1`, sup.ID)
	})
	e.super = mint(sup.ID)
	return e
}

// doReason is shopFixture.do with an X-Admin-Reason header.
func (e *shopModuleEnv) doReason(method, path, token, body, reason string) (int, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	rec := httptest.NewRecorder()
	e.srv.router.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(out) == 0 && len(raw) > 0 {
		out["_raw"] = string(raw)
	}
	return rec.Code, out
}

func dayStr(t time.Time) string { return t.UTC().Format("2006-01-02") }

// setShopPeriod replaces the organization's shop periods with one from a
// month ago to endsOn ("" = no end).
func (e *shopModuleEnv) setShopPeriod(org uuid.UUID, endsOn string) {
	e.t.Helper()
	e.exec(`DELETE FROM org_module_periods WHERE org_id = $1`, org)
	start := dayStr(time.Now().AddDate(0, -1, 0))
	if endsOn == "" {
		e.exec(`INSERT INTO org_module_periods (org_id, module, starts_on, reason) VALUES ($1, 'shop', $2::date, 'test')`, org, start)
		return
	}
	e.exec(`INSERT INTO org_module_periods (org_id, module, starts_on, ends_on, reason) VALUES ($1, 'shop', $2::date, $3::date, 'test')`, org, start, endsOn)
}

// expectGate fails unless the route answered want; for 404 the code must be
// shop.module_not_enabled.
func (e *shopModuleEnv) expectGate(want int, method, path, token, reason, body string) map[string]any {
	e.t.Helper()
	code, out := e.doReason(method, path, token, body, reason)
	if code != want {
		e.t.Fatalf("%s %s = %d %v; want %d", method, path, code, out, want)
	}
	if want == http.StatusNotFound && shopErrCode(out) != "shop.module_not_enabled" {
		e.t.Fatalf("%s %s = 404 %v; want shop.module_not_enabled", method, path, out)
	}
	return out
}

func TestShopModule_GateFollowsTheModule(t *testing.T) {
	e := newShopModuleEnv(t)
	settings := e.orgPath(e.orgA, "/settings")
	orders := e.orgPath(e.orgA, "/orders")
	callers := []struct {
		name, token, reason string
	}{
		{"owner", e.ownerA, ""},
		{"manager", e.managerA, ""},
		{"superadmin", e.super, "spec 61 module gate test"},
	}

	// Full: every caller reaches the settings and the orders.
	for _, c := range callers {
		out := e.expectGate(http.StatusOK, http.MethodGet, settings, c.token, c.reason, "")
		if out["module_active"] != true || out["shop_mode"] != "full" {
			t.Fatalf("%s settings in full mode: %v", c.name, out)
		}
		e.expectGate(http.StatusOK, http.MethodGet, orders, c.token, c.reason, "")
	}

	// No module: the shop does not exist, for the superadmin too.
	e.exec(`DELETE FROM org_module_periods WHERE org_id = $1`, e.orgA)
	for _, c := range callers {
		e.expectGate(http.StatusNotFound, http.MethodGet, settings, c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, orders, c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, e.orgPath(e.orgA, "/products"), c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodPut, e.orgPath(e.orgA, "/storefront"), c.token, c.reason, `{"enabled":false}`)
		e.expectGate(http.StatusNotFound, http.MethodGet, e.orgPath(e.orgA, "/referral-program"), c.token, c.reason, "")
	}

	// Orders only: the module ended yesterday while an order is open.
	e.setShopPeriod(e.orgA, "")
	_, variant := e.openShop(4500, 3)
	code, placed := e.checkout(variant, 1)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, placed)
	}
	token, _ := placed["order_token"].(string)
	order, err := e.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		t.Fatalf("order of token: %v", err)
	}
	e.setShopPeriod(e.orgA, dayStr(time.Now().AddDate(0, 0, -1)))
	for _, c := range callers {
		e.expectGate(http.StatusOK, http.MethodGet, orders, c.token, c.reason, "")
		e.expectGate(http.StatusOK, http.MethodGet, orders+"/counts", c.token, c.reason, "")
		e.expectGate(http.StatusOK, http.MethodGet, orders+"/"+order.ID.String(), c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, settings, c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, e.orgPath(e.orgA, "/products"), c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, e.orgPath(e.orgA, "/pages"), c.token, c.reason, "")
	}
	// The organization's identity answers say orders_only.
	code, me := e.doReason(http.MethodGet, "/v1/me", e.ownerA, "", "")
	if code != http.StatusOK || meShopMode(t, me, e.orgA) != "orders_only" {
		t.Fatalf("/v1/me in orders_only = %d %v", code, me)
	}

	// The storefront closed with the module although enabled is still true;
	// the buyer's order page still works.
	var enabled bool
	if err := e.pool.QueryRow(context.Background(), `SELECT enabled FROM shop_settings WHERE org_id = $1`, e.orgA).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("enabled must stay as it was: %v %v", enabled, err)
	}
	if code, body, _ := e.do(http.MethodGet, "/v1/public/shop/"+e.orgASlug, "", ""); code != http.StatusNotFound || shopErrCode(body) != "shop.not_found" {
		t.Fatalf("catalog of a disconnected shop = %d %v; want 404 shop.not_found", code, body)
	}
	if code, body, _ := e.do(http.MethodGet, "/v1/public/shop/pages/"+e.orgASlug, "", ""); code != http.StatusNotFound {
		t.Fatalf("pages of a disconnected shop = %d %v; want 404", code, body)
	}
	if code, body := e.checkout(variant, 1); code != http.StatusNotFound || shopErrCode(body) != "shop.not_found" {
		t.Fatalf("checkout in a disconnected shop = %d %v; want 404 shop.not_found", code, body)
	}
	if code, body, _ := e.do(http.MethodGet, "/v1/public/shop/checkout/"+token, "", ""); code != http.StatusOK {
		t.Fatalf("status page of an existing order = %d %v; want 200", code, body)
	}

	// The only order expires without ever being paid: nothing to read or
	// refund, the section is gone.
	e.exec(`UPDATE shop_orders SET status = 'expired' WHERE org_id = $1`, e.orgA)
	for _, c := range callers {
		e.expectGate(http.StatusNotFound, http.MethodGet, orders, c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, settings, c.token, c.reason, "")
	}

	// An order that was paid and has long been shipped keeps the order routes
	// open for good: the owner reads it and can still refund it (chargebacks,
	// late refund requests). Catalog and settings stay closed.
	e.exec(`UPDATE shop_orders SET status = 'shipped', paid_at = now(), shipped_at = now() WHERE org_id = $1`, e.orgA)
	for _, c := range callers {
		e.expectGate(http.StatusOK, http.MethodGet, orders, c.token, c.reason, "")
		e.expectGate(http.StatusOK, http.MethodGet, orders+"/"+order.ID.String(), c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, settings, c.token, c.reason, "")
		e.expectGate(http.StatusNotFound, http.MethodGet, e.orgPath(e.orgA, "/products"), c.token, c.reason, "")
	}
	// The refund route is past the module gate: whatever it answers for this
	// order (here the payment is not a real one), it is not the gate's 404.
	rcode, rbody, _ := e.do(http.MethodPost, orders+"/"+order.ID.String()+"/refund", e.ownerA, `{"reason":"late request"}`)
	if shopErrCode(rbody) == "shop.module_not_enabled" {
		t.Fatalf("refund of an old paid order hit the module gate: %d %v", rcode, rbody)
	}
}

// meShopMode is the shop_mode of org's membership in a /v1/me answer ("" for
// null, "missing" without the membership).
func meShopMode(t *testing.T, me map[string]any, org uuid.UUID) string {
	t.Helper()
	ms, _ := me["organization_memberships"].([]any)
	for _, raw := range ms {
		m, _ := raw.(map[string]any)
		if m["org_id"] != org.String() {
			continue
		}
		if _, ok := m["modules"].([]any); !ok {
			t.Fatalf("membership without a modules array: %v", m)
		}
		s, _ := m["shop_mode"].(string)
		return s
	}
	return "missing"
}

func blockersOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	var raw []any
	if e, ok := body["error"].(map[string]any); ok {
		d, _ := e["details"].(map[string]any)
		raw, _ = d["blockers"].([]any)
	} else {
		raw, _ = body["open_blockers"].([]any)
	}
	out := make([]string, 0, len(raw))
	for _, b := range raw {
		s, _ := b.(string)
		out = append(out, s)
	}
	return out
}

func TestShopModule_OrganizationOpensItsStorefront(t *testing.T) {
	e := newShopModuleEnv(t)
	storefront := e.orgPath(e.orgA, "/storefront")
	settings := e.orgPath(e.orgA, "/settings")

	// Nothing saved yet: every blocker is reported.
	code, body := e.doReason(http.MethodPut, storefront, e.managerA, `{"enabled":true}`, "")
	if code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.cannot_open" {
		t.Fatalf("opening an unsaved shop = %d %v; want 422 shop.cannot_open", code, body)
	}
	if got := strings.Join(blockersOf(t, body), ","); got != "settings_not_saved,no_active_product,payment_not_configured" {
		t.Fatalf("blockers of an unsaved shop = %s", got)
	}
	// Closing is always allowed, also with nothing to close.
	if code, body := e.doReason(http.MethodPut, storefront, e.managerA, `{"enabled":false}`, ""); code != http.StatusOK || body["enabled"] != false {
		t.Fatalf("closing an unsaved shop = %d %v", code, body)
	}
	if code, body := e.doReason(http.MethodPut, storefront, e.managerA, `{}`, ""); code != http.StatusBadRequest {
		t.Fatalf("storefront without enabled = %d %v; want 400", code, body)
	}

	// Saved, no product, unverified payment config.
	if code, body, _ := e.do(http.MethodPut, settings, e.managerA, `{"currency":"ILS"}`); code != http.StatusOK {
		t.Fatalf("manager saving the settings = %d %v", code, body)
	}
	code, body = e.doReason(http.MethodGet, settings, e.managerA, "", "")
	if code != http.StatusOK || body["can_open"] != false ||
		strings.Join(blockersOf(t, body), ",") != "no_active_product,payment_not_configured" {
		t.Fatalf("settings of a shop without products = %d %v", code, body)
	}
	code, body = e.doReason(http.MethodPut, storefront, e.managerA, `{"enabled":true}`, "")
	if code != http.StatusUnprocessableEntity || strings.Join(blockersOf(t, body), ",") != "no_active_product,payment_not_configured" {
		t.Fatalf("opening without products = %d %v", code, body)
	}

	// An active product; the payment config is still not verified.
	if code, body, _ := e.do(http.MethodPost, e.orgPath(e.orgA, "/products"), e.managerA,
		`{"title":"Mug","status":"active","variants":[{"price_minor":1500,"stock":5}]}`); code != http.StatusCreated {
		t.Fatalf("POST product = %d %v", code, body)
	}
	code, body = e.doReason(http.MethodPut, storefront, e.managerA, `{"enabled":true}`, "")
	if code != http.StatusUnprocessableEntity || strings.Join(blockersOf(t, body), ",") != "payment_not_configured" {
		t.Fatalf("opening with an unverified payment config = %d %v", code, body)
	}
	if code, body, _ := e.do(http.MethodGet, "/v1/public/shop/"+e.orgASlug, "", ""); code != http.StatusNotFound {
		t.Fatalf("catalog of a closed shop = %d %v", code, body)
	}

	// Verified: the manager opens it.
	e.exec(`UPDATE payment_provider_configs SET verification_status = 'ok' WHERE id = $1`, e.configA)
	code, body = e.doReason(http.MethodGet, settings, e.managerA, "", "")
	if code != http.StatusOK || body["can_open"] != true || len(blockersOf(t, body)) != 0 {
		t.Fatalf("settings of an openable shop = %d %v", code, body)
	}
	code, body = e.doReason(http.MethodPut, storefront, e.managerA, `{"enabled":true}`, "")
	if code != http.StatusOK || body["enabled"] != true || body["module_active"] != true {
		t.Fatalf("manager opening = %d %v", code, body)
	}
	if code, body, _ := e.do(http.MethodGet, "/v1/public/shop/"+e.orgASlug, "", ""); code != http.StatusOK {
		t.Fatalf("catalog of an open shop = %d %v", code, body)
	}
	var audits int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events
		WHERE action = 'v1.shop.storefront.update' AND resource_id = $1 AND metadata->>'enabled' = 'true'`, e.orgA.String()).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("storefront audit rows = %d %v; want 1", audits, err)
	}

	// The owner closes it; closing twice is fine.
	for i := 0; i < 2; i++ {
		if code, body := e.doReason(http.MethodPut, storefront, e.ownerA, `{"enabled":false}`, ""); code != http.StatusOK || body["enabled"] != false {
			t.Fatalf("owner closing (%d) = %d %v", i, code, body)
		}
	}
	// PUT settings opens under the same rules.
	if code, body, _ := e.do(http.MethodPut, settings, e.ownerA, `{"currency":"ILS","enabled":true}`); code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("owner opening through PUT settings = %d %v", code, body)
	}

	// The domain stays the superadmin's.
	code, body, _ = e.do(http.MethodPut, settings, e.managerA, `{"currency":"ILS","shop_domain":"shop.m1a.example"}`)
	if code != http.StatusForbidden || shopErrCode(body) != "shop.domain_superadmin_only" {
		t.Fatalf("manager setting the domain = %d %v; want 403 shop.domain_superadmin_only", code, body)
	}
	domain := "m1a-" + uuid.NewString()[:8] + ".example"
	code, body = e.doReason(http.MethodPut, settings, e.super, fmt.Sprintf(`{"currency":"ILS","shop_domain":%q}`, domain), "spec 61 domain test")
	if code != http.StatusOK || body["shop_domain"] != domain {
		t.Fatalf("superadmin setting the domain = %d %v", code, body)
	}
	e.exec(`UPDATE organizations SET shop_domain = NULL WHERE id = $1`, e.orgA)
}

func TestShopModule_MeAndOrganizationList(t *testing.T) {
	e := newShopModuleEnv(t)
	e.exec(`DELETE FROM org_module_periods WHERE org_id = $1`, e.orgB)

	code, me := e.doReason(http.MethodGet, "/v1/me", e.ownerA, "", "")
	if code != http.StatusOK || meShopMode(t, me, e.orgA) != "full" {
		t.Fatalf("/v1/me of owner A = %d %v", code, me)
	}
	ms := me["organization_memberships"].([]any)
	if mods := ms[0].(map[string]any)["modules"].([]any); len(mods) != 1 || mods[0] != "shop" {
		t.Fatalf("modules of A = %v", mods)
	}
	code, me = e.doReason(http.MethodGet, "/v1/me", e.ownerB, "", "")
	if code != http.StatusOK || meShopMode(t, me, e.orgB) != "" {
		t.Fatalf("/v1/me of owner B = %d %v; want shop_mode null", code, me)
	}
	if mods := me["organization_memberships"].([]any)[0].(map[string]any)["modules"].([]any); len(mods) != 0 {
		t.Fatalf("modules of B = %v; want []", mods)
	}

	// The superadmin's organization list (the bot's chooser).
	code, list := e.doReason(http.MethodGet, "/v1/organizations", e.super, "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/organizations = %d %v", code, list)
	}
	found := 0
	for _, raw := range list["organizations"].([]any) {
		o := raw.(map[string]any)
		mods, ok := o["modules"].([]any)
		if !ok {
			t.Fatalf("organization without a modules array: %v", o)
		}
		switch o["id"] {
		case e.orgA.String():
			found++
			if len(mods) != 1 || o["shop_mode"] != "full" {
				t.Fatalf("org A in the list: %v", o)
			}
		case e.orgB.String():
			found++
			if len(mods) != 0 || o["shop_mode"] != nil {
				t.Fatalf("org B in the list: %v", o)
			}
		}
	}
	if found != 2 {
		t.Fatalf("found %d of the two organizations in the superadmin's list", found)
	}
}

func TestOrgModules_AdminRoutes(t *testing.T) {
	e := newSec1Env(t)
	_, super := e.superadmin(t)
	org := e.org(t, "M1A")
	owner := e.user(t, "m1aowner")
	e.member(t, owner, org, "org_admin")
	ownerTok := e.token(t, owner)
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM org_module_periods WHERE org_id = $1`, org)
	})
	base := "/v1/admin/organizations/" + org.String() + "/modules"
	const reason = "spec 61 admin test"
	today := time.Now().UTC()

	// An owner is not a superadmin, with or without a reason.
	e.expect(t, http.StatusForbidden, http.MethodGet, base, ownerTok, "", "")
	e.expect(t, http.StatusForbidden, http.MethodPost, base+"/shop", ownerTok, reason, `{}`)
	// A write needs X-Admin-Reason.
	if body := e.expect(t, http.StatusBadRequest, http.MethodPost, base+"/shop", super, "", `{}`); sec1Code(body) != "superadmin.missing_reason" {
		t.Fatalf("connect without a reason: %s", body)
	}
	if body := e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, base+"/shop", super, reason, `{"starts_on":"tomorrow"}`); sec1Code(body) != "org_module.invalid" {
		t.Fatalf("bad date: %s", body)
	}

	body := e.expect(t, http.StatusCreated, http.MethodPost, base+"/shop", super, reason,
		`{"monthly_price_minor":1500,"currency":"eur","reason":"pilot client"}`)
	var created map[string]any
	_ = json.Unmarshal([]byte(body), &created)
	periodID, _ := created["id"].(string)
	if created["status"] != "in_force" || created["starts_on"] != dayStr(today) || created["currency"] != "EUR" || created["reason"] != "pilot client" {
		t.Fatalf("connected period: %v", created)
	}
	auditReason := func(action, id string) string {
		t.Helper()
		var got string
		if err := e.srv.pgxPool.QueryRow(context.Background(),
			`SELECT COALESCE(metadata->>'reason', '') FROM audit_events WHERE action = $1 AND resource_id = $2`, action, id).Scan(&got); err != nil {
			t.Fatalf("audit row %s %s: %v", action, id, err)
		}
		return got
	}
	if got := auditReason("v1.admin.org_module.connect", periodID); got != reason {
		t.Fatalf("connect audit reason = %q", got)
	}
	if body := e.expect(t, http.StatusConflict, http.MethodPost, base+"/shop", super, reason, `{}`); sec1Code(body) != "org_module.overlap" {
		t.Fatalf("overlap: %s", body)
	}

	// The list: the period, the shop state, the individual price.
	var listed map[string]any
	_ = json.Unmarshal([]byte(e.expect(t, http.StatusOK, http.MethodGet, base, super, "", "")), &listed)
	shopState, _ := listed["shop"].(map[string]any)
	if items, _ := listed["items"].([]any); len(items) != 1 || shopState["mode"] != "full" ||
		shopState["effective_monthly_minor"] != float64(1500) || shopState["price_source"] != "individual" || shopState["enabled"] != false {
		t.Fatalf("admin list: %v", listed)
	}

	// Reprice from tomorrow, free.
	tomorrow := dayStr(today.AddDate(0, 0, 1))
	body = e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/org-modules/"+periodID+"/reprice", super, reason,
		fmt.Sprintf(`{"from":%q,"monthly_price_minor":0,"currency":"EUR"}`, tomorrow))
	var repriced map[string]any
	_ = json.Unmarshal([]byte(body), &repriced)
	newID, _ := repriced["id"].(string)
	if newID == periodID || repriced["starts_on"] != tomorrow || repriced["monthly_price_minor"] != float64(0) {
		t.Fatalf("repriced period: %v", repriced)
	}
	if got := auditReason("v1.admin.org_module.reprice", newID); got != reason {
		t.Fatalf("reprice audit reason = %q", got)
	}

	// End the new period before it starts (it never takes effect), then the
	// old one already ends today.
	body = e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/org-modules/"+newID+"/end", super, reason,
		fmt.Sprintf(`{"ends_on":%q}`, dayStr(today)))
	if got := auditReason("v1.admin.org_module.end", newID); got != reason {
		t.Fatalf("end audit reason = %q (%s)", got, body)
	}
	if body := e.expect(t, http.StatusConflict, http.MethodPost, "/v1/admin/org-modules/"+periodID+"/end", super, reason,
		fmt.Sprintf(`{"ends_on":%q}`, dayStr(today))); sec1Code(body) != "org_module.already_ended" {
		t.Fatalf("ending an ended period: %s", body)
	}
	if body := e.expect(t, http.StatusNotFound, http.MethodPost, "/v1/admin/org-modules/"+uuid.NewString()+"/end", super, reason,
		fmt.Sprintf(`{"ends_on":%q}`, dayStr(today))); sec1Code(body) != "org_module.not_found" {
		t.Fatalf("ending an unknown period: %s", body)
	}
	e.expect(t, http.StatusForbidden, http.MethodPost, "/v1/admin/org-modules/"+periodID+"/end", ownerTok, reason,
		fmt.Sprintf(`{"ends_on":%q}`, dayStr(today)))

	// A platform superadmin reading the organizer's view needs a reason.
	if body := e.expect(t, http.StatusBadRequest, http.MethodGet, "/v1/organizations/"+org.String()+"/billing/modules", super, "", ""); sec1Code(body) != "superadmin.missing_reason" {
		t.Fatalf("superadmin without a reason: %s", body)
	}

	// The organizer's read-only view: no operator fields.
	var mine map[string]any
	_ = json.Unmarshal([]byte(e.expect(t, http.StatusOK, http.MethodGet,
		"/v1/organizations/"+org.String()+"/billing/modules", ownerTok, "", "")), &mine)
	items, _ := mine["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("organizer's periods: %v", mine)
	}
	for _, raw := range items {
		it := raw.(map[string]any)
		if _, leak := it["reason"]; leak {
			t.Fatalf("the organizer sees the reason: %v", it)
		}
		if _, leak := it["created_by"]; leak {
			t.Fatalf("the organizer sees created_by: %v", it)
		}
	}
	if mine["effective_monthly_minor"] != float64(1500) {
		t.Fatalf("organizer's price today: %v", mine)
	}
}

// A month already charged is never rewritten, so the routes take no date in
// the past: a start and a reprice from today, an end from yesterday (the
// same rules as white label).
func TestOrgModules_AdminRoutes_RefuseThePast(t *testing.T) {
	e := newSec1Env(t)
	_, super := e.superadmin(t)
	org := e.org(t, "M1P")
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM org_module_periods WHERE org_id = $1`, org)
	})
	base := "/v1/admin/organizations/" + org.String() + "/modules"
	const reason = "spec 61 past dates test"
	today := time.Now().UTC()
	periods := func() int {
		t.Helper()
		var n int
		if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM org_module_periods WHERE org_id = $1`, org).Scan(&n); err != nil {
			t.Fatalf("count periods: %v", err)
		}
		return n
	}

	// Connect from yesterday: refused, nothing written.
	if body := e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, base+"/shop", super, reason,
		fmt.Sprintf(`{"starts_on":%q,"monthly_price_minor":0,"currency":"EUR"}`, dayStr(today.AddDate(0, 0, -1)))); sec1Code(body) != "org_module.starts_on_in_past" {
		t.Fatalf("connect in the past: %s", body)
	}
	if n := periods(); n != 0 {
		t.Fatalf("a refused connect left %d periods", n)
	}
	// From today it works.
	var created map[string]any
	_ = json.Unmarshal([]byte(e.expect(t, http.StatusCreated, http.MethodPost, base+"/shop", super, reason,
		fmt.Sprintf(`{"starts_on":%q,"monthly_price_minor":1500,"currency":"EUR"}`, dayStr(today)))), &created)
	id, _ := created["id"].(string)

	// Reprice from yesterday: refused. From today: allowed.
	if body := e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, "/v1/admin/org-modules/"+id+"/reprice", super, reason,
		fmt.Sprintf(`{"from":%q,"monthly_price_minor":0,"currency":"EUR"}`, dayStr(today.AddDate(0, 0, -1)))); sec1Code(body) != "org_module.from_in_past" {
		t.Fatalf("reprice in the past: %s", body)
	}
	if n := periods(); n != 1 {
		t.Fatalf("a refused reprice changed the periods: %d", n)
	}
	var repriced map[string]any
	_ = json.Unmarshal([]byte(e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/org-modules/"+id+"/reprice", super, reason,
		fmt.Sprintf(`{"from":%q,"monthly_price_minor":0,"currency":"EUR"}`, dayStr(today)))), &repriced)
	newID, _ := repriced["id"].(string)

	// End two days ago: refused. Yesterday: allowed.
	if body := e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, "/v1/admin/org-modules/"+newID+"/end", super, reason,
		fmt.Sprintf(`{"ends_on":%q}`, dayStr(today.AddDate(0, 0, -2)))); sec1Code(body) != "org_module.ends_on_in_past" {
		t.Fatalf("end in the past: %s", body)
	}
	e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/org-modules/"+newID+"/end", super, reason,
		fmt.Sprintf(`{"ends_on":%q}`, dayStr(today.AddDate(0, 0, -1))))
}

// Reconnecting after a gap must not reopen the storefront by itself: the
// organization opens it again through the normal checks. A period that
// continues the previous one leaves the switch alone.
func TestOrgModules_ReconnectAfterGapClosesTheStorefront(t *testing.T) {
	e := newShopModuleEnv(t)
	storefront := e.orgPath(e.orgA, "/storefront")
	e.openShop(4500, 3)
	enabled := func() bool {
		t.Helper()
		var on bool
		if err := e.pool.QueryRow(context.Background(), `SELECT enabled FROM shop_settings WHERE org_id = $1`, e.orgA).Scan(&on); err != nil {
			t.Fatalf("read shop_settings: %v", err)
		}
		return on
	}
	if !enabled() {
		t.Fatal("the fixture's storefront should be open")
	}
	base := "/v1/admin/organizations/" + e.orgA.String() + "/modules"
	const reason = "spec 61 reconnect test"
	today := time.Now().UTC()

	// End the fixture's open period yesterday: the module is over.
	_, list := e.doReason(http.MethodGet, base, e.super, "", "")
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("periods before: %v", list)
	}
	firstID, _ := items[0].(map[string]any)["id"].(string)
	if code, body := e.doReason(http.MethodPost, "/v1/admin/org-modules/"+firstID+"/end",
		e.super, fmt.Sprintf(`{"ends_on":%q}`, dayStr(today.AddDate(0, 0, -1))), reason); code != http.StatusOK {
		t.Fatalf("end = %d %v", code, body)
	}
	if code, _, _ := e.do(http.MethodGet, "/v1/public/shop/"+e.orgASlug, "", ""); code != http.StatusNotFound {
		t.Fatalf("catalog of an organization without the module = %d; want 404", code)
	}
	// A period that ended yesterday still covers the day before today, so a
	// connection from today would continue it. Move its end back a few days to
	// leave a real gap (the API only ends a period from yesterday on).
	e.exec(`UPDATE org_module_periods SET ends_on = $2::date WHERE id = $1`, firstID, dayStr(today.AddDate(0, 0, -4)))

	// Connect again from today: a gap, so the storefront is closed.
	if code, body := e.doReason(http.MethodPost, base+"/shop", e.super,
		fmt.Sprintf(`{"starts_on":%q,"monthly_price_minor":0,"currency":"EUR"}`, dayStr(today)), reason); code != http.StatusCreated {
		t.Fatalf("reconnect = %d %v", code, body)
	}
	if enabled() {
		t.Fatal("reconnecting after a gap reopened the storefront")
	}
	if code, body, _ := e.do(http.MethodGet, "/v1/public/shop/"+e.orgASlug, "", ""); code != http.StatusNotFound {
		t.Fatalf("catalog right after the reconnection = %d %v; want 404", code, body)
	}
	// The organization opens it itself, through the blockers (its payment
	// config is not verified in this fixture).
	code, body := e.doReason(http.MethodPut, storefront, e.managerA, `{"enabled":true}`, "")
	if code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.cannot_open" {
		t.Fatalf("opening after the reconnection = %d %v; want 422 shop.cannot_open", code, body)
	}
	e.exec(`UPDATE payment_provider_configs SET verification_status = 'ok' WHERE id = $1`, e.configA)
	if code, body := e.doReason(http.MethodPut, storefront, e.managerA, `{"enabled":true}`, ""); code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("opening once the checks pass = %d %v", code, body)
	}

	// A period that continues the previous one (starts the day after it ends)
	// leaves the switch alone: end today's period tomorrow, connect from the
	// day after.
	_, cur := e.doReason(http.MethodGet, base, e.super, "", "")
	var openID string
	for _, raw := range cur["items"].([]any) {
		it := raw.(map[string]any)
		if it["ends_on"] == nil {
			openID, _ = it["id"].(string)
		}
	}
	tomorrow := today.AddDate(0, 0, 1)
	if code, body := e.doReason(http.MethodPost, "/v1/admin/org-modules/"+openID+"/end", e.super,
		fmt.Sprintf(`{"ends_on":%q}`, dayStr(tomorrow)), reason); code != http.StatusOK {
		t.Fatalf("end tomorrow = %d %v", code, body)
	}
	if code, body := e.doReason(http.MethodPost, base+"/shop", e.super,
		fmt.Sprintf(`{"starts_on":%q,"monthly_price_minor":0,"currency":"EUR"}`, dayStr(tomorrow.AddDate(0, 0, 1))), reason); code != http.StatusCreated {
		t.Fatalf("continue = %d %v", code, body)
	}
	if !enabled() {
		t.Fatal("a contiguous period closed the storefront")
	}
}
