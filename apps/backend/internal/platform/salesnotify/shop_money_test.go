package salesnotify

import "testing"

func TestFormatShopMoney(t *testing.T) {
	const nb = "\u00a0"
	cases := []struct {
		amount   int64
		cur, loc string
		want     string
	}{
		{45000, "ILS", "ru", "450,00" + nb + "₪"},
		{45000, "ILS", "en", "450.00" + nb + "₪"},
		{45000, "ILS", "es", "450,00" + nb + "₪"},
		{45000, "ILS", "he", "450.00" + nb + "₪"},
		{123450, "EUR", "ru", "1" + nb + "234,50" + nb + "€"},
		{123450, "EUR", "en", "1,234.50" + nb + "€"},
		{123450, "EUR", "es", "1" + nb + "234,50" + nb + "€"},
		{123450, "EUR", "he", "1,234.50" + nb + "€"},
		{123456789, "EUR", "cs", "1" + nb + "234" + nb + "567,89" + nb + "€"},
		{5, "USD", "en-US", "0.05" + nb + "$"},
		{1000, "CZK", "cs", "10,00" + nb + "Kč"},
		{1000, "gel", "ru", "10,00" + nb + "₾"},
		{1000, "GBP", "", "10.00" + nb + "£"},
		{1000, "CHF", "ru", "10,00" + nb + "CHF"},
		{-2550, "ILS", "ru", "-25,50" + nb + "₪"},
		{1000, "XYZ", "de", "10.00" + nb + "XYZ"},
	}
	for _, c := range cases {
		if got := formatShopMoney(c.amount, c.cur, c.loc); got != c.want {
			t.Errorf("formatShopMoney(%d, %q, %q) = %q, want %q", c.amount, c.cur, c.loc, got, c.want)
		}
	}
}
