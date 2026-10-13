package eventbot

// feed.go — the chat is a chronological feed (owner decision 2026-10-13).
//
// Until then a press, and many typed answers, EDITED an older message: the
// refund's final check replaced a screen sent half an hour earlier, far above
// the bottom of the chat, and the owner never saw it and typed the
// confirmation word twice. Now every action of the person gets a NEW message
// at the bottom, and the message whose button was pressed loses its keyboard
// (its text gains a "✔ <the pressed button>" line, so the history still reads
// as a conversation). Only two kinds of press may redraw their own message:
// ticking a box in the list the person is looking at, and turning a page (or
// a calendar month). Everything else is decided here, in reply/sendChecked,
// and no call site needs to know.
//
// The bot remembers, per private chat, which of its messages still carry
// buttons ("live"). A press on any other message — a stale screen whose
// keyboard could not be removed, or the second tap of a double tap that
// arrived after the first one already moved on — does nothing and answers
// "this message is out of date" (handleCallback). The memory is in-process:
// after a restart a chat is unknown until the bot writes to it again, and
// presses there are accepted as before (each dialog still checks its own
// step, and the refund's last button carries its operation's key).
//
// Background work (a refund answer, a reminder of the draft sweep) has no
// action: it always posts a new message and leaves the other live messages
// alone, so a reminder never takes the buttons from the screen the person is
// working on.

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	// feedMaxLive bounds the live messages remembered per chat; the oldest
	// one is forgotten (and then treated as stale) beyond it.
	feedMaxLive = 32
	// feedForgetIdle is how long a chat with no bot message is remembered.
	feedForgetIdle = 7 * 24 * time.Hour
	// feedEchoMark starts the line a retired message gains: the pressed
	// button's label in the past tense of a tick.
	feedEchoMark = "\n\n✔ "
	// tgTextLimit is Telegram's limit for a message text (UTF-16 units).
	tgTextLimit = 4096
)

// feedMemory is the bot's record of its live messages, per private chat.
// A nil *feedMemory remembers nothing and accepts every press.
type feedMemory struct {
	mu    sync.Mutex
	chats map[int64]*chatFeed
	now   func() time.Time
	swept time.Time
}

type chatFeed struct {
	live map[int]time.Time // message id -> when it went live
	seen time.Time
}

func newFeedMemory() *feedMemory {
	return &feedMemory{chats: map[int64]*chatFeed{}, now: time.Now}
}

// tracked reports whether a chat's messages are remembered at all: private
// chats only (a private chat's id is the person's id, always positive).
func (f *feedMemory) tracked(chatID int64) bool { return f != nil && chatID > 0 }

// chat returns the chat's record, creating it; f.mu must be held.
func (f *feedMemory) chat(chatID int64) *chatFeed {
	now := f.now()
	if now.Sub(f.swept) > time.Hour {
		f.swept = now
		for id, c := range f.chats {
			if now.Sub(c.seen) > feedForgetIdle {
				delete(f.chats, id)
			}
		}
	}
	c := f.chats[chatID]
	if c == nil {
		c = &chatFeed{live: map[int]time.Time{}}
		f.chats[chatID] = c
	}
	c.seen = now
	return c
}

// add records a message the bot just sent (or a pressed message of a chat
// it did not know yet) as live.
func (f *feedMemory) add(chatID int64, msgID int) {
	if !f.tracked(chatID) || msgID == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.chat(chatID)
	c.live[msgID] = f.now()
	for len(c.live) > feedMaxLive {
		oldest, at := 0, time.Time{}
		for id, t := range c.live {
			if oldest == 0 || t.Before(at) {
				oldest, at = id, t
			}
		}
		delete(c.live, oldest)
	}
}

// remove forgets a message whose keyboard is gone.
func (f *feedMemory) remove(chatID int64, msgID int) {
	if !f.tracked(chatID) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.chats[chatID]; c != nil {
		delete(c.live, msgID)
	}
}

