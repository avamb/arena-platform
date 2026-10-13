package eventbot

// menu_keyboard.go — the "☰ Menu" button under the message field (owner
// decision 2026-10-13).
//
// The chat is a feed (feed.go): old messages lose their buttons, and a press
// on a message the bot no longer knows answers "out of date". A person who
// scrolled away, or whose screen was retired, must never be left without a
// way back. Every linked user therefore gets a persistent reply keyboard
// with one button, «☰ Меню». Telegram keeps it under the message field until
// the bot replaces or removes it, also across a bot restart, so it is the one
// control that always works.
//
// A press of it is a TYPED message (the button's own text). It opens the
// main menu as a new message from any state and leaves every open dialog
// (leaveAllDialogs), except a refund the person was about to confirm: that
// one first asks whether to leave (the money has not gone back yet).
// Telegram's own command list keeps /start and /menu for the same purpose.
//
// The keyboard is sent with a short note, because a message carries either
// an inline keyboard or a reply keyboard, never both. The bot remembers in
// memory which chats got it (menuShown): /start always sends it, the main
// menu sends it to a chat it does not know yet (after a restart, or to a
// person linked before this button existed). Strangers never get it: they
// see the organizer application instead.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	menuButtonKey = "bot.menu_button"
	// menuCallback is the prefix of the "leave the refund" question's presses.
	menuCallback = "menu"
)

// menuShown remembers the chats that got the menu keyboard from this process.
type menuShown struct {
	mu    sync.Mutex
	chats map[int64]bool
}

func newMenuShown() *menuShown { return &menuShown{chats: map[int64]bool{}} }

func (m *menuShown) has(chatID int64) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chats[chatID]
}

func (m *menuShown) set(chatID int64, shown bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if shown {
		m.chats[chatID] = true
		return
	}
	delete(m.chats, chatID)
}

// menuReplyKeyboard is the persistent keyboard with the one menu button.
func (b *Bot) menuReplyKeyboard(loc string) *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard:       [][]models.KeyboardButton{{{Text: b.texts.T(loc, menuButtonKey, nil)}}},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
}

// isMenuButton reports a typed text that is the menu button in any language:
// the person may have changed the language since the keyboard was sent.
func (b *Bot) isMenuButton(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, loc := range SupportedLocales {
		if text == b.texts.T(loc, menuButtonKey, nil) {
			return true
		}
	}
	return false
}

// ensureMenuKeyboard sends the menu keyboard with its note when the chat
// does not have it yet (force: always). It is sent straight to Telegram, not
// through the feed: the note has no inline buttons, takes nothing away and
// is never redrawn.
func (b *Bot) ensureMenuKeyboard(ctx context.Context, chatID int64, loc string, force bool) {
	if chatID <= 0 || (!force && b.menuKB.has(chatID)) {
		return
	}
	_, err := b.tg.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        b.texts.T(loc, "bot.menu_keyboard_note", nil),
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: b.menuReplyKeyboard(loc),
	})
	if err != nil {
		b.logger.Warn("eventbot: menu keyboard not sent", slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
		return
	}
	b.menuKB.set(chatID, true)
}

// openMenu answers /start, /menu and the menu button: every open dialog is
// left and the main menu is a new message. A stranger gets the application
// instead (replyIdentityError).
func (b *Bot) openMenu(ctx context.Context, chatID int64, from *models.User, force bool) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if b.askLeaveRefund(ctx, chatID, from, id) {
		// Everything else still ends now: only the refund waits for the
		// answer, so no other dialog takes a text typed meanwhile.
		b.leaveAllDialogs(ctx, from.ID, id, jwt, true)
		return
	}
	b.leaveAllDialogs(ctx, from.ID, id, jwt, false)
	if force {
		b.ensureMenuKeyboard(ctx, chatID, id.Locale(), true)
	}
	b.showHome(ctx, chatID, nil, from, "") // sends the keyboard to a chat that lacks it
}

// menuCommand reports the commands that open the menu: /menu, and a /start
// that is neither an invitation link nor a stranger's "apply" link.
func (b *Bot) menuCommand(ctx context.Context, cmd, arg string, from *models.User) bool {
	switch cmd {
	case "/menu":
		return true
	case "/start":
		if strings.HasPrefix(arg, "inv_") {
			return false
		}
		if arg == "apply" && b.onbOn() {
			_, _, err := b.resolveIdentity(ctx, from.ID)
			return !errors.Is(err, ErrNotLinked)
		}
		return true
	}
	return false
}

