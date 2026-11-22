// Package accountclaim prepares an existing account for a new organization
// membership.
//
// An account whose e-mail address was never confirmed, that holds no
// membership in any organization and has no Telegram bot link has not proved
// that its owner reads that mailbox or uses the platform. When such an
// account receives a NEW membership
// (an organizer application is approved, a person is invited to a team or an
// administrator adds a member), the membership must reach the person behind
// the address. Claim therefore replaces the account's password with a random
// secret nobody knows, revokes every refresh token of the account and queues
// the set-your-password letter to the address. The person sets a password
// through that letter, and the reset confirmation marks the address as
// confirmed.
//
// An account with a confirmed address, with any membership row or with a
// Telegram bot link is not touched. Every function runs on the caller's transaction, so the change and
// the membership are committed together or not at all.
package accountclaim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// DefaultLetterMaxAttempts matches the self-service password-reset job.
const DefaultLetterMaxAttempts = 5

// NeedsReset reports whether an existing account must be reset before it
// receives a new membership: its e-mail address was never confirmed, it holds
// no membership row in any organization (in any state) and it has no
// Telegram bot link. Call it BEFORE the new membership is inserted. An
// account with a confirmed address is answered without reading the database.
func NeedsReset(ctx context.Context, tx pgx.Tx, userID uuid.UUID, emailVerifiedAt *time.Time) (bool, error) {
	if emailVerifiedAt != nil {
		return false, nil
	}
	var inUse bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = $1)
    OR EXISTS (SELECT 1 FROM bot_telegram_links WHERE user_id = $1)`, userID).Scan(&inUse); err != nil {
		return false, fmt.Errorf("accountclaim: read account use: %w", err)
	}
	return !inUse, nil
}

// ResetCredentials replaces the account's password with a random secret that
// nobody knows and revokes every active refresh token of the account.
func ResetCredentials(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	secret, err := users.GenerateVerificationToken()
	if err != nil {
		return fmt.Errorf("accountclaim: account secret: %w", err)
	}
	hash, err := users.HashPassword(secret)
	if err != nil {
		return fmt.Errorf("accountclaim: hash account secret: %w", err)
	}
	q := gen.New(tx)
	if err := q.UpdateUserPassword(ctx, userID, hash); err != nil {
		return fmt.Errorf("accountclaim: replace password: %w", err)
	}
	if err := q.RevokeAllUserRefreshTokens(ctx, userID); err != nil {
		return fmt.Errorf("accountclaim: revoke refresh tokens: %w", err)
	}
	return nil
}

// SetupLetter describes the set-your-password letter of QueueSetupLetter.
type SetupLetter struct {
	UserID    uuid.UUID
	Email     string
	OrgName   string
	Locale    string
	ExpiresAt time.Time
	// MaxAttempts of the delivery job; zero means DefaultLetterMaxAttempts.
	MaxAttempts int
}

// QueueSetupLetter stores a one-time password-setup token for the account
// and queues the organization-invitation letter that carries it. It returns
// the id of the queued job.
func QueueSetupLetter(ctx context.Context, tx pgx.Tx, in SetupLetter) (string, error) {
	if in.UserID == uuid.Nil || in.Email == "" {
		return "", errors.New("accountclaim: the letter needs a user and an e-mail address")
	}
	if in.ExpiresAt.IsZero() {
		return "", errors.New("accountclaim: the letter needs an expiry")
	}
	attempts := in.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultLetterMaxAttempts
	}
	token, err := users.GenerateVerificationToken()
	if err != nil {
		return "", fmt.Errorf("accountclaim: setup token: %w", err)
	}
	if err := gen.New(tx).InsertPasswordResetToken(ctx, users.TokenHash(token), in.UserID, in.ExpiresAt); err != nil {
		return "", fmt.Errorf("accountclaim: store setup token: %w", err)
	}
	jobID, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypePasswordResetEmail, authemail.PasswordResetEmailPayload{
		UserID:    in.UserID.String(),
		Email:     in.Email,
		Token:     token,
		ExpiresAt: in.ExpiresAt,
		Purpose:   authemail.PurposeOrgInvitation,
		OrgName:   in.OrgName,
		Locale:    in.Locale,
	}, attempts)
	if err != nil {
		return "", fmt.Errorf("accountclaim: queue setup letter: %w", err)
	}
	return jobID, nil
}

// Claim resets an account that NeedsReset selects and queues its
// set-your-password letter; otherwise it does nothing and reports
// claimed=false. Call it before the new membership is inserted.
func Claim(ctx context.Context, tx pgx.Tx, emailVerifiedAt *time.Time, letter SetupLetter) (claimed bool, jobID string, err error) {
	needs, err := NeedsReset(ctx, tx, letter.UserID, emailVerifiedAt)
	if err != nil || !needs {
		return false, "", err
	}
	if err := ResetCredentials(ctx, tx, letter.UserID); err != nil {
		return false, "", err
	}
	jobID, err = QueueSetupLetter(ctx, tx, letter)
	if err != nil {
		return false, "", err
	}
	return true, jobID, nil
}
