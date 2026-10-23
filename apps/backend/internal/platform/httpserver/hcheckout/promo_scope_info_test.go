package hcheckout

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func scopeRow(name string, start, salesEnd time.Time) gen.PromoScopeSessionRow {
	return gen.PromoScopeSessionRow{
		SessionID: uuid.New(), EventID: uuid.New(), EventName: name, EventStatus: "published",
		StartAt: start, SalesEndAt: salesEnd, SessionStatus: "scheduled", Timezone: "Asia/Jerusalem",
	}
}

func warnCodes(ws []promoWarning) string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return strings.Join(out, ",")
}

// The RED30 case: the code ended a day before the sales of its session.
func TestPromoWarnings(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	start := now.Add(30 * 24 * time.Hour)
	end := start.Add(-time.Hour)
	live := scopeRow("Red", start, end)

	until := end.Add(-24 * time.Hour)
	if got := warnCodes(PromoWarnings(nil, &until, []gen.PromoScopeSessionRow{live}, now)); got != PromoWarnUntilBeforeSalesEnd {
		t.Fatalf("until before sales end: %q", got)
	}
	ok := end
	if got := PromoWarnings(nil, &ok, []gen.PromoScopeSessionRow{live}, now); len(got) != 0 {
		t.Fatalf("until at sales end must be quiet: %v", got)
	}
	from := end.Add(time.Minute)
	if got := warnCodes(PromoWarnings(&from, nil, []gen.PromoScopeSessionRow{live}, now)); got != PromoWarnFromAfterSalesStart {
		t.Fatalf("from after sales: %q", got)
	}
	ss := now.Add(24 * time.Hour)
	live.SalesStartAt = &ss
	early := now
	if got := PromoWarnings(&early, nil, []gen.PromoScopeSessionRow{live}, now); len(got) != 0 {
		t.Fatalf("from before sales start must be quiet: %v", got)
	}
	late := ss.Add(time.Hour)
	if got := warnCodes(PromoWarnings(&late, nil, []gen.PromoScopeSessionRow{live}, now)); got != PromoWarnFromAfterSalesStart {
		t.Fatalf("from after sales start: %q", got)
	}

	cancelled := scopeRow("C", start, end)
	cancelled.SessionStatus = "cancelled"
	deleted := scopeRow("D", start, end)
	deleted.Deleted = true
	past := scopeRow("P", now.Add(-time.Hour), now.Add(-2*time.Hour))
	got := warnCodes(PromoWarnings(nil, &until, []gen.PromoScopeSessionRow{cancelled, deleted, past}, now))
	if got != PromoWarnSessionCancelled+","+PromoWarnSessionCancelled+","+PromoWarnSessionPast {
		t.Fatalf("cancelled/deleted/past: %q", got)
	}
	if got := PromoWarnings(nil, &until, nil, now); len(got) != 0 {
		t.Fatalf("a code for every session has nothing to compare: %v", got)
	}
}

func TestPromoHint_NamesOnlyPublishedOpenEvents(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	start := now.Add(48 * time.Hour)
	white := scopeRow("White", start, start)
	dupe := scopeRow("White", start.Add(time.Hour), start)
	draft := scopeRow("Secret", start, start)
	draft.EventStatus = "draft"
	finished := scopeRow("Old", now.Add(-72*time.Hour), now.Add(-73*time.Hour))
	gone := scopeRow("Gone", start, start)
	gone.Deleted = true
	if got := promoHintFromRows([]gen.PromoScopeSessionRow{white, dupe, draft, finished, gone}, now); got != "White" {
		t.Fatalf("hint = %q, want only White once", got)
	}
	if got := promoHintFromRows([]gen.PromoScopeSessionRow{draft}, now); got != "" {
		t.Fatalf("hint = %q, a draft must stay unnamed", got)
	}
}
