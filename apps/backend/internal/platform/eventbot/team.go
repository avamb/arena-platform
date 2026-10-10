package eventbot

// team.go — "Team" (spec 28 §3.3, §10 step 5): the owner sees the
// organization's owners and managers, invites a colleague by e-mail (the
// same POST .../bot-invitations the admin uses) and removes one. A manager
// is told this screen is the owner's. The two-step invite dialog (e-mail,
// then role) is kept in bot_dialogs (dialogs.go), so a deploy between the
// two steps loses nothing.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// membershipRoleOwner is the membership role of an organization owner.
const membershipRoleOwner = "org_admin"

// TeamMember is one member as GET .../bot-team answers it.
type TeamMember struct {
	UserID            uuid.UUID `json:"user_id"`
	Email             string    `json:"email"`
	Role              string    `json:"role"`
	MembershipRole    string    `json:"membership_role"`
	TelegramLinked    bool      `json:"telegram_linked"`
	InvitationPending bool      `json:"invitation_pending"`
	// Where the person's bot invitation stands (EC-16): "accepted", "waiting"
	// or "expired"; empty for a member who has none. InvitationID is what the
	// revoke and resend routes take.
	InvitationState string    `json:"invitation_state"`
	InvitationID    uuid.UUID `json:"invitation_id"`
}

// Invitation states as GET .../bot-team prints them.
const (
	invStateAccepted = "accepted"
	invStateWaiting  = "waiting"
	invStateExpired  = "expired"
)

// revokable reports whether the member has an invitation the owner can still
// annul or send again.
func (m TeamMember) revokable() bool {
	return (m.InvitationState == invStateWaiting || m.InvitationState == invStateExpired) && m.InvitationID != uuid.Nil
}

