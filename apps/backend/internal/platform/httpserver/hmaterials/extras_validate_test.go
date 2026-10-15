package hmaterials

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizePartnerURL(t *testing.T) {
	good := map[string]string{
		"https://ticketportal.cz/event/1":     "https://ticketportal.cz/event/1",
		"  https://www.goout.net/x?a=1&b=2  ": "https://www.goout.net/x?a=1&b=2",
		"HTTPS://Tickets.Example.com/a":       "https://Tickets.Example.com/a",
		"https://shop.example.com:8443/p":     "https://shop.example.com:8443/p",
	}
	for in, want := range good {
		got, ok := NormalizePartnerURL(in)
		if !ok || got != want {
			t.Errorf("NormalizePartnerURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	bad := []string{
		"", "   ", "http://example.com/a", "javascript:alert(1)", "data:text/html,x",
		"ftp://example.com", "//example.com/a", "example.com/a", "https://", "https:///path",
		"https://exa mple.com/a", "https://example.com/a b", "https://example.com/a\nb",
		"https://localhost/a", "https://user:pw@example.com/a", "https://.example.com/a",
		"https://example.com/" + strings.Repeat("a", 500),
	}
	for _, in := range bad {
		if got, ok := NormalizePartnerURL(in); ok {
			t.Errorf("NormalizePartnerURL(%q) = %q, true; want refusal", in, got)
		}
	}
}

func TestSellerNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://ticketportal.cz/event/1":               "ticketportal.cz",
		"https://www.GoOut.net/x":                       "goout.net",
		"https://tickets.example.com:8443/a":            "tickets.example.com",
		"https://www.bilety.pl./a":                      "bilety.pl",
		"https://www.www.example.com/a":                 "www.example.com",
		"https://" + strings.Repeat("a", 130) + ".com/": strings.Repeat("a", 120),
	}
	for in, want := range cases {
		if got := SellerNameFromURL(in); got != want {
			t.Errorf("SellerNameFromURL(%q) = %q; want %q", in, got, want)
		}
	}
	if got := SellerNameFromURL("::not a url"); got != "" {
		t.Errorf("SellerNameFromURL of garbage = %q; want empty", got)
	}
}

func TestNormalizeSellerName(t *testing.T) {
	if got, ok := NormalizeSellerName("  Ticket   Portal \t CZ "); !ok || got != "Ticket Portal CZ" {
		t.Errorf("got %q, %v", got, ok)
	}
	for _, in := range []string{"", "   ", strings.Repeat("x", 121), "a\x00b", "a\u0007b"} {
		if _, ok := NormalizeSellerName(in); ok {
			t.Errorf("NormalizeSellerName(%q) accepted", in)
		}
	}
	if _, ok := NormalizeSellerName(strings.Repeat("я", 120)); !ok {
		t.Error("120 runes must be accepted (the limit counts characters, not bytes)")
	}
}

func TestNormalizeCaptionAndLabel(t *testing.T) {
	if c, ok := NormalizeCaption("   "); !ok || c != nil {
		t.Errorf("blank caption = %v, %v; want nil, true", c, ok)
	}
	if c, ok := NormalizeCaption(" Stage "); !ok || c == nil || *c != "Stage" {
		t.Errorf("caption = %v, %v", c, ok)
	}
	if _, ok := NormalizeCaption(strings.Repeat("x", 201)); ok {
		t.Error("201-rune caption accepted")
	}
	if _, ok := NormalizeCaption("a\nb"); ok {
		t.Error("caption with a line break accepted")
	}
	if l, ok := NormalizeMapLabel(""); !ok || l != nil {
		t.Errorf("blank label = %v, %v", l, ok)
	}
	if _, ok := NormalizeMapLabel(strings.Repeat("x", 201)); ok {
		t.Error("201-rune label accepted")
	}
}

func TestValidMapPoint(t *testing.T) {
	ok := [][2]float64{{0, 0}, {50.0755, 14.4378}, {90, 180}, {-90, -180}, {-33.8688, 151.2093}}
	for _, p := range ok {
		if !ValidMapPoint(p[0], p[1]) {
			t.Errorf("ValidMapPoint(%v) = false", p)
		}
	}
	bad := [][2]float64{{90.0001, 0}, {-90.1, 0}, {0, 180.5}, {0, -181}, {math.NaN(), 0}, {0, math.Inf(1)}, {math.Inf(-1), 0}}
	for _, p := range bad {
		if ValidMapPoint(p[0], p[1]) {
			t.Errorf("ValidMapPoint(%v) = true", p)
		}
	}
}

func TestCoordString(t *testing.T) {
	if got := coordString(50.07554321); got != "50.075543" {
		t.Errorf("coordString = %q", got)
	}
	if got := coordString(-0.5); got != "-0.500000" {
		t.Errorf("coordString = %q", got)
	}
}

func TestIDsMatch(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	cur := []uuid.UUID{a, b, c}
	cases := []struct {
		ids  []uuid.UUID
		want bool
	}{
		{[]uuid.UUID{c, a, b}, true},
		{[]uuid.UUID{a, b, c}, true},
		{[]uuid.UUID{a, b}, false},
		{[]uuid.UUID{a, a, b}, false},
		{[]uuid.UUID{a, b, uuid.New()}, false},
		{[]uuid.UUID{a, b, c, uuid.New()}, false},
		{nil, false},
	}
	for i, tc := range cases {
		if got := idsMatch(cur, tc.ids); got != tc.want {
			t.Errorf("case %d: idsMatch = %v; want %v", i, got, tc.want)
		}
	}
	if !idsMatch(nil, nil) {
		t.Error("two empty lists must match")
	}
}
