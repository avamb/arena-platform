// invoice_state.go captures the platform-invoice state machine as a pure
// domain artefact. It is extracted from
// internal/platform/httpserver/billing_ledger.go in feature #187 so that
// both the HTTP handler layer and the Stripe billing adapter consume a
// single source of truth for invoice transitions.
package billing

import "time"

// Invoice state identifiers. The set is closed: any transition between two
// unknown states is rejected by ValidTransition.
const (
	// InvoiceStateDraft is the initial state of a freshly generated invoice.
	// It may transition to "issued" or "void".
	InvoiceStateDraft = "draft"
	// InvoiceStateIssued is the state of an invoice that has been finalised
	// and sent to the customer. It may transition to "paid" or "void".
	InvoiceStateIssued = "issued"
	// InvoiceStatePaid is a terminal state — no further transitions admitted.
	InvoiceStatePaid = "paid"
	// InvoiceStateVoid is a terminal state — no further transitions admitted.
	InvoiceStateVoid = "void"
)

// BillingPeriodLayout is the canonical 'YYYY-MM' billing period format.
const BillingPeriodLayout = "2006-01"

// BillingDateLayout is the canonical 'YYYY-MM-DD' tariff effective-from format.
const BillingDateLayout = "2006-01-02"

// ValidInvoiceTransitions describes the allowed invoice state transitions.
// Terminal states (paid, void) map to empty (but present) target sets.
//
// The map is exported so callers may inspect or render the state machine,
// but should NOT be mutated; helpers in this package are the canonical entry
// points for transition decisions.
var ValidInvoiceTransitions = map[string]map[string]bool{
	InvoiceStateDraft: {
		InvoiceStateIssued: true,
		InvoiceStateVoid:   true,
	},
	InvoiceStateIssued: {
		InvoiceStatePaid: true,
		InvoiceStateVoid: true,
	},
	InvoiceStatePaid: {},
	InvoiceStateVoid: {},
}

// AllInvoiceStates is the complete set of valid invoice states, in the
// canonical lifecycle order.
var AllInvoiceStates = []string{
	InvoiceStateDraft,
	InvoiceStateIssued,
	InvoiceStatePaid,
	InvoiceStateVoid,
}

// IsTerminalInvoiceState reports whether the given invoice state admits no
// further transitions. An unknown state returns false.
func IsTerminalInvoiceState(state string) bool {
	targets, exists := ValidInvoiceTransitions[state]
	return exists && len(targets) == 0
}

// CanTransitionInvoice reports whether a transition from -> to is permitted
// by the invoice state machine. Unknown 'from' or 'to' states yield false.
func CanTransitionInvoice(from, to string) bool {
	targets, ok := ValidInvoiceTransitions[from]
	if !ok {
		return false
	}
	return targets[to]
}

// PeriodForTime returns the 'YYYY-MM' billing period (UTC) for the
// given instant.
func PeriodForTime(t time.Time) string {
	return t.UTC().Format(BillingPeriodLayout)
}
