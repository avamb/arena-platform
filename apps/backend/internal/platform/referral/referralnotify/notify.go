// Package referralnotify tells an organization about a friend who got the money
// back after the cashback was paid (spec 52 section 5.2). It is a package of its
// own because it speaks through salesnotify, which imports the shop, and the
// shop imports the referral core: the core must not import salesnotify.
package referralnotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

// letterMaxAttempts is how often the owners' letter is tried (the value
// referral.letterMaxAttempts gives its own letters).
const letterMaxAttempts = 5

// OrgAnnouncer posts a text to an organization's own sales chats
// (salesnotify.Dispatcher.AnnounceToOrg).
type OrgAnnouncer interface {
	AnnounceToOrg(ctx context.Context, orgID, text string) (chats, delivered int, err error)
}

// OrganizerNotifier tells the organization about a friend who got the money
// back or opened a dispute after the cashback had been paid (spec 52 section
// 5.2): a message in the organization's sales chats and a letter to its owners.
// The notice carries the code, the friend's order number and the amount, never
// a buyer's e-mail, name or phone.
type OrganizerNotifier struct {
	pool      *pgxpool.Pool
	announcer OrgAnnouncer
	logger    *slog.Logger
}

// NewOrganizerNotifier builds the notifier. announcer may be nil (no Telegram
// bot configured): only the letter goes out then.
func NewOrganizerNotifier(pool *pgxpool.Pool, announcer OrgAnnouncer, logger *slog.Logger) *OrganizerNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &OrganizerNotifier{pool: pool, announcer: announcer, logger: logger}
}

// FriendReversed implements Notifier. The letter is queued first and once per
// reward (a repeated call finds the job and queues nothing), then the chats
// are told. A nil result means both were handed off; the sweep then marks the
// reward notified. A chat that exists but took nothing is an error, so the
// sweep tries the chats again on its next pass without a second letter.
func (n *OrganizerNotifier) FriendReversed(ctx context.Context, rw gen.ReferralRewardRow) error {
	facts, ok, err := referral.LoadReversalFacts(ctx, n.pool, rw)
	if err != nil {
		return fmt.Errorf("referral: reversal facts: %w", err)
	}
	if !ok {
		return nil // a shop reward: nothing to tell yet
	}
	if err := n.queueLetter(ctx, rw); err != nil {
		return err
	}
	if n.announcer == nil {
		return nil
	}
	text := salesnotify.FormatReferralReversal(salesnotify.ReferralReversalInfo{
		Locale: facts.OrgLocale, OrgName: facts.OrgName, Code: facts.Code,
		FriendOrderNumber: facts.FriendOrderNumber,
		AmountMinor:       rw.AmountMinor, Currency: rw.Currency,
		Disputed:   rw.FriendReversal != nil && *rw.FriendReversal == "disputed",
		CodePaused: facts.CodePaused(),
	})
	chats, delivered, err := n.announcer.AnnounceToOrg(ctx, rw.OrgID.String(), text)
	if err != nil {
		return fmt.Errorf("referral: announce reversal: %w", err)
	}
	if chats > 0 && delivered == 0 {
		return errors.New("referral: no sales chat took the reversal notice")
	}
	return nil
}

// queueLetter queues the owners' letter unless a job for this reward exists.
func (n *OrganizerNotifier) queueLetter(ctx context.Context, rw gen.ReferralRewardRow) error {
	_, err := n.pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, jsonb_build_object('reward_id', $2::text), $3, 'pending', now()
		WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND payload->>'reward_id' = $2::text)`,
		referral.JobTypeReversalEmail, rw.ID.String(), letterMaxAttempts)
	if err != nil {
		return fmt.Errorf("referral: queue reversal letter: %w", err)
	}
	return nil
}
