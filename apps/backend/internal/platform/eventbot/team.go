package eventbot

// team.go — "Team" (spec 28 §3.3, §10 step 5): the owner sees the
// organization's owners and managers, invites a colleague by e-mail (the
// same POST .../bot-invitations the admin uses) and removes one. A manager
// is told this screen is the owner's. The two-step invite dialog (e-mail,
// then role) is kept in memory: it is seconds long and a restart merely
// asks the e-mail again.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

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

// ─── the in-memory invite dialog ──────────────────────────────────────────────

const teamDialogTTL = 30 * time.Minute

type teamDialog struct {
	email   string // "" while the e-mail is awaited
	expires time.Time
}

type teamDialogs struct {
	mu   sync.Mutex
	byID map[int64]teamDialog
	// lapsed remembers whose dialog ran out, so the next e-mail or role press
	// from them is answered with "time is up" instead of silently falling
	// through to the events list.
	lapsed map[int64]bool
}

func newTeamDialogs() *teamDialogs {
	return &teamDialogs{byID: map[int64]teamDialog{}, lapsed: map[int64]bool{}}
}

func (t *teamDialogs) start(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.lapsed, id)
	t.byID[id] = teamDialog{expires: time.Now().Add(teamDialogTTL)}
}

func (t *teamDialogs) get(id int64) (teamDialog, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.byID[id]
	if !ok {
		return teamDialog{}, false
	}
	if time.Now().After(d.expires) {
		delete(t.byID, id)
		t.lapsed[id] = true
		return teamDialog{}, false
	}
	return d, true
}

// takeLapsed reports whether this person's invite dialog ran out since they
// last heard about it, and forgets the fact.
func (t *teamDialogs) takeLapsed(id int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	was := t.lapsed[id]
	delete(t.lapsed, id)
	return was
}

func (t *teamDialogs) setEmail(id int64, email string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byID[id] = teamDialog{email: email, expires: time.Now().Add(teamDialogTTL)}
}

func (t *teamDialogs) clear(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byID, id)
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
	b.team.clear(from.ID)
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
		if m.TelegramLinked {
			marks += b.texts.T(loc, "bot.team_linked_mark", nil)
		} else if m.InvitationPending {
			marks += b.texts.T(loc, "bot.team_pending_mark", nil)
		} else {
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
		b.team.start(from.ID)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.team_ask_email", nil), b.cancelKeyboard(loc, "team"))
	case "role":
		if len(parts) < 2 {
			return
		}
		dlg, ok := b.team.get(from.ID)
		if !ok || dlg.email == "" {
			prefix := ""
			if b.team.takeLapsed(from.ID) {
				prefix = b.texts.T(loc, "bot.team_dialog_expired", nil) + "\n\n"
			}
			b.showTeam(ctx, chatID, &msgID, from, prefix)
			return
		}
		role := parts[1]
		if role != "owner" && role != "manager" {
			return
		}
		b.team.clear(from.ID)
		res, err := b.arena.Invite(ctx, jwt, orgID, dlg.email, role, loc)
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
	dlg, ok := b.team.get(from.ID)
	if !ok && looksLikeEmail(strings.ToLower(strings.TrimSpace(text))) && b.team.takeLapsed(from.ID) {
		// The dialog ran out while the person was away: say so rather than
		// treating the address as a stray message.
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil && isOwner(id) {
			b.send(ctx, chatID, b.texts.T(id.Locale(), "bot.team_dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: b.texts.T(id.Locale(), "bot.team_invite_btn", nil), CallbackData: "team:invite"}},
			}})
			return true
		}
		return false
	}
	if !ok || dlg.email != "" {
		return false
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || !isOwner(id) {
		b.team.clear(from.ID)
		return false
	}
	loc := id.Locale()
	email := strings.ToLower(strings.TrimSpace(text))
	if !looksLikeEmail(email) {
		b.send(ctx, chatID, b.texts.T(loc, "bot.ask_email_again", nil), b.cancelKeyboard(loc, "team"))
		return true
	}
	b.team.setEmail(from.ID, email)
	b.send(ctx, chatID, b.texts.T(loc, "bot.team_ask_role", map[string]any{"Email": Esc(email)}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.team_role_manager_btn", nil), CallbackData: "team:role:manager"}},
		{{Text: b.texts.T(loc, "bot.team_role_owner_btn", nil), CallbackData: "team:role:owner"}},
		{{Text: b.texts.T(loc, "bot.wz.cancel_btn", nil), CallbackData: "team"}},
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

func (b *Bot) cancelKeyboard(locale, target string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(locale, "bot.wz.cancel_btn", nil), CallbackData: target}},
	}}
}
