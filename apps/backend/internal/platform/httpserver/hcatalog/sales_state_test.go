package hcatalog

import (
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestSalesStateFor(t *testing.T) {
	t.Parallel()
	next := time.Now().Add(24 * time.Hour)
	cases := []struct {
		name   string
		status string
		facts  gen.EventSalesFacts
		ok     bool
		want   string
	}{
		{"no sessions at all", "published", gen.EventSalesFacts{}, false, SalesStateArchived},
		{"every session ended", "published", gen.EventSalesFacts{SessionCount: 2, FutureSessions: 0}, true, SalesStateArchived},
		{"cancelled event still has future sessions", "cancelled",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, NextSessionAt: &next, Selling: true, PlacesLeft: true}, true, SalesStateArchived},
		{"archived event", "archived",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, Selling: true, PlacesLeft: true}, true, SalesStateArchived},
		{"selling now", "published",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, NextSessionAt: &next, Selling: true, PlacesLeft: true}, true, SalesStateOnSale},
		{"a draft is never on sale, whatever its sessions say", "draft",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, Selling: true, PlacesLeft: true}, true, SalesStateDraft},
		{"a sold-out draft is still a draft", "draft",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, Selling: false, PlacesLeft: false}, true, SalesStateDraft},
		{"a draft whose sessions all ended is archived", "draft",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 0}, true, SalesStateArchived},
		{"future session, no free place anywhere", "published",
			gen.EventSalesFacts{SessionCount: 2, FutureSessions: 1, Selling: false, PlacesLeft: false}, true, SalesStateSoldOut},
		{"places only in a closed category", "published",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, Selling: false, PlacesLeft: true}, true, SalesStateUpcoming},
		{"sales ended before the start, places left", "published",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, Selling: false, PlacesLeft: true}, true, SalesStateUpcoming},
		// Partner box office sessions (migration 0220) own no seats.
		{"only partner sessions: external, not sold out", "published",
			gen.EventSalesFacts{SessionCount: 2, FutureSessions: 2, FutureExternalSessions: 2}, true, SalesStateExternal},
		{"only partner sessions in the future, a past platform one", "published",
			gen.EventSalesFacts{SessionCount: 2, FutureSessions: 1, FutureExternalSessions: 1}, true, SalesStateExternal},
		{"a draft sold by a partner is still a draft", "draft",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1, FutureExternalSessions: 1}, true, SalesStateDraft},
		{"partner sessions all ended: archived", "published",
			gen.EventSalesFacts{SessionCount: 1, FutureSessions: 0}, true, SalesStateArchived},
		{"mixed: the platform session sells", "published",
			gen.EventSalesFacts{SessionCount: 2, FutureSessions: 2, FutureExternalSessions: 1, Selling: true, PlacesLeft: true}, true, SalesStateOnSale},
		{"mixed: the platform session is sold out", "published",
			gen.EventSalesFacts{SessionCount: 2, FutureSessions: 2, FutureExternalSessions: 1}, true, SalesStateSoldOut},
		{"mixed: the platform session is not open yet", "published",
			gen.EventSalesFacts{SessionCount: 2, FutureSessions: 2, FutureExternalSessions: 1, PlacesLeft: true}, true, SalesStateUpcoming},
	}
	if SalesStateExternal != "external" {
		t.Fatalf("SalesStateExternal = %q, want the session list's \"external\"", SalesStateExternal)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := salesStateFor(c.status, c.facts, c.ok); got != c.want {
				t.Fatalf("salesStateFor(%q, %+v, %v) = %q, want %q", c.status, c.facts, c.ok, got, c.want)
			}
		})
	}
}
