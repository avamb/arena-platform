package eventbot

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestPageStatsText(t *testing.T) {
	b := ecTestBot(t)
	if got := b.pageStatsText("en", openapi.PageStatsCounts{}); got != "" {
		t.Errorf("no visits must show no line, got %q", got)
	}
	got := b.pageStatsText("en", openapi.PageStatsCounts{Hits: 10, SuspectHits: 2})
	if !strings.Contains(got, "Page views: 12 in 30 days") || !strings.Contains(got, "2 of them look like bots") {
		t.Errorf("en = %q", got)
	}
	// Without suspicious visits the parenthesis is left out.
	if got := b.pageStatsText("en", openapi.PageStatsCounts{Hits: 5}); strings.Contains(got, "(") {
		t.Errorf("no suspect hits must leave out the note: %q", got)
	}
	ru := b.pageStatsText("ru", openapi.PageStatsCounts{Hits: 10, SuspectHits: 2})
	if !strings.Contains(ru, "Просмотры страницы: 12 за 30 дн.") || !strings.Contains(ru, "из них подозрительных 2") {
		t.Errorf("ru = %q", ru)
	}
	es := b.pageStatsText("es", openapi.PageStatsCounts{Hits: 1})
	if !strings.Contains(es, "Visitas a la página: 1 en 30 días") {
		t.Errorf("es = %q", es)
	}
}
