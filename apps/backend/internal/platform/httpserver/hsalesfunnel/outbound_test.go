package hsalesfunnel

import (
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestBuildOutbound_SumsAndSorts(t *testing.T) {
	d1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	rows := []gen.OutboundClickRow{
		{Day: d2, Seller: "Karabas", Hits: 3},
		{Day: d1, Seller: "Karabas", Hits: 2},
		{Day: d1, Seller: "Concert", Hits: 4},
	}
	got := BuildOutbound("e", "2026-11-01", "2026-11-02", nil, 18, rows, []gen.OutboundClickSource{{UTMSource: "meta", Hits: 9}})
	if got.Total != 9 || got.Visits != 18 {
		t.Fatalf("total/visits = %d/%d", got.Total, got.Visits)
	}
	if got.ShareOfVisitors == nil || *got.ShareOfVisitors != 0.5 {
		t.Errorf("share = %v", got.ShareOfVisitors)
	}
	if len(got.ByDay) != 2 || got.ByDay[0].Day != "2026-11-01" || got.ByDay[0].Clicks != 6 || got.ByDay[1].Clicks != 3 {
		t.Errorf("by_day = %+v", got.ByDay)
	}
	if len(got.BySeller) != 2 || got.BySeller[0].Seller != "Karabas" || got.BySeller[0].Clicks != 5 {
		t.Errorf("by_seller = %+v", got.BySeller)
	}
	if len(got.BySource) != 1 || got.BySource[0].UTMSource != "meta" {
		t.Errorf("by_source = %+v", got.BySource)
	}
}

func TestBuildOutbound_EmptyHasEmptyArraysAndNoShare(t *testing.T) {
	got := BuildOutbound("e", "a", "b", nil, 0, nil, nil)
	if got.ByDay == nil || got.BySeller == nil || got.BySource == nil {
		t.Error("arrays must not be nil")
	}
	if got.ShareOfVisitors != nil || got.Total != 0 {
		t.Errorf("share = %v total = %d", got.ShareOfVisitors, got.Total)
	}
}
