package extsale

import (
	"errors"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestValidateURL(t *testing.T) {
	ok := []string{
		"https://www.eventim.de/event/1",
		"  https://tickets.example.cz/a?b=c  ",
		"HTTPS://Shop.Example.com/",
	}
	for _, u := range ok {
		if _, err := ValidateURL(u); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want ok", u, err)
		}
	}
	bad := map[string]string{
		"empty":        "",
		"http":         "http://www.eventim.de/",
		"ipv4":         "https://203.0.113.5/x",
		"ipv6":         "https://[2001:db8::1]/x",
		"decimal ip":   "https://2130706433/x",
		"bare host":    "https://localhost/x",
		"platform":     "https://arenasoldout.com/x",
		"platform sub": "https://tickets.arenasoldout.com/a/b",
		"userinfo":     "https://user:pw@www.eventim.de/",
		"no scheme":    "www.eventim.de/x",
		"javascript":   "javascript:alert(1)",
		"too long":     "https://www.eventim.de/" + strings.Repeat("a", MaxURLLen),
	}
	for name, u := range bad {
		_, err := ValidateURL(u)
		if err == nil {
			t.Errorf("%s: ValidateURL(%q) accepted", name, u)
			continue
		}
		if err.Code != CodeInvalidURL {
			t.Errorf("%s: code = %q", name, err.Code)
		}
	}
}

// The database CHECK is LIKE 'https://%': an upper- or mixed-case scheme is
// stored in lower case, the rest of the address untouched.
func TestValidateURL_LowerCasesScheme(t *testing.T) {
	cases := map[string]string{
		"HTTPS://Shop.Example.com/A?b=C":   "https://Shop.Example.com/A?b=C",
		"  HttpS://tickets.example.cz/x  ": "https://tickets.example.cz/x",
		"https://www.eventim.de/event/1":   "https://www.eventim.de/event/1",
	}
	for in, want := range cases {
		got, err := ValidateURL(in)
		if err != nil {
			t.Fatalf("ValidateURL(%q) = %v", in, err)
		}
		if got != want {
			t.Errorf("ValidateURL(%q) = %q, want %q", in, got, want)
		}
	}
	sale, err := Normalize(Input{URL: "HTTPS://Shop.Example.com/", SellerName: "Shop"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !strings.HasPrefix(sale.URL, "https://") {
		t.Errorf("Normalize URL = %q, want an https:// prefix", sale.URL)
	}
}

func TestValidateSellerName(t *testing.T) {
	if got, err := ValidateSellerName("  Eventim "); err != nil || got != "Eventim" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, n := range []string{"", "   ", strings.Repeat("я", 81)} {
		if _, err := ValidateSellerName(n); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	if _, err := ValidateSellerName(strings.Repeat("я", 80)); err != nil {
		t.Errorf("80 runes refused: %v", err)
	}
}

func TestNormalizePrice(t *testing.T) {
	p := 39.5
	in := Input{URL: "https://www.eventim.de/x", SellerName: "Eventim", PriceFrom: &p, Currency: "eur"}
	s, err := Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	if s.PriceFromMinor == nil || *s.PriceFromMinor != 3950 || s.Currency == nil || *s.Currency != "EUR" {
		t.Fatalf("sale = %+v", s)
	}
	// Zero-decimal currency.
	y := 1200.0
	s, err = Normalize(Input{URL: in.URL, SellerName: "X", PriceFrom: &y, Currency: "JPY"})
	if err != nil || *s.PriceFromMinor != 1200 {
		t.Fatalf("JPY: %+v %v", s, err)
	}
	// No price at all is fine.
	if s, err = Normalize(Input{URL: in.URL, SellerName: "X"}); err != nil || s.PriceFromMinor != nil || s.Currency != nil {
		t.Fatalf("no price: %+v %v", s, err)
	}
	neg := -1.0
	cases := map[string]Input{
		"price without currency": {URL: in.URL, SellerName: "X", PriceFrom: &p},
		"currency without price": {URL: in.URL, SellerName: "X", Currency: "EUR"},
		"bad currency":           {URL: in.URL, SellerName: "X", PriceFrom: &p, Currency: "EURO"},
		"negative":               {URL: in.URL, SellerName: "X", PriceFrom: &neg, Currency: "EUR"},
		"bad url":                {URL: "http://x.com", SellerName: "X"},
		"no name":                {URL: in.URL},
	}
	for name, c := range cases {
		if _, err := Normalize(c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMajorMinorRoundTrip(t *testing.T) {
	if got := MajorFromMinor(3950, "EUR"); got != 39.5 {
		t.Errorf("EUR: %v", got)
	}
	if got := MajorFromMinor(1200, "JPY"); got != 1200 {
		t.Errorf("JPY: %v", got)
	}
}

func TestCheckSwitch(t *testing.T) {
	none := gen.SessionSalesModeLockFacts{}
	cases := []struct {
		name     string
		from, to string
		f        gen.SessionSalesModeLockFacts
		locked   bool
	}{
		{"same mode ignores facts", "platform", "platform", gen.SessionSalesModeLockFacts{HasOrders: true}, false},
		{"empty session to external", "platform", "external", none, false},
		{"order blocks external", "platform", "external", gen.SessionSalesModeLockFacts{HasOrders: true}, true},
		{"hold blocks external", "platform", "external", gen.SessionSalesModeLockFacts{HasHolds: true}, true},
		{"seats block external", "platform", "external", gen.SessionSalesModeLockFacts{HasSeats: true}, true},
		{"tiers block external", "platform", "external", gen.SessionSalesModeLockFacts{HasTiers: true}, true},
		{"external back to platform", "external", "platform", none, false},
		{"orders do not block going back", "external", "platform", gen.SessionSalesModeLockFacts{HasOrders: true}, false},
		{"seats block going back", "external", "platform", gen.SessionSalesModeLockFacts{HasSeats: true}, true},
		{"tiers block going back", "external", "platform", gen.SessionSalesModeLockFacts{HasTiers: true}, true},
		{"platform to control only", "platform", "control_only", none, true},
		{"external to control only", "external", "control_only", none, true},
		{"control only to platform", "control_only", "platform", none, true},
		{"control only to external", "control_only", "external", none, true},
		{"control only stays", "control_only", "control_only", gen.SessionSalesModeLockFacts{HasOrders: true}, false},
	}
	for _, c := range cases {
		err := CheckSwitch(c.from, c.to, c.f)
		if c.locked != errors.Is(err, ErrSalesModeLocked) {
			t.Errorf("%s: err = %v, locked want %v", c.name, err, c.locked)
		}
	}
}

func TestViewFromRow(t *testing.T) {
	if ViewFromRow(gen.SessionRow{SalesMode: gen.SessionSalesModePlatform}) != nil {
		t.Fatal("platform session has a view")
	}
	u, n, c := "https://www.eventim.de/x", "Eventim", "EUR "
	m := int64(3900)
	v := ViewFromRow(gen.SessionRow{SalesMode: gen.SessionSalesModeExternal,
		ExternalTicketURL: &u, ExternalSellerName: &n, DisplayPriceFromMinor: &m, DisplayCurrency: &c})
	if v == nil || v.PriceFrom == nil || *v.PriceFrom != 39 || *v.Currency != "EUR" || v.SellerName != "Eventim" {
		t.Fatalf("view = %+v", v)
	}
	v = ViewFromRow(gen.SessionRow{SalesMode: gen.SessionSalesModeExternal, ExternalTicketURL: &u, ExternalSellerName: &n})
	if v == nil || v.PriceFrom != nil || v.Currency != nil {
		t.Fatalf("view without price = %+v", v)
	}
}
