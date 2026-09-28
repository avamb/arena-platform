package eventbot

import (
	"testing"
	"time"
)

func TestFormatMoney(t *testing.T) {
	t.Parallel()
	cases := []struct {
		minor    int64
		currency string
		locale   string
		want     string
	}{
		{0, "EUR", "en", "0 EUR"},
		{19900, "eur", "en", "199 EUR"},
		{9990, "EUR", "en", "99.90 EUR"},
		{123450, "EUR", "en", "1,234.50 EUR"},
		{123450, "EUR", "ru", "1 234,50 EUR"},
		{100000000, "CZK", "ru", "1 000 000 CZK"},
		{-2500, "ILS", "en", "-25 ILS"},
		{5, "EUR", "en", "0.05 EUR"},
	}
	for _, c := range cases {
		if got := FormatMoney(c.minor, c.currency, c.locale); got != c.want {
			t.Errorf("FormatMoney(%d, %q, %q) = %q; want %q", c.minor, c.currency, c.locale, got, c.want)
		}
	}
}

func TestFormatWhen(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	if got := FormatWhen(at, "Europe/Prague"); got != "15.10.2026 20:00" {
		t.Errorf("Prague: %q", got)
	}
	if got := FormatWhen(at, ""); got != "15.10.2026 18:00 UTC" {
		t.Errorf("empty tz: %q", got)
	}
	if got := FormatWhen(at, "Mars/Olympus"); got != "15.10.2026 18:00 UTC" {
		t.Errorf("unknown tz: %q", got)
	}
}
