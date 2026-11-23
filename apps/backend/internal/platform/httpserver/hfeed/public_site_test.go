package hfeed

import (
	"strings"
	"testing"
)

func TestParseSiteThankYou(t *testing.T) {
	long := strings.Repeat("я", 2000)
	cases := []struct {
		name string
		in   string
		nil_ bool
	}{
		{"empty", ``, true},
		{"null", `null`, true},
		{"not object", `"x"`, true},
		{"invalid", `{`, true},
		{"blank", `{"title":"  ","text":""}`, true},
		{"wrong types", `{"title":5,"text":[]}`, true},
	}
	for _, c := range cases {
		if got := ParseSiteThankYou([]byte(c.in)); (got == nil) != c.nil_ {
			t.Errorf("%s: got %v", c.name, got)
		}
	}

	got := ParseSiteThankYou([]byte(`{"title":"  Thanks ","text":"` + long + `","promo_code":" CODE ","url":"https://example.com/x","extra":"leak"}`))
	if got == nil || got.Title != "Thanks" {
		t.Fatalf("got %+v", got)
	}
	if n := len([]rune(got.Text)); n != 1000 {
		t.Errorf("text len %d", n)
	}
	if got.PromoCode == nil || *got.PromoCode != "CODE" || got.URL == nil || *got.URL != "https://example.com/x" {
		t.Errorf("promo/url: %+v", got)
	}

	long2 := ParseSiteThankYou([]byte(`{"title":"` + long + `","text":"t","promo_code":"` + strings.Repeat("a", 100) + `"}`))
	if len([]rune(long2.Title)) != 120 || len(*long2.PromoCode) != 64 {
		t.Errorf("caps: %d %d", len([]rune(long2.Title)), len(*long2.PromoCode))
	}

	for _, u := range []string{"http://example.com", "javascript:alert(1)", "/rel", "https://", "https://u:p@example.com"} {
		g := ParseSiteThankYou([]byte(`{"title":"t","url":"` + u + `"}`))
		if g == nil || g.URL != nil || g.PromoCode != nil {
			t.Errorf("url %q accepted: %+v", u, g)
		}
	}
}
