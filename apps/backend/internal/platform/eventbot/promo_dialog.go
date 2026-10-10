package eventbot

// promo_dialog.go — the promo-code screens of the bot (EC-11, spec 35 §6.2):
// the list (reachable from the main menu and from an event's card), the card
// of a code with its actions, the "new code" dialog and the "Sessions" edit.
// Everything lives in ONE bot_dialogs row of kind "promo" (dialogs.go) — the
// list's page and the ids on screen, the code whose card is open, the draft of
// a code being created, the sessions being edited, the delete question — so a
// bot restart loses none of it and a typed text always knows what it answers.
//
// Presses are "pm:<kind>:<arg>". A row of a list is named by its INDEX on the
// page (paging.go); anything that changes a code carries the code's UUID, so
// an old message keeps meaning THAT code however many other cards the chat
// holds:
//
//	pm:l[:<event>]   the list (fresh)       pm:p:<n>       page       pm:b   back to the list
//	pm:o:<i>         open row i             pm:v:<code>    the card of a code
//	pm:n[:<event>]   start a new code       pm:c:<data>    one answer of the new-code dialog
//	pm:k:<data>      the picker (an event, then its sessions) of the dialog or of the edit
//	pm:ps:<code>     pause                  pm:ac:<code>   activate
//	pm:se:<code>     the Sessions screen    pm:sa:<code>   all sessions   pm:so:<code> only checked
//	pm:u:<code>      usage                  pm:up:<code>:<n>  its page    pm:csv:<code>  the CSV file
//	pm:del:<code>    delete (asks the word)
//
// A deletion is never one button: the press only asks, and the code goes
// when the person TYPES the delete word, with the number of uses shown first.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const promoDialogKind = "promo"

// maxPromoEvents bounds how many running events the event chooser and the
// currency look-up read.
const maxPromoEvents = 40

// promoDialog is the screens' state, stored as the bot_dialogs row's JSON.
type promoDialog struct {
	OrgID uuid.UUID `json:"org_id"`
	MsgID int       `json:"msg_id,omitempty"`
	// EventID/EventName narrow the list to one event (and are the event a new
	// code starts on); Page/IDs are the list on screen.
	EventID   *uuid.UUID  `json:"event_id,omitempty"`
	EventName string      `json:"event_name,omitempty"`
	Page      int         `json:"page"`
	IDs       []uuid.UUID `json:"ids,omitempty"`
	// CardID/CardCode/CardUses name the code of the open card, the usage
	// screen or the delete question.
	CardID   *uuid.UUID  `json:"card_id,omitempty"`
	CardCode string      `json:"card_code,omitempty"`
	CardUses int32       `json:"card_uses,omitempty"`
	Draft    *promoDraft `json:"draft,omitempty"`
	Edit     *promoEdit  `json:"edit,omitempty"`
}

// promoEdit is the "only the checked sessions" edit of an existing code.
type promoEdit struct {
	CodeID   uuid.UUID   `json:"code_id"`
	Code     string      `json:"code"`
	Fixed    bool        `json:"fixed,omitempty"`
	Currency string      `json:"currency,omitempty"`
	Existing []uuid.UUID `json:"existing,omitempty"`
	promoPicker
}

func newPromoDialog(orgID uuid.UUID) promoDialog { return promoDialog{OrgID: orgID, Page: 1} }

// canPromo reports whether the person may manage promo codes: the owner, the
// manager and the platform operator hold promo.read/create/update/delete (the
// agent role holds none). The API still decides every call.
func canPromo(id *Identity) bool { return canViewSales(id) }

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadPromoState reads the dialog, or starts an empty one when there is none,
// it ran out (expired is then true, once) or it belongs to another
// organization.
func (b *Bot) loadPromoState(ctx context.Context, tg int64, orgID uuid.UUID) (st promoDialog, step string, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, promoDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: promo dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return newPromoDialog(orgID), "", false
	}
	if !found || st.OrgID != orgID {
		return newPromoDialog(orgID), "", expired
	}
	if st.Page < 1 {
		st.Page = 1
	}
	return st, step, false
}