// others lists the live messages of a chat that are not in except.
func (f *feedMemory) others(chatID int64, except map[int]bool) []int {
	if !f.tracked(chatID) {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.chats[chatID]
	if c == nil {
		return nil
	}
	var out []int
	for id := range c.live {
		if !except[id] {
			out = append(out, id)
		}
	}
	return out
}

// admit decides a press on msgID: false for a message the bot knows is no
// longer live. A chat the bot does not know yet (a restart) is accepted and
// the pressed message becomes its first live one.
func (f *feedMemory) admit(chatID int64, msgID int) bool {
	if !f.tracked(chatID) {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.chats[chatID]; c != nil {
		_, live := c.live[msgID]
		c.seen = f.now()
		return live
	}
	f.chat(chatID).live[msgID] = f.now()
	return true
}

// ─── one action ──────────────────────────────────────────────────────────────

// feedAction is one press or one typed message of the person, carried on the
// context of its handler.
type feedAction struct {
	chatID  int64
	pressed int             // the pressed message; 0 for a typed message
	label   string          // the pressed button's label
	orig    *models.Message // the pressed message as Telegram reported it
	inPlace bool            // this press may redraw its own message
	retired bool            // the pressed message lost its keyboard already
	// alias maps a message the action was asked to edit to the new message
	// that took its place, so a second reply of the same action edits that
	// new message instead of posting a third one.
	alias map[int]int
	own   map[int]bool // the messages this action sent
}

type feedActionKey struct{}

func withFeedAction(ctx context.Context, a *feedAction) context.Context {
	return context.WithValue(ctx, feedActionKey{}, a)
}

// actionIn is the action of ctx when it belongs to chatID, else nil.
func actionIn(ctx context.Context, chatID int64) *feedAction {
	a, _ := ctx.Value(feedActionKey{}).(*feedAction)
	if a == nil || a.chatID != chatID {
		return nil
	}
	return a
}

// pressAction builds the action of a press.
func pressAction(cq *models.CallbackQuery) *feedAction {
	m := cq.Message.Message
	a := &feedAction{chatID: m.Chat.ID, pressed: m.ID, orig: m, alias: map[int]int{}, own: map[int]bool{}}
	row, label := pressedButton(m.ReplyMarkup, cq.Data)
	a.label = label
	a.inPlace = inPlacePress(cq.Data, row, label)
	return a
}

// textAction builds the action of a typed message (or a sent file).
func textAction(chatID int64) *feedAction {
	return &feedAction{chatID: chatID, alias: map[int]int{}, own: map[int]bool{}}
}

// pressedButton finds the pressed button in the message's keyboard: its row
// and its label ("" when the keyboard is not in the update).
func pressedButton(kb *models.InlineKeyboardMarkup, data string) ([]models.InlineKeyboardButton, string) {
	if kb == nil {
		return nil, ""
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == data {
				return row, b.Text
			}
		}
	}
	return nil, ""
}

// pagePosition is the middle button of a pager row ("2/5", paging.go).
var pagePosition = regexp.MustCompile(`^\d+/\d+$`)

// inPlaceToggles are the presses that tick a box in the list on screen: the
// list is redrawn where it is, because the person is looking at it.
var inPlaceToggles = []string{
	"or:f:t:",  // refund: a ticket
	"or:f:all", // refund: every ticket
	"or:f:nt",  // refund: the buyer letter
	"wz:ch:",   // wizard: a sales channel
	"pm:k:t:",  // promo code: a session
	"onb:ct:",  // application: a consent
	"onb:m:",   // application: an option of a multiple choice
}

// inPlacePress reports the presses that may redraw their own message: a tick
// in a list, a page of a list and a month of a calendar.
func inPlacePress(data string, row []models.InlineKeyboardButton, label string) bool {
	for _, p := range inPlaceToggles {
		if strings.HasPrefix(data, p) {
			return true
		}
	}
	if strings.HasPrefix(data, calCallback) || strings.Contains(data, ":"+calCallback) {
		return true // another month of the calendar
	}
	if pagePosition.MatchString(label) {
		return false // the position button itself does nothing
	}
	for _, b := range row {
		if pagePosition.MatchString(b.Text) {
			return true // a "‹ prev" / "next ›" button of a pager row
		}
	}
	return false
}

// hasCallbacks reports whether a keyboard can send presses to the bot. A
// keyboard of links only is never retired: it cannot act on anything.
func hasCallbacks(markup models.ReplyMarkup) bool {
	kb, ok := markup.(*models.InlineKeyboardMarkup)
	if !ok || kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData != "" {
				return true
			}
		}
	}
	return false
}

// emptyKeyboard removes an inline keyboard when sent as reply_markup.
func emptyKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
}

// ─── posting and editing ─────────────────────────────────────────────────────

// editTarget is the message a reply may redraw: a message this action
// already sent (or the one that replaced the requested one), or the pressed
// message of an in-place press. Anything else gets a new message.
func editTarget(a *feedAction, requested int) (int, bool) {
	if a == nil {
		return 0, false
	}
	if n, ok := a.alias[requested]; ok {
		return n, true
	}
	if a.own[requested] {
		return requested, true
	}
	if a.inPlace && !a.retired && requested == a.pressed {
		return requested, true
	}
	return 0, false
}

