// Package salesbot is the receiving half of the sales bot (spec
// 08_architecture/61 section 7b.3): a second Telegram poller inside arena-bot,
// with SALES_TELEGRAM_BOT_TOKEN, next to the event-center bot. arena-worker
// keeps SENDING with the same token (package salesnotify); this poller only
// receives what the groups send back:
//
//   - a press of a step button under a shop order card in a delivery group
//     ("📦 Packed", "🚚 Handed to courier", "✅ Delivered", "❌ Not delivered").
//     It goes to arena-api, POST /v1/bot/shop-delivery/steps, under the bot
//     service token; the answer carries the card as it must look now, which
//     replaces the pressed one at once. "Handed to courier" first asks for the
//     shipment number with a force-reply question (or "No number" under the
//     card); "Not delivered" offers the reasons as buttons, "Other" asks for
//     the text the same way. The questions live in sales_bot_prompts, so a
//     reply after a restart is still matched.
//   - "/connect <code>" in a group: the chat is bound to what the platform
//     superadmin prepared in the admin screen (POST /v1/bot/sales-chats/connect).
//   - my_chat_member saying the bot was removed from a group, and the service
//     message of a group that became a supergroup.
//
// Telegram's privacy mode is fine for all of it: a bot in a group always gets
// commands, replies to its own messages, service messages and button presses.
// In a group with several bots the command may need the bot's name:
// "/connect@<bot> <code>"; both forms are accepted.
//
// Exactly one replica may poll a token (Telegram answers a second getUpdates
// with 409), and arena-bot runs as one. Updates are handled one at a time in
// the order Telegram delivered them (one worker, no async handlers).
package salesbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// Options configures the bot.
type Options struct {
	// Token is SALES_TELEGRAM_BOT_TOKEN.
	Token string
	// API reaches arena-api with the bot service token.
	API API
	// Prompts keeps the bot's open questions.
	Prompts PromptStore
	Logger  *slog.Logger
	// TelegramServerURL replaces api.telegram.org (tests).
	TelegramServerURL string
	// HTTPClient is the client of the Bot API calls (tests); nil is the
	// library's default with a one-minute poll.
	HTTPClient *http.Client
	// Now is the clock (tests); nil is time.Now.
	Now func() time.Time
}

// Bot is the sales bot's poller.
type Bot struct {
	tg       *tgbot.Bot
	api      API
	prompts  PromptStore
	logger   *slog.Logger
	now      func() time.Time
	username string
	// migrated holds the old ids of groups that became supergroups: a later
	// "left" for such an id is the upgrade itself, not a removal.
	migMu    sync.Mutex
	migrated map[string]struct{}
}

// New builds the bot; it does not talk to Telegram until Run.
func New(opts Options) (*Bot, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("salesbot: Token is required")
	}
	if opts.API == nil || opts.Prompts == nil {
		return nil, errors.New("salesbot: API and Prompts are required")
	}
	b := &Bot{api: opts.API, prompts: opts.Prompts, logger: opts.Logger, now: opts.Now, migrated: map[string]struct{}{}}
	if b.logger == nil {
		b.logger = slog.Default()
	}
	if b.now == nil {
		b.now = time.Now
	}
	tgOpts := []tgbot.Option{
		tgbot.WithDefaultHandler(b.handleUpdate),
		tgbot.WithSkipGetMe(),
		// One reader, handled in order: two presses on one card must reach
		// the API in the order they were made.
		tgbot.WithWorkers(1),
		tgbot.WithNotAsyncHandlers(),
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{
			models.AllowedUpdateMessage, models.AllowedUpdateCallbackQuery, models.AllowedUpdateMyChatMember,
		}),
	}
	if opts.TelegramServerURL != "" {
		tgOpts = append(tgOpts, tgbot.WithServerURL(opts.TelegramServerURL))
	}
	if opts.HTTPClient != nil {
		tgOpts = append(tgOpts, tgbot.WithHTTPClient(15*time.Second, opts.HTTPClient))
	}
	tg, err := tgbot.New(opts.Token, tgOpts...)
	if err != nil {
		return nil, fmt.Errorf("salesbot: telegram client: %w", err)
	}
	b.tg = tg
	return b, nil
}