func (b *Bot) savePromoState(ctx context.Context, tg int64, st promoDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, promoDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: promo dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leavePromo ends the dialog: whoever opens another screen is no longer
// answering a code question, so a text typed next is not taken for a code, a
// number or the delete word.
func (b *Bot) leavePromo(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, promoDialogKind); err != nil {
		b.logger.Warn("eventbot: promo dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── entry points ─────────────────────────────────────────────────────────────

// promoPager is paging.go's "‹ 2/5 ›" row with the position button kept inside
// the "pm:" prefix: a bare "noop" press would end the dialog like any press
// that leaves the promo screens.
func (b *Bot) promoPager(loc string, p Pager) []models.InlineKeyboardButton {
	row := PagerRow(p, b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil))
	for i := range row {
		if row[i].CallbackData == calNoop {
			row[i].CallbackData = "pm:noop"
		}
	}
	return row
}

// promoMenuRow is the main menu's button, for a role that may manage codes.
func (b *Bot) promoMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if !canPromo(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.promo.btn", nil), CallbackData: "pm:l"}}
}

// promoEventRow is the event card's button: the codes that work on the event.
func (b *Bot) promoEventRow(loc string, id *Identity, eventID uuid.UUID) []models.InlineKeyboardButton {
	if !canPromo(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.promo.btn", nil), CallbackData: "pm:l:" + eventID.String()}}
}

// promoCallback handles every "pm:<data>" press.
func (b *Bot) promoCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canPromo(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")

	switch kind {
	case "noop": // the position button of a pager
		return
	case "l", "n":
		var eventID *uuid.UUID
		if arg != "" {
			parsed, err := uuid.Parse(arg)
			if err != nil {
				return
			}
			eventID = &parsed
		}
		st := newPromoDialog(orgID)
		if eventID != nil {
			name, ok := b.promoEventName(ctx, chatID, &msgID, id, jwt, *eventID)
			if !ok {
				return
			}
			st.EventID, st.EventName = eventID, name
		}
		if kind == "n" {
			b.promoStart(ctx, chatID, &msgID, id, jwt, st)
			return
		}
		b.promoRenderList(ctx, chatID, &msgID, id, jwt, st, "")
		return
	case "v", "ps", "ac", "se", "sa", "so", "u", "up", "csv", "del":
		b.promoCodePress(ctx, chatID, msgID, from, id, jwt, kind, arg)
		return
	}

	st, step, expired := b.loadPromoState(ctx, from.ID, orgID)
	if expired {
		// The screen ran out while the person was away: say so once and show
		// the list, rather than act on a press made on stale rows.
		b.promoRenderList(ctx, chatID, &msgID, id, jwt, newPromoDialog(orgID), b.texts.T(loc, "bot.dialog_expired", nil)+"\n\n")
		return
	}
	switch kind {
	case "b":
		b.promoRenderList(ctx, chatID, &msgID, id, jwt, st, "")
	case "p":
		st.Page = ParsePage(arg)
		b.promoRenderList(ctx, chatID, &msgID, id, jwt, st, "")
	case "o":
		i, ok := ParseIndex(arg, len(st.IDs))
		if !ok {
			// The row is gone from the stored page (a stale message): show the
			// list as it is now instead of opening the wrong code.
			b.promoRenderList(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.promoShowCard(ctx, chatID, &msgID, id, jwt, st, st.IDs[i], "")
	case "c":
		b.promoCreatePress(ctx, chatID, &msgID, id, jwt, st, step, arg)
	case "k":
		b.promoPickPress(ctx, chatID, &msgID, id, jwt, st, step, arg)
	}
}

// promoCodePress handles the presses that name a code by its UUID.
func (b *Bot) promoCodePress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, kind, arg string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	idPart, rest, _ := strings.Cut(arg, ":") // pm:up:<code>:<page> keeps the page in rest
	codeID, err := uuid.Parse(idPart)
	if err != nil {
		return
	}
	st, _, _ := b.loadPromoState(ctx, from.ID, orgID)
	switch kind {
	case "v":
		b.promoShowCard(ctx, chatID, &msgID, id, jwt, st, codeID, "")
	case "ps", "ac":
		to, doneKey := promoStatusPaused, "bot.promo.paused_done"
		if kind == "ac" {
			to, doneKey = promoStatusActive, "bot.promo.activated_done"
		}
		it, err := b.arena.SetPromoStatus(ctx, jwt, orgID, codeID, to)
		if err != nil {
			b.promoError(ctx, chatID, &msgID, id, err)
			return
		}
		b.promoShowCard(ctx, chatID, &msgID, id, jwt, st, codeID, b.texts.T(loc, doneKey, map[string]any{"Code": Esc(it.Code)})+"\n\n")
	case "se":
		b.promoShowSessions(ctx, chatID, &msgID, id, jwt, st, codeID, "")
	case "sa":
		it, err := b.arena.SetPromoSessions(ctx, jwt, orgID, codeID, nil)
		if err != nil {
			b.promoError(ctx, chatID, &msgID, id, err)
			return
		}
		b.promoShowCard(ctx, chatID, &msgID, id, jwt, st, codeID, b.texts.T(loc, "bot.promo.sess_all_done", map[string]any{"Code": Esc(it.Code)})+"\n\n")
	case "so":
		b.promoEditStart(ctx, chatID, &msgID, id, jwt, st, codeID)
	case "u":
		b.promoShowUsage(ctx, chatID, &msgID, id, jwt, st, codeID, 1)
	case "up":
		b.promoShowUsage(ctx, chatID, &msgID, id, jwt, st, codeID, ParsePage(rest))
	case "csv":
		b.promoSendCSV(ctx, chatID, id, jwt, codeID)
	case "del":
		b.promoAskDelete(ctx, chatID, &msgID, from, id, jwt, st, codeID)
	}
}

// promoError answers a failed call of a code screen: a missing code (or one of
// another organization — the API answers both 404) is "not found".
func (b *Bot) promoError(ctx context.Context, chatID int64, editMsgID *int, id *Identity, err error) {
	b.ecError(ctx, chatID, editMsgID, id, err, "pm:b")
}

// promoEventName names the event a list or a new code is scoped to; an event
// of another organization, or a missing one, is "not found".
func (b *Bot) promoEventName(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, eventID uuid.UUID) (string, bool) {
	events, err := b.arena.ListEvents(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return "", false
	}
	for _, e := range events {
		if e.Id == eventID {
			return e.Name, true
		}
	}
	b.reply(ctx, chatID, msgID, b.texts.T(id.Locale(), "bot.ec.not_found", nil), b.backKeyboard(id.Locale(), "home"))
	return "", false
}

// ─── the list ─────────────────────────────────────────────────────────────────

// promoRenderList loads the codes (narrowed to the event, when the list has
// one), draws one page and stores the ids of the rows it shows.
func (b *Bot) promoRenderList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoDialog, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	all, err := b.arena.ListPromoCodes(ctx, jwt, orgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	if st.EventID != nil {
		sessions, err := b.arena.ListSessions(ctx, jwt, orgID, *st.EventID)
		if err != nil {
			b.ecError(ctx, chatID, editMsgID, id, err, "home")
			return
		}
		ids := make(map[uuid.UUID]bool, len(sessions))
		for _, s := range sessions {
			ids[s.Id] = true
		}
		all = promoEventFilter(all, ids)
	}
	total := int64(len(all))
	pages := PagesFor(total, listPageSize)
	if st.Page < 1 {
		st.Page = 1
	}
	if st.Page > pages {
		st.Page = pages
	}
	start := (st.Page - 1) * listPageSize
	end := start + listPageSize
	if end > len(all) {
		end = len(all)
	}
	pageItems := all[start:end]
	st.IDs = st.IDs[:0]
	for _, it := range pageItems {
		st.IDs = append(st.IDs, it.Id)
	}
	st.CardID, st.Draft, st.Edit = nil, nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoState(ctx, id.Link.TelegramUserID, st, pmStepList)

	scope := ""
	if st.EventID != nil {
		scope = "\n" + b.texts.T(loc, "bot.promo.scope_line", map[string]any{"Name": Esc(st.EventName)})
	}
	text := note + b.texts.T(loc, "bot.promo.list_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Scope": scope, "Total": total, "Page": st.Page, "Pages": pages,
		"Legend": b.texts.T(loc, "bot.promo.legend", nil),
	})
	now := time.Now()
	if len(pageItems) == 0 {
		key := "bot.promo.empty"
		if st.EventID != nil {
			key = "bot.promo.empty_event"
		}
		text += "\n\n" + b.texts.T(loc, key, nil)
	}
	for i, it := range pageItems {
		text += "\n\n" + b.promoEntry(loc, start+i+1, it, now)
	}

	var rows [][]models.InlineKeyboardButton
	for i, it := range pageItems {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: fmt.Sprintf("%d · %s", start+i+1, truncate(it.Code, 40)), CallbackData: ItemCallback("pm:o", i),
		}})
	}
	if nav := b.promoPager(loc, Pager{Prefix: "pm:p", Page: st.Page, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	newData := "pm:n"
	if st.EventID != nil {
		newData += ":" + st.EventID.String()
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.promo.new_btn", nil), CallbackData: newData}})
	if st.EventID != nil {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.promo.list_all_btn", nil), CallbackData: "pm:l"}})
	}
	last := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}}
	if st.EventID != nil {
		last = append([]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ec:o:" + st.EventID.String()}}, last...)
	}
	rows = append(rows, last)
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── the card ─────────────────────────────────────────────────────────────────

// promoShowCard reads the code afresh and draws its card with the actions its
// state allows. note is a line above (what was just done).
func (b *Bot) promoShowCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoDialog, codeID uuid.UUID, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	it, err := b.arena.GetPromoCode(ctx, jwt, orgID, codeID)
	if err != nil {
		b.promoError(ctx, chatID, editMsgID, id, err)
		return
	}
	lines, more := b.promoSessionLines(ctx, jwt, loc, orgID, it)
	text := note + b.promoCardText(loc, it, time.Now(), lines, more)

	st.CardID, st.CardCode, st.CardUses = &it.Id, it.Code, it.Uses
	st.Draft, st.Edit = nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoState(ctx, id.Link.TelegramUserID, st, pmStepCard)

	code := it.Id.String()
	toggle := models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.promo.pause_btn", nil), CallbackData: "pm:ps:" + code}
	if string(it.Status) == promoStatusPaused {
		toggle = models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.promo.activate_btn", nil), CallbackData: "pm:ac:" + code}
	}
	rows := [][]models.InlineKeyboardButton{
		{toggle, {Text: b.texts.T(loc, "bot.promo.sessions_btn", nil), CallbackData: "pm:se:" + code}},
		{
			{Text: b.texts.T(loc, "bot.promo.usage_btn", nil), CallbackData: "pm:u:" + code},
			{Text: b.texts.T(loc, "bot.promo.csv_btn", nil), CallbackData: "pm:csv:" + code},
		},
		{{Text: b.texts.T(loc, "bot.promo.delete_btn", nil), CallbackData: "pm:del:" + code}},
		{
			{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "pm:b"},
			{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
		},
	}
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// promoSessionLines names up to promoMaxSessionLines of the code's sessions
// ("Swan Lake · 15.10.2026 20:00"), one summary call each, and counts the
// rest. A session that cannot be read is skipped, not fatal.
func (b *Bot) promoSessionLines(ctx context.Context, jwt, loc string, orgID uuid.UUID, it openapi.PromoCodeItem) (lines []string, more int) {
	for i, sid := range it.AppliesToSessionIds {
		if i >= promoMaxSessionLines {
			more = len(it.AppliesToSessionIds) - i
			break
		}
		sum, err := b.arena.SessionSummary(ctx, jwt, orgID, sid)
		if err != nil {
			b.logger.Warn("eventbot: promo session summary failed", slog.String("session_id", sid.String()), slog.String("error", err.Error()))
			continue
		}
		tz := ""
		if sum.Session.VenueTimezone != nil {
			tz = *sum.Session.VenueTimezone
		}
		lines = append(lines, b.texts.T(loc, "bot.promo.card_sess_line", map[string]any{
			"Name": Esc(sum.Session.EventName), "When": Esc(FormatWhen(sum.Session.StartAt, tz)),
		}))
	}
	return lines, more
}

// ─── usage ────────────────────────────────────────────────────────────────────

// promoShowUsage lists the orders that used the code, five a page, newest
// first: order number, day, the buyer's name, the discount and the order's
// state — with the totals above. The file with the same rows is one press away.
func (b *Bot) promoShowUsage(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st promoDialog, codeID uuid.UUID, page int) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	it, err := b.arena.GetPromoCode(ctx, jwt, orgID, codeID)
	if err != nil {
		b.promoError(ctx, chatID, editMsgID, id, err)
		return
	}
	rows, err := b.arena.PromoRedemptions(ctx, jwt, orgID, codeID)
	if err != nil {
		b.promoError(ctx, chatID, editMsgID, id, err)
		return
	}
	pages := PagesFor(int64(len(rows)), listPageSize)
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * listPageSize
	end := start + listPageSize
	if end > len(rows) {
		end = len(rows)
	}
	var body strings.Builder
	if len(rows) == 0 {
		body.WriteString(b.texts.T(loc, "bot.promo.usage_empty", nil))
	}
	for _, r := range rows[start:end] {
		body.WriteString("\n" + b.promoUsageRow(loc, r))
	}
	st.CardID, st.CardCode, st.CardUses = &it.Id, it.Code, it.Uses
	st.Draft, st.Edit = nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoState(ctx, id.Link.TelegramUserID, st, pmStepUsage)

	text := b.texts.T(loc, "bot.promo.usage_title", map[string]any{
		"Code": Esc(it.Code), "Text": b.promoUsageText(loc, it), "Page": page, "Pages": pages,
	}) + "\n" + body.String()
	var kb [][]models.InlineKeyboardButton
	if nav := b.promoPager(loc, Pager{Prefix: "pm:up:" + it.Id.String(), Page: page, Pages: pages}); nav != nil {
		kb = append(kb, nav)
	}
	if len(rows) > 0 {
		kb = append(kb, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.promo.csv_btn", nil), CallbackData: "pm:csv:" + it.Id.String()}})
	}
	kb = append(kb, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "pm:v:" + it.Id.String()},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: kb})
}

// promoSendCSV hands the usage file of one code to Telegram as a document
// (EC-07). The file holds buyers' names and e-mails, so it goes only to the
// private chat of the person who asked and is never logged or kept.
func (b *Bot) promoSendCSV(ctx context.Context, chatID int64, id *Identity, jwt string, codeID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	it, err := b.arena.GetPromoCode(ctx, jwt, orgID, codeID)
	if err != nil {
		b.promoCSVFailed(ctx, chatID, loc, err)
		return
	}
	file, err := b.arena.PromoRedemptionsCSV(ctx, jwt, orgID, codeID, loc)
	if err != nil {
		b.promoCSVFailed(ctx, chatID, loc, err)
		return
	}
	if file.Rows == 0 {
		b.send(ctx, chatID, b.texts.T(loc, "bot.promo.csv_empty", nil), nil)
		return
	}
	// allow:timeformat: the export date in a chat caption, not a wire timestamp
	date := time.Now().UTC().Format("02.01.2006")
	_, err = b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: file.Name, Data: bytes.NewReader(file.Body)},
		Caption: b.texts.T(loc, "bot.ec.csv_caption", map[string]any{
			"Name": Esc(truncate(it.Code, 120)), "Scope": b.texts.T(loc, "bot.promo.csv_scope", nil), "Rows": file.Rows,
			"What": b.texts.T(loc, "bot.promo.csv_what", nil), "Date": date,
		}),
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		b.logger.Warn("eventbot: send promo export failed", slog.Int("bytes", len(file.Body)), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.csv_failed", nil), nil)
	}
}

// promoCSVFailed tells why the file did not arrive; nothing from inside it is
// logged.
func (b *Bot) promoCSVFailed(ctx context.Context, chatID int64, loc string, err error) {
	switch {
	case IsAPIError(err, http.StatusRequestEntityTooLarge) || errors.Is(err, errCSVTooLarge):
		b.send(ctx, chatID, b.texts.T(loc, "bot.promo.csv_too_big", nil), nil)
	case IsAPIError(err, http.StatusNotFound):
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.not_found", nil), nil)
	case IsAPIError(err, http.StatusForbidden):
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.no_rights", nil), nil)
	default:
		b.logger.Warn("eventbot: promo export failed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.ec.csv_failed", nil), nil)
	}
}

