package eventbot

// materials.go — the "Materials" screens of the bot (task 7-3, spec 40 §5.7,
// plan stage 7 "EC-30"): the photos, videos, map point, "where else to buy"
// links and photo report that buyers see on the event's page. They open from
// the event card ("ec:mt:<event>") into a menu with one button per part and a
// counter (photos 2/6); a part is a list with a numbered button per entry, an
// entry has its own card (move up or down, caption or seller name, delete) and
// "Add" waits for the next thing the person sends: a photo for the two photo
// parts, a link for videos and links, coordinates or a Telegram location for
// the map. The rules (the limits, the video parser, the address and coordinate
// checks, which organization a photo belongs to) are arena-api's: the bot
// validates only what saves a round trip and shows the API's refusals in
// plain words (materials_input.go, materialsErrKey).
//
// State lives in ONE bot_dialogs row of kind "materials": the part and the
// ids on screen, the open entry and the step the dialog waits at, so a bot
// restart in the middle of a question loses nothing. Presses are
// "mt:<kind>[:<arg>]"; only the number of a list row carries an index
// (paging.go idiom: a list never carries ids), everything that acts on an
// entry acts on the one the card has open. A deletion asks first and is done
// by one press, not a typed word: a link or a photo is added again in a
// moment.
//
//	mt:m             the menu                mt:s:<part>   a part's list (g v p m r)
//	mt:o:<i>         open entry i            mt:l          back to the list
//	mt:u / mt:d      move the open entry up / down
//	mt:c / mt:cx     caption: ask / remove   mt:n          seller name: ask
//	mt:a             add (waits for input)   mt:x / mt:y   delete: ask / confirm
//
// Rights: the owner, the manager and the platform operator hold event.update
// (canViewSales); an agent holds only event.read and gets the same screens
// without a button that changes anything. Every call is still decided by the
// API, so a forged press from an agent writes nothing.

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

const materialsDialogKind = "materials"

// The dialog's steps.
const (
	mtStepMenu    = "menu"
	mtStepList    = "list"
	mtStepItem    = "item"
	mtStepConfirm = "confirm" // the delete question is open
	mtStepAdd     = "add"     // waiting for a photo, a link or a point
	mtStepCaption = "caption" // waiting for the caption of the open photo
	mtStepSeller  = "seller"  // waiting for the seller name of the open link
)

// materialsDialog is the screens' state, stored as the bot_dialogs row's JSON.
type materialsDialog struct {
	OrgID     uuid.UUID `json:"org_id"`
	EventID   uuid.UUID `json:"event_id"`
	EventName string    `json:"event_name"`
	MsgID     int       `json:"msg_id,omitempty"`
	// Section is the open part (a materialSection code), "" at the menu.
	Section string `json:"section,omitempty"`
	// IDs are the entries of the list on screen, in order.
	IDs []uuid.UUID `json:"ids,omitempty"`
	// OpenID is the entry whose card is open, the one a move, a caption or a
	// delete acts on.
	OpenID *uuid.UUID `json:"open_id,omitempty"`
}

// canEditMaterials reports whether the person may change an event's
// materials: the roles that hold event.update through the bot.
func canEditMaterials(id *Identity) bool { return canViewSales(id) }

// Limits the bot enforces before the API is asked.
const (
	maxMaterialCaptionRunes = 200
	maxMaterialSellerRunes  = 120
	// materialPhotoMaxBytes is the largest picture the bot downloads from
	// Telegram and uploads (a gallery photo is shown full width).
	materialPhotoMaxBytes = 10 << 20
	materialListRowSize   = 6 // numbered buttons in a row
)

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadMaterialsState reads the dialog; a missing one, an expired one or one of
// another organization or event starts empty.
func (b *Bot) loadMaterialsState(ctx context.Context, tg int64) (st materialsDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, materialsDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: materials dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return materialsDialog{}, "", false, false
	}
	return st, step, found, expired
}

