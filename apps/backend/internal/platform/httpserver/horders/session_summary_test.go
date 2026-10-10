package horders

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestBuildSessionSummary_FoldsPlacesMoneyAndRefunds(t *testing.T) {
	sess := uuid.New()
	seated := uuid.New()
	ga := uuid.New()
	plan := uuid.New()
	header := gen.SessionSummaryHeaderRow{
		ID: sess, EventID: uuid.New(), OrgID: uuid.New(),
		EventName: "Test Event", StartAt: time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC),
		Status: "scheduled", CapacityTotal: 566, SeatingPlanVersionID: &plan,
	}
	rows := summaryRows{
		places: []gen.SessionSummaryPlacesRow{
			{SessionID: sess, TierID: &seated, Kind: "seat", Available: 51, Held: 0, Sold: 22, SoldUpstream: 21, Unavailable: 17},
			{SessionID: sess, TierID: &ga, Kind: "ga_unit", Available: 475, Held: 0, Sold: 1},
			// A seat no category owns still counts in the hall total.
			{SessionID: sess, TierID: nil, Kind: "seat", Unavailable: 3},
		},
		tiers: []gen.SessionSummaryTierRow{
			{SessionID: sess, ID: seated, Name: "Balcony", PriceAmount: 50000, Currency: "CZK", IsOpen: true, PaidItems: 1, PaidRevenue: 50000},
			{SessionID: sess, ID: ga, Name: "Standing", PriceAmount: 59000, Currency: "CZK", IsOpen: true, PaidItems: 1, PaidRevenue: 59000},
		},
		orders: []gen.SessionSummaryOrdersRow{
			{SessionID: sess, Status: "paid", Source: "bil24_gateway", Currency: "CZK", Orders: 1, Total: 109000, Charge: 0},
			{SessionID: sess, Status: "refunded", Source: "bil24_gateway", Currency: "CZK", Orders: 1, Total: 59000, Charge: 2000, Discount: 1000},
			{SessionID: sess, Status: "pending_payment", Source: "bil24_gateway", Currency: "CZK", Orders: 2, Total: 100000},
			{SessionID: sess, Status: "expired", Source: "bil24_gateway", Currency: "CZK", Orders: 7, Total: 350000},
		},
		refunds: []gen.SessionSummaryRefundsRow{
			{SessionID: sess, Settlement: "external", State: "succeeded", Currency: "CZK", Refunds: 1, Amount: 59000},
			{SessionID: sess, Settlement: "provider", State: "requested", Currency: "CZK", Refunds: 1, Amount: 50000},
		},
		tickets: []gen.SessionSummaryTicketsRow{{SessionID: sess, Active: 2, Cancelled: 1, Used: 1}},
		promos: []gen.SessionSummaryPromoRow{
			{SessionID: sess, PromoCodeID: uuid.New(), Code: "ARENA10", Currency: "CZK", Orders: 1, Redemptions: 1, Discount: 4500},
		},
		complimentary: []gen.SessionSummaryComplimentaryRow{{SessionID: sess, Orders: 1, Tickets: 3}},
	}
	got := buildSessionSummary(header, rows)
	if len(got.Promos) != 1 || got.Promos[0].Code != "ARENA10" || got.Promos[0].Discount != 4500 || got.Promos[0].Redemptions != 1 {
		t.Fatalf("promos: %+v", got.Promos)
	}
	if got.Complimentary.Orders != 1 || got.Complimentary.Tickets != 3 {
		t.Fatalf("complimentary: %+v", got.Complimentary)
	}

	if !got.Session.HasSeatingPlan || got.Session.StartAt != "2026-10-01T18:00:00Z" {
		t.Fatalf("session header: %+v", got.Session)
	}
	if s := got.Places.Seats; s.Total != 93 || s.Sold != 22 || s.SoldUpstream != 21 || s.Unavailable != 20 || s.Available != 51 {
		t.Fatalf("seat counters: %+v", s)
	}
	if g := got.Places.GA; g.Total != 476 || g.Sold != 1 || g.Available != 475 {
		t.Fatalf("ga counters: %+v", g)
	}
	if len(got.Tiers) != 2 || got.Tiers[0].Kind != "seated" || got.Tiers[1].Kind != "ga" {
		t.Fatalf("tier kinds: %+v", got.Tiers)
	}
	if p := got.Tiers[0].Places; p.Total != 90 || p.SoldUpstream != 21 {
		t.Fatalf("seated tier places: %+v", p)
	}

	if len(got.Money) != 1 {
		t.Fatalf("money rows: %+v", got.Money)
	}
	m := got.Money[0]
	// Paid covers the refunded order too; only the SUCCEEDED refund comes off.
	if m.PaidOrders != 2 || m.Paid != 168000 || m.Refunded != 59000 || m.Net != 109000 {
		t.Fatalf("money: %+v", m)
	}
	if m.ServiceCharge != 2000 || m.Discount != 1000 {
		t.Fatalf("charge/discount: %+v", m)
	}
	// An expired order is neither paid nor pending.
	if m.PendingOrders != 2 || m.Pending != 100000 {
		t.Fatalf("pending: %+v", m)
	}
	if len(got.Orders) != 4 || got.Orders[0].Status != "expired" {
		t.Fatalf("orders must be sorted by status: %+v", got.Orders)
	}
	if got.Tickets.Active != 2 || got.Tickets.Used != 1 {
		t.Fatalf("tickets: %+v", got.Tickets)
	}
	if got.Entered.Used != 1 || got.Entered.Total != 2 {
		t.Fatalf("entered: %+v", got.Entered)
	}

	// The wire keeps the historical top-level keys (admin-web renders them)
	// and adds the EC-03 ones beside them.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session", "places", "tiers", "money", "orders", "tickets", "refunds", "promos", "entered", "complimentary"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("wire has no %q key: %s", key, raw)
		}
	}
}