// ─── delete ───────────────────────────────────────────────────────────────────

// promoAskDelete asks for the delete word. Nothing is deleted by the press.
// The screen says how many times the code was used, because the code leaves
// the list and its usage can no longer be opened or exported from the bot —
// and its name stays taken, so pausing is offered as the gentler way.
func (b *Bot) promoAskDelete(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st promoDialog, codeID uuid.UUID) {
	loc := id.Locale()
	it, err := b.arena.GetPromoCode(ctx, jwt, id.Current.OrgID, codeID)
	if err != nil {
		b.promoError(ctx, chatID, editMsgID, id, err)
		return
	}
	st.CardID, st.CardCode, st.CardUses = &it.Id, it.Code, it.Uses
	st.Draft, st.Edit = nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.savePromoState(ctx, from.ID, st, pmStepDel)
	b.promoShowDelete(ctx, chatID, editMsgID, loc, st, "")
}

func (b *Bot) promoShowDelete(ctx context.Context, chatID int64, editMsgID *int, loc string, st promoDialog, note string) {
	code := ""
	if st.CardID != nil {
		code = st.CardID.String()
	}
	b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.promo.delete_ask", map[string]any{
		"Code": Esc(st.CardCode), "Count": st.CardUses, "Word": b.promoDeleteWord(loc),
	}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "pm:v:" + code}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

