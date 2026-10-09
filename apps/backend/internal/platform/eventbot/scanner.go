package eventbot

import (
	"context"

	"github.com/go-telegram/bot/models"
)

// The ticket-scanner app the door staff use. Both stores redirect by the
// visitor's region, so the links carry no country or language segment.
const (
	ScannerAndroidURL = "https://play.google.com/store/apps/details?id=com.arenasoldout.macs"
	ScannerIOSURL     = "https://apps.apple.com/app/arenasoldout/id6770095694"
)

// scannerKeys are the texts of the scanner screen and its menu button.
var scannerKeys = []string{
	"bot.btn_scanner",
	"bot.scanner.text",
	"bot.scanner.android_btn",
	"bot.scanner.ios_btn",
}

func init() { MessageKeys = append(MessageKeys, scannerKeys...) }

// scannerRow is the menu row that opens the scanner screen.
func (b *Bot) scannerRow(loc string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_scanner", nil), CallbackData: "scanner"}}
}

// showScanner tells where to get the scanner app: one button per store, so
// the organizer can forward the screen to whoever stands at the door.
func (b *Bot) showScanner(ctx context.Context, chatID int64, editMsgID *int, from *models.User) {
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	loc := id.Locale()
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.scanner.text", nil), &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.scanner.android_btn", nil), URL: ScannerAndroidURL}},
			{{Text: b.texts.T(loc, "bot.scanner.ios_btn", nil), URL: ScannerIOSURL}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		},
	})
}
