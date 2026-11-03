// Package referral is the server core of the "Refer a friend" programme
// (spec 52, migration 0191).
//
// A buyer who paid for an event with a programme gets a personal promo code
// (IssueCode). A friend who pays with it earns the buyer a cashback: a row in
// referral_rewards written in the friend's payment transaction
// (RecordFriendOrderTx) and paid by the worker (Service.Payout) as a partial,
// money-only refund of the buyer's own payment through the refund engine
// (origin 'referral_cashback', tickets stay valid, scanned tickets allowed).
// The sweep (Service.Sweep) requeues and reads back payouts, records a friend
// who got the money back, and switches off codes that cannot pay any more.
//
// The money arithmetic is the pure functions of this file, in minor units.
package referral

import (
	"sort"

	"github.com/google/uuid"
)

// ItemsBase is the base every referral percentage is taken from: the price of
// the tickets (or the shop goods) after every discount, without the service
// charge, the delivery fee or any other fee or tax (owner decision 2026-11-03,
// it replaces R-RF1's "what the friend paid"). subtotal and discount are the
// order's own columns in minor units.
func ItemsBase(subtotalMinor, discountMinor int64) int64 {
	return max(subtotalMinor-discountMinor, 0)
}

// CashbackAmount is pct percent of the friend's ItemsBase, rounded down.
func CashbackAmount(friendBaseMinor int64, pct int) int64 {
	if friendBaseMinor <= 0 || pct <= 0 {
		return 0
	}
	return friendBaseMinor * int64(pct) / 100
}

// ApplyCap limits a cashback so that, together with what was already paid back
// to the referrer's order, it stays within capPct percent of the referrer's
// ItemsBase (spec 52 section 4). alreadyCashbackMinor is the cashback already owed
// or paid on that order, alreadyRefundedMinor the money other refunds have
// returned or are returning on it. pay is what may still be paid; capped says
// it is less than amount.
func ApplyCap(amount, referrerBaseMinor, alreadyCashbackMinor, alreadyRefundedMinor int64, capPct int) (pay int64, capped bool) {
	if amount <= 0 {
		return 0, false
	}
	room := max(referrerBaseMinor*int64(capPct)/100-alreadyCashbackMinor-alreadyRefundedMinor, 0)
	pay = min(amount, room)
	return pay, pay < amount
}

// TicketBudget is how much can still be returned for one ticket of the
// referrer's order: what it cost minus what is already refunded for it.
type TicketBudget struct {
	TicketID uuid.UUID
	Budget   int64
}

// Item is one ticket of a cashback refund with its explicit amount.
type Item struct {
	TicketID uuid.UUID
	Amount   int64
}

// SplitOverTickets spreads amount over the tickets from the one with the most
// budget left to the one with the least (ties keep the given order), never more
// than a ticket's budget. The items' sum is less than amount when the budgets
// together are smaller; the caller decides what that means.
func SplitOverTickets(amount int64, tickets []TicketBudget) []Item {
	if amount <= 0 {
		return nil
	}
	sorted := make([]TicketBudget, 0, len(tickets))
	for _, t := range tickets {
		if t.Budget > 0 {
			sorted = append(sorted, t)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Budget > sorted[j].Budget })
	var out []Item
	left := amount
	for _, t := range sorted {
		if left == 0 {
			break
		}
		part := min(t.Budget, left)
		out = append(out, Item{TicketID: t.TicketID, Amount: part})
		left -= part
	}
	return out
}

// Sum is the total amount of items.
func Sum(items []Item) int64 {
	var n int64
	for _, it := range items {
		n += it.Amount
	}
	return n
}
