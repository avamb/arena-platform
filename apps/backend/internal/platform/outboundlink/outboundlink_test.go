package outboundlink

import (
	"net/url"
	"strings"
	"testing"
)

func rules(t *testing.T, js string) *Rules {
	t.Helper()
	r, err := Parse([]byte(js))
	if err != nil || r == nil {
		t.Fatalf("parse: %v", err)
	}
	return r
}

const std = `{"utm_source":"lampyrisevents","sellers":{"bilesuparadize.lv":{"utm":false},"pankoncert.pl":{"sub_id_param":"subid"}}}`

func TestBuildAddsLabels(t *testing.T) {
	got, ok := Build(rules(t, std), "https://www.eventim.de/event/cory-henry?affiliate=X",
		Context{EventSlug: "Cory Henry", City: "Berlin"})
	if !ok {
		t.Fatal("not changed")
	}
	u, _ := url.Parse(got)
	q := u.Query()
	want := map[string]string{"utm_source": "lampyrisevents", "utm_medium": "referral", "utm_campaign": "cory-henry", "utm_content": "berlin", "affiliate": "X"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q (%s)", k, q.Get(k), v, got)
		}
	}
}

func TestBuildKeepsExistingUTM(t *testing.T) {
	got, _ := Build(rules(t, std), "https://eventim.de/e?utm_source=partner", Context{EventSlug: "a"})
	if !strings.Contains(got, "utm_source=partner") || strings.Contains(got, "lampyrisevents") {
		t.Errorf("overwrote: %s", got)
	}
}

func TestBuildSellerWithoutUTM(t *testing.T) {
	link := "https://www.bilesuparadize.lv/lv/event/1"
	got, ok := Build(rules(t, std), link, Context{EventSlug: "a", City: "Jurmala", ClickID: "c1"})
	if ok || got != link {
		t.Errorf("BP link changed: %s", got)
	}
}

func TestBuildSubID(t *testing.T) {
	got, ok := Build(rules(t, std), "https://pankoncert.pl/e", Context{EventSlug: "a", City: "Warszawa", ClickID: "ck_1"})
	if !ok || !strings.Contains(got, "subid=ck_1") {
		t.Errorf("no sub id: %s", got)
	}
	got, _ = Build(rules(t, std), "https://pankoncert.pl/e", Context{EventSlug: "a"})
	if strings.Contains(got, "subid") {
		t.Errorf("sub id without click: %s", got)
	}
}

func TestBuildLeavesOtherLinks(t *testing.T) {
	for _, l := range []string{"http://eventim.de/e", "javascript:alert(1)", "", "https://u:p@eventim.de/e", "/relative"} {
		if got, ok := Build(rules(t, std), l, Context{EventSlug: "a"}); ok || got != l {
			t.Errorf("%q changed to %q", l, got)
		}
	}
	if got, ok := Build(nil, "https://a.b/c", Context{}); ok || got != "https://a.b/c" {
		t.Error("nil rules changed link")
	}
}

func TestParseRefuses(t *testing.T) {
	for _, js := range []string{`[]`, `{}`, `{"utm_source":"a b"}`, `{"utm_source":"x","sellers":{"WWW.A.com":{}}}`, `{"utm_source":"x","sellers":{"a.com":{"sub_id_param":"a b"}}}`} {
		if _, err := Parse([]byte(js)); err == nil {
			t.Errorf("accepted %s", js)
		}
	}
	if r, err := Parse([]byte("null")); r != nil || err != nil {
		t.Error("null must be no rule")
	}
}

func TestSlugUnicode(t *testing.T) {
	if got := slug("  Tel  Aviv / Яффо "); got != "tel-aviv-яффо" {
		t.Errorf("slug = %q", got)
	}
}
