package hbot

// invitation_web.go — the web channel of POST
// /v1/organizations/{org_id}/bot-invitations (`channel: "web"`, task 0-5 of
// spec 08_architecture/40, stage 0 A2 of the unified event center plan): an
// organizer is invited by an e-mail that opens the web event center, with no
// Telegram involved.
//
// What is the same as the Telegram channel: the user is found or created by
// e-mail and the membership is inserted AT ONCE in the same transaction; an
// existing membership keeps its role (the invitation never widens it).
//
// What differs:
//
//   - There is no Telegram code. The bot_invitations row is written already
//     ACCEPTED (accepted_at set, accepted_telegram_user_id NULL; that pair is
//     what IsWebInvitation reads, no column was added) with a random code hash
//     whose plain text is thrown away. So `invitation_state` of GET
//     .../bot-team reads "accepted" for a web invitation from the moment it
//     is sent: the membership exists, and the API cannot see whether the person
//     has chosen a password yet.
//   - A NEW account gets the same one-time setup link as every other
//     organizer account: a password_reset_tokens row (SHA-256 of the raw
//     token) and an auth.password_reset_email job with Purpose
//     org_invitation, both written on the caller's transaction. arena-worker
//     picks the /accept-invite page of the event center for an organizer
//     (authemail/recipient.go).
//   - An EXISTING account gets the auth.org_added_email letter: "you were
//     added", the event center address and where to ask for a new password,
//     with no token.
//   - Revoking such an invitation voids the row only (the membership stays,
//     reason "accepted") and resending is refused (409
//     bot.invitation_channel_unsupported): the owner removes a wrongly added
//     person with the remove-member flow, and a lost link is replaced by the
//     person's own "forgot password".

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// IsWebInvitation reports whether the invitation row was written by the web
// channel: accepted, but not by a Telegram account.
func IsWebInvitation(inv gen.BotInvitationRow) bool {
	return inv.AcceptedAt != nil && inv.AcceptedTelegramUserID == nil
}

// webInvitationInput is what webInvitation needs from the create handler.
type webInvitationInput struct {
	OrgID, UserID     uuid.UUID
	OrgName           string
	Email, Role       string
	Locale            string
	InvitedBy         *uuid.UUID
	ExpiresAt         time.Time
	UserCreated       bool
	MembershipCreated bool
}

// webInvitation writes the invitation row and queues the letter on tx. It
// returns the row and the id of the queued job.
func (h *Handler) webInvitation(ctx context.Context, tx pgx.Tx, q *gen.Queries, in webInvitationInput) (gen.BotInvitationRow, string, error) {
	// The code of a Telegram invitation does not exist here; a random hash keeps
	// the unique column filled and nothing can ever redeem it.
	unusable, err := newInvitationCode()
	if err != nil {
		return gen.BotInvitationRow{}, "", err
	}
	inv, err := q.InsertWebBotInvitation(ctx, in.OrgID, in.UserID, in.Email, in.Role,
		users.TokenHash(unusable), in.InvitedBy, in.ExpiresAt, in.MembershipCreated)
	if err != nil {
		return gen.BotInvitationRow{}, "", fmt.Errorf("insert web invitation: %w", err)
	}

	if !in.UserCreated {
		jobID, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypeOrgAddedEmail, authemail.OrgAddedEmailPayload{
			UserID:  in.UserID.String(),
			Email:   in.Email,
			OrgName: in.OrgName,
			Locale:  in.Locale,
		}, invitationEmailMaxAttempts)
		if err != nil {
			return gen.BotInvitationRow{}, "", fmt.Errorf("enqueue org added email: %w", err)
		}
		return inv, jobID, nil
	}

	token, err := users.GenerateVerificationToken()
	if err != nil {
		return gen.BotInvitationRow{}, "", fmt.Errorf("generate setup token: %w", err)
	}
	// Stored as SHA-256(token) like every password_reset_tokens row; the
	// confirm endpoint hashes the raw token before the lookup.
	if err := q.InsertPasswordResetToken(ctx, users.TokenHash(token), in.UserID, in.ExpiresAt); err != nil {
		return gen.BotInvitationRow{}, "", fmt.Errorf("save setup token: %w", err)
	}
	jobID, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypePasswordResetEmail, authemail.PasswordResetEmailPayload{
		UserID:    in.UserID.String(),
		Email:     in.Email,
		Token:     token,
		ExpiresAt: in.ExpiresAt,
		Purpose:   authemail.PurposeOrgInvitation,
		OrgName:   in.OrgName,
	}, invitationEmailMaxAttempts)
	if err != nil {
		return gen.BotInvitationRow{}, "", fmt.Errorf("enqueue setup email: %w", err)
	}
	return inv, jobID, nil
}
