package referral

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// ErrNotWired is what a Notifier that has no channel yet answers; the sweep
// treats it as "not delivered, try again later" without logging an error.
var ErrNotWired = errors.New("referral: notifier is not wired")

// Notifier tells the organizer about a friend who got the money back or opened
// a dispute after the cashback had been paid (spec 52 section 5.2). A nil
// error means it was delivered; the sweep then never sends it again.
type Notifier interface {
	FriendReversed(ctx context.Context, reward gen.ReferralRewardRow) error
}

// NopNotifier is the notifier until the Telegram and e-mail one is wired
// (the next step): it delivers nothing and says so.
type NopNotifier struct{}

// FriendReversed implements Notifier.
func (NopNotifier) FriendReversed(context.Context, gen.ReferralRewardRow) error { return ErrNotWired }

// Service pays and sweeps rewards. It is built by arena-worker.
type Service struct {
	pool     *pgxpool.Pool
	engine   *refunds.Engine
	notifier Notifier
	// payoutNotifier is notifier when it also tells payout notices
	// (notices.go), else a no-op.
	payoutNotifier PayoutNotifier
	logger         *slog.Logger
	now            func() time.Time
}

// Options wires a Service. Notifier, Logger and Now are optional. A Notifier
// that also implements PayoutNotifier posts the payout notices too.
type Options struct {
	Pool     *pgxpool.Pool
	Engine   *refunds.Engine
	Notifier Notifier
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewService builds a Service.
func NewService(o Options) *Service {
	s := &Service{pool: o.Pool, engine: o.Engine, notifier: o.Notifier, logger: o.Logger, now: o.Now}
	if s.notifier == nil {
		s.notifier = NopNotifier{}
	}
	s.payoutNotifier = nopPayoutNotifier{}
	if pn, ok := s.notifier.(PayoutNotifier); ok {
		s.payoutNotifier = pn
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// outcome is a decision that ends a reward without a refund, returned from
// inside the refund engine's transaction (BatchInput.Prepare), which it rolls
// back. retry means "not yet": the job runs again later.
type outcome struct {
	state  string
	reason string
	retry  bool
}

func (o *outcome) Error() string {
	if o.retry {
		return "referral: retry later: " + o.reason
	}
	return "referral: reward ends " + o.state + ": " + o.reason
}

// refundView is the part of a refund the reward's state is judged by.
type refundView struct {
	State          string
	FailureCode    string
	Amount         int64
	HasProviderRef bool
	// Attempted: the refund really went to the provider (an attempt was made or
	// the provider answered). A refund that failed before that was refused by
	// the platform, not by the provider.
	Attempted bool
}

// reasonRefundNotSent is the reward reason when a refund failed without ever
// reaching the provider.
const reasonRefundNotSent = "refund_not_sent"

// reasonManualReview is the reward reason while a refund waits for a person
// (the refund engine parked it in manual_review). It is NOT final: the sweep
// reads the refund back, and when the operator closes it the reward becomes
// paid or not_possible. The provider's own code behind the parking is kept
// apart, in referral_rewards.provider_code.
const reasonManualReview = "manual_review"

// resultOf judges a reward by its refunds (pure). Pending work wins: the
// reward is 'sent' while any refund is still on its way. A reward whose
// refunds all failed is not_possible with the provider's code (owner
// decision R-RF5: no other card is tried, the money stays with the organizer).
// A refund parked in manual_review is not a refusal: with nothing returned the
// reward is not_possible with the fixed reason "manual_review" (no letter, the
// sweep re-reads it) whatever failure code the engine recorded, and that code
// goes to providerCode. A mix of money returned and refused keeps what was
// returned and says so in the reason; amount is always the money returned or
// on its way.
func resultOf(refs []refundView) (state, reason, providerCode string, amount int64) {
	var pending, bad, review bool
	var badCode, badProvider, reviewProvider string
	for _, r := range refs {
		switch r.State {
		case refunds.StateSucceeded:
			amount += r.Amount
		case refunds.StateRequested, refunds.StateApproved, refunds.StateProviderPending:
			amount += r.Amount
			pending = true
		case refunds.StateManualReview:
			bad, review = true, true
			if reviewProvider == "" {
				reviewProvider = r.FailureCode
			}
		default: // failed, rejected
			bad = true
			if badCode == "" {
				switch {
				case !r.Attempted && !r.HasProviderRef:
					// The provider never saw it: no code of the provider's.
					badCode = reasonRefundNotSent
				case r.FailureCode == "":
					badCode = ReasonProviderDeclined
				default:
					badProvider = r.FailureCode
					badCode = r.FailureCode
				}
			}
		}
	}
	switch {
	case pending:
		return StateSent, "", "", amount
	case bad && amount == 0 && review:
		return StateNotPossible, reasonManualReview, reviewProvider, 0
	case bad && amount == 0:
		return StateNotPossible, badCode, badProvider, 0
	case bad:
		return StatePaid, "partial", "", amount
	default:
		return StatePaid, "", "", amount
	}
}
