package hshop

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func TestReceiptLines_PaidDeliveryIsItsOwnLine(t *testing.T) {
	b, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		locale, goods, delivery string
	}{
		{"ru", "Einat Winery, заказ № 1000001911", "Доставка"},
		{"he", "Einat Winery, הזמנה מס׳ 1000001911", "משלוח"},
		{"en", "Einat Winery, order 1000001911", "Delivery"},
		{"cs", "Einat Winery, objednávka č. 1000001911", "Doprava"},
		{"es", "Einat Winery, pedido n.º 1000001911", "Envío"},
		{"fr", "Einat Winery, order 1000001911", "Delivery"},
	}
	for _, c := range cases {
		got := receiptLines(b, c.locale, "Einat Winery", 1000001911, 25000, 5000)
		want := []payments.HostedCheckoutLine{{Name: c.goods, Amount: 20000}, {Name: c.delivery, Amount: 5000}}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("locale %q: got %+v want %+v", c.locale, got, want)
		}
		if !payments.LinesMatch(got, 25000) {
			t.Errorf("locale %q: lines do not add up to the total", c.locale)
		}
	}
}

func TestReceiptLines_NoPaidDeliveryKeepsOneLine(t *testing.T) {
	for _, c := range []struct{ total, shipping int64 }{
		{25000, 0},    // free delivery or pickup
		{5000, 5000},  // nothing but the delivery: one line
		{4000, 5000},  // a discount larger than the goods
		{25000, -100}, // never a negative line
	} {
		if got := receiptLines(nil, "ru", "Shop", 1, c.total, c.shipping); got != nil {
			t.Errorf("total %d shipping %d: got %+v want nil", c.total, c.shipping, got)
		}
	}
}

func TestReceiptLines_NoBundleIsEnglish(t *testing.T) {
	got := receiptLines(nil, "ru", "Shop", 7, 1500, 500)
	if len(got) != 2 || got[0].Name != "Shop, order 7" || got[1].Name != "Delivery" {
		t.Fatalf("got %+v", got)
	}
}
