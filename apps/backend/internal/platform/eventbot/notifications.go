package eventbot

// notifications.go — the "Notifications" screen (EC-08, spec 35 §5.9, owner
// decision 4 of §1): sales and refund notifications are not sent by this bot
// but by the separate sales bot, whose group subscriptions the owners manage
// there. This screen only explains that and links to it; it has no data and
// calls no API.

import (
	"context"

	"github.com/go-telegram/bot/models"
)

// SalesBotUsername is the Telegram username (without "@") of the bot that
// sends sales and refund notifications — the one SALES_TELEGRAM_BOT_TOKEN
// belongs to (internal/platform/salesnotify). The repo carries only the
// token, never the username, so it is written here once; if the sales bot is
// ever renamed this constant is the only place to change.
const SalesBotUsername = "ArenaSoldOutSalesBot"

// salesBotURL is the t.me link of the sales bot.
func salesBotURL() string { return "https://t.me/" + SalesBotUsername }

func (b *Bot) showNotifications(ctx context.Context, chatID int64, editMsgID *int, id *Identity) {
	loc := id.Locale()
	key := "bot.ec.notif_manager"
	if isOwner(id) || id.Superadmin {
		key = "bot.ec.notif_owner"
	}
	text := b.texts.T(loc, key, map[string]any{"Bot": "@" + SalesBotUsername})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ec.notif_open_btn", map[string]any{"Bot": "@" + SalesBotUsername}), URL: salesBotURL()}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}