func (b *Bot) saveMaterialsState(ctx context.Context, tg int64, st materialsDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, materialsDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: materials dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveMaterials ends the dialog: whoever opens another screen is no longer
// adding a photo, so a picture or a text sent next is not taken for one.
func (b *Bot) leaveMaterials(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, materialsDialogKind); err != nil {
		b.logger.Warn("eventbot: materials dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── entry points ─────────────────────────────────────────────────────────────

// materialsEventRow is the event card's button.
func (b *Bot) materialsEventRow(loc string, eventID uuid.UUID) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.btn", nil), CallbackData: "ec:mt:" + eventID.String()}}
}

// showMaterials opens the menu of one event (the card's "Materials" button).
func (b *Bot) showMaterials(ctx context.Context, chatID int64, msgID *int, from *models.User, eventID uuid.UUID) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, msgID, from)
	if !ok {
		return
	}
	events, err := b.arena.ListEvents(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	for _, e := range events {
		if e.Id != eventID {
			continue
		}
		st := materialsDialog{OrgID: id.Current.OrgID, EventID: eventID, EventName: e.Name}
		if msgID != nil {
			st.MsgID = *msgID
		}
		b.materialsMenu(ctx, chatID, msgID, id, jwt, st, "")
		return
	}
	b.reply(ctx, chatID, msgID, b.texts.T(id.Locale(), "bot.ec.not_found", nil), b.backKeyboard(id.Locale(), "home"))
}

// materialsCallback handles every "mt:<data>" press.
func (b *Bot) materialsCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	st, step, found, expired := b.loadMaterialsState(ctx, from.ID)
	if expired || !found || st.OrgID != id.Current.OrgID {
		// The screen ran out while the person was away (or belongs to another
		// organization): say so and send them back to the event card, rather
		// than act on a press made on stale rows.
		note := ""
		if expired {
			note = b.texts.T(loc, "bot.dialog_expired", nil)
		}
		kb := b.backKeyboard(loc, "events")
		if found && st.EventID != uuid.Nil {
			kb = b.backKeyboard(loc, "ec:o:"+st.EventID.String())
		}
		b.reply(ctx, chatID, &msgID, strings.TrimSpace(note+"\n\n"+b.texts.T(loc, "bot.mt.gone_dialog", nil)), kb)
		return
	}
	st.MsgID = msgID
	kind, arg, _ := strings.Cut(data, ":")

	switch kind {
	case "m":
		b.materialsMenu(ctx, chatID, &msgID, id, jwt, st, "")
		return
	case "s":
		sec, ok := materialSectionByCode(arg)
		if !ok {
			return
		}
		b.materialsList(ctx, chatID, &msgID, id, jwt, st, sec, "")
		return
	case "l":
		sec, ok := materialSectionByCode(st.Section)
		if !ok {
			b.materialsMenu(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.materialsList(ctx, chatID, &msgID, id, jwt, st, sec, "")
		return
	}

	sec, ok := materialSectionByCode(st.Section)
	if !ok {
		b.materialsMenu(ctx, chatID, &msgID, id, jwt, st, "")
		return
	}
	if kind == "o" {
		i, ok := ParseIndex(arg, len(st.IDs))
		if !ok {
			b.materialsList(ctx, chatID, &msgID, id, jwt, st, sec, "")
			return
		}
		b.materialsItem(ctx, chatID, &msgID, id, jwt, st, sec, st.IDs[i], "")
		return
	}

	// Everything below changes something: only a role that may.
	if !canEditMaterials(id) {
		b.materialsList(ctx, chatID, &msgID, id, jwt, st, sec, b.texts.T(loc, "bot.mt.readonly", nil)+"\n\n")
		return
	}
	switch kind {
	case "a":
		b.materialsAskAdd(ctx, chatID, &msgID, id, jwt, st, sec)
	case "u", "d", "c", "cx", "n", "x":
		if sec.Code == "m" && kind == "x" {
			st.OpenID = nil
			b.materialsAskDelete(ctx, chatID, &msgID, id, st, sec)
			return
		}
		if st.OpenID == nil {
			b.materialsList(ctx, chatID, &msgID, id, jwt, st, sec, "")
			return
		}
		b.materialsItemAction(ctx, chatID, &msgID, id, jwt, st, sec, kind)
	case "y":
		// The deletion needs its own question open, for THIS entry.
		if step != mtStepConfirm || (sec.Code != "m" && st.OpenID == nil) {
			b.materialsList(ctx, chatID, &msgID, id, jwt, st, sec, "")
			return
		}
		b.materialsDelete(ctx, chatID, &msgID, id, jwt, st, sec)
	}
}

// materialsItemAction handles the presses of an open entry's card.
func (b *Bot) materialsItemAction(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection, kind string) {
	loc := id.Locale()
	switch kind {
	case "u", "d":
		b.materialsMove(ctx, chatID, msgID, id, jwt, st, sec, kind == "u")
	case "c":
		if !sec.isPhoto() {
			return
		}
		b.materialsAskText(ctx, chatID, msgID, id, st, sec, mtStepCaption, "bot.mt.ask_caption", "")
	case "n":
		if sec.Code != "p" {
			return
		}
		b.materialsAskText(ctx, chatID, msgID, id, st, sec, mtStepSeller, "bot.mt.ask_seller", "")
	case "cx":
		if !sec.isPhoto() {
			return
		}
		if _, err := b.arena.MaterialsPatch(ctx, jwt, st.OrgID, st.EventID, sec, *st.OpenID, map[string]any{"caption": nil}); err != nil {
			b.materialsFail(ctx, chatID, msgID, id, jwt, st, sec, err)
			return
		}
		b.materialsItem(ctx, chatID, msgID, id, jwt, st, sec, *st.OpenID, b.texts.T(loc, "bot.mt.saved", nil)+"\n\n")
	case "x":
		b.materialsAskDelete(ctx, chatID, msgID, id, st, sec)
	}
}

// ─── the menu ─────────────────────────────────────────────────────────────────

// materialsMenu draws the five parts with their counters.
func (b *Bot) materialsMenu(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st materialsDialog, note string) {
	loc := id.Locale()
	st.Section, st.IDs, st.OpenID = "", nil, nil
	var rows [][]models.InlineKeyboardButton
	for _, sec := range materialSections {
		label, err := b.materialsMenuLabel(ctx, loc, jwt, st, sec)
		if err != nil {
			b.ecError(ctx, chatID, editMsgID, id, err, "ec:o:"+st.EventID.String())
			return
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: "mt:s:" + sec.Code}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ec:o:" + st.EventID.String()},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveMaterialsState(ctx, id.Link.TelegramUserID, st, mtStepMenu)
	text := note + b.texts.T(loc, "bot.mt.menu", map[string]any{"Event": Esc(st.EventName)})
	if !canEditMaterials(id) {
		text += "\n\n" + b.texts.T(loc, "bot.mt.readonly", nil)
	}
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// materialsMenuLabel is one menu button: icon, name and the counter.
func (b *Bot) materialsMenuLabel(ctx context.Context, loc, jwt string, st materialsDialog, sec materialSection) (string, error) {
	if sec.Code == "m" {
		p, err := b.arena.MapPointGet(ctx, jwt, st.OrgID, st.EventID)
		if err != nil {
			return "", err
		}
		state := b.texts.T(loc, "bot.mt.state_unset", nil)
		if p != nil {
			state = b.texts.T(loc, "bot.mt.state_set", nil)
		}
		return b.texts.T(loc, "bot.mt.sec_m", map[string]any{"State": state}), nil
	}
	l, err := b.arena.MaterialsList(ctx, jwt, st.OrgID, st.EventID, sec)
	if err != nil {
		return "", err
	}
	return b.texts.T(loc, "bot.mt.sec_"+sec.Code, map[string]any{"N": len(l.Items), "Max": materialsMax(l, sec)}), nil
}

// materialsMax is the limit the API reported, else the table's.
func materialsMax(l MaterialList, sec materialSection) int {
	if l.MaxItems > 0 {
		return l.MaxItems
	}
	return sec.Max
}

// ─── a part's list ────────────────────────────────────────────────────────────

// materialsList reads the part afresh and draws it: a line per entry, a
// numbered button per entry, and "Add" while there is room.
func (b *Bot) materialsList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection, note string) {
	if sec.Code == "m" {
		b.materialsMapScreen(ctx, chatID, editMsgID, id, jwt, st, sec, note)
		return
	}
	loc := id.Locale()
	l, err := b.arena.MaterialsList(ctx, jwt, st.OrgID, st.EventID, sec)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "mt:m")
		return
	}
	st.Section, st.OpenID = sec.Code, nil
	st.IDs = st.IDs[:0]
	for _, it := range l.Items {
		st.IDs = append(st.IDs, it.ID)
	}
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveMaterialsState(ctx, id.Link.TelegramUserID, st, mtStepList)

	max := materialsMax(l, sec)
	text := note + b.texts.T(loc, "bot.mt.list_"+sec.Code, map[string]any{"Event": Esc(st.EventName), "N": len(l.Items), "Max": max})
	if len(l.Items) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.mt.empty_"+sec.Code, nil)
	} else {
		var sb strings.Builder
		for i, it := range l.Items {
			sb.WriteString("\n" + fmt.Sprintf("%d. ", i+1) + b.materialsLine(loc, sec, it))
		}
		text += "\n" + sb.String() + "\n\n" + b.texts.T(loc, "bot.mt.pick_hint", nil)
	}
	text += "\n\n" + b.texts.T(loc, "bot.mt.hint_"+sec.Code, nil)

	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for i := range l.Items {
		row = append(row, models.InlineKeyboardButton{Text: fmt.Sprint(i + 1), CallbackData: ItemCallback("mt:o", i)})
		if len(row) == materialListRowSize {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if canEditMaterials(id) && len(l.Items) < max {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.add_btn", nil), CallbackData: "mt:a"}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.mt.menu_btn", nil), CallbackData: "mt:m"}})
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// materialsLine is the text of one entry in a list.
func (b *Bot) materialsLine(loc string, sec materialSection, it MaterialItem) string {
	switch sec.Code {
	case "v":
		return Esc(truncate(it.URL, 60)) + " (" + Esc(it.Provider) + ")"
	case "p":
		return Esc(truncate(it.SellerName, 40)) + ": " + Esc(truncate(it.URL, 60))
	}
	if it.Caption != nil && strings.TrimSpace(*it.Caption) != "" {
		return Esc(truncate(*it.Caption, 60))
	}
	return b.texts.T(loc, "bot.mt.no_caption", nil)
}

