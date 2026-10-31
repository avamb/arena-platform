package eventbot

// textfmt_miniapp.go — the «Править в редакторе» button (spec 44, RT-12a): a
// button that opens the editor page of the web event center inside Telegram.
// It stands under the description preview (textfmt_dialog.go) and on the
// "Text in the letter" screen (textfmt_note.go).
//
// The code is drawn LAZILY, when the organizer presses the button, never when
// a screen is drawn. A screen that is redrawn ("Once more", "Back"), or left
// open for an hour, would otherwise write a code, an audit row and a revoke
// each time, and leave a button in the history that dies after 5 minutes.
// So the screen carries an ordinary callback button:
//
//	tf:m              the preview: the kind and the event are in the dialog
//	tf:m:<event id>   the letter screen (kind email_note), 41 bytes
//	tf:md:<event id>  the description question of the edit wizard (kind
//	                  description of an EXISTING event), 42 bytes
//
// The press asks arena-api for a code (POST /v1/bot/web-login-codes with
// purpose "miniapp") and replaces that same button, in the message that was
// pressed, with a web_app button that holds the fresh link
//
//	<EVENT_CENTER_PUBLIC_URL>/tg/edit#code=<code>&kind=<kind>&event=<event id>
//
//	code   a one-time sign-in code, 43 characters, good for 5 minutes. The page
//	       exchanges it at POST /v1/auth/web/link for a 30-minute session and
//	       clears the fragment. It is after "#", so it never reaches a server
//	       log (nginx.conf.template).
//	kind   "description" or "email_note": the text the page opens.
//	event  the id of the event whose text it is.
//
// A line "the link works for 5 minutes" goes under the text of the message.
// When the 5 minutes are over, the organizer opens the screen again and presses
// the callback button again: that draws a new code.
//
// The button is built only when BOT_MINIAPP_EDITOR_ENABLED is on, in a private
// chat (a web_app button works nowhere else), and for an event that exists (a
// description in the create wizard has none yet). In every other case the
// screen is exactly what it was before.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode/utf16"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// tgButtonURLMax is the length Telegram accepts for the URL of an inline
// button (2048 bytes, the limit of every URL in the Bot API's reply markup).
// A link is 43 characters of code plus the address, far below it; the check
// keeps a misconfigured address from making Telegram refuse the whole message.
const tgButtonURLMax = 2048

// miniappCallback is the data of the press on the preview's button.
const miniappCallback = "tf:m"

// miniappDescriptionCallback is the prefix of the press on the description
// question of an edited event: "tf:md:<event id>" (42 bytes, Telegram's cap is 64).
const miniappDescriptionCallback = "tf:md:"

// miniappRow is the row with the «Править в редакторе» callback button, or nil
// when there is none to show. It asks arena-api for nothing: the code is drawn
// when the button is pressed (tfMiniappCallback). chatID is the chat the screen
// goes to; eventID is the event whose text it is (for the preview the data
// stays "tf:m" and the dialog knows the event). A platform superadmin gets no
// button: arena-api refuses a sign-in code to that role (WL-1) and they edit
// texts in the web event center or the admin console.
func (b *Bot) miniappRow(chatID int64, id *Identity, loc string, kind tfKind, eventID string) []models.InlineKeyboardButton {
	data := miniappCallback
	if kind == tfKindEmailNote {
		data += ":" + eventID
	}
	return b.miniappRowData(chatID, id != nil && id.Superadmin, loc, kind, eventID, data)
}

// miniappDescriptionRow is the same button for the description QUESTION of the
// edit wizard (an existing event; the create wizard has no event yet, so no
// button). Its data names the event, because the wizard's screen is not a
// preview with a dialog behind it. The wizard itself is a pure state machine,
// so the bot adds the row when it draws the screen (wizardRender).
func (b *Bot) miniappDescriptionRow(chatID int64, superadmin bool, loc, eventID string) []models.InlineKeyboardButton {
	return b.miniappRowData(chatID, superadmin, loc, tfKindDescription, eventID, miniappDescriptionCallback+eventID)
}

