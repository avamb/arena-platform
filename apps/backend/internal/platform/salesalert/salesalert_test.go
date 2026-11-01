package salesalert

import (
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestIsSoldOut_TellsOnlyAFinalSellOut(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	base := gen.SessionSalesFactsRow{Status: "scheduled", SalesEndAt: future, Categories: 1, TotalPlaces: 10}
	for _, c := range []struct {
		name         string
		f            func(gen.SessionSalesFactsRow) gen.SessionSalesFactsRow
		sold, manual bool
	}{
		{"places left", func(r gen.SessionSalesFactsRow) gen.SessionSalesFactsRow {
			r.FreePlaces = 3
			r.Selling = true
			return r
		}, false, false},
		{"last place sold", func(r gen.SessionSalesFactsRow) gen.SessionSalesFactsRow { return r }, true, false},
		// A hold may still lapse and give the place back: not final yet.
		{"last place in an unpaid hold", func(r gen.SessionSalesFactsRow) gen.SessionSalesFactsRow { r.HeldPlaces = 1; return r }, false, false},
		{"set by hand with places left", func(r gen.SessionSalesFactsRow) gen.SessionSalesFactsRow {
			r.Manual, r.FreePlaces = "sold_out", 5
			return r
		}, true, true},
		{"coming soon by hand on an empty hall", func(r gen.SessionSalesFactsRow) gen.SessionSalesFactsRow { r.Manual = "soon"; return r }, false, false},
		// Owner decision 2026-10-31: a session without places is never sold out by itself.
		{"no places at all", func(r gen.SessionSalesFactsRow) gen.SessionSalesFactsRow {
			r.TotalPlaces, r.Categories = 0, 0
			return r
		}, false, false},
		{"sales end passed", func(r gen.SessionSalesFactsRow) gen.SessionSalesFactsRow {
			r.SalesEndAt = now.Add(-time.Hour)
			return r
		}, false, false},
	} {
		sold, manual := IsSoldOut(c.f(base), now)
		if sold != c.sold || manual != c.manual {
			t.Errorf("%s: sold=%v manual=%v, want %v %v", c.name, sold, manual, c.sold, c.manual)
		}
	}
}

func TestTexts_EveryLocaleRendersEveryAlert(t *testing.T) {
	in := SessionInfo{EventName: "Gala <b>", Start: time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC), Venue: "Hall", Timezone: "Europe/Madrid", TotalPlaces: 120, FreePlaces: 7}
	for _, loc := range []string{"en", "ru", "es", "cs", "pt-BR", ""} {
		in.OrgLocale = loc
		for _, text := range []string{
			SoldOutText(in, false, ""),
			SoldOutText(in, true, "Ana & Co"),
			SiteStaleText(in, "soon", 16),
		} {
			if strings.Contains(text, "%!") || !strings.Contains(text, "Gala &lt;b&gt;") || !strings.Contains(text, "20.11.2026 19:00") {
				t.Errorf("%q: bad text:\n%s", loc, text)
			}
		}
		if !strings.Contains(SoldOutText(in, false, ""), "120") {
			t.Errorf("%q: the sold places are not said", loc)
		}
		if m := SoldOutText(in, true, "Ana & Co"); !strings.Contains(m, "Ana &amp; Co") || !strings.Contains(m, "7") {
			t.Errorf("%q: manual text lacks the actor or the free places:\n%s", loc, m)
		}
	}
	if ru := SiteStaleText(SessionInfo{OrgLocale: "ru-RU"}, "sold_out", 20); !strings.Contains(ru, "Распродано") {
		t.Errorf("ru-RU must use the Russian texts:\n%s", ru)
	}
}