// post sends a new message at the bottom of the chat and keeps the feed:
// within an action, the pressed message (and a message the reply was asked
// to edit) loses its keyboard once the new one is out, and a new keyboard
// takes the buttons from every other live message. replaced is the message
// the caller wanted to edit, or 0.
func (b *Bot) post(ctx context.Context, chatID int64, replaced int, text string, markup models.ReplyMarkup) (int, error) {
	params := &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: tgbot.True(),
		},
	}
	if markup != nil {
		params.ReplyMarkup = markup
	}
	m, err := b.tg.SendMessage(ctx, params)
	if err != nil {
		return 0, err
	}
	id := 0
	if m != nil {
		id = m.ID
	}
	live := hasCallbacks(markup)
	if live {
		b.feed.add(chatID, id)
	}
	a := actionIn(ctx, chatID)
	if a != nil {
		a.own[id] = true
		if replaced != 0 {
			a.alias[replaced] = id
		}
	}
	if live && replaced != id {
		b.takeOver(ctx, chatID, a, replaced)
	}
	return id, nil
}

// takeOver runs when a new keyboard reaches the chat: the screens it
// replaces lose theirs. Within an action that is the pressed message (with
// its echo), the message the reply was asked to edit and every other live
// message; background work only retires the message it was asked to edit.
//
// An answer WITHOUT buttons (a note, an error, a document) takes nothing
// away: the screen above stays usable, or the person would be left with no
// button at all. A press that must not be repeated (money) retires its
// message itself through consumePress.
func (b *Bot) takeOver(ctx context.Context, chatID int64, a *feedAction, replaced int) {
	if a == nil {
		if replaced != 0 {
			b.retire(ctx, chatID, replaced)
		}
		return
	}
	b.retirePressed(ctx, a)
	if replaced != 0 && replaced != a.pressed && !a.own[replaced] {
		b.retire(ctx, chatID, replaced)
	}
	for _, other := range b.feed.others(chatID, a.own) {
		b.retire(ctx, chatID, other)
	}
}

// consumePress takes the keyboard from the pressed message now, whatever the
// answer: for a press that starts something which must not start twice (a
// refund being sent), even when its answer has no buttons of its own.
func (b *Bot) consumePress(ctx context.Context, chatID int64) {
	if a := actionIn(ctx, chatID); a != nil {
		b.retirePressed(ctx, a)
	}
}

// edit redraws a message in place.
func (b *Bot) edit(ctx context.Context, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup) error {
	params := &tgbot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: tgbot.True(),
		},
	}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	_, err := b.tg.EditMessageText(ctx, params)
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		return err
	}
	if hasCallbacks(kb) {
		b.feed.add(chatID, msgID)
	} else {
		b.feed.remove(chatID, msgID)
	}
	return nil
}

// retirePressed takes the keyboard from the pressed message once, and notes
// under its text which button was pressed.
func (b *Bot) retirePressed(ctx context.Context, a *feedAction) {
	if a.pressed == 0 || a.retired || a.own[a.pressed] {
		return
	}
	a.retired = true
	b.feed.remove(a.chatID, a.pressed)
	if a.orig != nil && a.orig.Text != "" && a.label != "" {
		text := a.orig.Text + feedEchoMark + a.label
		if len(utf16.Encode([]rune(text))) <= tgTextLimit {
			_, err := b.tg.EditMessageText(ctx, &tgbot.EditMessageTextParams{
				ChatID:             a.chatID,
				MessageID:          a.pressed,
				Text:               text,
				Entities:           a.orig.Entities,
				LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
				ReplyMarkup:        emptyKeyboard(),
			})
			if err == nil || strings.Contains(err.Error(), "message is not modified") {
				return
			}
		}
	}
	b.stripKeyboard(ctx, a.chatID, a.pressed)
}

// retire takes the keyboard from one message.
func (b *Bot) retire(ctx context.Context, chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	b.feed.remove(chatID, msgID)
	b.stripKeyboard(ctx, chatID, msgID)
}

func (b *Bot) stripKeyboard(ctx context.Context, chatID int64, msgID int) {
	_, err := b.tg.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID: chatID, MessageID: msgID, ReplyMarkup: emptyKeyboard(),
	})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") && b.logger != nil {
		// A message Telegram no longer lets us edit keeps its buttons; a
		// press on it is refused by handleCallback all the same.
		b.logger.Debug("eventbot: removing an old keyboard failed", slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
	}
}

// refuseStalePress answers a press on a message that is no longer live: the
// spinner stops with a short note, nothing else happens, and the message
// loses the keyboard it should not have any more.
func (b *Bot) refuseStalePress(ctx context.Context, cq *models.CallbackQuery) {
	loc := NormalizeLocale(cq.From.LanguageCode)
	if b.queries != nil {
		if link, err := b.queries.GetBotTelegramLink(ctx, cq.From.ID); err == nil && link.RevokedAt == nil {
			loc = NormalizeLocale(link.Locale)
		}
	}
	_, _ = b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
		CallbackQueryID: cq.ID,
		Text:            b.texts.T(loc, "bot.stale_message", nil),
		ShowAlert:       true,
	})
	m := cq.Message.Message
	b.stripKeyboard(ctx, m.Chat.ID, m.ID)
}

func init() {
	MessageKeys = append(MessageKeys, "bot.stale_message")
}
