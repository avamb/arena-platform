package hfeed

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
)

func TestCheckoutStatusToPublic_ManualReviewNeedsHelp(t *testing.T) {
	cases := map[string]string{
		"completed": "paid", "expired": "expired", "abandoned": "failed",
		"manual_review": "needs_help",
		"created":       "pending", "pricing_confirmed": "pending", "payment_started": "pending",
	}
	for state, want := range cases {
		if got := checkoutStatusToPublic(state); got != want {
			t.Errorf("checkoutStatusToPublic(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestPublicOrderStatus_TheOrderRefinesTheSession(t *testing.T) {
	cases := []struct{ session, order, want string }{
		{"paid", "refunded", "refunded"},
		{"paid", "cancelled", "cancelled"},
		{"paid", "partially_refunded", "paid"},
		{"pending", "paid", "paid"},
		{"pending", "manual_review", "needs_help"},
		{"needs_help", "pending_payment", "needs_help"},
		{"expired", "expired", "expired"},
		{"failed", "abandoned", "failed"},
		{"pending", "pending_payment", "pending"},
	}
	for _, c := range cases {
		if got := publicOrderStatus(c.session, c.order); got != c.want {
			t.Errorf("publicOrderStatus(%q,%q) = %q, want %q", c.session, c.order, got, c.want)
		}
	}
}

func TestRemovedFeedStatus(t *testing.T) {
	str := func(s string) *string { return &s }
	now := time.Now()
	cases := []struct {
		row  gen.PublicFeedRemovedEventRow
		want string
	}{
		{gen.PublicFeedRemovedEventRow{EventID: uuid.New()}, RemovedStatusDeleted},
		{gen.PublicFeedRemovedEventRow{Status: str("published"), DeletedAt: &now}, RemovedStatusDeleted},
		{gen.PublicFeedRemovedEventRow{Status: str("cancelled")}, RemovedStatusCancelled},
		{gen.PublicFeedRemovedEventRow{Status: str("archived")}, RemovedStatusArchived},
		{gen.PublicFeedRemovedEventRow{Status: str("draft")}, RemovedStatusUnpublished},
		{gen.PublicFeedRemovedEventRow{Status: str("published")}, RemovedStatusUnpublished},
	}
	for i, c := range cases {
		if got := removedFeedStatus(c.row); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

func TestAbsoluteAPIURL(t *testing.T) {
	h := &Handler{}
	if got := h.absoluteAPIURL("/v1/x"); got != "/v1/x" {
		t.Errorf("no base: %q", got)
	}
	h.WithAPIPublicBase(func() string { return "https://api.example.com/" })
	if got := h.absoluteAPIURL("/v1/x"); got != "https://api.example.com/v1/x" {
		t.Errorf("with base: %q", got)
	}
	h.WithAPIPublicBase(func() string { return "" })
	if got := h.absoluteAPIURL("/v1/x"); got != "/v1/x" {
		t.Errorf("empty base: %q", got)
	}
}

func TestMyTicketsEscapeLike_MatchesTheAddressExactly(t *testing.T) {
	if got := mytickets.EscapeLike(`a_b%c\d@x.co`); got != `a\_b\%c\\d@x.co` {
		t.Errorf("EscapeLike = %q", got)
	}
}
