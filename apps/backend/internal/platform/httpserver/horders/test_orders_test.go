package horders

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestOrderSummary_CarriesIsTest(t *testing.T) {
	if orderSummary(gen.OrderRow{})["is_test"] != false {
		t.Fatal("is_test must be present and false for a live order")
	}
	if orderSummary(gen.OrderRow{IsTest: true})["is_test"] != true {
		t.Fatal("is_test must be true for a test order")
	}
}

func TestBuildSummaryCore_CountsTestOrdersAcrossSessions(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	rows := summaryRows{testOrders: []gen.SessionSummaryTestOrdersRow{
		{SessionID: a, Orders: 2}, {SessionID: b, Orders: 3},
	}}
	if got := buildSummaryCore(rows).TestOrders; got != 5 {
		t.Fatalf("TestOrders = %d, want 5", got)
	}
	if got := buildSummaryCore(rows.forSession(a)).TestOrders; got != 2 {
		t.Fatalf("session a TestOrders = %d, want 2", got)
	}
	var merged summaryRows
	merged.append(rows.forSession(b))
	if got := buildSummaryCore(merged).TestOrders; got != 3 {
		t.Fatalf("merged TestOrders = %d, want 3", got)
	}
}