func (b *Bot) promoDeleteWord(loc string) string { return b.texts.T(loc, "bot.promo.delete_word", nil) }

// promoIsDeleteWord reports whether the typed text is the explicit command:
// the word of the person's language, or the English one in any language.
func (b *Bot) promoIsDeleteWord(loc, text string) bool {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "\"'«».!"))
	return strings.EqualFold(text, b.promoDeleteWord(loc)) || strings.EqualFold(text, "delete")
}

// ─── typed text ───────────────────────────────────────────────────────────────

// promoText takes a typed text while a promo screen waits for one: the code,
// a number, a currency, a date, or the delete word. It reports whether the
// text was taken. An expired dialog is reported once, like every dialog.
func (b *Bot) promoText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st promoDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, promoDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: promo dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.promo.btn", nil), CallbackData: "pm:l"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || (step != pmStepDel && !strings.HasPrefix(step, "c_")) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canPromo(id) {
		b.leavePromo(ctx, from.ID)
		return false
	}
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if step == pmStepDel {
		b.promoDeleteTyped(ctx, chatID, edit, from, id, jwt, st, text)
		return true
	}
	if st.Draft == nil {
		b.leavePromo(ctx, from.ID)
		return false
	}
	b.promoDraftInput(ctx, chatID, edit, id, jwt, st, text, "")
	return true
}

// promoDeleteTyped carries out (or refuses) the delete word.
func (b *Bot) promoDeleteTyped(ctx context.Context, chatID int64, edit *int, from *models.User, id *Identity, jwt string, st promoDialog, text string) {
	loc := id.Locale()
	if st.CardID == nil {
		b.leavePromo(ctx, from.ID)
		return
	}
	if !b.promoIsDeleteWord(loc, text) {
		b.promoShowDelete(ctx, chatID, edit, loc, st, b.texts.T(loc, "bot.promo.delete_wrong", map[string]any{"Word": b.promoDeleteWord(loc)})+"\n\n")
		return
	}
	// The word is right: the dialog ends first, so a message sent twice cannot
	// delete twice, then the API decides.
	b.leavePromo(ctx, from.ID)
	err := b.arena.DeletePromoCode(ctx, jwt, id.Current.OrgID, *st.CardID)
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.promo.btn", nil), CallbackData: "pm:l"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
	switch {
	case err == nil:
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.promo.deleted_done", map[string]any{"Code": Esc(st.CardCode)}), kb)
	case IsAPIError(err, http.StatusNotFound):
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ec.not_found", nil), kb)
	default:
		b.ecError(ctx, chatID, edit, id, err, "pm:b")
	}
}
