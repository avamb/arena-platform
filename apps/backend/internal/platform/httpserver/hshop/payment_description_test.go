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
