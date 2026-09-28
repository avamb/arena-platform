package eventbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// pendingInviteTTL is how long the bot waits for the e-mail after a person
// opened an invitation link.
const pendingInviteTTL = 30 * time.Minute

// Options wires a Bot.
type Options struct {
	// Token is the Telegram bot token (EVENTS_TELEGRAM_BOT_TOKEN).
	Token string
	// Queries reads and writes the bot_* tables only.
	Queries *gen.Queries
	// Arena is the REST client to arena-api.
	Arena *ArenaClient
	// Minter issues the linked user's JWTs.
	Minter *TokenMinter
	// Texts renders messages; nil renders key names (tests only).
	Texts *Texts
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// TelegramServerURL overrides https://api.telegram.org (tests).
	TelegramServerURL string
	// HTTPClient is the client used towards Telegram; nil uses a default.
	HTTPClient *http.Client
	// TicketsBaseURL is the storefront origin (PUBLIC_TICKETS_BASE_URL); the
	// sales link after a save is "<base>/<org slug>/<event slug>". Empty
	// means no link is shown.
	TicketsBaseURL string
	// Refs overrides the wizard's reference source (tests); nil uses Arena.
	Refs RefIO
}

// Bot is the running event-center bot.
type Bot struct {
	tg      *tgbot.Bot
	queries *gen.Queries
	arena   *ArenaClient
	minter  *TokenMinter
	texts   *Texts
	logger  *slog.Logger
	pending *pendingInvites
	wizard  *Wizard
	// fileClient downloads Telegram files (posters); nil uses a default.
	fileClient     *http.Client
	ticketsBaseURL string
}

// New builds the bot; it does not talk to Telegram until Run.
func New(opts Options) (*Bot, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("eventbot: Token is required")
	}
	if opts.Queries == nil || opts.Arena == nil || opts.Minter == nil {
		return nil, errors.New("eventbot: Queries, Arena and Minter are required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	b := &Bot{
		queries: opts.Queries,
		arena:   opts.Arena,
		minter:  opts.Minter,
		texts:   opts.Texts,
		logger:  logger,
		pending: newPendingInvites(pendingInviteTTL),

		fileClient:     opts.HTTPClient,
		ticketsBaseURL: opts.TicketsBaseURL,
	}
	if b.texts == nil {
		b.texts = NewTexts(nil)
	}
	var refs RefIO = opts.Arena
	if opts.Refs != nil {
		refs = opts.Refs
	}
	b.wizard = NewWizard(b.texts, refs)
	tgOpts := []tgbot.Option{
		tgbot.WithDefaultHandler(b.handleUpdate),
		tgbot.WithSkipGetMe(),
		// One worker: an organizer's messages are handled in the order they
		// were sent, so "e-mail after /start" and "next page after page 1"
		// cannot overtake each other. The load is a few operators, not a crowd.
		tgbot.WithWorkers(1),
	}
	if opts.TelegramServerURL != "" {
		tgOpts = append(tgOpts, tgbot.WithServerURL(opts.TelegramServerURL))
	}
	if opts.HTTPClient != nil {
		tgOpts = append(tgOpts, tgbot.WithHTTPClient(15*time.Second, opts.HTTPClient))
	}
	tg, err := tgbot.New(opts.Token, tgOpts...)
	if err != nil {
		return nil, fmt.Errorf("eventbot: telegram client: %w", err)
	}
	b.tg = tg
	return b, nil
}

// Run publishes the command menu and long-polls updates until ctx ends.
// Exactly one replica may run: Telegram answers a second poller with 409.
func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("eventbot: getMe: %w", err)
	}
	b.logger.Info("eventbot: connected", slog.String("username", me.Username), slog.Int64("bot_id", me.ID))
	b.publishCommands(ctx)
	b.tg.Start(ctx)
	return nil
}

