package hshop

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func TestPaymentDescription_Locales(t *testing.T) {
	b, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	const org = "Einat Winery (staging)"
	cases := []struct{ locale, want string }{
		{"en", "Einat Winery (staging), order 1000001790"},
		{"ru", "Einat Winery (staging), заказ № 1000001790"},
		{"ru-RU", "Einat Winery (staging), заказ № 1000001790"},
		{"RU", "Einat Winery (staging), заказ № 1000001790"},
		{"es", "Einat Winery (staging), pedido n.º 1000001790"},
		{"", "Einat Winery (staging), order 1000001790"},
		{"fr", "Einat Winery (staging), order 1000001790"},
		{"xx-garbage", "Einat Winery (staging), order 1000001790"},
	}
	for _, c := range cases {
		if got := paymentDescription(b, c.locale, org, 1000001790); got != c.want {
			t.Errorf("locale %q: got %q want %q", c.locale, got, c.want)
		}
	}
}

func TestPaymentDescription_NoBundleIsEnglish(t *testing.T) {
	got := paymentDescription(nil, "ru", "Shop", 7)
	if got != "Shop, order 7" || strings.Contains(got, "{{") {
		t.Fatalf("got %q", got)
	}
}

func TestReceiptSeller_BrandWithLegalSeller(t *testing.T) {
	name := func(s string) *string { return &s }
	cases := []struct {
		shop *string
		org  string
		want string
	}{
		{name("Einat Winery"), "Vino&Co", "Einat Winery (Vino&Co)"},
		{name("  Einat Winery "), " Vino&Co ", "Einat Winery (Vino&Co)"},
		{nil, "Vino&Co", "Vino&Co"},
		{name(""), "Vino&Co", "Vino&Co"},
		{name("vino&co"), "Vino&Co", "Vino&Co"},
		{name("Einat Winery"), "", "Einat Winery"},
	}
	for _, c := range cases {
		if got := receiptSeller(c.shop, c.org); got != c.want {
			t.Errorf("receiptSeller(%v, %q) = %q, want %q", c.shop, c.org, got, c.want)
		}
	}
	b, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	lines := receiptLines(b, "ru", receiptSeller(name("Einat Winery"), "Vino&Co"), 1003206377, 25000, 5000)
	if len(lines) != 2 || lines[0].Name != "Einat Winery (Vino&Co), заказ № 1003206377" || lines[0].Amount != 20000 {
		t.Fatalf("receipt lines = %+v", lines)
	}
}
