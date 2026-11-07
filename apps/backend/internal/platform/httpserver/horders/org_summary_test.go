package horders

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestBuildOrgSummary_CardsPerSessionAndCurrency(t *testing.T) {
	s1, s2, s3 := uuid.New(), uuid.New(), uuid.New()
	tier := uuid.New()
	start := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	rows := orgSummaryRows{
		sessions: []gen.OrgSummarySessionRow{
			{ID: s1, EventID: uuid.New(), EventName: "Wine night", StartAt: start, Status: "scheduled"},
			{ID: s2, EventID: uuid.New(), EventName: "Quiet", StartAt: start, Status: "scheduled"},
			{ID: s3, EventID: uuid.New(), EventName: "Only tests", StartAt: start, Status: "scheduled"},
		},
		money: summaryRows{
			orders: []gen.SessionSummaryOrdersRow{
				{SessionID: s1, Status: "paid", Source: "widget", Currency: "EUR", Orders: 2, Total: 9000},
				{SessionID: s1, Status: "refunded", Source: "widget", Currency: "EUR", Orders: 1, Total: 3000},
				{SessionID: s1, Status: "paid", Source: "widget", Currency: "ILS", Orders: 1, Total: 20000},
				{SessionID: s1, Status: "pending_payment", Source: "widget", Currency: "USD", Orders: 4, Total: 99999},
			},
			refunds: []gen.SessionSummaryRefundsRow{
				{SessionID: s1, Settlement: "provider", State: "succeeded", Currency: "EUR", Refunds: 1, Amount: 1000},
				{SessionID: s1, Settlement: "external", State: "succeeded", Currency: "EUR", Refunds: 1, Amount: 2000},
				{SessionID: s1, Settlement: "provider", State: "requested", Currency: "EUR", Refunds: 1, Amount: 500},
			},
			promos: []gen.SessionSummaryPromoRow{
				{SessionID: s1, PromoCodeID: uuid.New(), Code: "CLUB", Currency: "EUR", Orders: 1, Redemptions: 1, Discount: 500},
			},
			testOrders: []gen.SessionSummaryTestOrdersRow{{SessionID: s1, Orders: 1}, {SessionID: s3, Orders: 2}},
		},
		tickets: []gen.SessionSummaryTicketCardRow{
			{SessionID: s1, Currency: "EUR", Sold: 4, Complimentary: 1, Refunded: 2, Active: 3},
			{SessionID: s1, Currency: "ILS", Sold: 1, Active: 1},
		},
		breakdown: []gen.SessionSummaryBreakdownRow{
			{SessionID: s1, Currency: "EUR", TierID: tier, TierName: "Standard", UnitPrice: 3000, PromoCode: "", Tickets: 3, Revenue: 9000},
			{SessionID: s1, Currency: "EUR", TierID: tier, TierName: "Standard", UnitPrice: 3000, PromoCode: "CLUB", Tickets: 1, Revenue: 2500, Discount: 500},
		},
	}
	got := buildOrgSummary(rows)

	if len(got.Sessions) != 1 || got.Sessions[0].ID != s1.String() {
		t.Fatalf("only the session with sales is listed: %+v", got.Sessions)
	}
	if got.TestOrders != 3 || got.Sessions[0].TestOrders != 1 {
		t.Fatalf("test orders: total %d, session %d", got.TestOrders, got.Sessions[0].TestOrders)
	}
	cards := got.Sessions[0].Cards
	if len(cards) != 2 || cards[0].Currency != "EUR" || cards[1].Currency != "ILS" {
		t.Fatalf("cards (pending-only USD must not make one): %+v", cards)
	}
	eur := cards[0]
	if eur.Orders != 3 || eur.Revenue != 12000 || eur.Refunded != 3000 || eur.Net != 9000 {
		t.Fatalf("EUR money (both settlements count, a requested refund does not): %+v", eur)
	}
	if eur.Sold != 4 || eur.Complimentary != 1 || eur.RefundedTickets != 2 || eur.ActiveTickets != 3 {
		t.Fatalf("EUR tickets: %+v", eur)
	}
	if len(eur.Breakdown) != 2 || eur.Breakdown[0].PromoCode != nil || eur.Breakdown[1].PromoCode == nil || *eur.Breakdown[1].PromoCode != "CLUB" {
		t.Fatalf("breakdown: %+v", eur.Breakdown)
	}
	if len(eur.Promos) != 1 || eur.Promos[0].Code != "CLUB" || len(cards[1].Promos) != 0 {
		t.Fatalf("promos: %+v / %+v", eur.Promos, cards[1].Promos)
	}
	if len(got.Totals) != 2 || got.Totals[0].Currency != "EUR" || got.Totals[0].Revenue != 12000 || got.Totals[1].Revenue != 20000 {
		t.Fatalf("totals: %+v", got.Totals)
	}
}

func TestParseOrgSummaryFilter(t *testing.T) {
	req := func(q string) *http.Request { return httptest.NewRequest(http.MethodGet, "/x?"+q, nil) }
	f, code, _ := ParseOrgSummaryFilter(req("from=2026-11-01&to=2026-11-30"))
	if code != "" || f.From == nil || f.To == nil {
		t.Fatalf("dates: %+v %q", f, code)
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); !f.To.Equal(want) {
		t.Fatalf("a date-only `to` includes that whole day: %v", f.To)
	}
	id := uuid.New()
	if f, code, _ := ParseOrgSummaryFilter(req("event_id=" + id.String())); code != "" || f.EventID == nil || *f.EventID != id {
		t.Fatalf("event: %+v %q", f, code)
	}
	if f, code, _ := ParseOrgSummaryFilter(req("")); code != "" || f.From != nil || f.To != nil || f.EventID != nil {
		t.Fatalf("empty: %+v %q", f, code)
	}
	for _, bad := range []string{"from=yesterday", "to=2026-13-40", "from=2026-11-10&to=2026-11-01", "event_id=nope"} {
		if _, code, _ := ParseOrgSummaryFilter(req(bad)); code == "" {
			t.Errorf("%q must be refused", bad)
		}
	}
}
