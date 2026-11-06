// Package latepayment names a payment that arrived after its order stopped
// waiting for it (spec 62 §5.4 п. 4, П-15): the three cases, the three
// outcomes and the buyer's letter job. The decision itself is made by the
// payment webhook (hcheckout late_payment.go) on its own transaction; the
// letter is written by the delivery worker. This package is the vocabulary
// both share without importing each other.
package latepayment

import (
	"time"
)

// Cases (late_payments.late_case, migration 0205).
const (
	// CaseBuyerCancelled: the buyer cancelled the order (П-2) and the
	// payment came anyway. Always refunded in full, no tickets: the buyer
	// said no.
	CaseBuyerCancelled = "buyer_cancelled"
	// CaseDelayedNotice: paid in time, the provider's notification came
	// after the order had expired. The same places are taken again when
	// they are still free, otherwise the money goes back.
	CaseDelayedNotice = "delayed_notice"
	// CaseAfterWindow: paid after the window closed (the provider states
	// when it was paid). Refunded in full.
	CaseAfterWindow = "after_window"
)

// Outcomes (late_payments.outcome).
const (
	// OutcomeReacquired: the places were taken again, the order is paid and
	// the tickets go out as usual.
	OutcomeReacquired = "reacquired"
	// OutcomeRefundRequested: a full refund was handed to the refund engine,
	// which sends it to the provider (refund.sweep) within minutes.
	OutcomeRefundRequested = "refund_requested"
	// OutcomeOperatorReview: arena cannot refund through this provider; the
	// order is parked in manual_review for the organizer to return the
	// money by hand.
	OutcomeOperatorReview = "operator_review"
)

// JobTypeEmail is the worker job that tells the buyer what happened to a
// late payment. Payload: {"payment_intent_id": "<uuid>"}.
const JobTypeEmail = "checkout.late_payment_email"

// JobPayload is the letter job's payload.
type JobPayload struct {
	PaymentIntentID string `json:"payment_intent_id"`
}

// Classify decides the case of a payment whose checkout could no longer be
// completed. csState is checkout_sessions.state, orderStatus the order's
// status ("" without an order), paidAt when the provider says the buyer paid
// (nil: it does not say), windowEnd when the order stopped waiting. ok is
// false for a state that is not a late payment at all (an operator's
// manual_review, a checkout that never reached pricing): the webhook then
// parks it as before.
func Classify(csState, orderStatus string, paidAt *time.Time, windowEnd time.Time) (lateCase string, ok bool) {
	switch {
	case csState == "abandoned" || orderStatus == "cancelled":
		return CaseBuyerCancelled, true
	case csState == "expired" || orderStatus == "expired":
		// A provider with a page expiry (AllPay's expire, Stripe's
		// expires_at) cannot take a payment after the window, so a payment
		// whose time the provider does not state counts as made in time.
		if paidAt != nil && !windowEnd.IsZero() && paidAt.After(windowEnd) {
			return CaseAfterWindow, true
		}
		return CaseDelayedNotice, true
	}
	return "", false
}
