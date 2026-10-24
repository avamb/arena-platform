package hsalelinks

import (
	"net/http/httptest"
	"testing"
)

func TestQRParams(t *testing.T) {
	ok := []struct {
		query      string
		format     string
		size, marg int
	}{
		{"", "png", 1024, 4},
		{"format=svg", "svg", 1024, 4},
		{"format=PNG&size=256&margin=2", "png", 256, 2},
		{"size=2048&margin=8", "png", 2048, 8},
	}
	for _, c := range ok {
		r := httptest.NewRequest("GET", "/x?"+c.query, nil)
		f, s, m, err := qrParams(r)
		if err != nil || f != c.format || s != c.size || m != c.marg {
			t.Errorf("qrParams(%q) = %q %d %d %v", c.query, f, s, m, err)
		}
	}
	for _, q := range []string{
		"format=gif", "size=255", "size=2049", "size=abc", "size=-1", "margin=1", "margin=9", "margin=x",
	} {
		r := httptest.NewRequest("GET", "/x?"+q, nil)
		if _, _, _, err := qrParams(r); err == nil || err.code != "sales_link.invalid_qr_params" || err.status != 400 {
			t.Errorf("qrParams(%q) accepted or wrong error: %v", q, err)
		}
	}
}

func TestETagMatches(t *testing.T) {
	const tag = `"abc"`
	for header, want := range map[string]bool{
		"": false, `"abc"`: true, `W/"abc"`: true, `"x", "abc"`: true, "*": true, `"x"`: false,
	} {
		if got := etagMatches(header, tag); got != want {
			t.Errorf("etagMatches(%q) = %v, want %v", header, got, want)
		}
	}
}
