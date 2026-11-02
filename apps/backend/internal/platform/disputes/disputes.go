// Package disputes records payment disputes (chargebacks), builds their
// evidence package and answers them (spec 08_architecture/51_3_…, PAY-16).
//
// A dispute reaches arena three ways:
//
//   - a provider webhook with a dispute (Stripe charge.dispute.*, AllPay
//     type=chargeback), recorded by Service.RecordProviderEvent;
//   - the organizer enters one by hand from a provider's letter
//     (Service.CreateManual) — PayPlus has no notification at all;
//   - (later) a provider report poll (Flitt report 500).
//
// The answer goes through the provider's API when the module is a
// payments.DisputeResponder (channel "api", Stripe) and by e-mail otherwise
// (channel "email" or "manual"). Nothing is ever sent to the bank or to the
// provider before a person pressed "Submit" (decision 2026-10-13): the job
// only builds the package and, for Stripe, saves a DRAFT (submit=false).
package disputes

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Channels.
const (
	ChannelAPI    = "api"
	ChannelEmail  = "email"
	ChannelManual = "manual"
)

// States (payment_disputes.state).
const (
	StateNeedsResponse = "needs_response"
	StateDraftReady    = "draft_ready"
	StateSubmitted     = "submitted"
	StateUnderReview   = "under_review"
	StateAccepted      = "accepted"
	StateExpired       = "expired"
	StateClosed        = "closed"
)

// Outcomes (payment_disputes.outcome).
const (
	OutcomeWon       = "won"
	OutcomeLost      = "lost"
	OutcomeAccepted  = "accepted"
	OutcomeWithdrawn = "withdrawn"
	OutcomeExpired   = "expired"
)

// Reason groups (payment_disputes.reason_group): what the package argues.
const (
	ReasonFraud         = "fraud"
	ReasonNotReceived   = "not_received"
	ReasonNotRecognized = "not_recognized"
	ReasonRefund        = "refund"
	ReasonQuality       = "quality"
	ReasonDuplicate     = "duplicate"
	ReasonOther         = "other"
)

// DefaultDueAfter is the deadline of a dispute whose provider states none
// (AllPay, a hand-entered one): seven days from the opening, until the
// organizer enters the date from the provider's letter (owner decision
// 2026-11-02).
const DefaultDueAfter = 7 * 24 * time.Hour

// Dispute is one payment_disputes row.
type Dispute struct {
	ID                 uuid.UUID
	OrgID              uuid.UUID
	Provider           string
	ProviderDisputeID  *string
	Channel            string
	PaymentIntentID    *uuid.UUID
	OrderID            *uuid.UUID
	ConfigID           *uuid.UUID
	AmountMinor        int64
	Currency           string
	Reason             string
	ReasonGroup        string
	ProviderStatus     string
	State              string
	IsInquiry          bool
	IsChargeRefundable bool
	DueBy              *time.Time
	DueBySource        string
	RespondTo          string
	FundsWithdrawnAt   *time.Time
	FundsReinstatedAt  *time.Time
	Outcome            *string
	ClosedAt           *time.Time
	LowValue           bool
	OpenedAt           time.Time
	SubmittedAt        *time.Time
	SubmittedBy        *uuid.UUID
	AcceptedAt         *time.Time
	AcceptedBy         *uuid.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Open reports whether the dispute still waits for the organizer.
func (d Dispute) Open() bool {
	return d.State == StateNeedsResponse || d.State == StateDraftReady
}

// Answered reports whether an answer already left (or the dispute ended).
func (d Dispute) Answered() bool {
	switch d.State {
	case StateSubmitted, StateUnderReview, StateAccepted, StateClosed, StateExpired:
		return true
	}
	return false
}

// ReasonGroupOf maps a provider's reason onto the package's argument.
// Stripe's vocabulary is the richest; a reason nobody names is "other".
func ReasonGroupOf(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "fraudulent", "debit_not_authorized", "fraud":
		return ReasonFraud
	case "product_not_received", "not_received":
		return ReasonNotReceived
	case "unrecognized", "not_recognized":
		return ReasonNotRecognized
	case "credit_not_processed", "subscription_canceled", "refund", "cancelled", "canceled":
		return ReasonRefund
	case "product_unacceptable", "quality", "not_as_described":
		return ReasonQuality
	case "duplicate":
		return ReasonDuplicate
	}
	return ReasonOther
}

// ManualReasons are the reasons an organizer may pick for a hand-entered
// dispute; each maps onto a group through ReasonGroupOf.
var ManualReasons = []string{"fraudulent", "product_not_received", "unrecognized", "credit_not_processed", "product_unacceptable", "duplicate", "general"}

// providerOutcome maps a provider status onto (state, outcome, closed).
// cur is the current state; an accepted dispute stays accepted.
func providerTransition(cur, status string, submitted bool) (state string, outcome string) {
	switch strings.ToLower(status) {
	case "warning_needs_response", "needs_response", "chargeback":
		if cur == "" {
			return StateNeedsResponse, ""
		}
		return cur, ""
	case "warning_under_review", "under_review":
		if cur == StateAccepted || cur == StateClosed {
			return cur, ""
		}
		return StateUnderReview, ""
	case "won":
		return StateClosed, OutcomeWon
	case "lost":
		if cur == StateAccepted {
			return StateAccepted, OutcomeAccepted
		}
		if cur == StateExpired {
			return StateExpired, OutcomeExpired
		}
		return StateClosed, OutcomeLost
	case "warning_closed", "prevented", "charge_refunded":
		return StateClosed, OutcomeWithdrawn
	case "chargeback_revert":
		if submitted {
			return StateClosed, OutcomeWon
		}
		return StateClosed, OutcomeWithdrawn
	}
	if cur == "" {
		return StateNeedsResponse, ""
	}
	return cur, ""
}

// isInquiry: Stripe's early "request for information" stage.
func isInquiry(status string) bool {
	return strings.HasPrefix(strings.ToLower(status), "warning_")
}