func (b *Bot) publishCommands(ctx context.Context) {
	for _, locale := range SupportedLocales {
		cmds := []models.BotCommand{
			{Command: "start", Description: b.texts.T(locale, "bot.cmd_start", nil)},
			{Command: "events", Description: b.texts.T(locale, "bot.cmd_events", nil)},
			{Command: "new", Description: b.texts.T(locale, "bot.cmd_new", nil)},
			{Command: "org", Description: b.texts.T(locale, "bot.cmd_org", nil)},
			{Command: "lang", Description: b.texts.T(locale, "bot.cmd_lang", nil)},
			{Command: "help", Description: b.texts.T(locale, "bot.cmd_help", nil)},
		}
		params := &tgbot.SetMyCommandsParams{Commands: cmds}
		if locale != "en" {
			params.LanguageCode = locale
		}
		if _, err := b.tg.SetMyCommands(ctx, params); err != nil {
			b.logger.Warn("eventbot: setMyCommands failed", slog.String("locale", locale), slog.String("error", err.Error()))
		}
	}
}

// ─── update dispatch ──────────────────────────────────────────────────────────

// handleUpdate is the single entry point for every Telegram update.
func (b *Bot) handleUpdate(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("eventbot: panic in update handler", slog.Any("panic", r))
		}
	}()
	switch {
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.From != nil && !u.Message.From.IsBot:
		if string(u.Message.Chat.Type) != "private" {
			return // the bot works in private chats only
		}
		b.handleMessage(ctx, u.Message)
	}
}

func (b *Bot) handleMessage(ctx context.Context, m *models.Message) {
	text := strings.TrimSpace(m.Text)
	from := m.From
	chatID := m.Chat.ID
	if strings.HasPrefix(text, "/") {
		cmd, arg, _ := strings.Cut(text, " ")
		cmd = strings.ToLower(strings.TrimSpace(cmd))
		if i := strings.Index(cmd, "@"); i > 0 {
			cmd = cmd[:i]
		}
		switch cmd {
		case "/start":
			arg = strings.TrimSpace(arg)
			if strings.HasPrefix(arg, "inv_") {
				b.startInvitation(ctx, chatID, from, strings.TrimPrefix(arg, "inv_"))
				return
			}
			b.showHome(ctx, chatID, nil, from, "")
			return
		case "/events":
			b.showEvents(ctx, chatID, nil, from, 1)
			return
		case "/new":
			b.wizardStart(ctx, chatID, nil, from, false)
			return
		case "/org":
			b.showOrgChooser(ctx, chatID, nil, from)
			return
		case "/lang":
			b.showLangChooser(ctx, chatID, nil, from)
			return
		case "/help":
			b.showHelp(ctx, chatID, nil, from)
			return
		}
	}
	if code, ok := b.pending.take(from.ID); ok {
		b.acceptInvitation(ctx, chatID, from, code, text)
		return
	}
	if m.Document != nil || len(m.Photo) > 0 {
		if b.wizardPoster(ctx, chatID, from, m) {
			return
		}
	} else if text != "" && b.wizardText(ctx, chatID, from, text) {
		return
	}
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	b.send(ctx, chatID, b.texts.T(id.Locale(), "bot.unknown_input", nil), b.homeKeyboard(id))
}

func (b *Bot) handleCallback(ctx context.Context, cq *models.CallbackQuery) {
	_, _ = b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID})
	if cq.Message.Message == nil {
		return
	}
	chatID := cq.Message.Message.Chat.ID
	msgID := cq.Message.Message.ID
	from := &cq.From
	parts := strings.Split(cq.Data, ":")
	switch parts[0] {
	case "home":
		b.showHome(ctx, chatID, &msgID, from, "")
	case "help":
		b.showHelp(ctx, chatID, &msgID, from)
	case "events":
		page := 1
		if len(parts) > 1 {
			_, _ = fmt.Sscanf(parts[1], "%d", &page)
		}
		b.showEvents(ctx, chatID, &msgID, from, page)
	case "event":
		if len(parts) < 2 {
			return
		}
		eventID, err := uuid.Parse(parts[1])
		if err != nil {
			return
		}
		page := 1
		if len(parts) > 2 {
			_, _ = fmt.Sscanf(parts[2], "%d", &page)
		}
		b.showEvent(ctx, chatID, &msgID, from, eventID, page)
	case "org":
		if len(parts) == 1 {
			b.showOrgChooser(ctx, chatID, &msgID, from)
			return
		}
		orgID, err := uuid.Parse(parts[1])
		if err != nil {
			return
		}
		b.switchOrg(ctx, chatID, &msgID, from, orgID)
	case "lang":
		if len(parts) == 1 {
			b.showLangChooser(ctx, chatID, &msgID, from)
			return
		}
		b.switchLang(ctx, chatID, &msgID, from, parts[1])
	case "wz":
		b.wizardCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "wz:"))
	}
}

