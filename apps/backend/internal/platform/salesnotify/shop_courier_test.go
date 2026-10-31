package salesnotify

import (
	"strings"
	"testing"
)

const (
	nl = "\n"
	nb = "\u00a0"
)

func TestFormatShopCourierText_RussianShopCountryHidden(t *testing.T) {
	s := shopSample()
	s.Locale = "ru"
	s.ShopCountry = "IL"
	got := FormatShopCourierText(s)
	q := "Herzl+1%2C+Haifa%2C+3100000%2C+%D0%98%D0%B7%D1%80%D0%B0%D0%B8%D0%BB%D1%8C"
	want := strings.Join([]string{
		"Заказ № 1000777",
		"Dan L",
		"+972500000000",
		"Herzl 1",
		"apt 4",
		"Haifa",
		"3100000",
		"",
		"Google Карты: https://www.google.com/maps/search/?api=1&query=" + q,
		"Waze: https://waze.com/ul?q=" + q + "&navigate=yes",
		"",
		"Rosé × 2",
		"Merlot (Magnum) × 1",
		"Сумма: 230,00" + nb + "₪",
		"Комментарий покупателя: call before",
	}, nl)
	if got != want {
		t.Errorf("courier text =\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{"<", "&amp;", "🧾", "ILS", "Израиль\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("courier text must be plain, found %q:\n%s", bad, got)
		}
	}
}

func TestFormatShopCourierText_ForeignCountryIsNamedInTheLocale(t *testing.T) {
	s := shopSample()
	s.Locale = "ru"
	s.Order.ShipCountry = "CY"
	s.ShopCountry = "IL"
	if got := FormatShopCourierText(s); !strings.Contains(got, "3100000\nКипр\n") {
		t.Errorf("another country is printed after the postal code, in Russian:\n%s", got)
	}
	// Without a shop country every country is printed.
	s.ShopCountry = ""
	s.Order.ShipCountry = "IL"
	if got := FormatShopCourierText(s); !strings.Contains(got, "3100000\nИзраиль\n") {
		t.Errorf("an unknown shop country prints the country:\n%s", got)
	}
}

func TestFormatShopCourierText_EnglishWithoutApartmentAndNote(t *testing.T) {
	s := shopSample()
	s.ShopCountry = "il" // case does not matter
	s.Order.ShipAddress2, s.Order.DeliveryNote = "", ""
	got := FormatShopCourierText(s)
	if !strings.HasPrefix(got, "Order #1000777\nDan L\n+972500000000\nHerzl 1\nHaifa\n3100000\n\nGoogle Maps: ") {
		t.Errorf("English courier text:\n%s", got)
	}
	for _, want := range []string{"Rosé × 2", "Total: 230.00" + nb + "₪"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	head, _, _ := strings.Cut(got, "Google Maps:") // the map query keeps the country
	if strings.Contains(got, "note") || strings.Contains(got, "apt") || strings.Contains(head, "Israel") {
		t.Errorf("no note, no apartment line, no home country:\n%s", got)
	}
	if !strings.HasSuffix(got, "230.00"+nb+"₪") {
		t.Errorf("the text ends with the total when there is no note:\n%s", got)
	}
}

func TestFormatShopCourierText_PickupAndNoDelivery(t *testing.T) {
	s := shopSample()
	s.Locale = "ru"
	s.Order.DeliveryKind = "pickup"
	got := FormatShopCourierText(s)
	if !strings.Contains(got, "Заказ № 1000777\nСамовывоз\nDan L") || strings.Contains(got, "Google") || strings.Contains(got, "Waze") {
		t.Errorf("pickup says so and has no map links:\n%s", got)
	}

	s.Order.DeliveryKind = "none"
	s.Order.ShipName, s.Order.ShipCity, s.Order.ShipAddress1, s.Order.ShipAddress2, s.Order.ShipPostal, s.Order.ShipCountry = "", "", "", "", "", ""
	got = FormatShopCourierText(s)
	if !strings.HasPrefix(got, "Заказ № 1000777\nБез доставки (цифровой товар или услуга)\nAnna <K>\n+972500000000\n\nRosé × 2") {
		t.Errorf("no-delivery text names the buyer, not an address:\n%s", got)
	}
	if strings.Contains(got, "Google") || strings.Contains(got, "Waze") {
		t.Errorf("no map links without a delivery:\n%s", got)
	}
}

// The Telegram announcement and the courier text print the same form lines:
// one writer, so the two cannot drift again.
func TestShopCourierText_SharesTheFormWithTheTelegramMessage(t *testing.T) {
	s := shopSample()
	s.Locale = "ru"
	tg := FormatShopOrderPaid(s)
	plain := FormatShopCourierText(s)
	form := "Dan L\n+972500000000\nHerzl 1\napt 4\nHaifa\n3100000\n"
	if !strings.Contains(tg, form+"Израиль") || !strings.Contains(plain, form) {
		t.Errorf("the form lines differ:\n%s\n----\n%s", tg, plain)
	}
}
