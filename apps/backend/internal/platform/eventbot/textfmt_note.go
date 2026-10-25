package eventbot

// textfmt_note.go — "Text in the letter" on the event card (spec 44, RT-06).
// The organizer's own text of the buyer letters (the ticket letter and the
// letter about a moved or cancelled date): the screen shows the current text,
// "Change" runs the same type-format-preview loop as the description
// (textfmt_dialog.go, kind email_note) and the save is the event PATCH with
// email_note and email_note_format "art"; "Delete the text" asks first;
// "Test letter to me" queues the sample letter the API already knows
// (POST .../email-note/test).
//
// The letter adds its own greeting and the date. The bot says so plainly and
// has no substitutions ({name}, {date}...): they are not part of Arena.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// eventOf finds one event of the organization in its list (the list carries
// the letter text; the single-event route does not).
func (b *Bot) eventOf(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (*openapi.EventItem, error) {
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		return nil, err
	}
	for i := range events {
		if events[i].Id == eventID {
			return &events[i], nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Code: "event.not_found", Message: "event not found"}
}

// emailNoteART is the stored letter text of an event as ART ("" when none). A
// text kept in the old notation is converted the way the letter reads it.
func emailNoteART(ev openapi.EventItem) string {
	if ev.EmailNote == nil || *ev.EmailNote == "" {
		return ""
	}
	if ev.EmailNoteFormat != nil && *ev.EmailNoteFormat == richtext.FormatART {
		return *ev.EmailNote
	}
	return richtext.FromLegacy(*ev.EmailNote, richtext.ProfileEmailNote)
}

// showEmailNote draws the "Text in the letter" screen. prefix is a line of the
// action that led here ("Done, ...").
func (b *Bot) showEmailNote(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID uuid.UUID, prefix string) {
	loc := id.Locale()
	ev, err := b.eventOf(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "el:b")
		return
	}
	art := emailNoteART(*ev)
	text := prefix + b.texts.T(loc, "bot.tf.note_title", map[string]any{"Name": Esc(ev.Name)}) + "\n\n" + b.texts.T(loc, "bot.tf.note_about", nil) + "\n\n"
	if art == "" {
		text += b.texts.T(loc, "bot.tf.note_none", nil)
	} else {
		text += b.texts.T(loc, "bot.tf.note_now", map[string]any{"Text": richtext.ToTelegramHTML(art)})
	}
	t := func(key string) string { return b.texts.T(loc, key, nil) }
	ids := eventID.String()
	editKey := "bot.tf.note_add_btn"
	rows := [][]models.InlineKeyboardButton{}
	if art != "" {
		editKey = "bot.tf.note_edit_btn"
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: t(editKey), CallbackData: "tf:e:" + ids}})
	if art != "" {
		rows = append(rows,
			[]models.InlineKeyboardButton{{Text: t("bot.tf.note_test_btn"), CallbackData: "tf:t:" + ids}},
			[]models.InlineKeyboardButton{{Text: t("bot.tf.note_delete_btn"), CallbackData: "tf:d:" + ids}})
	}
	// The editor of the Telegram mini app (RT-12a), behind its flag.
	if edit := b.miniappRow(chatID, loc, tfKindEmailNote, ids); edit != nil {
		rows = append(rows, edit)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + t("bot.btn_back"), CallbackData: "event:" + ids + ":1"},
		{Text: t("bot.btn_home"), CallbackData: "home"},
	})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// tfNoteCallback handles the buttons of the screen: n (open), e (change), d