// Run long-polls updates until ctx ends.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("salesbot: getMe: %w", err)
	}
	b.username = me.Username
	b.logger.Info("salesbot: connected", slog.String("username", me.Username), slog.Int64("bot_id", me.ID))
	b.tg.Start(ctx)
	return nil
}

// handleUpdate routes one update. It never panics the poller.
func (b *Bot) handleUpdate(ctx context.Context, _ *tgbot.Bot, upd *models.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("salesbot: panic while handling an update", "panic", fmt.Sprint(r))
		}
	}()
	switch {
	case upd.CallbackQuery != nil:
		b.onCallback(ctx, upd.CallbackQuery)
	case upd.MyChatMember != nil:
		b.onMyChatMember(ctx, upd.MyChatMember)
	case upd.Message != nil:
		b.onMessage(ctx, upd.Message)
	}
}

func chatKey(id int64) string { return strconv.FormatInt(id, 10) }

// telegramName is how a person is named in the order's history.
func telegramName(u models.User) string {
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name == "" && u.Username != "" {
		name = "@" + u.Username
	}
	if name == "" {
		name = "Telegram " + strconv.FormatInt(u.ID, 10)
	}
	return name
}

// ParseConnect reads "/connect <code>" (or "/connect@bot <code>"): ok is true
// for the command, code is what follows ("" when nothing does). A command for
// another bot is not ours.
func ParseConnect(text, botUsername string) (code string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", false
	}
	cmd, at, hasAt := strings.Cut(fields[0], "@")
	if !strings.EqualFold(cmd, "/connect") {
		return "", false
	}
	if hasAt && botUsername != "" && !strings.EqualFold(at, botUsername) {
		return "", false
	}
	return strings.Join(fields[1:], ""), true
}

// onMessage handles a text in a chat: the commands, the replies to the bot's
// questions and the supergroup migration. Everything else is ignored.
func (b *Bot) onMessage(ctx context.Context, m *models.Message) {
	chat := chatKey(m.Chat.ID)
	if m.MigrateToChatID != 0 {
		b.rememberMigrated(chat)
		if err := b.api.Migrated(ctx, chat, chatKey(m.MigrateToChatID)); err != nil {
			b.logger.Warn("salesbot: migrate chat", "error", err.Error())
		}
		return
	}
	if code, ok := ParseConnect(m.Text, b.username); ok {
		b.onConnect(ctx, m, code)
		return
	}
	if m.ReplyToMessage != nil && m.From != nil {
		b.onReply(ctx, m)
	}
}

// onConnect binds the chat with a code from the admin screen.
func (b *Bot) onConnect(ctx context.Context, m *models.Message, code string) {
	lang := "en"
	if m.From != nil && m.From.LanguageCode != "" {
		lang = m.From.LanguageCode
	}
	if m.Chat.Type != models.ChatTypeGroup && m.Chat.Type != models.ChatTypeSupergroup {
		// A code is bound to the chat it is typed in: in a private chat (or
		// a channel) it would bind the person's own dialog. Refuse before
		// the API sees it, so the code is not spent.
		b.say(ctx, m, salesnotify.Say(lang, "salesnotify.bot.connect_group_only", nil))
		return
	}
	if code == "" {
		b.say(ctx, m, salesnotify.Say(lang, "salesnotify.bot.connect_usage", nil))
		return
	}
	req := ConnectRequest{Code: code, ChatID: chatKey(m.Chat.ID), ChatTitle: m.Chat.Title}
	if m.From != nil {
		req.TelegramUserID, req.TelegramName = m.From.ID, telegramName(*m.From)
	}
	ans, status, err := b.api.Connect(ctx, req)
	if err != nil || (status != http.StatusOK && status != http.StatusNotFound) {
		b.logger.Warn("salesbot: connect failed", "status", status, "error", fmt.Sprint(err))
		b.say(ctx, m, salesnotify.Say(lang, "salesnotify.bot.toast_error", nil))
		return
	}
	if status == http.StatusNotFound {
		b.say(ctx, m, salesnotify.Say(lang, "salesnotify.bot.connect_failed", nil))
		return
	}
	loc := ans.Locale
	org := ans.OrgName
	if ans.Operator {
		loc = lang
		org = salesnotify.Say(loc, "salesnotify.bot.operator", nil)
	}
	b.say(ctx, m, salesnotify.Say(loc, "salesnotify.bot.connected", map[string]any{
		"Org": org, "Kinds": salesnotify.KindsText(loc, ans.Chat.Kinds),
	}))
}

