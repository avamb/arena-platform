package eventbot

// team_invitations.go - the Team screen's controls for a colleague who has
// not joined yet (spec 35 EC-16): "Resend e-mail" sends the letter again with
// a new link, "Revoke invitation" annuls it after ONE confirmation press. Both
// are owner-only on the server too, and the screen itself is the owner's.
// The member is looked up afresh on every press, so a stale button for an
// invitation that was accepted or revoked meanwhile ends in a plain "no longer
// exists" note rather than an error.

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// keptReasons are the kept_reason values the revoke route can answer; each
// has a bot.team.kept_<reason> text.
var keptReasons = map[string]bool{
	"accepted": true, "member_before": true, "other_invitation": true,
	"in_use": true, "role_changed": true, "last_owner": true,
}

// teamInvitationCallback handles "team:rs:<user>" (resend) and
// "team:ri:<user>[:yes]" (revoke, with its confirmation).
func (b *Bot) teamInvitationCallback(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, parts []string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	if len(parts) < 2 {
		return
	}
	userID, err := uuid.Parse(parts[1])
	if err != nil || userID == id.Link.UserID {
		return
	}
	member, found := b.teamMember(ctx, jwt, orgID, userID)
	if !found || !member.revokable() {
		b.showTeam(ctx, chatID, &msgID, from, b.texts.T(loc, "bot.team.invitation_gone", nil)+"\n\n")
		return
	}
	email := Esc(member.Email)
	switch parts[0] {
	case "rs":
		err := b.arena.ResendInvitation(ctx, jwt, orgID, member.InvitationID, loc)
		switch {
		case err == nil:
			b.showTeam(ctx, chatID, &msgID, from, b.texts.T(loc, "bot.team.resent", map[string]any{"Email": email})+"\n\n")
		case IsAPIError(err, http.StatusTooManyRequests):
			b.showTeam(ctx, chatID, &msgID, from, b.texts.T(loc, "bot.team.resend_too_soon", map[string]any{"Email": email})+"\n\n")
		case IsAPIError(err, http.StatusNotFound) || IsAPIError(err, http.StatusConflict):
			b.showTeam(ctx, chatID, &msgID, from, b.texts.T(loc, "bot.team.invitation_gone", nil)+"\n\n")
		default:
			b.logger.Warn("eventbot: invitation resend failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.team.resend_failed", nil), b.backKeyboard(loc, "team"))
		}
	case "ri":
		if len(parts) < 3 || parts[2] != "yes" {
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.team.revoke_confirm", map[string]any{"Email": email}),
				&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
					{Text: b.texts.T(loc, "bot.team.revoke_yes_btn", nil), CallbackData: "team:ri:" + userID.String() + ":yes"},
					{Text: b.texts.T(loc, "bot.wz.cancel_btn", nil), CallbackData: "team"},
				}}})
			return
		}
		res, err := b.arena.RevokeInvitation(ctx, jwt, orgID, member.InvitationID)
		switch {
		case err == nil:
			b.showTeam(ctx, chatID, &msgID, from, b.revokedText(loc, member.Email, res)+"\n\n")
		case IsAPIError(err, http.StatusNotFound) || IsAPIError(err, http.StatusConflict):
			b.showTeam(ctx, chatID, &msgID, from, b.texts.T(loc, "bot.team.invitation_gone", nil)+"\n\n")
		default:
			b.logger.Warn("eventbot: invitation revoke failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.team.revoke_failed", nil), b.backKeyboard(loc, "team"))
		}
	}
}

// revokedText says what the revoke did: the person is out of the team, or why
// they stay.
func (b *Bot) revokedText(loc, email string, res RevokeResult) string {
	if res.MembershipRemoved || !keptReasons[res.KeptReason] {
		return b.texts.T(loc, "bot.team.revoked_removed", map[string]any{"Email": Esc(email)})
	}
	return b.texts.T(loc, "bot.team.revoked_kept", map[string]any{
		"Email":  Esc(email),
		"Reason": b.texts.T(loc, "bot.team.kept_"+res.KeptReason, nil),
	})
}