// ─── invitation flow ──────────────────────────────────────────────────────────

func (b *Bot) startInvitation(ctx context.Context, chatID int64, from *models.User, code string) {
	locale := NormalizeLocale(from.LanguageCode)
	if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
		b.pending.clear(from.ID)
		b.send(ctx, chatID, b.texts.T(id.Locale(), "bot.already_linked", nil), nil)
		b.showHome(ctx, chatID, nil, from, "")
		return
	}
	b.pending.put(from.ID, code)
	b.send(ctx, chatID, b.texts.T(locale, "bot.ask_email", nil), nil)
}

func (b *Bot) acceptInvitation(ctx context.Context, chatID int64, from *models.User, code, email string) {
	locale := NormalizeLocale(from.LanguageCode)
	email = strings.TrimSpace(email)
	if !looksLikeEmail(email) {
		b.send(ctx, chatID, b.texts.T(locale, "bot.ask_email_again", nil), nil)
		return
	}
	res, err := b.arena.AcceptInvitation(ctx, AcceptInvitationRequest{
		Code:             code,
		Email:            email,
		TelegramUserID:   from.ID,
		TelegramUsername: from.Username,
		Locale:           locale,
	})
	if err != nil {
		switch {
		case IsAPIError(err, http.StatusNotFound):
			b.pending.clear(from.ID)
			b.send(ctx, chatID, b.texts.T(locale, "bot.invite_not_found", nil), nil)
		case IsAPIError(err, http.StatusUnprocessableEntity):
			b.send(ctx, chatID, b.texts.T(locale, "bot.invite_email_mismatch", nil), nil)
		case IsAPIError(err, http.StatusConflict):
			b.pending.clear(from.ID)
			b.send(ctx, chatID, b.texts.T(locale, "bot.invite_already_linked", nil), nil)
		default:
			b.logger.Error("eventbot: accept invitation failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
			b.send(ctx, chatID, b.texts.T(locale, "bot.invite_failed", nil), nil)
		}
		return
	}
	b.pending.clear(from.ID)
	roleKey := "bot.role_manager"
	if res.Role == "owner" {
		roleKey = "bot.role_owner"
	}
	b.send(ctx, chatID, b.texts.T(res.Locale, "bot.invite_accepted", map[string]any{
		"Org":  Esc(res.OrgName),
		"Role": b.texts.T(res.Locale, roleKey, nil),
	}), nil)
	b.showHome(ctx, chatID, nil, from, "")
}

func looksLikeEmail(s string) bool {
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\n") && strings.Contains(s[at:], ".")
}

// ─── screens ──────────────────────────────────────────────────────────────────

func (b *Bot) showHome(ctx context.Context, chatID int64, editMsgID *int, from *models.User, prefix string) {
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	text := prefix + b.texts.T(id.Locale(), "bot.menu_title", map[string]any{"Org": Esc(id.Current.OrgName)})
	b.reply(ctx, chatID, editMsgID, text, b.homeKeyboard(id))
}

func (b *Bot) homeKeyboard(id *Identity) *models.InlineKeyboardMarkup {
	loc := id.Locale()
	rows := [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.wz.new_event_btn", nil), CallbackData: "wz:new"}},
		{{Text: b.texts.T(loc, "bot.btn_events", nil), CallbackData: "events:1"}},
	}
	if len(id.Memberships) > 1 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_org", nil), CallbackData: "org"}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: b.texts.T(loc, "bot.btn_lang", nil), CallbackData: "lang"},
		{Text: b.texts.T(loc, "bot.btn_help", nil), CallbackData: "help"},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) showHelp(ctx context.Context, chatID int64, editMsgID *int, from *models.User) {
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(id.Locale(), "bot.help", nil), b.backKeyboard(id.Locale(), "home"))
}

