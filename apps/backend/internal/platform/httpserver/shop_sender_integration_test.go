//go:build integration

// The shop's own name and sender (migration 0200): the owner and the manager
// name the shop, only a platform superadmin sets the From address, a PUT that
// leaves the fields out keeps them, and the storefront gets the name but never
// the address.
package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestShopSettings_ShopNameAndSender(t *testing.T) {
	e := newShopModuleEnv(t)
	e.setShopPeriod(e.orgA, "")
	settings := e.orgPath(e.orgA, "/settings")
	const reason = "migration 0200 sender test"

	// The owner names the shop; there is no sender yet.
	code, body, _ := e.do(http.MethodPut, settings, e.ownerA, `{"currency":"ILS","shop_name":"  Einat Winery  "}`)
	if code != http.StatusOK || body["shop_name"] != "Einat Winery" || body["sender_email"] != nil {
		t.Fatalf("owner naming the shop = %d %v", code, body)
	}

	// The From address is the platform operator's, not the manager's or the owner's.
	for who, tok := range map[string]string{"manager": e.managerA, "owner": e.ownerA} {
		code, body, _ = e.do(http.MethodPut, settings, tok, `{"currency":"ILS","sender_email":"shop@einat.example"}`)
		if code != http.StatusForbidden || shopErrCode(body) != "shop.sender_superadmin_only" {
			t.Fatalf("%s setting the sender = %d %v; want 403 shop.sender_superadmin_only", who, code, body)
		}
	}
	code, body = e.doReason(http.MethodPut, settings, e.super, `{"currency":"ILS","sender_email":" Shop@Einat.Example "}`, reason)
	if code != http.StatusOK || body["sender_email"] != "shop@einat.example" || body["shop_name"] != "Einat Winery" {
		t.Fatalf("superadmin setting the sender = %d %v; want it lower-cased and the name kept", code, body)
	}
	code, body = e.doReason(http.MethodPut, settings, e.super, `{"currency":"ILS","sender_email":"not an address"}`, reason)
	if code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_sender_email" {
		t.Fatalf("superadmin setting a bad sender = %d %v; want 422", code, body)
	}

	// An editor that does not know the fields keeps them; repeating the
	// stored sender is not a change, so the owner may send it back.
	code, body, _ = e.do(http.MethodPut, settings, e.ownerA, `{"currency":"ILS","shipping_label":"Courier"}`)
	if code != http.StatusOK || body["shop_name"] != "Einat Winery" || body["sender_email"] != "shop@einat.example" {
		t.Fatalf("PUT without the fields = %d %v; want both kept", code, body)
	}
	code, body, _ = e.do(http.MethodPut, settings, e.ownerA, `{"currency":"ILS","sender_email":"shop@einat.example","shop_name":"Einat Winery"}`)
	if code != http.StatusOK {
		t.Fatalf("owner sending the stored sender back = %d %v; want 200", code, body)
	}
	code, body, _ = e.do(http.MethodPut, settings, e.managerA, `{"currency":"ILS","shop_name":"`+strings.Repeat("x", 101)+`"}`)
	if code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_shop_name" {
		t.Fatalf("101-character name = %d %v; want 422 shop.invalid_shop_name", code, body)
	}

	// The storefront gets the name, never the sender address.
	e.exec(`UPDATE shop_settings SET enabled = true WHERE org_id = $1`, e.orgA)
	code, cat, _ := e.do(http.MethodGet, "/v1/public/shop/"+e.orgASlug, "", "")
	if code != http.StatusOK {
		t.Fatalf("public catalog = %d %v", code, cat)
	}
	st, _ := cat["settings"].(map[string]any)
	if st["shop_name"] != "Einat Winery" {
		t.Fatalf("public settings = %v; want shop_name Einat Winery", st)
	}
	if _, leaked := st["sender_email"]; leaked {
		t.Fatalf("public settings carry sender_email: %v", st)
	}

	// An empty string clears both (the sender by the superadmin).
	code, body = e.doReason(http.MethodPut, settings, e.super, `{"currency":"ILS","shop_name":"","sender_email":""}`, reason)
	if code != http.StatusOK || body["shop_name"] != nil || body["sender_email"] != nil {
		t.Fatalf("clearing = %d %v; want both null", code, body)
	}
}
