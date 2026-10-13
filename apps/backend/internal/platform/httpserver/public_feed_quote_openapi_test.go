package httpserver

import (
	"os"
	"strings"
	"testing"
)

// operationBlock returns the text of one path item of openapi.yaml: from its
// "  <path>:" line to the next path item.
func operationBlock(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../openapi/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	spec := strings.ReplaceAll(string(raw), "\r\n", "\n")
	start := strings.Index(spec, "\n  "+path+":\n")
	if start < 0 {
		t.Fatalf("openapi.yaml has no path %s", path)
	}
	rest := spec[start+1:]
	if end := strings.Index(rest[1:], "\n  /"); end >= 0 {
		rest = rest[:end+1]
	}
	return rest
}

// The description of the public cart routes is the contract a site integrator
// reads; it must name what the routes really answer.
func TestOpenAPI_PublicCartRoutesDocumentWhatTheyAnswer(t *testing.T) {
	quote := operationBlock(t, "/v1/public/feeds/{feed_token}/checkout/quote")
	start := operationBlock(t, "/v1/public/feeds/{feed_token}/checkout/start")

	for _, want := range []string{
		"checkout.cart_too_large", "checkout.invalid_qty", "tier.closed", "tier.not_on_sale",
		"promo.rate_limited", "promo.not_applicable", "promo.minimum_not_reached", "promo.exhausted",
		"no-store", "/64",
	} {
		if !strings.Contains(quote, want) {
			t.Errorf("checkout/quote documentation does not mention %q", want)
		}
	}
	for _, stale := range []string{
		"It does not check\n        that a category is on sale",
		"`promo.exhausted` and\n        `promo.per_customer_limit` are only answered for a code that does fit",
	} {
		if strings.Contains(quote, stale) {
			t.Errorf("checkout/quote documentation still carries the stale text %q", stale)
		}
	}
	if strings.Contains(quote, "before the cart is looked at") {
		t.Error("checkout/quote documentation claims the budget is checked before the cart; the cart size and body are validated first")
	}
	if !strings.Contains(quote, "after the body and the cart size have been validated") {
		t.Error("checkout/quote documentation must say the budget answer comes after body and cart-size validation")
	}
	if !strings.Contains(quote, "does not check the per-customer limit") {
		t.Error("checkout/quote documentation must say the quote does not check the per-customer limit")
	}

	for _, want := range []string{"checkout.cart_too_large", "checkout.invalid_qty", "tier.not_on_sale", "promo.rate_limited"} {
		if !strings.Contains(start, want) {
			t.Errorf("checkout/start documentation does not mention %q", want)
		}
	}
}