// Team lists the organization's owners and managers.
func (c *ArenaClient) Team(ctx context.Context, jwt string, orgID uuid.UUID) ([]TeamMember, error) {
	var out struct {
		Members []TeamMember `json:"members"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/bot-team", jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

// InviteResult is what POST .../bot-invitations answers the bot.
type InviteResult struct {
	Email       string `json:"email"`
	Role        string `json:"role"`
	UserCreated bool   `json:"user_created"`
	Delivery    string `json:"delivery"`
}

// Invite creates (or re-creates) a bot invitation for an e-mail.
func (c *ArenaClient) Invite(ctx context.Context, jwt string, orgID uuid.UUID, email, role, locale string) (InviteResult, error) {
	var out struct {
		Invitation InviteResult `json:"invitation"`
	}
	body := map[string]any{"email": email, "role": role, "locale": NormalizeLocale(locale)}
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/bot-invitations", jwt, body, &out)
	return out.Invitation, err
}

// RemoveMember revokes a membership. The route revokes one ROLE of the
// user, so the member's current membership role travels in the body.
func (c *ArenaClient) RemoveMember(ctx context.Context, jwt string, orgID, userID uuid.UUID, membershipRole string) error {
	body := map[string]any{"role": membershipRole}
	return c.do(ctx, http.MethodDelete, "/v1/organizations/"+orgID.String()+"/members/"+userID.String(), jwt, body, nil)
}

// ─── the invite dialog ────────────────────────────────────────────────────────

// The invite dialog lives in bot_dialogs (dialogs.go) under kind "team": step
// "email" while the address is awaited, then "role" until a role is pressed.
// It used to be a map in this process, and an owner who typed the e-mail
// after a deploy got no invitation and no error (2026-10-01).
const (
	teamDialogKind = "team"
	teamStepEmail  = "email"
	teamStepRole   = "role"
)

// teamDialog is the invite dialog's state.
type teamDialog struct {
	Email string `json:"email"`  // "" while the e-mail is awaited
	MsgID int    `json:"msg_id"` // the one bot message the dialog lives in; each step edits it
}

// startTeamDialog opens (or restarts) the invite dialog in the organization.
func (b *Bot) startTeamDialog(ctx context.Context, tg int64, orgID uuid.UUID, msgID int) error {
	return b.dialogs.Save(ctx, tg, &orgID, teamDialogKind, teamStepEmail, teamDialog{MsgID: msgID}, dialogTTL)
}

// setTeamDialogEmail records the address and moves the dialog to the role.
func (b *Bot) setTeamDialogEmail(ctx context.Context, tg int64, orgID uuid.UUID, dlg teamDialog) error {
	return b.dialogs.Save(ctx, tg, &orgID, teamDialogKind, teamStepRole, dlg, dialogTTL)
}

// loadTeamDialog reads the invite dialog. expired is true exactly once after
// it ran out, so the caller can say "time is up" instead of letting the
// e-mail or role press fall silently through to the events list. A failed
// read is logged and treated as no dialog: the person then just starts again.
func (b *Bot) loadTeamDialog(ctx context.Context, tg int64) (dlg teamDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, teamDialogKind, &dlg)
	if err != nil {
		b.logger.Error("eventbot: team dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return teamDialog{}, "", false, false
	}
	return dlg, step, found, expired
}

// clearTeamDialog ends the invite dialog, if any.
func (b *Bot) clearTeamDialog(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, teamDialogKind); err != nil {
		b.logger.Warn("eventbot: team dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── screens ──────────────────────────────────────────────────────────────────

func isOwner(id *Identity) bool {
	return id != nil && id.Current != nil && id.Current.Role == membershipRoleOwner
}

// ownerIsAlone reports whether the current organization's team is just
// this one person (or empty — a superadmin working in an organization they
// are not a member of). A failed lookup answers false, so the menu falls
// back to the full team screen rather than hiding it.
func (b *Bot) ownerIsAlone(ctx context.Context, id *Identity, jwt string) bool {
	if id == nil || id.Current == nil || jwt == "" {
		return false
	}
	members, err := b.arena.Team(ctx, jwt, id.Current.OrgID)
	if err != nil {
		return false
	}
	others := 0
	for _, m := range members {
		if m.UserID != id.Link.UserID {
			others++
		}
	}
	return others == 0
}

// showTeam renders the team screen (owners only).
func (b *Bot) showTeam(ctx context.Context, chatID int64, editMsgID *int, from *models.User, prefix string) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	loc := id.Locale()
	if !isOwner(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.team_owner_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	b.clearTeamDialog(ctx, from.ID)
	members, err := b.arena.Team(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.logger.Error("eventbot: team failed", slog.String("org_id", id.Current.OrgID.String()), slog.String("error", err.Error()))
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return
	}
	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteString(b.texts.T(loc, "bot.team_title", map[string]any{"Org": Esc(id.Current.OrgName)}))
	rows := [][]models.InlineKeyboardButton{}
	for _, m := range members {
		marks := ""
		switch {
		case m.TelegramLinked:
			marks += b.texts.T(loc, "bot.team_linked_mark", nil)
		case m.InvitationState == invStateWaiting:
			marks += b.texts.T(loc, "bot.team.state_waiting", nil)
		case m.InvitationState == invStateExpired:
			marks += b.texts.T(loc, "bot.team.state_expired", nil)
		case m.InvitationState == invStateAccepted:
			marks += b.texts.T(loc, "bot.team.state_accepted", nil)
		case m.InvitationPending:
			marks += b.texts.T(loc, "bot.team_pending_mark", nil)
		default:
			marks += b.texts.T(loc, "bot.team_not_linked_mark", nil)
		}
		self := m.UserID == id.Link.UserID
		if self {
			marks += b.texts.T(loc, "bot.team_self_mark", nil)
		}
		roleKey := "bot.role_manager"
		if m.Role == "owner" {
			roleKey = "bot.role_owner"
		}
		sb.WriteString("\n")
		sb.WriteString(b.texts.T(loc, "bot.team_member_line", map[string]any{"Email": Esc(m.Email), "Role": b.texts.T(loc, roleKey, nil), "Marks": marks}))
		if !self && m.revokable() {
			rows = append(rows,
				[]models.InlineKeyboardButton{{
					Text:         b.texts.T(loc, "bot.team.resend_btn", map[string]any{"Email": truncate(m.Email, 36)}),
					CallbackData: "team:rs:" + m.UserID.String(),
				}},
				[]models.InlineKeyboardButton{{
					Text:         b.texts.T(loc, "bot.team.revoke_btn", map[string]any{"Email": truncate(m.Email, 36)}),
					CallbackData: "team:ri:" + m.UserID.String(),
				}})
		}
		if !self {
			rows = append(rows, []models.InlineKeyboardButton{{
				Text:         b.texts.T(loc, "bot.team_remove_btn", map[string]any{"Email": truncate(m.Email, 40)}),
				CallbackData: "team:rm:" + m.UserID.String(),
			}})
		}
	}
	sb.WriteString("\n\n")
	sb.WriteString(b.texts.T(loc, "bot.team_hint", nil))
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.team_invite_btn", nil), CallbackData: "team:invite"}},
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	)
	b.reply(ctx, chatID, editMsgID, sb.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// teamCallback handles every "team:<data>" button.
func (b *Bot) teamCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, &msgID, id)
		return
	}
	loc := id.Locale()
	if !isOwner(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.team_owner_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	parts := strings.Split(data, ":")
	switch parts[0] {
	case "":
		b.showTeam(ctx, chatID, &msgID, from, "")
	case "invite":
		if err := b.startTeamDialog(ctx, from.ID, orgID, msgID); err != nil {
			b.logger.Error("eventbot: team dialog start failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
			return
		}
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.team_ask_email", nil), b.backKeyboard(loc, "home"))
	case "role":
		if len(parts) < 2 {
			return
		}
		dlg, step, found, expired := b.loadTeamDialog(ctx, from.ID)
		if !found || step != teamStepRole || dlg.Email == "" {
			prefix := ""
			if expired {
				prefix = b.texts.T(loc, "bot.team_dialog_expired", nil) + "\n\n"
			}
			b.showTeam(ctx, chatID, &msgID, from, prefix)
			return
		}
		role := parts[1]
		if role != "owner" && role != "manager" {
			return
		}
		b.clearTeamDialog(ctx, from.ID)
		res, err := b.arena.Invite(ctx, jwt, orgID, dlg.Email, role, loc)
		if err != nil {
			b.logger.Warn("eventbot: invite failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
			key := "bot.team_invite_failed"
			if IsAPIError(err, http.StatusForbidden) {
				key = "bot.wz.err_forbidden"
			}
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, key, nil), b.backKeyboard(loc, "team"))
			return
		}
		roleKey := "bot.role_manager"
		if res.Role == "owner" {
			roleKey = "bot.role_owner"
		}
		prefix := b.texts.T(loc, "bot.team_invited", map[string]any{"Email": Esc(res.Email), "Role": b.texts.T(loc, roleKey, nil)}) + "\n\n"
		b.showTeam(ctx, chatID, &msgID, from, prefix)
	case "rs", "ri":
		b.teamInvitationCallback(ctx, chatID, msgID, from, id, jwt, parts)
	case "rm":
		if len(parts) < 2 {
			return
		}
		userID, err := uuid.Parse(parts[1])
		if err != nil || userID == id.Link.UserID {
			return
		}
		member, found := b.teamMember(ctx, jwt, orgID, userID)
		if !found {
			b.showTeam(ctx, chatID, &msgID, from, "")
			return
		}
		if len(parts) < 3 || parts[2] != "yes" {
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.team_confirm_remove", map[string]any{"Email": Esc(member.Email)}),
				&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
					{Text: b.texts.T(loc, "bot.team_remove_yes_btn", nil), CallbackData: "team:rm:" + userID.String() + ":yes"},
					{Text: b.texts.T(loc, "bot.wz.cancel_btn", nil), CallbackData: "team"},
				}}})
			return
		}
		if err := b.arena.RemoveMember(ctx, jwt, orgID, userID, member.MembershipRole); err != nil {
			b.logger.Warn("eventbot: remove member failed", slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
			key := "bot.team_remove_failed"
			if IsAPIError(err, http.StatusForbidden) {
				key = "bot.wz.err_forbidden"
			}
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, key, nil), b.backKeyboard(loc, "team"))
			return
		}
		b.showTeam(ctx, chatID, &msgID, from, b.texts.T(loc, "bot.team_removed", map[string]any{"Email": Esc(member.Email)})+"\n\n")
	}
}

// teamText consumes the e-mail of an invite dialog in progress. It reports
// whether the text was taken.
func (b *Bot) teamText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	dlg, step, ok, expired := b.loadTeamDialog(ctx, from.ID)
	// The lapse is reported only for something that looks like the address
	// the dialog asked for; any other text goes on to the wizard and the
	// rest as if there had been no dialog. Either way the lapse is spent:
	// deliberately, any text uses up the expired notice, e-mail or not.
	if expired && looksLikeEmail(strings.ToLower(strings.TrimSpace(text))) {
		// The dialog ran out while the person was away: say so rather than
		// treating the address as a stray message.
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil && isOwner(id) {
			b.send(ctx, chatID, b.texts.T(id.Locale(), "bot.team_dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: b.texts.T(id.Locale(), "bot.team_invite_btn", nil), CallbackData: "team:invite"}},
				{{Text: b.texts.T(id.Locale(), "bot.btn_home", nil), CallbackData: "home"}},
			}})
			return true
		}
		return false
	}
	if !ok || step != teamStepEmail {
		return false
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || !isOwner(id) || id.Current == nil {
		b.clearTeamDialog(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	email := strings.ToLower(strings.TrimSpace(text))
	// The dialog lives in ONE message: every step edits the prompt in place, so
	// no stale prompt with a dead button is left behind in the chat.
	var edit *int
	if dlg.MsgID != 0 {
		edit = &dlg.MsgID
	}
	if !looksLikeEmail(email) {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ask_email_again", nil), b.backKeyboard(loc, "home"))
		return true
	}
	dlg.Email = email
	if err := b.setTeamDialogEmail(ctx, from.ID, id.Current.OrgID, dlg); err != nil {
		b.logger.Error("eventbot: team dialog save failed", slog.String("org_id", id.Current.OrgID.String()), slog.String("error", err.Error()))
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
		return true
	}
	b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.team_ask_role", map[string]any{"Email": Esc(email)}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.team_role_manager_btn", nil), CallbackData: "team:role:manager"}},
		{{Text: b.texts.T(loc, "bot.team_role_owner_btn", nil), CallbackData: "team:role:owner"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
	return true
}

// teamMember finds one member of the current team.
func (b *Bot) teamMember(ctx context.Context, jwt string, orgID, userID uuid.UUID) (TeamMember, bool) {
	members, err := b.arena.Team(ctx, jwt, orgID)
	if err != nil {
		return TeamMember{}, false
	}
	for _, m := range members {
		if m.UserID == userID {
			return m, true
		}
	}
	return TeamMember{}, false
}