// (delete?), dy (delete), t (test letter). It reports whether the press was
// one of them.
func (b *Bot) tfNoteCallback(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, parts []string) bool {
	switch parts[0] {
	case "n", "e", "d", "dy", "t":
	default:
		return false
	}
	loc := id.Locale()
	if len(parts) < 2 {
		return true
	}
	eventID, err := uuid.Parse(parts[1])
	if err != nil {
		return true
	}
	if !canViewSales(id) { // the owner, the manager and the operator may change event texts
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return true
	}
	// The mini app editor saved a text since this screen was drawn (RT-12b): say so
	// once and draw the saved text, whatever was pressed.
	if b.tfEditorSavedNote(ctx, chatID, msgID, from, id, jwt, eventID) {
		return true
	}
	orgID := id.Current.OrgID
	t := func(key string, data map[string]any) string { return b.texts.T(loc, key, data) }
	switch parts[0] {
	case "n":
		b.clearTextFmtDialog(ctx, from.ID)
		b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, "")
	case "e":
		dlg := tfDialog{Kind: string(tfKindEmailNote), EventID: eventID.String()}
		if err := b.tfSave(ctx, from.ID, orgID, tfStepAwait, dlg); err != nil {
			b.logger.Error("eventbot: text formatting dialog save failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
			b.reply(ctx, chatID, &msgID, t("bot.error_generic", nil), b.backKeyboard(loc, "home"))
			return true
		}
		p := richtext.ProfileEmailNote
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: t("bot.tf.cancel_btn", nil), CallbackData: "tf:c"}},
		}}
		b.reply(ctx, chatID, &msgID, t("bot.tf.note_ask", map[string]any{"Limit": p.MaxVisible, "Max": p.MaxLinks}), kb)
	case "d":
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: t("bot.tf.note_delete_yes_btn", nil), CallbackData: "tf:dy:" + eventID.String()},
			{Text: t("bot.tf.note_delete_no_btn", nil), CallbackData: "tf:n:" + eventID.String()},
		}}}
		b.reply(ctx, chatID, &msgID, t("bot.tf.note_delete_ask", nil), kb)
	case "dy":
		if err := b.arena.SetEmailNote(ctx, jwt, orgID, eventID, ""); err != nil {
			b.noteError(ctx, chatID, &msgID, id, err, "tf:n:"+eventID.String())
			return true
		}
		b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, t("bot.tf.note_deleted", nil)+"\n\n")
	case "t":
		out, err := b.arena.SendEmailNoteTest(ctx, jwt, orgID, eventID, loc)
		switch {
		case err == nil:
			b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, t("bot.tf.note_test_sent", map[string]any{"Email": Esc(out.To)})+"\n\n")
		case APIErrorCode(err) == "event.email_note_empty":
			b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, t("bot.tf.note_test_empty", nil)+"\n\n")
		case IsAPIError(err, http.StatusTooManyRequests):
			b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, t("bot.tf.note_test_limit", nil)+"\n\n")
		default:
			b.noteError(ctx, chatID, &msgID, id, err, "tf:n:"+eventID.String())
		}
	}
	return true
}

// noteError answers a failed call of the screen: no rights, not found, or a
// logged generic error with a way back.
func (b *Bot) noteError(ctx context.Context, chatID int64, editMsgID *int, id *Identity, err error, back string) {
	var api *APIError
	if errors.As(err, &api) && (api.Status == http.StatusForbidden || api.Status == http.StatusNotFound) {
		b.ecError(ctx, chatID, editMsgID, id, err, back)
		return
	}
	b.logger.Error("eventbot: letter text call failed", slog.String("error", routeOnly(err)))
	b.reply(ctx, chatID, editMsgID, b.texts.T(id.Locale(), "bot.tf.note_save_failed", nil), b.backKeyboard(id.Locale(), back))
}

// tfCommitNote stores the letter text the organizer chose. A refusal of the
// server is answered in the bot's words and the preview comes back, so the
// organizer can press again or change the text.
func (b *Bot) tfCommitNote(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, dlg tfDialog, art string) {
	loc := id.Locale()
	eventID, err := uuid.Parse(dlg.EventID)
	if err != nil {
		b.clearTextFmtDialog(ctx, from.ID)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.tf.expired", nil), b.backKeyboard(loc, "home"))
		return
	}
	// The filter once more: what is sent is what the server will accept.
	clean, nerr := richtext.Normalize(art, richtext.FormatART, richtext.ProfileEmailNote)
	if nerr != nil || clean == "" {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.tf.err_generic", nil), nil)
		b.tfShowPreview(ctx, chatID, nil, loc, dlg)
		return
	}
	if err := b.arena.SetEmailNote(ctx, jwt, id.Current.OrgID, eventID, clean); err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Status == http.StatusUnprocessableEntity {
			ref := tfRefusalForReason(api.Reason, tfKindEmailNote)
			b.reply(ctx, chatID, &msgID, b.tfRefusalText(loc, &ref), nil)
		} else {
			b.logger.Error("eventbot: letter text save failed", slog.String("error", routeOnly(err)))
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.tf.note_save_failed", nil), nil)
		}
		b.tfShowPreview(ctx, chatID, nil, loc, dlg)
		return
	}
	b.clearTextFmtDialog(ctx, from.ID)
	b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, b.texts.T(loc, "bot.tf.note_saved", nil)+"\n\n")
}

// tfCancelledNote answers "Cancel" in the letter text dialog: back to the
// screen, nothing changed.
func (b *Bot) tfCancelledNote(ctx context.Context, chatID int64, msgID int, from *models.User, dlg tfDialog) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	eventID, err := uuid.Parse(dlg.EventID)
	if err != nil {
		b.showHome(ctx, chatID, &msgID, from, "")
		return
	}
	b.showEmailNote(ctx, chatID, &msgID, id, jwt, eventID, b.texts.T(id.Locale(), "bot.tf.cancelled", nil)+"\n\n")
}