func (b *Bot) showOrgChooser(ctx context.Context, chatID int64, editMsgID *int, from *models.User) {
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	b.showOrgChooserFor(ctx, chatID, editMsgID, id)
}

func (b *Bot) showOrgChooserFor(ctx context.Context, chatID int64, editMsgID *int, id *Identity) {
	loc := id.Locale()
	if len(id.Memberships) == 0 {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.org_none", nil), nil)
		return
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(id.Memberships)+1)
	for _, m := range id.Memberships {
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(m.OrgName, 48), CallbackData: "org:" + m.OrgID.String()}})
	}
	if id.Current != nil {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.org_choose", nil), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) switchOrg(ctx context.Context, chatID int64, editMsgID *int, from *models.User, orgID uuid.UUID) {
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	for i := range id.Memberships {
		if id.Memberships[i].OrgID == orgID {
			if err := b.queries.SetBotTelegramLinkCurrentOrg(ctx, from.ID, &orgID); err != nil {
				b.logger.Error("eventbot: set current org failed", slog.String("error", err.Error()))
				b.reply(ctx, chatID, editMsgID, b.texts.T(id.Locale(), "bot.error_generic", nil), nil)
				return
			}
			prefix := b.texts.T(id.Locale(), "bot.org_switched", map[string]any{"Org": Esc(id.Memberships[i].OrgName)}) + "\n\n"
			b.showHome(ctx, chatID, editMsgID, from, prefix)
			return
		}
	}
	b.showOrgChooserFor(ctx, chatID, editMsgID, id)
}

func (b *Bot) showLangChooser(ctx context.Context, chatID int64, editMsgID *int, from *models.User) {
	id, _, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	rows := [][]models.InlineKeyboardButton{
		{{Text: "English", CallbackData: "lang:en"}, {Text: "Русский", CallbackData: "lang:ru"}},
		{{Text: b.texts.T(id.Locale(), "bot.btn_home", nil), CallbackData: "home"}},
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(id.Locale(), "bot.lang_choose", nil), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) switchLang(ctx context.Context, chatID int64, editMsgID *int, from *models.User, raw string) {
	locale := NormalizeLocale(raw)
	if err := b.queries.UpdateBotTelegramLinkLocale(ctx, from.ID, locale); err != nil {
		b.logger.Error("eventbot: set locale failed", slog.String("error", err.Error()))
	}
	b.showHome(ctx, chatID, editMsgID, from, b.texts.T(locale, "bot.lang_set", nil)+"\n\n")
}

func (b *Bot) backKeyboard(locale, target string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(locale, "bot.btn_home", nil), CallbackData: target}},
	}}
}

// ─── plumbing ─────────────────────────────────────────────────────────────────

func (b *Bot) replyIdentityError(ctx context.Context, chatID int64, from *models.User, err error) {
	locale := NormalizeLocale(from.LanguageCode)
	if errors.Is(err, ErrNotLinked) {
		b.send(ctx, chatID, b.texts.T(locale, "bot.not_invited", nil), nil)
		return
	}
	b.logger.Error("eventbot: resolve identity failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
	b.send(ctx, chatID, b.texts.T(locale, "bot.error_generic", nil), nil)
}

// reply edits the message the button came from, or sends a new one.
func (b *Bot) reply(ctx context.Context, chatID int64, editMsgID *int, text string, kb *models.InlineKeyboardMarkup) {
	if editMsgID != nil {
		params := &tgbot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: *editMsgID,
			Text:      text,
			ParseMode: models.ParseModeHTML,
		}
		if kb != nil {
			params.ReplyMarkup = kb
		}
		if _, err := b.tg.EditMessageText(ctx, params); err == nil || strings.Contains(err.Error(), "message is not modified") {
			return
		}
	}
	b.send(ctx, chatID, text, kb)
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, kb *models.InlineKeyboardMarkup) {
	params := &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: tgbot.True(),
		},
	}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	if _, err := b.tg.SendMessage(ctx, params); err != nil {
		b.logger.Warn("eventbot: sendMessage failed", slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
	}
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