// miniappRowData is the gates of every «Править в редакторе» button (the flag,
// a private chat, a known kind, a valid event id, no platform superadmin) and
// the button itself with the given callback data.
func (b *Bot) miniappRowData(chatID int64, superadmin bool, loc string, kind tfKind, eventID, data string) []models.InlineKeyboardButton {
	if !b.miniappEditor || chatID <= 0 || (kind != tfKindDescription && kind != tfKindEmailNote) {
		return nil
	}
	if superadmin {
		return nil
	}
	if _, err := uuid.Parse(eventID); err != nil {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.tf.edit_btn", nil), CallbackData: data}}
}

// tfMiniappCallback handles the press of the button: "m" with no more is the
// preview, "m:<event id>" the letter screen, "md:<event id>" the description
// question of the edit wizard. It reports whether the press was one of them.
func (b *Bot) tfMiniappCallback(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt string, parts []string) bool {
	if parts[0] != "m" && parts[0] != "md" {
		return false
	}
	loc := id.Locale()
	data := miniappCallback
	var kind tfKind
	var eventID string
	var pdlg tfDialog // the dialog of the preview (len(parts) == 1)
	question := parts[0] == "md"
	if question && len(parts) != 2 {
		return true // a button this version never draws
	}
	if !question && len(parts) == 1 {
		dlg, step, found, _ := b.tfLoad(ctx, from.ID)
		if !found || step != tfStepPreview || dlg.OrgID != id.Current.OrgID.String() {
			// An older preview, or the 30 minutes ran out: the person types again.
			b.clearTextFmtDialog(ctx, from.ID)
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.tf.expired", nil), b.backKeyboard(loc, "home"))
			return true
		}
		kind, eventID, pdlg = dlg.kind(), dlg.EventID, dlg
	} else {
		if !canViewSales(id) { // the same people who may change event texts
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
			return true
		}
		kind, eventID = tfKindEmailNote, parts[1]
		data += ":" + parts[1]
		if question {
			kind, data = tfKindDescription, miniappDescriptionCallback+parts[1]
		}
	}
	if id.Superadmin {
		// A screen drawn before the person got the role, or an old button: say
		// where to edit instead of a generic failure.
		b.send(ctx, chatID, b.texts.T(loc, "bot.tf.edit_superadmin", nil), nil)
		return true
	}
	if len(b.miniappRowData(chatID, false, loc, kind, eventID, data)) == 0 {
		// The flag was turned off after the screen was drawn, or the press is
		// not one the screen can make.
		b.send(ctx, chatID, b.texts.T(loc, "bot.tf.edit_failed", nil), nil)
		return true
	}
	// The editor was opened from this screen before and may have saved since: say
	// so first (RT-12b), never open it again over a mark that has not been told.
	// The press is not carried out; the organizer presses the button once more.
	// The description question has no dialog behind it: the wizard's draft is
	// brought up to the saved text when the organizer goes on (wizard_editor_sync.go).
	if question {
		// nothing to ask
	} else if len(parts) == 1 {
		if pdlg.Editor && pdlg.Seen != "" && b.tfEditorSavedPreview(ctx, chatID, msgID, from, id, jwt, pdlg) {
			return true
		}
	} else if eid, perr := uuid.Parse(eventID); perr == nil && b.tfEditorSavedNote(ctx, chatID, msgID, from, id, jwt, eid) {
		return true
	}
	link, err := b.arena.MiniappLink(ctx, chatID, string(kind), eventID)
	if err != nil {
		// A platform superadmin, an unlinked account, an address that is not set.
		b.logger.Warn("eventbot: mini app link failed", slog.String("kind", string(kind)), slog.String("error", routeOnly(err)))
		b.send(ctx, chatID, b.texts.T(loc, miniappLinkErrorKey(err), nil), nil)
		return true
	}
	if !miniappURLOK(link.URL) {
		b.logger.Warn("eventbot: mini app link refused by the bot", slog.Int("length", len(link.URL)))
		b.send(ctx, chatID, b.texts.T(loc, "bot.tf.edit_failed", nil), nil)
		return true
	}
	// What the text is NOW, before the editor can be opened: the next action of the
	// organizer compares it with what the event holds then (RT-12b).
	if !question {
		b.tfEditorMark(ctx, from, id, jwt, kind, eventID, len(parts) == 1)
	}
	b.miniappOpen(ctx, chatID, msgID, loc, data, link.URL)
	return true
}

// miniappLinkErrorKey turns a refusal of POST /v1/bot/web-login-codes into the
// text the person sees. The platform superadmin is the one refusal that needs
// its own words: retrying never helps, and the text says where to edit.
func miniappLinkErrorKey(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == "bot.web_login_superadmin" {
		return "bot.tf.edit_superadmin"
	}
	return "bot.tf.edit_failed"
}

// miniappOpen puts the link into the pressed message: the button with data
// becomes a web_app button, the other rows stay, and the note about the five
// minutes goes under the text. When the pressed message cannot be rebuilt (its
// keyboard is not in the update), the link comes as a message of its own.
func (b *Bot) miniappOpen(ctx context.Context, chatID int64, msgID int, loc, data, url string) {
	b.miniappOpenLabeled(ctx, chatID, msgID, loc, data, url, "bot.tf.edit_btn")
}

// miniappOpenLabeled is miniappOpen for a button with its own label key (used
// when the pressed message cannot be rebuilt and the button comes alone).
func (b *Bot) miniappOpenLabeled(ctx context.Context, chatID int64, msgID int, loc, data, url, btnKey string) {
	note := b.texts.T(loc, "bot.tf.edit_note", nil)
	var orig *models.Message
	if a := actionIn(ctx, chatID); a != nil && a.pressed == msgID {
		orig = a.orig
	}
	kb, ok := withWebAppButton(orig, data, url)
	if !ok {
		own := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: b.texts.T(loc, btnKey, nil), WebApp: &models.WebAppInfo{URL: url}},
		}}}
		b.send(ctx, chatID, note, own)
		return
	}
	text := orig.Text + "\n\n" + note
	if orig.Text != "" && len(utf16.Encode([]rune(text))) <= tgTextLimit {
		_, err := b.tg.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:             chatID,
			MessageID:          msgID,
			Text:               text,
			Entities:           orig.Entities,
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
			ReplyMarkup:        kb,
		})
		if err == nil || strings.Contains(err.Error(), "message is not modified") {
			return
		}
		b.logger.Debug("eventbot: mini app note under the text failed", slog.String("error", err.Error()))
	}
	// A text too long for one more line, or one Telegram would not take back:
	// the keyboard alone changes, the note is its own short message (it has no
	// buttons, so it takes no keyboard away).
	if _, err := b.tg.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID: chatID, MessageID: msgID, ReplyMarkup: kb,
	}); err != nil && !strings.Contains(err.Error(), "message is not modified") {
		b.logger.Warn("eventbot: mini app button not placed", slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.tf.edit_failed", nil), nil)
		return
	}
	b.send(ctx, chatID, note, nil)
}

// withWebAppButton is the keyboard of msg with the callback button data made a
// web_app button of the same label that opens url. ok is false when msg has no
// such button.
func withWebAppButton(msg *models.Message, data, url string) (*models.InlineKeyboardMarkup, bool) {
	if msg == nil || msg.ReplyMarkup == nil {
		return nil, false
	}
	found := false
	rows := make([][]models.InlineKeyboardButton, 0, len(msg.ReplyMarkup.InlineKeyboard))
	for _, row := range msg.ReplyMarkup.InlineKeyboard {
		out := make([]models.InlineKeyboardButton, len(row))
		copy(out, row)
		for i := range out {
			if out[i].CallbackData == data {
				out[i] = models.InlineKeyboardButton{Text: out[i].Text, WebApp: &models.WebAppInfo{URL: url}}
				found = true
			}
		}
		rows = append(rows, out)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}, found
}

// miniappURLOK reports whether Telegram will take url as the address of a
// web_app button: https only, and within the URL limit.
func miniappURLOK(url string) bool {
	return strings.HasPrefix(url, "https://") && len(url) <= tgButtonURLMax
}
