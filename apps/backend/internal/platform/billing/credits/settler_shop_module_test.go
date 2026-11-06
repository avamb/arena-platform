package credits_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/credits"
)

// A credit pack pays for tickets, never for a monthly service: Settle returns
// before it touches the database (a nil *gen.Queries would panic otherwise),
// for white label and for the shop module alike.
func TestSettle_SubscriptionChargesAreNeverPaidWithCredits(t *testing.T) {
	s := credits.NewSettler(credits.Options{})
	for _, kind := range []billing.EventKind{billing.KindShopModuleMonth, billing.KindWhiteLabelMonth} {
		ev := gen.BillableEventRow{ID: uuid.New(), Kind: string(kind), OccurredAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
		ch := gen.ChargeRow{ID: uuid.New(), BillableEventID: ev.ID, AccountID: uuid.New(), Currency: "EUR",
			AmountMinor: 2900, RuleType: string(billing.RuleSubscription)}
		if err := s.Settle(context.Background(), nil, ev, ch); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}
