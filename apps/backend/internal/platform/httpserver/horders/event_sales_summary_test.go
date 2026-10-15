package horders

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestParseSalesSummaryIDs(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	ids, code, _ := parseSalesSummaryIDs(" " + a.String() + " , " + b.String() + "," + a.String())
	if code != "" || len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Fatalf("ids = %v code = %q, want [a b] (a repeat counts once, spaces are trimmed)", ids, code)
	}
	cases := map[string]string{
		"":                              "events.ids_required",
		"   ":                           "events.ids_required",
		a.String() + ",nope":            "events.invalid_ids",
		a.String() + ",":                "events.invalid_ids",
		",":                             "events.invalid_ids",
		manyIDs(maxSalesSummaryIDs + 1): "events.too_many_ids",
	}
	for in, want := range cases {
		if _, code, _ := parseSalesSummaryIDs(in); code != want {
			t.Errorf("%q: code %q, want %q", shorten(in), code, want)
		}
	}
	if ids, code, _ := parseSalesSummaryIDs(manyIDs(maxSalesSummaryIDs)); code != "" || len(ids) != maxSalesSummaryIDs {
		t.Errorf("exactly %d ids must pass: %d ids, code %q", maxSalesSummaryIDs, len(ids), code)
	}
}

func manyIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	return strings.Join(ids, ",")
}

func shorten(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

func TestBuildEventSalesSummaries_FoldsPerEventInRequestOrder(t *testing.T) {
	org := uuid.New()
	e1, e2, missing := uuid.New(), uuid.New(), uuid.New()
	s1, s2, s3 := uuid.New(), uuid.New(), uuid.New()
	headers := []gen.EventSummaryHeaderRow{
		{ID: e1, OrgID: org, Name: "One", Status: "published"},
		{ID: e2, OrgID: org, Name: "Two", Status: "published"},
	}
	sessions := []gen.EventSummarySessionIDRow{{EventID: e1, SessionID: s1}, {EventID: e1, SessionID: s2}, {EventID: e2, SessionID: s3}}
	rows := summaryRows{
		tickets: []gen.SessionSummaryTicketsRow{
			{SessionID: s1, Active: 2, Used: 1},
			{SessionID: s2, Active: 3},
			{SessionID: s3, Active: 7},
		},
		orders: []gen.SessionSummaryOrdersRow{
			{SessionID: s1, Status: "paid", Source: "public_feed", Currency: "EUR", Orders: 1, Total: 1000},
			{SessionID: s2, Status: "paid", Source: "public_feed", Currency: "EUR", Orders: 2, Total: 500},
		},
	}
	got := buildEventSalesSummaries([]uuid.UUID{e2, missing, e1}, headers, sessions, rows)
	if len(got) != 2 || got[0].Event.ID != e2.String() || got[1].Event.ID != e1.String() {
		t.Fatalf("entries = %+v, want e2 then e1, the missing id skipped", got)
	}
	if got[0].Tickets.Active != 7 || got[0].Event.SessionCount != 1 {
		t.Errorf("e2: %+v", got[0])
	}
	// Two sessions of e1 fold into one event total, exactly as buildEventSummary does.
	if got[1].Tickets.Active != 5 || got[1].Entered.Used != 1 || got[1].Event.SessionCount != 2 {
		t.Errorf("e1 tickets: %+v", got[1])
	}
	if len(got[1].Money) != 1 || got[1].Money[0].Paid != 1500 || got[1].Money[0].PaidOrders != 3 {
		t.Errorf("e1 money: %+v", got[1].Money)
	}
	if got := buildEventSalesSummaries(nil, nil, nil, summaryRows{}); got == nil || len(got) != 0 {
		t.Errorf("an empty batch must be an empty non-nil list, got %v", got)
	}
}