func TestBuildSessionSummary_EmptySessionHasNoNulls(t *testing.T) {
	got := buildSessionSummary(gen.SessionSummaryHeaderRow{ID: uuid.New()}, summaryRows{})
	if got.Tiers == nil || got.Money == nil || got.Orders == nil || got.Refunds == nil || got.Promos == nil {
		t.Fatalf("empty lists must encode as [] not null: %+v", got)
	}
	if got.Session.HasSeatingPlan {
		t.Fatal("a session without a plan must say so")
	}
	if got.Entered.Total != 0 || got.Complimentary.Tickets != 0 {
		t.Fatalf("zero counters: %+v", got)
	}
}

func TestBuildSessionSummary_KeepsCurrenciesApart(t *testing.T) {
	rows := summaryRows{
		orders: []gen.SessionSummaryOrdersRow{
			{Status: "paid", Source: "public_feed", Currency: "EUR", Orders: 3, Total: 15000},
			{Status: "paid", Source: "bil24_gateway", Currency: "CZK", Orders: 1, Total: 109000},
		},
		refunds: []gen.SessionSummaryRefundsRow{
			{Settlement: "provider", State: "succeeded", Currency: "EUR", Refunds: 1, Amount: 5000},
		},
	}
	got := buildSessionSummary(gen.SessionSummaryHeaderRow{}, rows)
	if len(got.Money) != 2 || got.Money[0].Currency != "CZK" || got.Money[1].Currency != "EUR" {
		t.Fatalf("money must be one row per currency, sorted: %+v", got.Money)
	}
	if got.Money[0].Net != 109000 || got.Money[1].Net != 10000 {
		t.Fatalf("net per currency: %+v", got.Money)
	}
}