// say answers a message with plain text.
func (b *Bot) say(ctx context.Context, m *models.Message, text string) {
	if _, err := b.tg.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: m.Chat.ID, Text: text, ReplyParameters: &models.ReplyParameters{MessageID: m.ID, AllowSendingWithoutReply: true},
	}); err != nil {
		b.logger.Warn("salesbot: send", "error", err.Error())
	}
}

func (b *Bot) rememberMigrated(chat string) {
	b.migMu.Lock()
	b.migrated[chat] = struct{}{}
	b.migMu.Unlock()
}

func (b *Bot) wasMigrated(chat string) bool {
	b.migMu.Lock()
	defer b.migMu.Unlock()
	_, ok := b.migrated[chat]
	return ok
}

// onMyChatMember marks a chat the bot was removed from.
func (b *Bot) onMyChatMember(ctx context.Context, u *models.ChatMemberUpdated) {
	switch u.NewChatMember.Type {
	case models.ChatMemberTypeLeft, models.ChatMemberTypeBanned:
		chat := chatKey(u.Chat.ID)
		if b.wasMigrated(chat) {
			// The old group of a supergroup upgrade: the bot did not leave,
			// the subscriptions already follow the new id.
			return
		}
		if err := b.api.Removed(ctx, chat); err != nil {
			b.logger.Warn("salesbot: mark removed", "error", err.Error())
		}
	}
}

// keyboard turns card rows into Telegram's inline keyboard.
func keyboard(rows [][]salesnotify.Button) models.InlineKeyboardMarkup {
	kb := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, r := range rows {
		row := make([]models.InlineKeyboardButton, 0, len(r))
		for _, btn := range r {
			row = append(row, models.InlineKeyboardButton{Text: btn.Text, CallbackData: btn.Data})
		}
		kb = append(kb, row)
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: kb}
}

// editCard puts a card in place of the message.
func (b *Bot) editCard(ctx context.Context, chatID int64, messageID int, card salesnotify.DeliveryCard) {
	_, err := b.tg.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID: chatID, MessageID: messageID, Text: card.Text, ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
		ReplyMarkup:        keyboard(card.Rows),
	})
	if err != nil && !salesnotify.IsNotModified(err) {
		b.logger.Warn("salesbot: edit card", "error", err.Error())
	}
}

// setButtons replaces only the buttons of the card.
func (b *Bot) setButtons(ctx context.Context, chatID int64, messageID int, rows [][]salesnotify.Button) {
	_, err := b.tg.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID: chatID, MessageID: messageID, ReplyMarkup: keyboard(rows),
	})
	if err != nil && !salesnotify.IsNotModified(err) {
		b.logger.Warn("salesbot: edit buttons", "error", err.Error())
	}
}

func (b *Bot) toast(ctx context.Context, q *models.CallbackQuery, text string) {
	if _, err := b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text}); err != nil {
		b.logger.Warn("salesbot: answer callback", "error", err.Error())
	}
}

// stepOfOp is the delivery step a press sets directly ("" for the ops that
// ask something first).
func stepOfOp(op string) string {
	switch op {
	case salesnotify.OpPacked:
		return shop.StepPacked
	case salesnotify.OpHandedOverPlain:
		return shop.StepHandedOver
	case salesnotify.OpDelivered:
		return shop.StepDelivered
	}
	if salesnotify.ReasonOf(op) != "" {
		return shop.StepFailed
	}
	return ""
}

