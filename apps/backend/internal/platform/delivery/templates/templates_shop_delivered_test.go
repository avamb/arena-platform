package templates

import (
	"strings"
	"testing"
)

// The "delivered" letter (spec 61 section 7b.4) exists in every locale, names
// the order, says something else than the "shipped" one and is right-to-left
// in Hebrew.
func TestRender_ShopDelivered_EveryLocale(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		if got := r.ResolveLocale(TemplateKindShopOrderDelivered, loc); got != loc {
			t.Fatalf("%s: resolved to %s", loc, got)
		}
		out, err := r.Render(TemplateKindShopOrderDelivered, loc, shopTestData())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		shipped, err := r.Render(TemplateKindShopOrderShipped, loc, shopTestData())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.Subject, "1000777") || out.Subject == shipped.Subject {
			t.Errorf("%s: subject %q (shipped %q)", loc, out.Subject, shipped.Subject)
		}
		if !strings.Contains(out.HTMLBody, "1000777") || !strings.Contains(out.TextBody, "1000777") {
			t.Errorf("%s: the body does not name the order", loc)
		}
		if rtl := strings.Contains(out.HTMLBody, `dir="rtl"`); rtl != (loc == "he") {
			t.Errorf("%s: dir rtl = %v", loc, rtl)
		}
	}
	en, _ := r.Render(TemplateKindShopOrderDelivered, "en", shopTestData())
	if !strings.Contains(en.Subject, "delivered") {
		t.Errorf("en subject = %q", en.Subject)
	}
	pick := shopTestData()
	pick.Shop.Pickup = true
	en, _ = r.Render(TemplateKindShopOrderDelivered, "en", pick)
	if !strings.Contains(en.Subject, "collected") {
		t.Errorf("en pickup subject = %q", en.Subject)
	}
}