// Two sessions of one event: the totals merge the groups of both, the
// per-session list keeps each session's own figures, and categories with the
// same name and price become one row of the event table.
func TestBuildEventSummary_TotalsAndPerSession(t *testing.T) {
	s1, s2 := uuid.New(), uuid.New()
	t1, t2 := uuid.New(), uuid.New()
	promo := uuid.New()
	first := time.Date(2026, 11, 1, 19, 0, 0, 0, time.UTC)
	last := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	header := gen.EventSummaryHeaderRow{
		ID: uuid.New(), OrgID: uuid.New(), Name: "Tour", Status: "published",
		FirstSessionAt: &first, LastSessionAt: &last,
	}
	venue := "Hall"
	sessions := []gen.EventSummarySessionRow{
		{ID: s1, StartAt: first, EndAt: first.Add(2 * time.Hour), Status: "scheduled", CapacityTotal: 10, VenueName: &venue},
		{ID: s2, StartAt: last.Add(-2 * time.Hour), EndAt: last, Status: "cancelled", CapacityTotal: 10, VenueName: &venue},
	}
	rows := summaryRows{
		places: []gen.SessionSummaryPlacesRow{
			{SessionID: s1, TierID: &t1, Kind: "ga_unit", Available: 7, Sold: 3},
			{SessionID: s2, TierID: &t2, Kind: "ga_unit", Available: 9, Sold: 1},
		},
		tiers: []gen.SessionSummaryTierRow{
			{SessionID: s1, ID: t1, Name: "GA", PriceAmount: 2000, Currency: "EUR", IsOpen: true, PaidItems: 3, PaidRevenue: 6000},
			{SessionID: s2, ID: t2, Name: "GA", PriceAmount: 2000, Currency: "EUR", IsOpen: false, PaidItems: 1, PaidRevenue: 2000},
		},
		orders: []gen.SessionSummaryOrdersRow{
			{SessionID: s1, Status: "paid", Source: "public_feed", Currency: "EUR", Orders: 2, Total: 6000},
			{SessionID: s2, Status: "paid", Source: "public_feed", Currency: "EUR", Orders: 1, Total: 2000},
			{SessionID: s2, Status: "refunded", Source: "public_feed", Currency: "EUR", Orders: 1, Total: 2000},
		},
		refunds: []gen.SessionSummaryRefundsRow{
			{SessionID: s2, Settlement: "provider", State: "succeeded", Currency: "EUR", Refunds: 1, Amount: 2000},
		},
		tickets: []gen.SessionSummaryTicketsRow{
			{SessionID: s1, Active: 3, Used: 2},
			{SessionID: s2, Active: 1, Cancelled: 1},
		},
		promos: []gen.SessionSummaryPromoRow{
			{SessionID: s1, PromoCodeID: promo, Code: "TOUR", Currency: "EUR", Orders: 1, Redemptions: 1, Discount: 500},
			{SessionID: s2, PromoCodeID: promo, Code: "TOUR", Currency: "EUR", Orders: 1, Redemptions: 0, Discount: 500},
		},
		complimentary: []gen.SessionSummaryComplimentaryRow{{SessionID: s1}, {SessionID: s2, Orders: 1, Tickets: 1}},
	}
	got := buildEventSummary(header, sessions, rows)

	if got.Event.SessionCount != 2 || got.Event.FirstSessionAt == nil || *got.Event.FirstSessionAt != "2026-11-01T19:00:00Z" {
		t.Fatalf("event header: %+v", got.Event)
	}
	if got.Places.GA.Total != 20 || got.Places.GA.Sold != 4 || got.Places.GA.Available != 16 {
		t.Fatalf("event places: %+v", got.Places)
	}
	if len(got.Tiers) != 1 || got.Tiers[0].Sessions != 2 || !got.Tiers[0].IsOpen ||
		got.Tiers[0].PaidItems != 4 || got.Tiers[0].PaidRevenue != 8000 || got.Tiers[0].Places.Sold != 4 {
		t.Fatalf("merged tiers: %+v", got.Tiers)
	}
	if len(got.Money) != 1 || got.Money[0].PaidOrders != 4 || got.Money[0].Paid != 10000 ||
		got.Money[0].Refunded != 2000 || got.Money[0].Net != 8000 {
		t.Fatalf("event money: %+v", got.Money)
	}
	// Order groups of both sessions merge into one row per status.
	if len(got.Orders) != 2 || got.Orders[0].Status != "paid" || got.Orders[0].Orders != 3 {
		t.Fatalf("merged orders: %+v", got.Orders)
	}
	if got.Tickets.Active != 4 || got.Entered.Used != 2 || got.Entered.Total != 4 {
		t.Fatalf("event tickets: %+v %+v", got.Tickets, got.Entered)
	}
	if len(got.Promos) != 1 || got.Promos[0].Orders != 2 || got.Promos[0].Redemptions != 1 || got.Promos[0].Discount != 1000 {
		t.Fatalf("merged promos: %+v", got.Promos)
	}
	if got.Complimentary.Orders != 1 || got.Complimentary.Tickets != 1 {
		t.Fatalf("event complimentary: %+v", got.Complimentary)
	}

	if len(got.Sessions) != 2 {
		t.Fatalf("sessions: %+v", got.Sessions)
	}
	a, b := got.Sessions[0], got.Sessions[1]
	if a.ID != s1.String() || a.Status != "scheduled" || a.VenueName == nil || a.EndAt != "2026-11-01T21:00:00Z" {
		t.Fatalf("session 1 header: %+v", a.eventSummarySessionHeader)
	}
	if len(a.Money) != 1 || a.Money[0].Paid != 6000 || a.Money[0].Net != 6000 || a.Entered.Used != 2 {
		t.Fatalf("session 1 figures: %+v", a.summaryCore)
	}
	if len(b.Money) != 1 || b.Money[0].Paid != 4000 || b.Money[0].Refunded != 2000 || b.Money[0].Net != 2000 ||
		len(b.Tiers) != 1 || b.Tiers[0].ID != t2.String() || b.Complimentary.Tickets != 1 {
		t.Fatalf("session 2 figures: %+v", b.summaryCore)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"event", "places", "tiers", "money", "orders", "tickets", "entered", "refunds", "promos", "complimentary", "sessions"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("wire has no %q key", key)
		}
	}
	var sess []map[string]json.RawMessage
	if err := json.Unmarshal(wire["sessions"], &sess); err != nil {
		t.Fatal(err)
	}
	if _, ok := sess[0]["start_at"]; !ok {
		t.Errorf("session entry must flatten its header: %s", wire["sessions"])
	}
	if _, ok := sess[0]["money"]; !ok {
		t.Errorf("session entry must flatten its figures: %s", wire["sessions"])
	}
}

func TestMergeEventTiers_DifferentPricesStayApart(t *testing.T) {
	tiers := []summaryTier{
		{ID: "a", Name: "VIP", Kind: "ga", PriceAmount: 5000, Currency: "EUR", PaidItems: 1},
		{ID: "b", Name: "GA", Kind: "ga", PriceAmount: 2000, Currency: "EUR", PaidItems: 2},
		{ID: "c", Name: "VIP", Kind: "ga", PriceAmount: 6000, Currency: "EUR", PaidItems: 3},
		{ID: "d", Name: "GA", Kind: "ga", PriceAmount: 2000, Currency: "EUR", PaidItems: 4},
	}
	got := mergeEventTiers(tiers)
	if len(got) != 3 {
		t.Fatalf("rows: %+v", got)
	}
	// First appearance orders the names; the two VIP prices sit together.
	if got[0].Name != "VIP" || got[0].PriceAmount != 5000 || got[1].Name != "VIP" || got[1].PriceAmount != 6000 ||
		got[2].Name != "GA" || got[2].PaidItems != 6 || got[2].Sessions != 2 {
		t.Fatalf("order: %+v", got)
	}
	if mergeEventTiers(nil) == nil {
		t.Fatal("an empty table must encode as [] not null")
	}
}
