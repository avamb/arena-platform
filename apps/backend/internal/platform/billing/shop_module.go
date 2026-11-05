package billing

import (
	"github.com/google/uuid"
)

// ModuleShop is the organization module of the paid shop (org_module_periods).
const ModuleShop = "shop"

// ShopModuleMonth is what a shop_module_month billable event carries in its
// attributes (spec 08_architecture/61 section 3.2): which module period and
// month it charges, how many days of the month the period covered, and the
// period's individual monthly price (nil = the price list; 0 = free; in
// Currency, the account currency).
type ShopModuleMonth struct {
	PeriodID          uuid.UUID `json:"org_module_period_id"`
	Module            string    `json:"module"`
	Month             string    `json:"month"`
	DaysActive        int64     `json:"days_active"`
	DaysInMonth       int64     `json:"days_in_month"`
	MonthlyPriceMinor *int64    `json:"monthly_price_minor,omitempty"`
	Currency          string    `json:"currency,omitempty"`
}

// ShopModuleFromAttributes copies the proration of a shop_module_month event
// into ev. Unreadable attributes leave ev as it is; Rate then refuses the
// event (days_in_month 0), so a broken row is never charged at zero.
func ShopModuleFromAttributes(raw []byte, ev *Event) { prorationFromAttributes(raw, ev) }

// ShopModuleMonthlyMinor is the monthly shop module price of the price list
// the terms name, in the account currency (Terms.Currency); false when the
// price list has no such rule.
func (t Terms) ShopModuleMonthlyMinor() (int64, bool) {
	r, ok := t.rule(lineStandard, string(KindShopModuleMonth), RuleSubscription, "")
	if !ok || r.Subscription == nil {
		return 0, false
	}
	return r.Subscription.MonthlyMinor, true
}