// onCallback handles a press of a card button.
func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	a, ok := salesnotify.DecodeCardAction(q.Data)
	if !ok || q.Message.Message == nil {
		b.toast(ctx, q, "")
		return
	}
	msg := q.Message.Message
	chatID, cardID := msg.Chat.ID, msg.ID
	chat := chatKey(chatID)
	loc := a.Locale
	if step := stepOfOp(a.Op); step != "" {
		b.clearPrompts(ctx, chatID, a)
		b.setStep(ctx, chatID, cardID, loc, q.From, a, StepRequest{Step: step, Reason: salesnotify.ReasonOf(a.Op)}, q)
		return
	}
	switch a.Op {
	case salesnotify.OpBack:
		b.clearPrompts(ctx, chatID, a)
		ans, status, err := b.api.Card(ctx, chat, a.OrderID.String())
		if !b.usable(ctx, q, loc, status, err) {
			return
		}
		b.editCard(ctx, chatID, cardID, ans.Card)
		b.toast(ctx, q, "")
	case salesnotify.OpHandedOver, salesnotify.OpFailed, salesnotify.OpReasonOther:
		// Ask first, but only when the step still follows: the card is read
		// again, so a refunded order says "do not ship" before any question.
		ans, status, err := b.api.Card(ctx, chat, a.OrderID.String())
		if !b.usable(ctx, q, loc, status, err) {
			return
		}
		want := shop.StepHandedOver
		if a.Op != salesnotify.OpHandedOver {
			want = shop.StepFailed
		}
		cur := ""
		if ans.Step != nil {
			cur = *ans.Step
		}
		if ans.Outcome == OutcomeClosed || len(ans.Card.Rows) == 0 || !shop.CanMove(cur, want) {
			b.editCard(ctx, chatID, cardID, ans.Card)
			b.toastOutcome(ctx, q, loc, ans)
			return
		}
		switch a.Op {
		case salesnotify.OpHandedOver:
			b.setButtons(ctx, chatID, cardID, salesnotify.TrackingButtons(loc, a.OrderID))
			b.ask(ctx, chatID, cardID, loc, q.From, a, shop.StepHandedOver, "salesnotify.shop.ask_tracking")
		case salesnotify.OpFailed:
			b.setButtons(ctx, chatID, cardID, salesnotify.ReasonButtons(loc, a.OrderID))
		default:
			b.ask(ctx, chatID, cardID, loc, q.From, a, shop.StepFailed, "salesnotify.shop.ask_reason")
		}
		b.toast(ctx, q, "")
	default:
		b.toast(ctx, q, "")
	}
}

// usable checks a card answer; on a refusal it tells the presser and answers
// false.
func (b *Bot) usable(ctx context.Context, q *models.CallbackQuery, loc string, status int, err error) bool {
	switch {
	case err == nil && status == http.StatusOK:
		return true
	case err == nil && status == http.StatusNotFound:
		b.toast(ctx, q, salesnotify.Say(loc, "salesnotify.bot.toast_not_connected", nil))
	default:
		b.logger.Warn("salesbot: card", "status", status, "error", fmt.Sprint(err))
		b.toast(ctx, q, salesnotify.Say(loc, "salesnotify.bot.toast_error", nil))
	}
	return false
}

// ask sends a force-reply question that replies to the card and remembers it.
func (b *Bot) ask(ctx context.Context, chatID int64, cardID int, loc string, from models.User, a salesnotify.CardAction, step, key string) {
	sent, err := b.tg.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID, Text: salesnotify.Say(loc, key, nil),
		ReplyParameters: &models.ReplyParameters{MessageID: cardID, AllowSendingWithoutReply: true},
		ReplyMarkup:     &models.ForceReply{ForceReply: true},
	})
	if err != nil {
		b.logger.Warn("salesbot: ask", "error", err.Error())
		return
	}
	p := Prompt{ChatID: chatKey(chatID), MessageID: int64(sent.ID), CardMessageID: int64(cardID), OrderID: a.OrderID,
		Step: step, Locale: loc, AskedBy: from.ID, ExpiresAt: b.now().Add(PromptTTL)}
	if err := b.prompts.Save(ctx, p); err != nil {
		b.logger.Warn("salesbot: save question", "error", err.Error())
	}
}