// ─── one entry ────────────────────────────────────────────────────────────────

// materialsItem reads the part afresh and draws the card of one entry.
func (b *Bot) materialsItem(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection, itemID uuid.UUID, note string) {
	loc := id.Locale()
	l, err := b.arena.MaterialsList(ctx, jwt, st.OrgID, st.EventID, sec)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "mt:m")
		return
	}
	pos := -1
	for i, it := range l.Items {
		if it.ID == itemID {
			pos = i
		}
	}
	if pos < 0 {
		b.materialsList(ctx, chatID, editMsgID, id, jwt, st, sec, b.texts.T(loc, "bot.mt.err_gone", nil)+"\n\n")
		return
	}
	it := l.Items[pos]
	st.Section, st.OpenID = sec.Code, &itemID
	st.IDs = st.IDs[:0]
	for _, x := range l.Items {
		st.IDs = append(st.IDs, x.ID)
	}
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveMaterialsState(ctx, id.Link.TelegramUserID, st, mtStepItem)

	n, total := pos+1, len(l.Items)
	var text string
	switch sec.Code {
	case "v":
		text = b.texts.T(loc, "bot.mt.card_video", map[string]any{"N": n, "Total": total, "Url": Esc(it.URL), "Provider": Esc(it.Provider)})
	case "p":
		text = b.texts.T(loc, "bot.mt.card_partner", map[string]any{"N": n, "Total": total, "Url": Esc(it.URL), "Seller": Esc(it.SellerName)})
	default:
		caption := b.texts.T(loc, "bot.mt.no_caption", nil)
		if it.Caption != nil && strings.TrimSpace(*it.Caption) != "" {
			caption = Esc(*it.Caption)
		}
		text = b.texts.T(loc, "bot.mt.card_photo_"+sec.Code, map[string]any{"N": n, "Total": total, "Caption": caption})
	}

	var rows [][]models.InlineKeyboardButton
	if canEditMaterials(id) {
		var move []models.InlineKeyboardButton
		if pos > 0 {
			move = append(move, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.mt.up_btn", nil), CallbackData: "mt:u"})
		}
		if pos < total-1 {
			move = append(move, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.mt.down_btn", nil), CallbackData: "mt:d"})
		}
		if len(move) > 0 {
			rows = append(rows, move)
		}
		switch {
		case sec.isPhoto():
			edit := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.caption_btn", nil), CallbackData: "mt:c"}}
			if it.Caption != nil && strings.TrimSpace(*it.Caption) != "" {
				edit = append(edit, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.mt.caption_clear_btn", nil), CallbackData: "mt:cx"})
			}
			rows = append(rows, edit)
		case sec.Code == "p":
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.seller_btn", nil), CallbackData: "mt:n"}})
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.delete_btn", nil), CallbackData: "mt:x"}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "mt:l"}})
	b.reply(ctx, chatID, editMsgID, clipMessage(note+text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// materialsMove swaps the open entry with its neighbour: the list is read
// afresh, the swap made on that and the whole order sent back.
func (b *Bot) materialsMove(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection, up bool) {
	loc := id.Locale()
	l, err := b.arena.MaterialsList(ctx, jwt, st.OrgID, st.EventID, sec)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "mt:m")
		return
	}
	pos := -1
	ids := make([]uuid.UUID, len(l.Items))
	for i, it := range l.Items {
		ids[i] = it.ID
		if it.ID == *st.OpenID {
			pos = i
		}
	}
	to := pos + 1
	if up {
		to = pos - 1
	}
	if pos < 0 || to < 0 || to >= len(ids) {
		b.materialsList(ctx, chatID, msgID, id, jwt, st, sec, b.texts.T(loc, "bot.mt.err_gone", nil)+"\n\n")
		return
	}
	ids[pos], ids[to] = ids[to], ids[pos]
	if _, err := b.arena.MaterialsReorder(ctx, jwt, st.OrgID, st.EventID, sec, ids); err != nil {
		b.materialsFail(ctx, chatID, msgID, id, jwt, st, sec, err)
		return
	}
	b.materialsItem(ctx, chatID, msgID, id, jwt, st, sec, *st.OpenID, b.texts.T(loc, "bot.mt.moved", nil)+"\n\n")
}

// ─── delete ───────────────────────────────────────────────────────────────────

// materialsAskDelete opens the question; the deletion is the next press.
func (b *Bot) materialsAskDelete(ctx context.Context, chatID int64, msgID *int, id *Identity, st materialsDialog, sec materialSection) {
	loc := id.Locale()
	if msgID != nil {
		st.MsgID = *msgID
	}
	b.saveMaterialsState(ctx, id.Link.TelegramUserID, st, mtStepConfirm)
	back := "mt:l"
	if sec.Code == "m" {
		back = "mt:s:m"
	}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.mt.delete_yes_btn", nil), CallbackData: "mt:y"}},
		{{Text: b.texts.T(loc, "bot.mt.cancel_btn", nil), CallbackData: back}},
	}}
	b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.mt.del_ask_"+sec.Code, nil), kb)
}

// materialsDelete removes the open entry (or the map point) and shows the part.
func (b *Bot) materialsDelete(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection) {
	loc := id.Locale()
	var err error
	if sec.Code == "m" {
		err = b.arena.MapPointDelete(ctx, jwt, st.OrgID, st.EventID)
	} else {
		err = b.arena.MaterialsDelete(ctx, jwt, st.OrgID, st.EventID, sec, *st.OpenID)
	}
	if err != nil {
		b.materialsFail(ctx, chatID, msgID, id, jwt, st, sec, err)
		return
	}
	b.materialsList(ctx, chatID, msgID, id, jwt, st, sec, b.texts.T(loc, "bot.mt.deleted", nil)+"\n\n")
}

// ─── add and the questions ────────────────────────────────────────────────────

// materialsAskAdd opens "Add" for the part: it checks the part has room first,
// so a photo is not uploaded for nothing.
func (b *Bot) materialsAskAdd(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection) {
	loc := id.Locale()
	if sec.Code != "m" {
		l, err := b.arena.MaterialsList(ctx, jwt, st.OrgID, st.EventID, sec)
		if err != nil {
			b.ecError(ctx, chatID, msgID, id, err, "mt:m")
			return
		}
		if len(l.Items) >= materialsMax(l, sec) {
			b.materialsList(ctx, chatID, msgID, id, jwt, st, sec, b.texts.T(loc, "bot.mt.err_full", map[string]any{"Max": materialsMax(l, sec)})+"\n\n")
			return
		}
	}
	st.OpenID = nil
	b.materialsAskText(ctx, chatID, msgID, id, st, sec, mtStepAdd, "bot.mt.ask_"+sec.Code, "")
}

// materialsAskText stores the step and asks the question of the given key,
// with the part's Cancel button. prefix goes above the question (a refusal).
func (b *Bot) materialsAskText(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st materialsDialog, sec materialSection, step, key, prefix string) {
	loc := id.Locale()
	st.Section = sec.Code
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveMaterialsState(ctx, id.Link.TelegramUserID, st, step)
	back := "mt:s:" + sec.Code
	if st.OpenID != nil && step != mtStepAdd {
		back = "mt:o:" + b.materialsOpenIndex(st)
	}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.mt.cancel_btn", nil), CallbackData: back}},
	}}
	b.reply(ctx, chatID, editMsgID, prefix+b.texts.T(loc, key, nil), kb)
}

// materialsOpenIndex is the index of the open entry in the stored list, as a
// callback argument.
func (b *Bot) materialsOpenIndex(st materialsDialog) string {
	for i, x := range st.IDs {
		if st.OpenID != nil && x == *st.OpenID {
			return fmt.Sprint(i)
		}
	}
	return "0"
}

// ─── the map ──────────────────────────────────────────────────────────────────

// osmLink is the OpenStreetMap address of a point, the one the card shows so
// the organizer can check it on a map.
func osmLink(lat, lon float64) string {
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", lat, lon, lat, lon)
}

// materialsMapScreen is the map part: the point (or "none") with its buttons.
func (b *Bot) materialsMapScreen(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection, note string) {
	loc := id.Locale()
	p, err := b.arena.MapPointGet(ctx, jwt, st.OrgID, st.EventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "mt:m")
		return
	}
	st.Section, st.OpenID, st.IDs = sec.Code, nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveMaterialsState(ctx, id.Link.TelegramUserID, st, mtStepList)

	var text string
	var rows [][]models.InlineKeyboardButton
	if p == nil {
		text = note + b.texts.T(loc, "bot.mt.map_none", map[string]any{"Event": Esc(st.EventName)})
		if canEditMaterials(id) {
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.map_set_btn", nil), CallbackData: "mt:a"}})
		}
	} else {
		text = note + b.texts.T(loc, "bot.mt.map_card", map[string]any{
			"Event": Esc(st.EventName), "Lat": fmt.Sprintf("%.6f", p.Latitude), "Lon": fmt.Sprintf("%.6f", p.Longitude),
			"Link": Esc(osmLink(p.Latitude, p.Longitude)),
		})
		if canEditMaterials(id) {
			rows = append(rows,
				[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.map_change_btn", nil), CallbackData: "mt:a"}},
				[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.mt.delete_btn", nil), CallbackData: "mt:x"}},
			)
		}
	}
	text += "\n\n" + b.texts.T(loc, "bot.mt.hint_m", nil)
	rows = append(rows, []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.mt.menu_btn", nil), CallbackData: "mt:m"}})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── refusals ─────────────────────────────────────────────────────────────────

// materialsErrKey maps a refusal of the API to the message that says plainly
// what to do. "" means the API's answer has no words of its own.
func materialsErrKey(err error) string {
	switch APIErrorCode(err) {
	case matCodeGalleryFull, matCodeVideosFull, matCodePartnersFull, matCodeReportFull:
		return "bot.mt.err_full"
	case matCodeInvalidMedia, matCodeInvalidReport:
		return "bot.mt.err_photo_failed"
	case matCodeInvalidCaption:
		return "bot.mt.err_caption"
	case matCodeInvalidVideo:
		return "bot.mt.err_video_url"
	case matCodeInvalidPartner:
		return "bot.mt.err_partner_url"
	case matCodeInvalidSeller:
		return "bot.mt.err_seller"
	case matCodeInvalidPoint:
		return "bot.mt.err_map"
	case "event.gallery_duplicate", "event.report_photos_duplicate":
		return "bot.mt.err_duplicate"
	case "event.gallery_item_not_found", "event.video_not_found", "event.partner_link_not_found",
		"event.photo_not_found", matCodeMapNotFound:
		return "bot.mt.err_gone"
	case "event.gallery_order_mismatch", "event.videos_order_mismatch",
		"event.partner_links_order_mismatch", "event.report_photos_order_mismatch":
		return "bot.mt.err_changed"
	}
	return ""
}

// materialsFail answers a failed change: a refusal the bot has words for shows
// the part again with the reason above it, anything else is the generic error.
func (b *Bot) materialsFail(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st materialsDialog, sec materialSection, err error) {
	if key := materialsErrKey(err); key != "" {
		b.materialsList(ctx, chatID, msgID, id, jwt, st, sec, b.texts.T(id.Locale(), key, map[string]any{"Max": sec.Max})+"\n\n")
		return
	}
	b.ecError(ctx, chatID, msgID, id, err, "mt:m")
}

// normalizeMaterialLink turns what the person typed into a link the API
// accepts: it adds https:// to a bare address and leaves everything else (a
// blank, a space inside, a wrong scheme) for the API to refuse in its words.
func normalizeMaterialLink(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t\r\n") || strings.Contains(s, "://") {
		return s
	}
	if u, err := url.Parse("https://" + s); err != nil || !strings.Contains(u.Host, ".") {
		return s
	}
	return "https://" + s
}

// cleanMaterialText folds a typed caption or name to one line.
func cleanMaterialText(raw string) string { return strings.Join(strings.Fields(raw), " ") }

// runeLen is the length in characters.
func runeLen(s string) int { return utf8.RuneCountInString(s) }
