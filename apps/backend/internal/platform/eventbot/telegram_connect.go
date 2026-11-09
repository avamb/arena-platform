package eventbot

// telegram_connect.go — "Connect Telegram" (acceptance run 2026-11-09,
// finding F-007).
//
// A person who already works in the web event center opens
// t.me/<bot>?start=con_<code> (a button or a QR code on the web page). The
// bot asks "Connect this Telegram to <organization>?" and links the account
// only on the press of "Connect" (con:ok:<code>), then opens that
// organization's menu. Nothing is linked by opening the link alone: a photo
// of somebody's QR code cannot link a stranger's Telegram without a press in
// that Telegram, and the screen names the account (its e-mail masked).
//
// A stranger's /start (welcome screen) offers the same way in: "I already
// have an organization" (con:how) explains where the button is, next to
// "Apply to become an organizer".
//
// Presses: con:ok:<code> (the code is 43 characters, the data 50 bytes),
// con:no, con:how. They need no dialog: the code travels in the button.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot/models"
)

// connectStartPrefix starts the /start payload of a "Connect Telegram" link
// (hbot.TelegramConnectStartPrefix on the server).
const connectStartPrefix = "con_"

// TelegramConnectPreview is what POST /v1/bot/telegram-connect/preview answers.
type TelegramConnectPreview struct {
	OrgID       string `json:"org_id"`
	OrgName     string `json:"org_name"`
	EmailMasked string `json:"email_masked"`
	State       string `json:"state"`
}

// TelegramConnectAccepted is what POST /v1/bot/telegram-connect/accept answers.
type TelegramConnectAccepted struct {
	OrgID         string `json:"org_id"`
	OrgName       string `json:"org_name"`
	UserID        string `json:"user_id"`
	Role          string `json:"role"`
	Locale        string `json:"locale"`
	AlreadyLinked bool   `json:"already_linked"`
}

type telegramConnectBody struct {
	Code             string `json:"code"`
	TelegramUserID   int64  `json:"telegram_user_id"`
	TelegramUsername string `json:"telegram_username,omitempty"`
	Locale           string `json:"locale,omitempty"`
}

// TelegramConnectPreview reads what a connect link would connect; it uses
// nothing.
func (c *ArenaClient) TelegramConnectPreview(ctx context.Context, code string, telegramUserID int64) (TelegramConnectPreview, error) {
	var out TelegramConnectPreview
	err := c.do(ctx, http.MethodPost, "/v1/bot/telegram-connect/preview", c.serviceToken,
		telegramConnectBody{Code: code, TelegramUserID: telegramUserID}, &out)
	return out, err
}

// TelegramConnectAccept links the Telegram account through a connect link.
func (c *ArenaClient) TelegramConnectAccept(ctx context.Context, code string, from *models.User) (TelegramConnectAccepted, error) {
	var out TelegramConnectAccepted
	err := c.do(ctx, http.MethodPost, "/v1/bot/telegram-connect/accept", c.serviceToken, telegramConnectBody{
		Code: code, TelegramUserID: from.ID, TelegramUsername: from.Username, Locale: NormalizeLocale(from.LanguageCode),
	}, &out)
	return out, err
}

// connectLocale is the language of the connect screens: the linked person's
// own, else the Telegram client's.
func (b *Bot) connectLocale(ctx context.Context, from *models.User) string {
	if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
		return id.Locale()
	}
	return NormalizeLocale(from.LanguageCode)
}

// startConnect answers /start con_<code>: the confirmation screen.
func (b *Bot) startConnect(ctx context.Context, chatID int64, from *models.User, code string) {
	loc := b.connectLocale(ctx, from)
	if chatID != from.ID {
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.private_only", nil), nil)
		return
	}
	code = strings.TrimSpace(code)
	if code == "" || len("con:ok:"+code) > 64 {
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.invalid", nil), nil)
		return
	}
	p, err := b.arena.TelegramConnectPreview(ctx, code, from.ID)
	if err != nil {
		b.connectFailed(ctx, chatID, loc, err)
		return
	}
	data := map[string]any{"Org": Esc(p.OrgName), "Email": Esc(p.EmailMasked)}
	switch p.State {
	case "other_user":
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.other_user", nil), nil)
		return
	case "same_user":
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.confirm_same", data), inline(
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.con.open_btn", data), "con:ok:"+code)},
			[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.con.no_btn", nil), "con:no")},
		))
		return
	}
	b.send(ctx, chatID, b.texts.T(loc, "bot.con.confirm", data), inline(
		[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.con.yes_btn", nil), "con:ok:"+code)},
		[]models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.con.no_btn", nil), "con:no")},
	))
}

// connectFailed explains a refused or failed connect call.
func (b *Bot) connectFailed(ctx context.Context, chatID int64, loc string, err error) {
	switch {
	case IsAPIError(err, http.StatusNotFound):
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.invalid", nil), nil)
	case IsAPIError(err, http.StatusConflict):
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.other_user", nil), nil)
	default:
		b.logger.Error("eventbot: telegram connect failed", slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.failed", nil), nil)
	}
}

// connectCallback handles every "con:*" press.
func (b *Bot) connectCallback(ctx context.Context, chatID int64, from *models.User, data string) {
	loc := b.connectLocale(ctx, from)
	rest := strings.TrimPrefix(data, "con:")
	switch {
	case strings.HasPrefix(rest, "ok:"):
		code := strings.TrimPrefix(rest, "ok:")
		if chatID != from.ID || code == "" {
			b.send(ctx, chatID, b.texts.T(loc, "bot.con.private_only", nil), nil)
			return
		}
		res, err := b.arena.TelegramConnectAccept(ctx, code, from)
		b.an.forget(from.ID) // the account is linked now: analytics asks again
		if err != nil {
			b.connectFailed(ctx, chatID, loc, err)
			return
		}
		loc = NormalizeLocale(res.Locale)
		b.pending.clear(from.ID) // an invitation started earlier is no longer awaited
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.done", map[string]any{"Org": Esc(res.OrgName)}), nil)
		b.showHome(ctx, chatID, nil, from, "")
	case rest == "no":
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.cancelled", nil), nil)
	case rest == "how":
		b.send(ctx, chatID, b.texts.T(loc, "bot.con.how", nil), b.connectHowKeyboard(loc))
	}
}

// connectHowKeyboard opens the web event center when the bot knows its
// address; without it the explanation stands alone.
func (b *Bot) connectHowKeyboard(loc string) *models.InlineKeyboardMarkup {
	if b.eventCenterURL == "" {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: b.texts.T(loc, "bot.con.open_web_btn", nil), URL: b.eventCenterURL + "/login"},
	}}}
}

// connectHaveRow is the stranger's "I already have an organization" row.
func (b *Bot) connectHaveRow(loc string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{btn(b.texts.T(loc, "bot.con.have_btn", nil), "con:how")}
}

// connectKeys lists every bot.con.* key. The locale test checks them in
// every supported catalog together with MessageKeys.
var connectKeys = []string{
	"bot.con.confirm", "bot.con.confirm_same", "bot.con.yes_btn", "bot.con.open_btn", "bot.con.no_btn",
	"bot.con.done", "bot.con.invalid", "bot.con.other_user", "bot.con.failed", "bot.con.cancelled",
	"bot.con.private_only", "bot.con.have_btn", "bot.con.how", "bot.con.open_web_btn",
}

func init() {
	MessageKeys = append(MessageKeys, connectKeys...)
}
