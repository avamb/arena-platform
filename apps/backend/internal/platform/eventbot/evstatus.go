package eventbot

// evstatus.go — the status buttons of the event card (EC-10, spec 35 §6.1):
// Publish (a draft), Take off sale, Archive and Delete. Which of them the card
// shows follows the API's own lifecycle (catalogdomain.IsValidEventTransition)
// and the person's role; the API still decides every press.
//
//   - Publish acts at once; the API's publish gate (a date, a priced category)
//     answers in words.
//   - Take off sale and Archive are one confirmation press, and the screen
//     says plainly that tickets already sold stay valid.
//   - Delete first asks the API what it would touch (GET .../delete-impact). An
//     event that has sold anything cannot be deleted — the screen says so and
//     offers the archive instead. Otherwise the person has to TYPE the delete
//     word; a button never deletes, and a wrong word deletes nothing. That
//     step lives in bot_dialogs under kind "evstatus" (dialogs.go), so a bot
//     restart between the question and the answer loses nothing.
//
// Callbacks are "ec:ev:<event id>:<act>" (ec_callbacks.go); the act is one of
// the evsAct* constants.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	catalogdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/catalog"
)

const (
	evsDialogKind = "evstatus"
	// evsStepType awaits the typed delete word.
	evsStepType = "type"

	evsActPublish     = "pub"
	evsActOff         = "off"
	evsActOffYes      = "offy"
	evsActArchive     = "arc"
	evsActArchiveYes  = "arcy"
	evsActDelete      = "del"
	evsCodeNeedDate   = "event.publish_requires_session"
	evsCodeNeedTier   = "event.publish_requires_priced_tier"
	evsCodeHasSales   = "event.has_paid_orders"
	evsCodeBadMove    = "event.invalid_transition"
	evsStatusDraft    = "draft"
	evsStatusPublish  = "published"
	evsStatusArchived = "archived"
)

// evsDialog is the delete dialog's state: the event the typed word is for.
type evsDialog struct {
	OrgID    uuid.UUID `json:"org_id"`
	EventID  uuid.UUID `json:"event_id"`
	Name     string    `json:"name"`
	Sessions int       `json:"sessions"`
	MsgID    int       `json:"msg_id"` // the prompt message; a wrong word edits it in place
}

// canChangeEventStatus reports whether the person may publish, take off sale,
// archive or delete an event: the owner, the manager (both hold event.publish
// and event.delete) and the platform operator. Same set as the sales screens.
func canChangeEventStatus(id *Identity) bool { return canViewSales(id) }

// eventStatusRows is the card's status buttons: the transitions the lifecycle
// allows from the current status, then Delete (the dry run decides on press).
// A role that may not change the status gets none.
func (b *Bot) eventStatusRows(loc string, id *Identity, status string, eventID uuid.UUID) [][]models.InlineKeyboardButton {
	if !canChangeEventStatus(id) {
		return nil
	}
	data := func(act string) string { return "ec:ev:" + eventID.String() + ":" + act }
	btn := func(key, act string) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: b.texts.T(loc, key, nil), CallbackData: data(act)}
	}
	var rows [][]models.InlineKeyboardButton
	var first []models.InlineKeyboardButton
	if catalogdomain.IsValidEventTransition(status, evsStatusPublish) {
		first = append(first, btn("bot.evs.publish_btn", evsActPublish))
	}
	if catalogdomain.IsValidEventTransition(status, evsStatusDraft) {
		first = append(first, btn("bot.evs.off_btn", evsActOff))
	}
	if catalogdomain.IsValidEventTransition(status, evsStatusArchived) {
		first = append(first, btn("bot.evs.archive_btn", evsActArchive))
	}
	if len(first) > 0 {
		rows = append(rows, first)
	}
	return append(rows, []models.InlineKeyboardButton{btn("bot.evs.delete_btn", evsActDelete)})
}

// ─── dialog storage ───────────────────────────────────────────────────────────

func (b *Bot) saveEvsDialog(ctx context.Context, tg int64, st evsDialog) error {
	return b.dialogs.Save(ctx, tg, &st.OrgID, evsDialogKind, evsStepType, st, dialogTTL)
}

