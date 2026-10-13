// payment_amount_guard.go — a paid event must name the amount and currency
// arena asked for (PAY-14b, spec 08_architecture/37 §7).
//
// Only a provider whose module owns the whole normalization
// (Capabilities.WebhookConfigRouteOnly: Flitt, AllPay) is checked here: its
// module reports what the buyer was charged in NormalizedEvent.AmountMinor
// and Currency. A paid event that disagrees with the payment intent is not
// trusted with tickets: the intent goes to manual_review (the ops watchdog
// alerts on it) and the checkout is not completed.
package hcheckout

import (
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// failureCodeAmountMismatch marks an intent parked because the provider
// reported another amount or currency than arena asked for.
const failureCodeAmountMismatch = "paid_amount_mismatch"

// paidAmountMismatch explains how a paid event disagrees with the intent's
// amount and currency, or returns "" when it agrees. An event that names no
// amount (0) or no currency is not judged on that field.
func paidAmountMismatch(ev payments.NormalizedEvent, amount int64, currency string) string {
	if ev.Currency != "" && currency != "" && !strings.EqualFold(ev.Currency, currency) {
		return fmt.Sprintf("the provider reports a payment in %s, arena asked for %s",
			strings.ToUpper(ev.Currency), strings.ToUpper(currency))
	}
	if ev.AmountMinor != 0 && ev.AmountMinor != amount {
		return fmt.Sprintf("the provider reports %d minor units paid, arena asked for %d", ev.AmountMinor, amount)
	}
	return ""
}
