package hcatalog

import (
	"context"
	"testing"
)

func TestNormalizePromoterSlug(t *testing.T) {
	ok := map[string]string{
		"teatrkolibel":    "teatrkolibel",
		"  TeatrKolibel ": "teatrkolibel",
		"vino-and-co-2":   "vino-and-co-2",
		"ab":              "ab",
	}
	for in, want := range ok {
		got, valid := NormalizePromoterSlug(in)
		if !valid || got != want {
			t.Errorf("NormalizePromoterSlug(%q) = %q, %v; want %q, true", in, got, valid, want)
		}
	}
	for _, bad := range []string{"", "a", "-abc", "abc-", "a--b", "с кириллицей", "with space", "under_score", "dot.com", "x" + string(make([]byte, 70))} {
		if got, valid := NormalizePromoterSlug(bad); valid {
			t.Errorf("NormalizePromoterSlug(%q) accepted as %q", bad, got)
		}
	}
}

type fakeSlugChecker map[string]bool

func (f fakeSlugChecker) PromoterSlugTaken(_ context.Context, slug string) (bool, error) {
	return f[slug], nil
}

// The automatic slug transliterates the name and steps past taken ones —
// including an organization's slug, which the checker also reports.
func TestAutoPromoterSlug(t *testing.T) {
	cases := []struct {
		name  string
		taken fakeSlugChecker
		want  string
	}{
		{"Семейный Театр Колыбель", fakeSlugChecker{}, "semeinyi-teatr-kolybel"},
		{"Partner Agency s.r.o.", fakeSlugChecker{"partner-agency-s-r-o": true}, "partner-agency-s-r-o-2"},
		{"Lampyris", fakeSlugChecker{"lampyris": true, "lampyris-2": true}, "lampyris-3"},
		{"???", fakeSlugChecker{}, "promoter"},
		{"אמן", fakeSlugChecker{"promoter": true}, "promoter-2"},
	}
	for _, c := range cases {
		got, err := autoPromoterSlug(context.Background(), c.taken, c.name)
		if err != nil || got != c.want {
			t.Errorf("autoPromoterSlug(%q) = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}