// leaveEvStatus ends the delete dialog, if any. Any other screen, button or
// command does: a delete word typed after the person has gone elsewhere must
// never delete anything.
func (b *Bot) leaveEvStatus(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, evsDialogKind); err != nil {
		b.logger.Warn("eventbot: event status dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── the word ─────────────────────────────────────────────────────────────────

// evsDeleteWord is the word the person types to delete, in their language.
func (b *Bot) evsDeleteWord(loc string) string {
	return b.texts.T(loc, "bot.evs.delete_word", nil)
}

// evsIsDeleteWord reports whether the typed text is the explicit command: the
// word of the person's language, or the English one in any language.
func (b *Bot) evsIsDeleteWord(loc, text string) bool {
	text = strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "\"'«».!"))
	return strings.EqualFold(text, b.evsDeleteWord(loc)) || strings.EqualFold(text, "delete")
}

// ─── buttons ──────────────────────────────────────────────────────────────────

// evStatusCallback handles "ec:ev:<event>:<act>".
func (b *Bot) evStatusCallback(ctx context.Context, chatID int64, msgID int, from *models.User, eventID uuid.UUID, act string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canChangeEventStatus(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	ev, err := b.arena.GetEvent(ctx, jwt, eventID)
	if err != nil || ev.OrgId != orgID {
		if err == nil {
			err = &APIError{Status: http.StatusNotFound, Code: "event.not_found"}
		}
		b.ecError(ctx, chatID, &msgID, id, err, "el:b")
		return
	}
	name := Esc(ev.Name)
	card := "ec:o:" + eventID.String()

	switch act {
	case evsActPublish:
		b.evsApply(ctx, chatID, msgID, id, jwt, ev, evsStatusPublish, "bot.evs.published_done")
	case evsActOff:
		b.evsAsk(ctx, chatID, msgID, loc, eventID, b.texts.T(loc, "bot.evs.off_ask", map[string]any{"Name": name}), "bot.evs.off_yes_btn", evsActOffYes)
	case evsActOffYes:
		b.evsApply(ctx, chatID, msgID, id, jwt, ev, evsStatusDraft, "bot.evs.off_done")
	case evsActArchive:
		b.evsAsk(ctx, chatID, msgID, loc, eventID, b.texts.T(loc, "bot.evs.archive_ask", map[string]any{"Name": name}), "bot.evs.archive_yes_btn", evsActArchiveYes)
	case evsActArchiveYes:
		b.evsApply(ctx, chatID, msgID, id, jwt, ev, evsStatusArchived, "bot.evs.archived_done")
	case evsActDelete:
		b.evsAskDelete(ctx, chatID, msgID, from, id, jwt, ev)
	default:
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.error_generic", nil), b.cardKeyboard(loc, card))
	}
}

// cardKeyboard is the single "back to the card" button.
func (b *Bot) cardKeyboard(loc, card string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: card}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
}

// evsAsk is the one-press confirmation of taking off sale or archiving: the
// text says what happens (and that sold tickets stay valid), "yes" does it.
func (b *Bot) evsAsk(ctx context.Context, chatID int64, msgID int, loc string, eventID uuid.UUID, text, yesKey, yesAct string) {
	b.reply(ctx, chatID, &msgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, yesKey, nil), CallbackData: "ec:ev:" + eventID.String() + ":" + yesAct}},
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ec:o:" + eventID.String()}},
	}})
}

// evsApply moves the event to a status and shows the result. The API's
// refusals are answered in words; the card is one press away either way.
func (b *Bot) evsApply(ctx context.Context, chatID int64, msgID int, id *Identity, jwt string, ev openapi.EventItem, to, doneKey string) {
	loc := id.Locale()
	card := b.cardKeyboard(loc, "ec:o:"+ev.Id.String())
	err := b.arena.SetEventStatus(ctx, jwt, id.Current.OrgID, ev.Id, to)
	if err == nil {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, doneKey, map[string]any{"Name": Esc(ev.Name)}), card)
		return
	}
	key := ""
	switch APIErrorCode(err) {
	case evsCodeNeedDate:
		key = "bot.evs.need_session"
	case evsCodeNeedTier:
		key = "bot.evs.need_tier"
	case evsCodeBadMove:
		key = "bot.evs.not_allowed"
	}
	if key != "" {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, key, nil), card)
		return
	}
	b.ecError(ctx, chatID, &msgID, id, err, "ec:o:"+ev.Id.String())
}

// ─── delete ───────────────────────────────────────────────────────────────────

