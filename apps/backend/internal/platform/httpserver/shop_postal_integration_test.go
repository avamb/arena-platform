//go:build integration

package httpserver

// shop_postal_integration_test.go — a shipped order needs a postal code that
// fits its country (courier address fix, 2026-10-29): 422 shop.postal_required
// when it is missing, 422 shop.invalid_postal when it does not fit (Israel is
// exactly 7 digits, any other country 3-12 characters). A cart with no
// physical product takes no address and so no postal code.

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestShop_CheckoutPostalCode(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 100)

	place := func(ship string) (int, map[string]any) {
		code, body, _ := f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(`{
			"items":[{"variant_id":%q,"qty":1}],
			"buyer":{"name":"Anna","email":"postal-%s@example.test"},
			"ship":%s,
			"return_url":"%s/checkout"}`, variant, uuid.NewString()[:8], ship, shopTestBase))
		return code, body
	}

	refused := []struct{ name, ship, code string }{
		{"Israel without a postal code", `{"address1":"Herzl 1","city":"Haifa","country":"IL"}`, "shop.postal_required"},
		{"Israel with a blank postal code", `{"address1":"Herzl 1","city":"Haifa","postal":"   ","country":"IL"}`, "shop.postal_required"},
		{"Israel with 6 digits", `{"address1":"Herzl 1","city":"Haifa","postal":"310000","country":"IL"}`, "shop.invalid_postal"},
		{"Israel with letters", `{"address1":"Herzl 1","city":"Haifa","postal":"31000AB","country":"IL"}`, "shop.invalid_postal"},
		{"Spain without a postal code", `{"address1":"Calle Mayor 1","city":"Madrid","country":"ES"}`, "shop.postal_required"},
		{"Spain with 2 characters", `{"address1":"Calle Mayor 1","city":"Madrid","postal":"28","country":"ES"}`, "shop.invalid_postal"},
	}
	for _, c := range refused {
		code, body := place(c.ship)
		if code != http.StatusUnprocessableEntity || shopErrCode(body) != c.code {
			t.Errorf("%s = %d %v; want 422 %s", c.name, code, body, c.code)
		}
	}

	// Nothing was held by the refused attempts.
	if v := f.variant(variant); v.Held != 0 {
		t.Errorf("a refused checkout held %d unit(s)", v.Held)
	}

	for name, ship := range map[string]string{
		"Israel with 7 digits":              `{"address1":"Herzl 1","city":"Haifa","postal":"3100000","country":"IL"}`,
		"a 4-character code outside Israel": `{"address1":"Wenceslas Sq 1","city":"Prague","postal":"1000","country":"CZ"}`,
	} {
		if code, body := place(ship); code != http.StatusCreated {
			t.Errorf("%s = %d %v; want 201", name, code, body)
		}
	}
}
