package eventbot

// menu_sections.go — the main menu in sections (2026-11-12). The home screen
// keeps nine buttons and the "First steps" block; the rest of the functions
// sit one level down on the screens of the "mn:" prefix. Every old callback
// of the flat menu still works: only the screen that carries its button moved.
//
// The section screens are stateless: a title, buttons, and the way home. No
// dialog kind belongs to "mn:", so a press of it ends every open dialog like
// any press that is not a dialog's own (dialog_owners.go).

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot/models"
)

// menuSectionPrefix is the callback prefix of the section screens.
const menuSectionPrefix = "mn"

// stepButtonData maps each first step the bot knows to the press of its button.
var stepButtonData = map[string]string{
	"org_data":   "og:h",
	"payments":   "mn:pay",
	"event":      "wz:new",
	"sales_link": "ql:menu",
}

type button = models.InlineKeyboardButton

func (b *Bot) mnBtn(loc, key, data string) []button {
	return []button{{Text: b.texts.T(loc, key, nil), CallbackData: data}}
}

func (b *Bot) mnHomeRow(loc string) []button {
	return b.mnBtn(loc, "bot.btn_home", "home")
}

// ─── first steps ──────────────────────────────────────────────────────────────

// firstSteps reads the person's first steps. It answers nil when there is
// nothing to show: the platform operator, a failed call, a complete list or a
// list without a step the bot knows. A failure never changes the menu.
func (b *Bot) firstSteps(ctx context.Context, id *Identity, jwt string) *GettingStarted {
	if b.arena == nil || id == nil || id.Current == nil || id.Superadmin || jwt == "" {
		return nil
	}
	gs, err := b.arena.GettingStarted(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.logger.Warn("eventbot: getting started read failed", slog.String("error", err.Error()))
		return nil
	}
	if gs.Complete {
		return nil
	}
	known := make([]GettingStartedStep, 0, len(gs.Steps))
	for _, s := range gs.Steps {
		if _, ok := stepButtonData[s.Key]; ok {
			known = append(known, s)
		}
	}
	if len(known) == 0 {
		return nil
	}
	gs.Steps = known
	return &gs
}

// firstStepsText is the block under the menu title.
func (b *Bot) firstStepsText(loc string, gs *GettingStarted) string {
	if gs == nil {
		return ""
	}
	done := 0
	var lines strings.Builder
	for _, s := range gs.Steps {
		mark := "▫️ "
		if s.Done {
			done++
			mark = "✅ "
		}
		lines.WriteString("\n" + mark + b.texts.T(loc, "bot.mn.step_"+s.Key, nil))
	}
	return "\n\n" + b.texts.T(loc, "bot.mn.steps_title", map[string]any{"Done": done, "Total": len(gs.Steps)}) + lines.String()
}

// firstStepRows is one button per undone step.
func (b *Bot) firstStepRows(loc string, gs *GettingStarted) [][]button {
	if gs == nil {
		return nil
	}
	var rows [][]button
	for _, s := range gs.Steps {
		if !s.Done {
			rows = append(rows, b.mnBtn(loc, "bot.mn.step_"+s.Key+"_btn", stepButtonData[s.Key]))
		}
	}
	return rows
}

// ─── the home menu ────────────────────────────────────────────────────────────

// homeKeyboard is the main menu with the first steps read from the server.
func (b *Bot) homeKeyboard(ctx context.Context, id *Identity, jwt string) *models.InlineKeyboardMarkup {
	return b.homeKeyboardFor(id, b.firstSteps(ctx, id, jwt))
}

