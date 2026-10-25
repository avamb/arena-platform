package eventbot

// textfmt_miniapp.go — the «Править в редакторе» button (spec 44, RT-12a): a
// Telegram web_app button that opens the editor page of the web event center
// inside Telegram. It stands under the description preview (textfmt_dialog.go)
// and on the "Text in the letter" screen (textfmt_note.go).
//
// The link is made by arena-api (POST /v1/bot/web-login-codes with purpose
// "miniapp") and has the shape
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
// The button is built only when BOT_MINIAPP_EDITOR_ENABLED is on, in a private
// chat (a web_app button works nowhere else), for an event that exists (a
// description in the create wizard has none yet), and when arena-api gave a
// link Telegram accepts. In every other case the screen is exactly what it was
// before: a missing button is never an error for the organizer.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// tgButtonURLMax is the length Telegram accepts for the URL of an inline
// button (2048 bytes, the limit of every URL in the Bot API's reply markup).
// A link is 43 characters of code plus the address, far below it; the check
// keeps a misconfigured address from making Telegram refuse the whole message.
const tgButtonURLMax = 2048

// miniappRow is the row with the web_app button, or nil when there is none to
// show. chatID is the chat the screen goes to: in a private chat it is the
// Telegram account the code is asked for.
func (b *Bot) miniappRow(ctx context.Context, chatID int64, loc string, kind tfKind, eventID string) []models.InlineKeyboardButton {
	if !b.miniappEditor || chatID <= 0 || (kind != tfKindDescription && kind != tfKindEmailNote) {
		return nil
	}
	if _, err := uuid.Parse(eventID); err != nil {
		return nil
	}
	link, err := b.arena.MiniappLink(ctx, chatID, string(kind), eventID)
	if err != nil {
		// A platform superadmin, an unlinked account, an address that is not
		// set: the screen simply has no editor button.
		b.logger.Warn("eventbot: mini app link failed", slog.String("kind", string(kind)), slog.String("error", routeOnly(err)))
		return nil
	}
	if !miniappURLOK(link.URL) {
		b.logger.Warn("eventbot: mini app link refused by the bot", slog.Int("length", len(link.URL)))
		return nil
	}
	return []models.InlineKeyboardButton{{
		Text:   b.texts.T(loc, "bot.tf.edit_btn", nil),
		WebApp: &models.WebAppInfo{URL: link.URL},
	}}
}

// miniappURLOK reports whether Telegram will take url as the address of a
// web_app button: https only, and within the URL limit.
func miniappURLOK(url string) bool {
	return strings.HasPrefix(url, "https://") && len(url) <= tgButtonURLMax
}
