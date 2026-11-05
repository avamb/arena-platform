package invoicing

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestUsageLineDescription_NamesTheService(t *testing.T) {
	for _, tc := range []struct {
		rule, kind string
		n          int
		want       string
	}{
		{"subscription", "white_label_month", 3, "White label: 3 brand-months"},
		{"subscription", "shop_module_month", 2, "Shop module: 2 months"},
		{"fixed_per_ticket", "ticket_sold", 40, "Platform service: fixed_per_ticket, 40 charges"},
	} {
		if got := usageLineDescription(tc.rule, tc.kind, tc.n); got != tc.want {
			t.Errorf("%s/%s: %q, want %q", tc.rule, tc.kind, got, tc.want)
		}
	}
}

// Both services are subscription charges but never share an invoice line.
func TestUsageLineKey_SplitsSubscriptionsByService(t *testing.T) {
	wl := gen.UninvoicedChargeRow{RuleType: "subscription", EventKind: "white_label_month"}
	shop := gen.UninvoicedChargeRow{RuleType: "subscription", EventKind: "shop_module_month"}
	if usageLineKey(wl) == usageLineKey(shop) {
		t.Error("white label and shop module share a line")
	}
	a := gen.UninvoicedChargeRow{RuleType: "fixed_per_ticket", EventKind: "ticket_sold"}
	b := gen.UninvoicedChargeRow{RuleType: "fixed_per_ticket", EventKind: "ticket_sold"}
	if usageLineKey(a) != "fixed_per_ticket" || usageLineKey(a) != usageLineKey(b) {
		t.Error("ticket charges must stay grouped by rule type only")
	}
}