// homeKeyboardFor is the main menu: one button per row, the open first steps
// above. The sections hold the rest.
func (b *Bot) homeKeyboardFor(id *Identity, gs *GettingStarted) *models.InlineKeyboardMarkup {
	loc := id.Locale()
	rows := b.firstStepRows(loc, gs)
	rows = append(rows, b.mnBtn(loc, "bot.wz.new_event_btn", "wz:new"), b.mnBtn(loc, "bot.btn_events", "el:new"))
	if canViewSales(id) {
		rows = append(rows, b.mnBtn(loc, "bot.mn.orders_btn", "mn:ord"))
	}
	if len(b.promoSectionRows(loc, id)) > 0 {
		rows = append(rows, b.mnBtn(loc, "bot.mn.promo_btn", "mn:pro"))
	}
	rows = append(rows, b.mnBtn(loc, "bot.mn.door_btn", "mn:door"))
	if row := b.shopMenuRow(loc, id); row != nil { // the shop (spec 61, stage M3)
		rows = append(rows, row)
	}
	rows = append(rows, b.mnBtn(loc, "bot.mn.settings_btn", "mn:set"))
	if row := b.webMenuRow(loc, id); row != nil { // the web event center (spec 37 §4.3)
		rows = append(rows, row)
	}
	rows = append(rows, b.mnBtn(loc, "bot.btn_help", "help"))
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ─── the section screens ──────────────────────────────────────────────────────

// menuSectionCallback handles every "mn:<data>" press.
func (b *Bot) menuSectionCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	noRights := func() {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
	}
	var text string
	var rows [][]button
	switch data {
	case "ord":
		if !canViewSales(id) {
			noRights()
			return
		}
		text, rows = b.texts.T(loc, "bot.mn.orders_title", nil), b.ordersSectionRows(ctx, loc, id, jwt)
	case "pro":
		rows = b.promoSectionRows(loc, id)
		if len(rows) == 0 {
			noRights()
			return
		}
		text = b.texts.T(loc, "bot.mn.promo_title", nil)
	case "door":
		text, rows = b.texts.T(loc, "bot.mn.door_title", nil), b.doorSectionRows(loc, id)
	case "set":
		text, rows = b.texts.T(loc, "bot.mn.settings_title", nil), b.settingsSectionRows(ctx, loc, id, jwt)
	case "mail":
		if !canViewSales(id) {
			noRights()
			return
		}
		text = b.texts.T(loc, "bot.mn.mail_text", nil)
		if row := b.webMenuRow(loc, id); row != nil {
			rows = append(rows, row)
		}
	case "ven":
		if id.Superadmin {
			noRights()
			return
		}
		text = b.texts.T(loc, "bot.mn.venues_text", nil)
		if row := b.webMenuRow(loc, id); row != nil {
			rows = append(rows, row)
		}
	case "gl":
		if !canViewSales(id) {
			noRights()
			return
		}
		text, rows = b.guestListSection(ctx, chatID, loc, id, jwt)
	case "pay":
		if !isOwner(id) && !id.Superadmin {
			noRights()
			return
		}
		text = b.paymentsSectionText(ctx, loc, id, jwt)
	default:
		return
	}
	rows = append(rows, b.mnHomeRow(loc))
	b.reply(ctx, chatID, &msgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ordersSectionRows: Orders, Refunds, and Payment disputes when the
// organization has any.
func (b *Bot) ordersSectionRows(ctx context.Context, loc string, id *Identity, jwt string) [][]button {
	rows := [][]button{b.mnBtn(loc, "bot.ord.btn", "or:new")}
	if row := b.queueMenuRow(loc, id); row != nil {
		rows = append(rows, row)
	}
	if row := b.disputeSectionRow(ctx, loc, id, jwt); row != nil {
		rows = append(rows, row)
	}
	return rows
}

// disputeSectionRow is the disputes button: hidden while the organization has
// no dispute at all, with a counter while some wait for the organizer, plain
// when the server does not answer (a failure never hides a function).
func (b *Bot) disputeSectionRow(ctx context.Context, loc string, id *Identity, jwt string) []button {
	row := b.disputeMenuRow(loc, id)
	if row == nil || b.arena == nil || jwt == "" {
		return row
	}
	list, err := b.arena.DisputeList(ctx, jwt, id.Current.OrgID, "", 1, 0)
	if err != nil {
		return row
	}
	if list.Total == 0 && list.OpenCount == 0 {
		return nil
	}
	if list.OpenCount > 0 {
		row[0].Text = b.texts.T(loc, "bot.mn.disputes_count_btn", map[string]any{"Count": list.OpenCount})
	}
	return row
}

// promoSectionRows are the rows of the Promotion section for this role; none
// means the section is not shown.
func (b *Bot) promoSectionRows(loc string, id *Identity) [][]button {
	var rows [][]button
	if row := b.promoMenuRow(loc, id); row != nil {
		rows = append(rows, row)
	}
	if canInvite(id) {
		rows = append(rows, b.mnBtn(loc, "bot.inv.btn", "iv:new"))
	}
	if row := b.salesLinkMenuRow(loc, id); row != nil {
		rows = append(rows, row)
	}
	if canViewSales(id) && b.webMenuRow(loc, id) != nil {
		rows = append(rows, b.mnBtn(loc, "bot.mn.mail_btn", "mn:mail"))
	}
	return rows
}

func (b *Bot) doorSectionRows(loc string, id *Identity) [][]button {
	rows := [][]button{b.scannerRow(loc)}
	if canViewSales(id) {
		rows = append(rows, b.mnBtn(loc, "bot.mn.guests_btn", "mn:gl"))
	}
	return rows
}

// settingsSectionRows: the owner's own tools, the team, and the person's own
// settings.
func (b *Bot) settingsSectionRows(ctx context.Context, loc string, id *Identity, jwt string) [][]button {
	var rows [][]button
	if row := b.orgDataMenuRow(loc, id); row != nil {
		rows = append(rows, row)
	}
	if isOwner(id) || id.Superadmin {
		rows = append(rows, b.mnBtn(loc, "bot.mn.pay_btn", "mn:pay"))
	}
	if isOwner(id) {
		// An owner who is still alone goes straight into the invite dialog: a
		// Team screen would list only themselves.
		if b.ownerIsAlone(ctx, id, jwt) {
			rows = append(rows, b.mnBtn(loc, "bot.btn_team_add", "team:invite"))
		} else {
			rows = append(rows, b.mnBtn(loc, "bot.btn_team", "team"))
		}
	} else if isTeamReader(id) { // the manager reads the team, never changes it (V-11)
		rows = append(rows, b.mnBtn(loc, "bot.btn_team", "team"))
	}
	if row := b.promoterMenuRow(loc, id); row != nil {
		rows = append(rows, row)
	}
	if b.webMenuRow(loc, id) != nil {
		rows = append(rows, b.mnBtn(loc, "bot.mn.venues_btn", "mn:ven"))
	}
	rows = append(rows, b.mnBtn(loc, "bot.ec.notif_btn", "ec:nt"), b.mnBtn(loc, "bot.btn_lang", "lang"))
	if len(id.Memberships) > 1 {
		rows = append(rows, b.mnBtn(loc, "bot.btn_org", "org"))
	}
	return rows
}

// paymentsSectionText says whether the organization can take money.
func (b *Bot) paymentsSectionText(ctx context.Context, loc string, id *Identity, jwt string) string {
	key := "bot.mn.pay_unknown"
	if b.arena != nil && jwt != "" {
		if connected, known := b.arena.PaymentsConnected(ctx, jwt, id.Current.OrgID); known {
			key = "bot.mn.pay_off"
			if connected {
				key = "bot.mn.pay_ok"
			}
		}
	}
	return b.texts.T(loc, key, nil)
}

// guestListSection explains where a session's guest list is and carries the
// toggle of the automatic one (guest_list.go). The toggle's presses end with
// ":mn" so the screen redraws itself.
func (b *Bot) guestListSection(ctx context.Context, chatID int64, loc string, id *Identity, jwt string) (string, [][]button) {
	text := b.texts.T(loc, "bot.mn.guests_text", nil)
	var rows [][]button
	if glText, glRow := b.guestListRow(ctx, chatID, jwt, id); glRow != nil {
		text += "\n\n" + glText
		for i := range glRow {
			glRow[i].CallbackData += ":" + menuSectionPrefix
		}
		rows = append(rows, glRow)
	}
	rows = append(rows, b.mnBtn(loc, "bot.btn_events", "el:new"))
	return text, rows
}