// evsAskDelete shows the consequences from the dry run. An event that has sold
// anything cannot be deleted: the screen says how much and offers the archive.
// Otherwise the typed-word step begins; nothing is deleted by this press.
func (b *Bot) evsAskDelete(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, ev openapi.EventItem) {
	loc := id.Locale()
	imp, err := b.arena.EventDeleteImpact(ctx, jwt, id.Current.OrgID, ev.Id)
	if err != nil {
		b.ecError(ctx, chatID, &msgID, id, err, "ec:o:"+ev.Id.String())
		return
	}
	if !imp.CanDelete {
		b.evsShowBlocked(ctx, chatID, &msgID, loc, ev, imp)
		return
	}
	st := evsDialog{OrgID: id.Current.OrgID, EventID: ev.Id, Name: ev.Name, Sessions: imp.Sessions, MsgID: msgID}
	if err := b.saveEvsDialog(ctx, from.ID, st); err != nil {
		b.logger.Error("eventbot: event status dialog save failed", slog.String("error", err.Error()))
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.error_generic", nil), b.cardKeyboard(loc, "ec:o:"+ev.Id.String()))
		return
	}
	b.evsShowPrompt(ctx, chatID, &msgID, loc, st, "")
}

// evsShowPrompt is the typed-word question; note is an optional line above it.
func (b *Bot) evsShowPrompt(ctx context.Context, chatID int64, editMsgID *int, loc string, st evsDialog, note string) {
	text := note + b.texts.T(loc, "bot.evs.delete_ask", map[string]any{
		"Name": Esc(st.Name), "N": st.Sessions, "Word": b.evsDeleteWord(loc),
	})
	b.reply(ctx, chatID, editMsgID, text, b.cardKeyboard(loc, "ec:o:"+st.EventID.String()))
}

// evsShowBlocked says that the event cannot be deleted and offers the archive
// (when the lifecycle allows it) instead.
func (b *Bot) evsShowBlocked(ctx context.Context, chatID int64, editMsgID *int, loc string, ev openapi.EventItem, imp openapi.EventDeleteImpact) {
	key := "bot.evs.delete_blocked_arch"
	var rows [][]models.InlineKeyboardButton
	if imp.CanArchive {
		key = "bot.evs.delete_blocked"
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: b.texts.T(loc, "bot.evs.archive_instead_btn", nil), CallbackData: "ec:ev:" + ev.Id.String() + ":" + evsActArchive,
		}})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ec:o:" + ev.Id.String()}},
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, map[string]any{
		"Name": Esc(ev.Name), "Orders": imp.PaidOrders, "Tickets": imp.Tickets,
	}), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── typed text ───────────────────────────────────────────────────────────────

// evStatusText takes the text typed while the delete word is awaited. It
// reports whether the text was taken. A dialog that ran out is reported once,
// like every dialog (spec 35 §4.1); nothing is deleted then.
func (b *Bot) evStatusText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st evsDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, evsDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: event status dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.btn_events", nil), CallbackData: "el:new"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || step != evsStepType {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canChangeEventStatus(id) {
		b.leaveEvStatus(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if !b.evsIsDeleteWord(loc, text) {
		b.evsShowPrompt(ctx, chatID, edit, loc, st, b.texts.T(loc, "bot.evs.delete_wrong", map[string]any{"Word": b.evsDeleteWord(loc)})+"\n\n")
		return true
	}
	// The word is right: the dialog ends first, so a message sent twice cannot
	// delete twice, then the API decides (it re-checks that nothing was sold).
	b.leaveEvStatus(ctx, from.ID)
	card := b.cardKeyboard(loc, "ec:o:"+st.EventID.String())
	err = b.arena.DeleteEvent(ctx, jwt, st.OrgID, st.EventID)
	switch {
	case err == nil:
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.evs.deleted_done", map[string]any{"Name": Esc(st.Name)}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.btn_events", nil), CallbackData: "el:new"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
	case APIErrorCode(err) == evsCodeHasSales:
		// Something was sold between the question and the word.
		ev, gErr := b.arena.GetEvent(ctx, jwt, st.EventID)
		imp, iErr := b.arena.EventDeleteImpact(ctx, jwt, st.OrgID, st.EventID)
		if gErr != nil || iErr != nil {
			b.ecError(ctx, chatID, edit, id, err, "ec:o:"+st.EventID.String())
			return true
		}
		b.evsShowBlocked(ctx, chatID, edit, loc, ev, imp)
	case IsAPIError(err, http.StatusNotFound):
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.ec.not_found", nil), card)
	default:
		b.ecError(ctx, chatID, edit, id, err, "ec:o:"+st.EventID.String())
	}
	return true
}
