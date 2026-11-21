package rate

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// An approved hall plan intake is always free (spec 64 section 4.7): it gets
// its zero charge even when the account has no usable terms, so it never stays
// unrated. Journal-only events keep their own reason; every other kind waits
// for terms.
func TestChargeWithoutTerms(t *testing.T) {
	cases := map[billing.EventKind]string{
		billing.KindTicketRefunded:      billing.ReasonJournalOnly,
		billing.KindTicketCancelled:     billing.ReasonJournalOnly,
		billing.KindSessionCancelled:    billing.ReasonJournalOnly,
		billing.KindVenuePlanIntake:     billing.ReasonFree,
		billing.KindTicketSold:          "",
		billing.KindTicketComplimentary: "",
		billing.KindEventPublished:      "",
		billing.KindWhiteLabelMonth:     "",
		billing.KindShopModuleMonth:     "",
		billing.EventKind("unknown"):    "",
	}
	for kind, want := range cases {
		if got := chargeWithoutTerms(string(kind)); got != want {
			t.Errorf("chargeWithoutTerms(%q) = %q, want %q", kind, got, want)
		}
	}
}