// clearPrompts forgets the open questions about the order and deletes them
// from the chat (the people's replies stay).
func (b *Bot) clearPrompts(ctx context.Context, chatID int64, a salesnotify.CardAction) {
	ids, err := b.prompts.Clear(ctx, chatKey(chatID), a.OrderID)
	if err != nil {
		b.logger.Warn("salesbot: clear questions", "error", err.Error())
		return
	}
	for _, id := range ids {
		if _, err := b.tg.DeleteMessage(ctx, &tgbot.DeleteMessageParams{ChatID: chatID, MessageID: int(id)}); err != nil {
			b.logger.Debug("salesbot: delete question", "error", err.Error())
		}
	}
}

// setStep sends a step to the API and shows the answer: the card in place of
// the pressed one, a short note to the presser (q, nil for a reply).
func (b *Bot) setStep(ctx context.Context, chatID int64, cardID int, loc string, from models.User, a salesnotify.CardAction, req StepRequest, q *models.CallbackQuery) (Answer, bool) {
	req.ChatID, req.OrderID = chatKey(chatID), a.OrderID.String()
	req.TelegramUserID, req.TelegramName = from.ID, telegramName(from)
	ans, status, err := b.api.Step(ctx, req)
	note := ""
	ok := false
	switch {
	case err == nil && status == http.StatusOK:
		b.editCard(ctx, chatID, cardID, ans.Card)
		note, ok = outcomeNote(loc, ans), true
	case err == nil && status == http.StatusNotFound:
		note = salesnotify.Say(loc, "salesnotify.bot.toast_not_connected", nil)
	default:
		b.logger.Warn("salesbot: step", "status", status, "error", fmt.Sprint(err))
		note = salesnotify.Say(loc, "salesnotify.bot.toast_error", nil)
	}
	if q != nil {
		b.toast(ctx, q, note)
	}
	return ans, ok
}

// outcomeNote is the short note of an answer.
func outcomeNote(loc string, ans Answer) string {
	step := ""
	if ans.Step != nil {
		step = salesnotify.StepLabel(loc, *ans.Step)
	}
	switch ans.Outcome {
	case OutcomeApplied:
		return salesnotify.Say(loc, "salesnotify.bot.toast_done", map[string]any{"Step": step})
	case OutcomeClosed:
		return salesnotify.Say(loc, "salesnotify.bot.toast_closed", nil)
	default:
		return salesnotify.Say(loc, "salesnotify.bot.toast_stale", map[string]any{"Step": step})
	}
}

func (b *Bot) toastOutcome(ctx context.Context, q *models.CallbackQuery, loc string, ans Answer) {
	b.toast(ctx, q, outcomeNote(loc, ans))
}

// onReply matches a reply to one of the bot's questions and sets the step
// with the text: the shipment number or the reason.
func (b *Bot) onReply(ctx context.Context, m *models.Message) {
	chat := chatKey(m.Chat.ID)
	p, ok, err := b.prompts.Get(ctx, chat, int64(m.ReplyToMessage.ID))
	if err != nil {
		b.logger.Warn("salesbot: read question", "error", err.Error())
		return
	}
	if !ok {
		return // a reply to something else of the bot: not a question
	}
	a := salesnotify.CardAction{OrderID: p.OrderID, Locale: p.Locale}
	if !b.now().Before(p.ExpiresAt) {
		b.clearPrompts(ctx, m.Chat.ID, a)
		b.say(ctx, m, salesnotify.Say(p.Locale, "salesnotify.bot.prompt_expired", nil))
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	req := StepRequest{Step: p.Step}
	if p.Step == shop.StepHandedOver {
		req.Tracking = &text
	} else {
		req.Reason = text
	}
	ans, done := b.setStep(ctx, m.Chat.ID, int(p.CardMessageID), p.Locale, *m.From, a, req, nil)
	if !done {
		b.say(ctx, m, salesnotify.Say(p.Locale, "salesnotify.bot.toast_error", nil))
		return
	}
	b.clearPrompts(ctx, m.Chat.ID, a)
	if ans.Outcome != OutcomeApplied {
		b.say(ctx, m, outcomeNote(p.Locale, ans))
	}
}
