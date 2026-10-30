package shop

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestNormalizeSlug(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"  Rosé-2023 ", "rosé-2023", true},
		{"кагор", "кагор", true},
		{"", "", false},
		{"a/b", "", false},
		{"a b", "", false},
		{"a?b", "", false},
		{"a#b", "", false},
		{strings.Repeat("x", 201), "", false},
	} {
		got, ok := NormalizeSlug(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeSlug(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSlugFromTitle(t *testing.T) {
	if got := SlugFromTitle("Cabernet Sauvignon 2021"); got != "cabernet-sauvignon-2021" {
		t.Errorf("SlugFromTitle = %q", got)
	}
	if got := SlugFromTitle("!!!"); got != "product" {
		t.Errorf("SlugFromTitle of nothing = %q, want product", got)
	}
}

func TestNormalizeDomain(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"EinatWinery.com", "einatwinery.com", true},
		{"shop.example.com.", "shop.example.com", true},
		{"", "", true},
		{"https://example.com", "", false},
		{"example", "", false},
		{"example.com:8080", "", false},
		{"ex ample.com", "", false},
	} {
		got, ok := NormalizeDomain(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRefundedStatus(t *testing.T) {
	if s := RefundedStatus(StatusPaid, 1000, 0); s != StatusPaid {
		t.Errorf("nothing refunded: %s", s)
	}
	if s := RefundedStatus(StatusShipped, 1000, 500); s != StatusPartiallyRefunded {
		t.Errorf("half refunded: %s", s)
	}
	if s := RefundedStatus(StatusPartiallyRefunded, 1000, 1000); s != StatusRefunded {
		t.Errorf("all refunded: %s", s)
	}
}

func TestPublicStatus(t *testing.T) {
	for status, want := range map[string]string{
		StatusPendingPayment: "pending", StatusPaid: "paid", StatusShipped: "paid",
		StatusRefunded: "paid", StatusPartiallyRefunded: "paid", StatusExpired: "expired", StatusCancelled: "cancelled",
	} {
		if got := PublicStatus(gen.ShopOrderRow{Status: status}, false); got != want {
			t.Errorf("PublicStatus(%s) = %s, want %s", status, got, want)
		}
	}
	if got := PublicStatus(gen.ShopOrderRow{Status: StatusPendingPayment}, true); got != "expired" {
		t.Errorf("a pending order past its hold reads %s, want expired", got)
	}
}

// SB-02: a parked order reads by its cause. An unconfirmed payment is not paid
// (still pending, no payment page, the cause named); a late payment whose
// goods are gone is paid.
func TestPublicStatusOfAParkedOrder(t *testing.T) {
	unconfirmed, late := ReviewPaymentUnconfirmed, ReviewLatePaymentNoStock
	for _, c := range []struct {
		name        string
		reason      *string
		status, why string
	}{
		{"unconfirmed payment", &unconfirmed, "pending", ReviewPaymentUnconfirmed},
		{"late payment, goods gone", &late, "paid", ReviewLatePaymentNoStock},
		{"legacy row without a reason", nil, "paid", ""},
	} {
		o := gen.ShopOrderRow{Status: StatusManualReview, ReviewReason: c.reason}
		if got := PublicStatus(o, true); got != c.status {
			t.Errorf("%s: PublicStatus = %s, want %s", c.name, got, c.status)
		}
		if got := PublicReview(o); got != c.why {
			t.Errorf("%s: PublicReview = %q, want %q", c.name, got, c.why)
		}
	}
	// The reason of an order that is not parked is never shown.
	if got := PublicReview(gen.ShopOrderRow{Status: StatusPaid, ReviewReason: &unconfirmed}); got != "" {
		t.Errorf("PublicReview of a paid order = %q, want empty", got)
	}
}

func TestCourierText(t *testing.T) {
	o := gen.ShopOrderRow{
		BuyerName: "Anna K", BuyerPhone: "+972500000000", ShipCity: "Haifa",
		ShipAddress1: "Herzl 1", ShipAddress2: "apt 4", ShipPostal: "3100000", ShipCountry: "IL",
		Total: 23000, Currency: "ILS", DeliveryNote: "call before",
	}
	items := []gen.ShopOrderItemRow{
		{ProductTitle: "Rosé", VariantTitle: "Default Title", Qty: 2},
		{ProductTitle: "Merlot", VariantTitle: "Magnum", Qty: 1},
	}
	want := "Anna K\n+972500000000\nHerzl 1, apt 4\nHaifa\n3100000\nIsrael\nRosé x 2\nMerlot (Magnum) x 1\n230.00 ILS\ncall before"
	if got := CourierText(o, items); got != want {
		t.Errorf("CourierText =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatMinor(t *testing.T) {
	if got := FormatMinor(5005, "EUR"); got != "50.05 EUR" {
		t.Errorf("FormatMinor = %q", got)
	}
	if got := FormatMinor(-150, "ILS"); got != "-1.50 ILS" {
		t.Errorf("FormatMinor negative = %q", got)
	}
}

func TestAvailable(t *testing.T) {
	if a := Available(gen.ShopVariantRow{TrackStock: true, Stock: 5, Held: 2}); a != 3 {
		t.Errorf("Available = %d, want 3", a)
	}
	if a := Available(gen.ShopVariantRow{TrackStock: true, Stock: 1, Held: 4}); a != 0 {
		t.Errorf("an oversold variant has %d available, want 0", a)
	}
	if a := Available(gen.ShopVariantRow{TrackStock: false}); a != -1 {
		t.Errorf("an untracked variant reads %d, want -1 (unlimited)", a)
	}
}

func validCheckout() CheckoutInput {
	v := uuid.New()
	return CheckoutInput{
		Items:   []CheckoutItem{{VariantID: v, Qty: 1}, {VariantID: v, Qty: 2}},
		Buyer:   Buyer{Name: " Anna ", Email: "anna@example.test"},
		Ship:    Address{Address1: "Herzl 1", City: "Haifa", Postal: "3100000", Country: "il"},
		Locale:  "RU-ru",
		HoldFor: time.Minute,
	}
}

func TestNormalizeCheckout_MergesLinesAndFillsDefaults(t *testing.T) {
	in := validCheckout()
	if err := normalizeCheckout(&in); err != nil {
		t.Fatalf("normalizeCheckout: %v", err)
	}
	if len(in.Items) != 1 || in.Items[0].Qty != 3 {
		t.Errorf("items = %+v, want one merged line of 3", in.Items)
	}
	if in.Ship.Name != "Anna" || in.Ship.Country != "IL" || in.Locale != "ru" {
		t.Errorf("defaults not applied: ship %+v locale %q", in.Ship, in.Locale)
	}
}

func TestNormalizeCheckout_Refusals(t *testing.T) {
	for name, mutate := range map[string]func(*CheckoutInput){
		"no items": func(in *CheckoutInput) { in.Items = nil },
		"zero qty": func(in *CheckoutInput) { in.Items = []CheckoutItem{{VariantID: uuid.New(), Qty: 0}} },
		"too many lines": func(in *CheckoutInput) {
			in.Items = nil
			for i := 0; i < 51; i++ {
				in.Items = append(in.Items, CheckoutItem{VariantID: uuid.New(), Qty: 1})
			}
		},
		"merged > 99":   func(in *CheckoutInput) { in.Items[0].Qty, in.Items[1].Qty = 50, 50 },
		"bad email":     func(in *CheckoutInput) { in.Buyer.Email = "nope" },
		"no name":       func(in *CheckoutInput) { in.Buyer.Name = " " },
		"bad country":   func(in *CheckoutInput) { in.Ship.Country = "Israel" },
		"note too long": func(in *CheckoutInput) { in.DeliveryNote = strings.Repeat("x", 2001) },
	} {
		in := validCheckout()
		mutate(&in)
		err := normalizeCheckout(&in)
		var se *Error
		if !errors.As(err, &se) || se.Status != 422 {
			t.Errorf("%s: err = %v, want a 422 refusal", name, err)
		}
	}
}

func TestValidateVariants(t *testing.T) {
	id := uuid.New()
	ok := []VariantInput{{Title: "", PriceMinor: 100, Stock: 3}}
	if err := validateVariants(ok); err != nil || ok[0].Title != DefaultVariantTitle {
		t.Fatalf("validateVariants(default) = %v, title %q", err, ok[0].Title)
	}
	for name, vs := range map[string][]VariantInput{
		"empty":          {},
		"negative price": {{PriceMinor: -1}},
		"negative stock": {{Stock: -1}},
		"duplicate id":   {{ID: &id}, {ID: &id}},
	} {
		if err := validateVariants(vs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateSettings(t *testing.T) {
	c, age := " il ", int32(18)
	in := SettingsInput{Currency: "ils", CountryISO2: &c, AgeGateMinAge: &age}
	if err := validateSettings(&in); err != nil {
		t.Fatalf("validateSettings: %v", err)
	}
	if in.Currency != "ILS" || *in.CountryISO2 != "IL" {
		t.Errorf("not normalized: %+v", in)
	}
	bad := int32(120)
	for name, in := range map[string]SettingsInput{
		"currency": {Currency: "EURO"},
		"fee":      {Currency: "EUR", ShippingFeeMinor: -1},
		"age":      {Currency: "EUR", AgeGateMinAge: &bad},
	} {
		if err := validateSettings(&in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNewOrderToken(t *testing.T) {
	a, err := NewOrderToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewOrderToken()
	if len(a) != 64 || a == b {
		t.Errorf("tokens %q %q: want 64 hex characters, distinct", a, b)
	}
}

func TestSanitizeBody_KeepsTheRichTextSubsetAndDropsTheRest(t *testing.T) {
	in := `<section style="color:red"><h3>Rosé</h3><p class="x">Dry <b>and</b> <a href="https://vino.example/x" onclick="steal()">fresh</a></p>` +
		`<img src="https://cdn.example/a.png"><script>alert(1)</script><style>p{}</style><table><tr><td>cell</td></tr></table></section>`
	out, err := SanitizeBody(in, ProductBodyProfile)
	if err != nil {
		t.Fatalf("SanitizeBody: %v", err)
	}
	for _, gone := range []string{"<section", "style", "class=", "onclick", "<img", "<script", "alert(1)", "<table", "<td"} {
		if strings.Contains(out, gone) {
			t.Errorf("sanitized body still holds %q: %s", gone, out)
		}
	}
	for _, kept := range []string{"<h3>Rosé</h3>", "<strong>and</strong>", `href="https://vino.example/x"`, "cell"} {
		if !strings.Contains(out, kept) {
			t.Errorf("sanitized body lost %q: %s", kept, out)
		}
	}
	if again, _ := SanitizeBody(out, ProductBodyProfile); again != out {
		t.Errorf("sanitizing twice changed the text:\n%s\n%s", out, again)
	}
	if out, err := SanitizeBody("  ", PageBodyProfile); err != nil || out != "" {
		t.Errorf("blank body = %q %v", out, err)
	}
}

func TestSanitizeBody_RefusesWhatTheSanitizerRefuses(t *testing.T) {
	_, err := SanitizeBody("<p>"+strings.Repeat("a", MaxBodyHTMLLen+1)+"</p>", ProductBodyProfile)
	if e, ok := AsError(err); !ok || e.Code != "shop.invalid_body_html" || e.Status != 422 {
		t.Fatalf("over-long body = %v; want 422 shop.invalid_body_html", err)
	}
}

var lateNoStock = ReviewLatePaymentNoStock

func TestRefundStatus(t *testing.T) {
	shipped := time.Now()
	paid := gen.ShopOrderRow{Status: StatusPaid, Total: 1000}
	cases := []struct {
		name     string
		o        gen.ShopOrderRow
		accepted int64
		want     string
	}{
		{"paid, nothing back", paid, 0, StatusPaid},
		{"paid, part back", paid, 400, StatusPartiallyRefunded},
		{"paid, all back", paid, 1000, StatusRefunded},
		{"refunded, reversed", gen.ShopOrderRow{Status: StatusRefunded, Total: 1000, RefundedMinor: 1000}, 0, StatusPaid},
		{"refunded shipped, reversed to part", gen.ShopOrderRow{Status: StatusRefunded, Total: 1000, ShippedAt: &shipped}, 300, StatusPartiallyRefunded},
		{"partially refunded shipped, reversed", gen.ShopOrderRow{Status: StatusPartiallyRefunded, Total: 1000, ShippedAt: &shipped}, 0, StatusShipped},
		{"manual review, part back", gen.ShopOrderRow{Status: StatusManualReview, Total: 1000}, 300, StatusManualReview},
		{"manual review, all back", gen.ShopOrderRow{Status: StatusManualReview, Total: 1000}, 1000, StatusRefunded},
		{"refunded parked order, reversed", gen.ShopOrderRow{Status: StatusRefunded, Total: 1000,
			ReviewReason: &lateNoStock}, 0, StatusManualReview},
		{"expired keeps its status", gen.ShopOrderRow{Status: StatusExpired, Total: 1000}, 1000, StatusExpired},
	}
	for _, c := range cases {
		if got := refundStatus(c.o, c.accepted); got != c.want {
			t.Errorf("%s: refundStatus = %s; want %s", c.name, got, c.want)
		}
	}
}

func TestRequireShipping(t *testing.T) {
	if err := requireShipping(Address{Address1: "Herzl 1", City: "Haifa", Postal: "3100000", Country: "IL"}); err != nil {
		t.Fatalf("full address refused: %v", err)
	}
	for name, a := range map[string]Address{
		"no street":  {City: "Haifa", Country: "IL"},
		"no city":    {Address1: "Herzl 1", Country: "IL"},
		"no country": {Address1: "Herzl 1", City: "Haifa"},
	} {
		if e, ok := AsError(requireShipping(a)); !ok || e.Code != "shop.invalid_address" {
			t.Errorf("%s: %v; want shop.invalid_address", name, e)
		}
	}
}

func TestProductInput_KindsAndOptionNames(t *testing.T) {
	str := func(s string) *string { return &s }
	media := uuid.New()
	p := gen.ShopProductParams{Title: "E-book"}
	if err := (ProductInput{Kind: str("digital"), DigitalMediaID: &media, OptionNames: &[]string{" Format ", "Language"}}).apply(&p); err != nil {
		t.Fatalf("digital product: %v", err)
	}
	if p.Kind != KindDigital || p.DigitalMediaID == nil || *p.Option1Name != "Format" || *p.Option2Name != "Language" || p.Option3Name != nil {
		t.Fatalf("applied = %+v", p)
	}
	if err := (ProductInput{Kind: str("physical")}).apply(&p); err != nil || p.DigitalMediaID != nil {
		t.Fatalf("turning physical kept the file: %v %v", err, p.DigitalMediaID)
	}
	if err := (ProductInput{OptionNames: &[]string{}}).apply(&p); err != nil || p.Option1Name != nil {
		t.Fatalf("empty option names did not clear them: %v", err)
	}
	for name, in := range map[string]ProductInput{
		"bad kind":          {Kind: str("ticket")},
		"file on physical":  {Kind: str("physical"), DigitalMediaID: &media},
		"four option names": {OptionNames: &[]string{"a", "b", "c", "d"}},
		"blank option name": {OptionNames: &[]string{" "}},
	} {
		q := gen.ShopProductParams{Title: "x"}
		if _, ok := AsError(in.apply(&q)); !ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSanitizeBody_PageKeepsPicturesAndTables(t *testing.T) {
	in := `<section class="x"><h3>Partners</h3>` +
		`<img src="https://cdn.example/a.png" alt="Winery" width="300" height="200" loading="lazy" class="c" onerror="steal()" style="x">` +
		`<img src="data:image/png;base64,AAAA" alt="inline"><img src="http://cdn.example/b.png"><img src="javascript:alert(1)">` +
		`<table class="t"><thead><tr><th colspan="2" style="x">Data</th></tr></thead>` +
		`<tbody><tr><td rowspan="3" onclick="x()">Name</td><td colspan="abc">Why</td></tr></tbody></table>` +
		`<p>Read <a href="https://vino.example/p" target="_blank">this</a> <a href="javascript:x()">bad</a></p>` +
		`<script>alert(1)</script><style>p{}</style></section>`
	out, err := SanitizeBody(in, PageBodyProfile)
	if err != nil {
		t.Fatalf("SanitizeBody page: %v", err)
	}
	for _, want := range []string{
		`<img src="https://cdn.example/a.png" alt="Winery" width="300" height="200" loading="lazy">`,
		`<table><thead><tr><th colspan="2">Data</th></tr></thead>`,
		`<td rowspan="3">Name</td><td>Why</td>`,
		`<a href="https://vino.example/p" rel="noopener noreferrer">this</a>`,
		`<h3>Partners</h3>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page lost %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"data:", "http://", "javascript", "onerror", "onclick", "style", "class", "<script", "alert", "<section", "target"} {
		if strings.Contains(out, gone) {
			t.Errorf("page still holds %q:\n%s", gone, out)
		}
	}
	if again, _ := SanitizeBody(out, PageBodyProfile); again != out {
		t.Errorf("sanitizing a page twice changed it:\n%s\n%s", out, again)
	}
	if _, err := SanitizeBody(strings.Repeat("a", 4*MaxPageBodyLen+1), PageBodyProfile); err == nil {
		t.Error("an oversized page was accepted")
	}
}

func TestSanitizeBody_ProductStillDropsPicturesAndTables(t *testing.T) {
	out, err := SanitizeBody(`<p>Dry</p><img src="https://cdn.example/a.png" alt="x"><table><tr><td>cell</td></tr></table>`, ProductBodyProfile)
	if err != nil {
		t.Fatalf("SanitizeBody product: %v", err)
	}
	if strings.Contains(out, "<img") || strings.Contains(out, "<table") || strings.Contains(out, "<td") {
		t.Fatalf("product body kept a picture or a table: %s", out)
	}
	if !strings.Contains(out, "cell") {
		t.Fatalf("product body lost the table's text: %s", out)
	}
}

func TestSanitizeBody_PageKeepsLinksToOtherPagesOfTheStorefront(t *testing.T) {
	// The button of the imported "partners" page: a Shopify page body links
	// "/pages/<handle>" and lost its href here (finding H-01).
	in := `<p><a href="/pages/contact" class="button button--primary" style="x">Напишите нам</a></p>` +
		`<p><a href="/pages/%D0%BE-%D0%BD%D0%B0%D1%81">О нас</a> <a href="/collections/all?x=1#top">all</a></p>` +
		`<p><a href="//evil.example/x">protocol-relative</a> <a href="/\evil.example">backslash</a> <a href="/a b">space</a> <a href="javascript:x()">js</a></p>`
	out, err := SanitizeBody(in, PageBodyProfile)
	if err != nil {
		t.Fatalf("SanitizeBody: %v", err)
	}
	for _, want := range []string{
		`<a href="/pages/contact" rel="noopener noreferrer">Напишите нам</a>`,
		`<a href="/pages/%D0%BE-%D0%BD%D0%B0%D1%81" rel="noopener noreferrer">О нас</a>`,
		`<a href="/collections/all?x=1#top" rel="noopener noreferrer">all</a>`,
		`<a>protocol-relative</a> <a>backslash</a> <a>space</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"evil.example", "javascript", `href="/a b"`, `class=`} {
		if strings.Contains(out, gone) {
			t.Errorf("still holds %q:\n%s", gone, out)
		}
	}
	if again, _ := SanitizeBody(out, PageBodyProfile); again != out {
		t.Errorf("sanitizing twice changed it:\n%s\n%s", out, again)
	}
}

func TestRequireShipping_Postal(t *testing.T) {
	ship := func(country, postal string) Address {
		return Address{Address1: "Herzl 1", City: "Haifa", Postal: postal, Country: country}
	}
	cases := []struct {
		name string
		a    Address
		code string // "" = accepted
	}{
		{"IL 7 digits", ship("IL", "3100000"), ""},
		{"IL 6 digits", ship("IL", "310000"), "shop.invalid_postal"},
		{"IL 8 digits", ship("IL", "31000000"), "shop.invalid_postal"},
		{"IL letters", ship("IL", "31000AB"), "shop.invalid_postal"},
		{"IL with a space", ship("IL", "310 0000"), "shop.invalid_postal"},
		{"IL empty", ship("IL", ""), "shop.postal_required"},
		{"ES 5 digits", ship("ES", "28013"), ""},
		{"4 characters outside IL", ship("CZ", "1000"), ""},
		{"UK style", ship("GB", "SW1A 1AA"), ""},
		{"too short outside IL", ship("ES", "12"), "shop.invalid_postal"},
		{"too long outside IL", ship("ES", "1234567890123"), "shop.invalid_postal"},
		{"empty outside IL", ship("ES", ""), "shop.postal_required"},
	}
	for _, c := range cases {
		err := requireShipping(c.a)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: err = %v, want accepted", c.name, err)
			}
			continue
		}
		var se *Error
		if !errors.As(err, &se) || se.Status != 422 || se.Code != c.code {
			t.Errorf("%s: err = %v, want 422 %s", c.name, err, c.code)
		}
	}
}

// A cart with no physical product takes no address, so no postal code: the
// postal rule lives in requireShipping and normalizeCheckout leaves it alone.
func TestNormalizeCheckout_EmptyPostalIsNotRefusedHere(t *testing.T) {
	in := validCheckout()
	in.Ship.Postal = ""
	if err := normalizeCheckout(&in); err != nil {
		t.Fatalf("normalizeCheckout refused an empty postal: %v (only a shipped order needs one)", err)
	}
}

// SA-37: the decimals follow the currency (a yen has none, a dinar three).
func TestFormatMinor_FollowsTheCurrencyExponent(t *testing.T) {
	for _, c := range []struct {
		amount   int64
		currency string
		want     string
	}{
		{2500, "JPY", "2500 JPY"},
		{-2500, "JPY", "-2500 JPY"},
		{1500, "KWD", "1.500 KWD"},
		{5, "KWD", "0.005 KWD"},
		{5, "ILS", "0.05 ILS"},
		{0, "EUR", "0.00 EUR"},
	} {
		if got := FormatMinor(c.amount, c.currency); got != c.want {
			t.Errorf("FormatMinor(%d, %s) = %q; want %q", c.amount, c.currency, got, c.want)
		}
	}
}
