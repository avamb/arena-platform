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

func TestParseSiteSocials(t *testing.T) {
	for _, in := range []string{``, `null`, `"x"`, `{`, `[]`, `{}`, `5`} {
		if got := ParseSiteSocials([]byte(in)); got == nil || len(got) != 0 {
			t.Errorf("%q: got %#v, want empty non-nil", in, got)
		}
	}

	got := ParseSiteSocials([]byte(`[
		{"network":"Instagram","url":" https://instagram.com/a "},
		{"network":"myspace","url":"https://myspace.com/a"},
		{"network":"facebook","url":"http://facebook.com/a"},
		{"network":"x","url":"https://u:p@x.com/a"},
		{"network":"telegram","url":"javascript:alert(1)"},
		{"network":"youtube","url":5},
		{"network":"tiktok","url":"https://tiktok.com/@a","extra":"leak"},
		{"url":"https://vk.com/a"}
	]`))
	want := []SiteSocial{{"instagram", "https://instagram.com/a"}, {"tiktok", "https://tiktok.com/@a"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("array: got %+v", got)
	}

	obj := ParseSiteSocials([]byte(`{"youtube":"https://youtube.com/c","bogus":"https://b.example","facebook":"https://fb.com/a"}`))
	if len(obj) != 2 || obj[0].Network != "facebook" || obj[1].Network != "youtube" {
		t.Errorf("object: got %+v", obj)
	}

	long := "https://example.com/" + strings.Repeat("a", 2040)
	if g := ParseSiteSocials([]byte(`[{"network":"x","url":"` + long + `"}]`)); len(g) != 0 {
		t.Errorf("long url kept: %+v", g)
	}

	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, `{"network":"threads","url":"https://threads.net/u`+strings.Repeat("a", i)+`"}`)
	}
	g := ParseSiteSocials([]byte("[" + strings.Join(many, ",") + "]"))
	if len(g) != 8 || g[0].URL != "https://threads.net/u" || g[7].URL != "https://threads.net/uaaaaaaa" {
		t.Errorf("cap/order: %d %+v", len(g), g)
	}
}
