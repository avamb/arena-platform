package hsalesfunnel

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestBuild_SixStepsWithShares(t *testing.T) {
	link := uuid.New()
	code, label := "abc2345", "Poster"
	out := Build(Input{
		EventID: uuid.New(),
		From:    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		To:      time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
		Client: []gen.SalesFunnelClientRow{
			{Step: "view", UTMSource: "", UTMCampaign: "", Hits: 80, SuspectHits: 5},
			{Step: "view", UTMSource: "ig", UTMCampaign: "spring", Hits: 20},
			{Step: "session_open", UTMSource: "ig", UTMCampaign: "spring", Hits: 10},
			{Step: "session_open", Hits: 40},
			{Step: "seats_chosen", Hits: 25},
		},
		Server: []gen.FunnelServerRow{
			{UTMSource: "ig", UTMCampaign: "spring", SalesLinkID: &link, SalesLinkCode: &code, SalesLinkLabel: &label, Currency: "EUR", Started: 4, PaymentOpen: 3, Paid: 2, Revenue: 5400},
			{Currency: "EUR", Started: 6, PaymentOpen: 5, Paid: 4, Revenue: 9000},
			{Currency: "CZK", Started: 1, PaymentOpen: 1, Paid: 1, Revenue: 70000},
		},
	})
	want := []int64{100, 50, 25, 11, 9, 7}
	if len(out.Steps) != 6 {
		t.Fatalf("steps = %d; want 6", len(out.Steps))
	}
	for i, w := range want {
		if out.Steps[i].Count == nil || *out.Steps[i].Count != w {
			t.Errorf("step %s count = %v; want %d", out.Steps[i].Step, out.Steps[i].Count, w)
		}
	}
	if out.Steps[0].SuspectCount != 5 {
		t.Errorf("view suspect = %d; want 5", out.Steps[0].SuspectCount)
	}
	if out.Steps[0].ShareOfPrevious != nil {
		t.Error("the first step has no share")
	}
	if s := out.Steps[1].ShareOfPrevious; s == nil || *s != 0.5 {
		t.Errorf("session_open share = %v; want 0.5", s)
	}
	if len(out.Revenue) != 2 || out.Revenue[0].Currency != "CZK" || out.Revenue[0].AmountMinor != 70000 ||
		out.Revenue[1].Currency != "EUR" || out.Revenue[1].AmountMinor != 14400 {
		t.Errorf("revenue = %+v", out.Revenue)
	}
	if len(out.Sources) != 2 {
		t.Fatalf("sources = %d; want 2", len(out.Sources))
	}
	if out.Sources[0].UTMSource != "" || *out.Sources[0].Steps[0].Count != 80 {
		t.Errorf("the biggest source first, got %+v", out.Sources[0])
	}
	if len(out.SalesLink) != 1 || out.SalesLink[0].Paid != 2 || out.SalesLink[0].CheckoutStart != 4 || out.SalesLink[0].Revenue[0].AmountMinor != 5400 {
		t.Errorf("sales links = %+v", out.SalesLink)
	}
	if out.Gateway != nil {
		t.Error("no gateway activity: gateway must be null")
	}
	if out.From != "2026-10-01" || out.To != "2026-10-30" {
		t.Errorf("window = %s..%s", out.From, out.To)
	}
}

func TestBuild_SessionFilterHasNoViewCount(t *testing.T) {
	sid := uuid.New()
	out := Build(Input{
		EventID: uuid.New(), SessionID: &sid,
		Client: []gen.SalesFunnelClientRow{{Step: "session_open", Hits: 10}, {Step: "seats_chosen", Hits: 5}},
	})
	if out.Steps[0].Count != nil {
		t.Error("view must have no count for one session")
	}
	if out.Steps[1].ShareOfPrevious != nil {
		t.Error("session_open has nothing to be a share of")
	}
	if s := out.Steps[2].ShareOfPrevious; s == nil || *s != 0.5 {
		t.Errorf("seats_chosen share = %v; want 0.5", s)
	}
	if out.SessionID == nil || *out.SessionID != sid.String() {
		t.Errorf("session_id = %v", out.SessionID)
	}
}

func TestBuild_ZeroPreviousStepGivesNoShare(t *testing.T) {
	out := Build(Input{EventID: uuid.New(), Client: []gen.SalesFunnelClientRow{{Step: "session_open", Hits: 3}}})
	if out.Steps[1].ShareOfPrevious != nil {
		t.Error("a share over zero views must be null, not infinity")
	}
	if len(out.Revenue) != 0 || len(out.Sources) != 1 {
		t.Errorf("revenue=%v sources=%d", out.Revenue, len(out.Sources))
	}
}

func TestBuild_GatewayStartsAtTheCart(t *testing.T) {
	out := Build(Input{
		EventID:      uuid.New(),
		GatewayCarts: 10,
		GatewayOrders: []gen.FunnelGatewayOrderRow{
			{Currency: "ILS", Started: 4, Paid: 2, Revenue: 30000},
		},
	})
	if out.Gateway == nil {
		t.Fatal("gateway must be reported")
	}
	got := out.Gateway.Steps
	if len(got) != 3 || got[0].Step != "seats_chosen" || got[1].Step != "checkout_start" || got[2].Step != "paid" {
		t.Fatalf("gateway steps = %+v", got)
	}
	if *got[0].Count != 10 || *got[1].Count != 4 || *got[2].Count != 2 {
		t.Errorf("gateway counts = %d/%d/%d", *got[0].Count, *got[1].Count, *got[2].Count)
	}
	if got[0].ShareOfPrevious != nil || *got[1].ShareOfPrevious != 0.4 || *got[2].ShareOfPrevious != 0.5 {
		t.Errorf("gateway shares = %v %v %v", got[0].ShareOfPrevious, got[1].ShareOfPrevious, got[2].ShareOfPrevious)
	}
	if out.Gateway.Revenue[0].Currency != "ILS" {
		t.Errorf("gateway revenue = %+v", out.Gateway.Revenue)
	}
	// The gateway must not leak into the main funnel.
	if *out.Steps[3].Count != 0 || *out.Steps[5].Count != 0 {
		t.Error("gateway orders counted in the main funnel")
	}
}

func TestZoneName(t *testing.T) {
	good, bad, empty := "Europe/Prague", "Not/AZone", ""
	if zoneName(&good) != good || zoneName(&bad) != "UTC" || zoneName(&empty) != "UTC" || zoneName(nil) != "UTC" {
		t.Error("zoneName must return the venue zone, else UTC")
	}
}
