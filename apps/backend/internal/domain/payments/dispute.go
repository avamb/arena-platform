// dispute.go — disputes and chargebacks in arena's vocabulary (spec 51.3,
// PAY-16).
//
// Every provider can tell arena that a buyer disputed a payment (a webhook
// normalized to EventDisputeOpened / EventDisputeUpdated with a Dispute).
// Only a provider whose API accepts evidence implements DisputeResponder and
// declares Capabilities.Disputes (Stripe); for the others the organizer sends
// the evidence package by e-mail.
package payments

import (
	"context"
	"errors"
	"time"
)

// Dispute is a provider's dispute object.
type Dispute struct {
	// ID is the provider's id of the dispute (Stripe dp_…). AllPay sends no
	// id of its own; its module uses the order id.
	ID string
	// Status is the provider's own status (Stripe needs_response,
	// warning_needs_response, under_review, won, lost, …).
	Status string
	// Reason is the provider's own reason (Stripe fraudulent,
	// product_not_received, …); empty when the delivery names none.
	Reason string
	// AmountMinor and Currency of the disputed sum.
	AmountMinor int64
	Currency    string
	// ChargeID and PaymentIntentID name the disputed payment (Stripe ch_…
	// and pi_…). PaymentIntentID is what arena stored as
	// payment_intents.provider_charge_ref.
	ChargeID        string
	PaymentIntentID string
	// DueBy is the last moment to answer; nil when the provider states none.
	DueBy *time.Time
	// IsChargeRefundable: the payment can still be refunded instead.
	IsChargeRefundable bool
	// HasEvidence: evidence was already saved (a draft or an answer).
	HasEvidence bool
	// SubmissionCount: how many times an answer was submitted (Stripe allows one).
	SubmissionCount int
	// FundsWithdrawn / FundsReinstated: the provider moved the disputed sum.
	FundsWithdrawn  bool
	FundsReinstated bool
	// Created is when the provider opened the dispute.
	Created time.Time
}

// DisputeFile is one evidence file to upload.
type DisputeFile struct {
	Filename string
	Content  []byte
}

// ChargeFacts is what a provider knows about a disputed payment that arena
// does not store: the payment behind a charge and the 3-D Secure result.
type ChargeFacts struct {
	PaymentIntentID string
	// ThreeDSecure is the provider's result ("authenticated", "attempted",
	// …), empty when the payment went without 3-D Secure.
	ThreeDSecure string
	// StatementDescriptor is what the buyer saw on the card statement.
	StatementDescriptor string
	// CardBrand and Last4 of the card, empty when unknown.
	CardBrand string
	Last4     string
}

// ErrDisputeNotWritable: the key may read the dispute but not answer it
// (a restricted key without Disputes or Files write). The organizer answers
// in the provider's dashboard with arena's package.
var ErrDisputeNotWritable = errors.New("payments: the key cannot answer disputes")

// ErrDisputeClosedForEvidence: the provider no longer accepts an answer
// (already submitted, closed, past its deadline).
var ErrDisputeClosedForEvidence = errors.New("payments: the dispute no longer accepts evidence")

// DisputeResponder reads and answers disputes through the provider's API.
// Every call is idempotent on the provider's side when a key is given.
type DisputeResponder interface {
	// GetDispute reads the dispute back (an event body is only a hint).
	GetDispute(ctx context.Context, disputeID string) (Dispute, error)
	// ChargeFacts reads the disputed payment's charge.
	ChargeFacts(ctx context.Context, chargeID string) (ChargeFacts, error)
	// UploadDisputeFile stores one evidence file and returns its id.
	UploadDisputeFile(ctx context.Context, f DisputeFile, idempotencyKey string) (string, error)
	// UpdateDisputeEvidence writes the evidence (text fields and file ids by
	// field name). submit=false keeps a draft; submit=true sends it to the
	// bank — once, irreversibly.
	UpdateDisputeEvidence(ctx context.Context, disputeID string, evidence map[string]string, submit bool, idempotencyKey string) (Dispute, error)
	// CloseDispute accepts the dispute: the money stays with the buyer.
	CloseDispute(ctx context.Context, disputeID, idempotencyKey string) (Dispute, error)
}