// askLeaveRefund stops the menu when leaving would decide a refund's fate,
// so the person chooses:
//   - a refund waiting for its typed word or its final press (nothing sent):
//     leaving drops it;
//   - a refund the server got but did not answer (a timeout, a server error,
//     a restart): it may have started, the order should be checked, and
//     leaving keeps its key (leaveIdleRefund parks it), so going back sends
//     the same operation again and never refunds twice.
//
// A refund with the server right now, or one already left, is no question.
func (b *Bot) askLeaveRefund(ctx context.Context, chatID int64, from *models.User, id *Identity) bool {
	var st refundDialog
	step, found, _, err := b.dialogs.Load(ctx, from.ID, refundDialogKind, &st)
	if err != nil || !found || st.Parked || b.sendingNow(step, st) {
		return false
	}
	loc := id.Locale()
	back := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.menu_back_refund_btn", nil), CallbackData: "or:f:cur"}}
	leave := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.menu_leave_btn", nil), CallbackData: menuCallback + ":leave"}}
	vars := map[string]any{"Num": st.Num}
	switch {
	case st.mayHaveStarted(step):
		order := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.menu_open_order_btn", nil), CallbackData: "or:v:" + st.OrderID.String()}}
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.menu_leave_refund_maybe", vars),
			&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{back, order, leave}})
	case step == refStepWord || step == refStepFinal:
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.menu_leave_refund", vars),
			&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{back, leave}})
	default:
		return false
	}
	return true
}

// menuPress handles "menu:<data>": the person chose to leave the refund. A
// refund that may have started keeps its key (leaveIdleRefund parks it).
func (b *Bot) menuPress(ctx context.Context, chatID int64, from *models.User, data string) {
	if data != "leave" {
		return
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	b.leaveAllDialogs(ctx, from.ID, id, jwt, false)
	b.showHome(ctx, chatID, nil, from, "")
}

// leaveAllDialogs ends every dialog that waits for a typed answer, so a text
// typed after the menu is never taken as one. A refund already with the
// server keeps its dialog (its answer still needs it), one that may have
// started is parked with its key, and keepRefund leaves the refund alone (the
// menu's question about it is on screen). An event draft is paused, not
// deleted: "+ Event" offers to continue it. A NEW dialog kind must be ended
// here (TestLeaveAllDialogs_EndsEveryDialogKind).
func (b *Bot) leaveAllDialogs(ctx context.Context, tg int64, id *Identity, jwt string, keepRefund bool) {
	b.leaveEvents(ctx, tg)
	b.leaveOrders(ctx, tg)
	b.leaveEvStatus(ctx, tg)
	b.leavePromo(ctx, tg)
	if !keepRefund {
		b.leaveIdleRefund(ctx, tg)
	}
	b.leaveScanExc(ctx, tg)
	b.leaveResend(ctx, tg)
	b.leaveInvite(ctx, tg)
	b.leavePromoter(ctx, tg)
	b.leaveCategories(ctx, tg)
	b.clearTeamDialog(ctx, tg)
	if err := b.dialogs.Delete(ctx, tg, sesDialogKind); err != nil {
		b.logger.Warn("eventbot: sessions dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
	b.pauseDraft(ctx, tg, id, jwt)
}

// pauseDraft marks an unfinished event draft as paused: it no longer takes
// typed text or a poster until the person continues it with a wizard button.
func (b *Bot) pauseDraft(ctx context.Context, tg int64, id *Identity, jwt string) {
	if id == nil || id.Current == nil {
		return
	}
	ws := b.wizardSession(id, jwt)
	d, _, err := b.loadDraft(ctx, tg, ws)
	if err != nil || d == nil || d.Step == stDone || d.Paused {
		return
	}
	d.Paused = true
	if _, err := b.storeDraft(ctx, tg, ws, d); err != nil {
		b.logger.Warn("eventbot: draft pause failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// menuKeys are the texts of the menu button and its question.
var menuKeys = []string{
	menuButtonKey, "bot.menu_keyboard_note", "bot.cmd_menu", "bot.menu_leave_refund",
	"bot.menu_back_refund_btn", "bot.menu_leave_btn", "bot.stale_message_stranger",
	"bot.menu_leave_refund_maybe", "bot.menu_open_order_btn", "bot.ref.maybe_started",
}

func init() {
	MessageKeys = append(MessageKeys, menuKeys...)
}
