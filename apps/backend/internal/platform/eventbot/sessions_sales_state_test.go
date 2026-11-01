package eventbot

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

func TestSesSalesStateButtons_OfferEveryStateButTheCurrentOne(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		manual, effective string
		want              string
	}{
		{"", "open", "soon,sold_out"},
		// No state set but nothing sells yet: "open" is still the current
		// choice, the platform decided the rest.
		{"", "soon", "soon,sold_out"},
		{"", "sold_out", "soon,sold_out"},
		{"soon", "soon", "open,sold_out"},
		{"sold_out", "sold_out", "open,soon"},
		{"", "ended", ""},
		{"sold_out", "cancelled", ""},
	} {
		if got := strings.Join(sesSalesStateButtons(c.manual, c.effective), ","); got != c.want {
			t.Errorf("buttons(%q, %q) = %q, want %q", c.manual, c.effective, got, c.want)
		}
	}
}

func TestSesSalesState_EveryTextRendersInEveryLocale(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	str := func(s string) *string { return &s }
	for _, loc := range SupportedLocales {
		var texts []string
		for _, st := range []string{salesstate.Open, salesstate.Soon, salesstate.SoldOut, salesstate.Ended, salesstate.Cancelled} {
			texts = append(texts, b.sesSalesStateName(loc, st))
		}
		manual := sesItem{Raw: openapi.SessionItem{SalesState: str("sold_out"), ManualSalesState: str("sold_out")}}
		auto := sesItem{Raw: openapi.SessionItem{SalesState: str("open")}}
		texts = append(texts, b.sesSalesStateLine(loc, manual), b.sesSalesStateLine(loc, auto))
		for _, bt := range b.sesSalesStateRow(loc, manual) {
			texts = append(texts, bt.Label)
		}
		for _, bt := range b.sesSalesStateRow(loc, auto) {
			texts = append(texts, bt.Label)
		}
		for _, s := range []string{salesstate.SitePending, salesstate.SiteRetrying, salesstate.SiteDelivered, salesstate.SiteFailed} {
			texts = append(texts, b.sesSiteNote(loc, openapi.SessionSalesStateSiteSync{Status: s, Sites: 2, Attempts: 3}))
		}
		for _, s := range sesSalesStates {
			texts = append(texts, b.sesT(loc, "bot.ses.ss_confirm_"+s, map[string]any{"Name": "E", "When": "W"}))
		}
		for _, text := range texts {
			if text == "" || strings.Contains(text, "bot.ses.") || strings.Contains(text, "<no value>") {
				t.Errorf("%s: bad text %q", loc, text)
			}
		}
		if got := b.sesSiteNote(loc, openapi.SessionSalesStateSiteSync{Status: salesstate.SiteNone}); got != "" {
			t.Errorf("%s: a session no site sells says %q about sites", loc, got)
		}
		if strings.Contains(b.sesSalesStateLine(loc, auto), b.sesSalesStateLine(loc, manual)) {
			t.Errorf("%s: the manual line must differ from the automatic one", loc)
		}
	}
}
