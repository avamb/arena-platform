// Package pricing implements the AB-48 scheduled-price resolution — the
// ONE resolver used by the pricing quote, checkout, widget and public
// feed alike. Window-selection logic must never be reimplemented per
// surface: divergence here is a pricing incident.
//
// Contract (ticket_tier_prices, migration 0087):
//
//   - The effective price at time t is the amount of the window
//     containing t ('[)' semantics: valid_from inclusive, valid_to
//     exclusive, NULL valid_to = open-ended).
//   - GAP POLICY (decided once, AB-48 step 12): when no window contains
//     t, the price FALLS BACK TO THE TIER'S BASE price_amount. A
//     schedule is never required to tile the timeline and a silently
//     zero price is impossible.
//   - Windows of one tier never overlap — enforced by a GiST exclusion
//     constraint at the database level, not here.
//
// Resolve additionally reports when the price will next change, so
// surfaces can render "price rises on <date>" (AB-48 step 11).
package pricing

import "time"

// Window is one ticket_tier_prices row, decoupled from the persistence
// layer so every surface passes the same shape.
type Window struct {
	// From is the inclusive start of the window.
	From time.Time
	// To is the exclusive end; nil = open-ended.
	To *time.Time
	// Amount is the price in minor units while the window applies.
	Amount int64
}

// contains reports whether t falls inside the window ('[)').
func (w Window) contains(t time.Time) bool {
	if t.Before(w.From) {
		return false
	}
	return w.To == nil || t.Before(*w.To)
}

// Resolve returns the effective price of a tier at time t given its base
// price and its (non-overlapping) windows, plus the moment the price
// next changes — nil when no future change is known.
//
// The next-change moment is the earliest of:
//   - the exclusive end of the window containing t (price reverts to
//     base or hands over to a back-to-back window), and
//   - the start of the earliest window beginning strictly after t.
func Resolve(base int64, windows []Window, t time.Time) (amount int64, nextChange *time.Time) {
	amount = base

	var containingEnd *time.Time
	haveContaining := false
	var earliestFuture *time.Time

	for i := range windows {
		w := windows[i]
		if w.contains(t) {
			amount = w.Amount
			haveContaining = true
			containingEnd = w.To
			continue
		}
		if w.From.After(t) {
			if earliestFuture == nil || w.From.Before(*earliestFuture) {
				from := w.From
				earliestFuture = &from
			}
		}
	}

	switch {
	case haveContaining && containingEnd != nil:
		if earliestFuture != nil && earliestFuture.Before(*containingEnd) {
			// Cannot happen with the DB exclusion constraint (a future
			// window cannot start inside the containing one), but be
			// defensive: report the earliest boundary.
			return amount, earliestFuture
		}
		return amount, containingEnd
	case haveContaining:
		// Open-ended containing window: the price only changes if a later
		// window starts (impossible under the exclusion constraint) —
		// report no known change.
		return amount, nil
	default:
		return amount, earliestFuture
	}
}
