package horders

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestBuildSessionSummary_FoldsPlacesMoneyAndRefunds(t *testing.T) {
	seated := uuid.New()
	ga := uuid.New()
	plan := uuid.New()
	header := gen.SessionSummaryHeaderRow{
		ID: uuid.New(), EventID: uuid.New(), OrgID: uuid.New(),
		EventName: "Test Event", StartAt: time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC),
		Status: "scheduled", CapacityTotal: 566, SeatingPlanVersionID: &plan,
	}
	places := []gen.SessionSummaryPlacesRow{
		{TierID: &seated, Kind: "seat", Available: 51, Held: 0, Sold: 22, SoldUpstream: 21, Unavailable: 17},
		{TierID: &ga, Kind: "ga_unit", Available: 475, Held: 0, Sold: 1},
		// A seat no category owns still counts in the hall total.
		{TierID: nil, Kind: "seat", Unavailable: 3},
	}
	tiers := []gen.SessionSummaryTierRow{
		{ID: seated, Name: "Balcony", PriceAmount: 50000, Currency: "CZK", IsOpen: true, PaidItems: 1, PaidRevenue: 50000},
		{ID: ga, Name: "Standing", PriceAmount: 59000, Currency: "CZK", IsOpen: true, PaidItems: 1, PaidRevenue: 59000},
	}
	orders := []gen.SessionSummaryOrdersRow{
		{Status: "paid", Source: "bil24_gateway", Currency: "CZK", Orders: 1, Total: 109000, Charge: 0},
		{Status: "refunded", Source: "bil24_gateway", Currency: "CZK", Orders: 1, Total: 59000, Charge: 2000, Discount: 1000},
		{Status: "pending_payment", Source: "bil24_gateway", Currency: "CZK", Orders: 2, Total: 100000},
		{Status: "expired", Source: "bil24_gateway", Currency: "CZK", Orders: 7, Total: 350000},
	}
	refunds := []gen.SessionSummaryRefundsRow{
		{Settlement: "external", State: "succeeded", Currency: "CZK", Refunds: 1, Amount: 59000},
		{Settlement: "provider", State: "requested", Currency: "CZK", Refunds: 1, Amount: 50000},
	}
	tickets := gen.SessionSummaryTicketsRow{Active: 2, Cancelled: 1, Used: 1}

	promos := []gen.SessionSummaryPromoRow{
		{PromoCodeID: uuid.New(), Code: "ARENA10", Currency: "CZK", Orders: 1, Discount: 4500},
	}
	got := buildSessionSummary(header, places, tiers, orders, tickets, refunds, promos)
	if len(got.Promos) != 1 || got.Promos[0].Code != "ARENA10" || got.Promos[0].Discount != 4500 {
		t.Fatalf("promos: %+v", got.Promos)
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
}

func TestBuildSessionSummary_EmptySessionHasNoNulls(t *testing.T) {
	got := buildSessionSummary(gen.SessionSummaryHeaderRow{ID: uuid.New()}, nil, nil, nil,
		gen.SessionSummaryTicketsRow{}, nil, nil)
	if got.Tiers == nil || got.Money == nil || got.Orders == nil || got.Refunds == nil || got.Promos == nil {
		t.Fatalf("empty lists must encode as [] not null: %+v", got)
	}
	if got.Session.HasSeatingPlan {
		t.Fatal("a session without a plan must say so")
	}
}

func TestBuildSessionSummary_KeepsCurrenciesApart(t *testing.T) {
	orders := []gen.SessionSummaryOrdersRow{
		{Status: "paid", Source: "public_feed", Currency: "EUR", Orders: 3, Total: 15000},
		{Status: "paid", Source: "bil24_gateway", Currency: "CZK", Orders: 1, Total: 109000},
	}
	refunds := []gen.SessionSummaryRefundsRow{
		{Settlement: "provider", State: "succeeded", Currency: "EUR", Refunds: 1, Amount: 5000},
	}
	got := buildSessionSummary(gen.SessionSummaryHeaderRow{}, nil, nil, orders,
		gen.SessionSummaryTicketsRow{}, refunds, nil)
	if len(got.Money) != 2 || got.Money[0].Currency != "CZK" || got.Money[1].Currency != "EUR" {
		t.Fatalf("money must be one row per currency, sorted: %+v", got.Money)
	}
	if got.Money[0].Net != 109000 || got.Money[1].Net != 10000 {
		t.Fatalf("net per currency: %+v", got.Money)
	}
}
