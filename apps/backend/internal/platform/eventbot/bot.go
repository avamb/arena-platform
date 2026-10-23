package eventbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	// SelfOnboarding (BOT_SELF_ONBOARDING_ENABLED) offers a stranger the
	// organizer application instead of "you need an invitation".
	SelfOnboarding bool
	// NoticeEvery is how often decisions on applications are announced
	// (default 45 s); tests shorten it.
	NoticeEvery time.Duration
	// SiteButton (BOT_ONBOARDING_SITE_BUTTON) shows "Open on the website"
	// in the application dialog; keep it off until the site's continue page
	// (/start/confirm) is live.
	SiteButton bool
	// DialogSweepEvery is how often long-expired bot_dialogs rows are
	// deleted (default 10 min); tests shorten it.
	DialogSweepEvery time.Duration
	// DraftSweepEvery is how often idle wizard drafts are reminded of and
	// expired (default 10 min); tests shorten it.
	DraftSweepEvery time.Duration
	// MessageRateLimit caps the messages one Telegram account may send per
	// minute: 0 means the spec's 30, a negative value switches the limit off.
	// Button presses have their own fixed budget (callbackRateLimit). Only
	// end-to-end tests that type faster than any person should ever set it.
	MessageRateLimit int
}

// Bot is the running event-center bot.
type Bot struct {
	tg       *tgbot.Bot
	queries  *gen.Queries
	arena    *ArenaClient
	minter   *TokenMinter
	texts    *Texts
	logger   *slog.Logger
	pending  *pendingInvites
	sessions *sessionDialogs
	// dialogs keeps the short multi-step dialogs (the team invite, ...) in
	// bot_dialogs, so a restart loses none of them.
	dialogs          DialogStore
	textRoutes       map[string]dialogTextHandler // nil: dialogTexts (tests replace it)
	dialogSweepEvery time.Duration
	draftSweepEvery  time.Duration
	// limiter is the per-account message limit (spec 35 §4.4); cbLimiter is
	// the separate, wider one for button presses.
	limiter   *userRateLimiter
	cbLimiter *userRateLimiter
	wizard    *Wizard
	// fileClient downloads Telegram files (posters); nil uses a default.
	fileClient     *http.Client
	ticketsBaseURL string
	// posterOffUntil (Unix seconds) holds poster reading back after arena-api
	// answered that it has no model key.
	posterOffUntil atomic.Int64

	selfOnboarding bool
	noticeEvery    time.Duration
	siteButton     bool
	onb            *onbDialogs
	onbForms       *onbFormCache

	// serial runs one account's updates one at a time, in order (serial.go).
	serial *userSerial
	// route handles one admitted update inside its sender's turn; tests
	// replace it to drive the dispatch without a database.
	route func(ctx context.Context, u *models.Update)
	// runID names this process (a refund dialog marks the send it started
	// with it, so a restart is told from a send still under way).
	runID string
	// bg tracks the work started off the single update worker (a refund
	// call that may wait minutes for the provider), so Run can let it end.
	bg bgGroup
	// retrying holds the refund ids whose queue retry is under way.
	retrying sync.Map
	// feed remembers which of the bot's messages still carry buttons, per
	// private chat (feed.go): a press on any other message is refused.
	feed *feedMemory
	// menuKB remembers the chats that got the "☰ Menu" keyboard
	// (menu_keyboard.go).
	menuKB *menuShown
}

// bgGroup is the WaitGroup of the background work. Once Run starts waiting
// for it (close), no new work joins: a WaitGroup must not be added to while
// it is being waited on, so later work runs on the caller's goroutine.
type bgGroup struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// add joins the group and reports false once it is closed.
func (g *bgGroup) add() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.wg.Add(1)
	return true
}

func (g *bgGroup) done() { g.wg.Done() }

// close stops new work from joining; wait then waits for what joined.
func (g *bgGroup) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

func (g *bgGroup) wait() { g.wg.Wait() }

// backgroundDrain bounds how long Run waits for that work on shutdown.
const backgroundDrain = 15 * time.Second

// goBackground runs fn off the update worker and tracks it. A panic in fn
// is recovered and logged (it must not take the bot down for everyone), and
// cleanup, when given, then puts the person's dialog back in a usable state.
//
// It reports false, and runs nothing, once Run has begun its shutdown wait:
// the caller is a handler holding the person's lane, and work that writes the
// dialog through that lane would wait for itself. The caller then tells the
// person nothing was started.
func (b *Bot) goBackground(name string, fn, cleanup func()) bool {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error("eventbot: background work panicked", slog.String("work", name),
					slog.Any("panic", r), slog.String("stack", string(debug.Stack())))
				if cleanup != nil {
					b.runCleanup(name, cleanup)
				}
			}
		}()
		fn()
	}
	if !b.bg.add() {
		return false
	}
	go func() {
		defer b.bg.done()
		run()
	}()
	return true
}

// detached is the context of work that outlives its handler: no deadline and
// no cancel, and WITHOUT the handler's per-account values. context.WithoutCancel
// alone keeps them, among them the account's turn marker (serial.go), and
// serial.do would then run that work inline, outside the account's lane,
// racing the person's next update; and the save-before-reply hook
// (reply_hook.go) of a dialog the work does not own. Background work writes
// dialog state through Bot.inPersonsTurn on a context made from this one.
func detached(ctx context.Context) context.Context {
	ctx = context.WithValue(context.WithoutCancel(ctx), turnKey{}, nil)
	// Nor the person's action (feed.go): an answer that arrives later is a
	// new message, never an edit of whatever the action had sent.
	ctx = context.WithValue(ctx, feedActionKey{}, (*feedAction)(nil))
	return context.WithValue(ctx, beforeReplyKey{}, nil)
}

// runCleanup runs a panicked work's clean-up, itself guarded.
func (b *Bot) runCleanup(name string, cleanup func()) {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error("eventbot: background clean-up panicked", slog.String("work", name), slog.Any("panic", r))
		}
	}()
	cleanup()
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
		runID:   uuid.NewString(),
		queries: opts.Queries,
		arena:   opts.Arena,
		minter:  opts.Minter,
		texts:   opts.Texts,
		logger:  logger,
		pending: newPendingInvites(pendingInviteTTL),

		dialogs:          newPGDialogStore(opts.Queries),
		dialogSweepEvery: opts.DialogSweepEvery,
		draftSweepEvery:  opts.DraftSweepEvery,
		limiter:          newUserRateLimiter(rateLimitFrom(opts.MessageRateLimit), messageRateWindow),
		cbLimiter:        newUserRateLimiter(callbackRateLimit, messageRateWindow),

		fileClient:     opts.HTTPClient,
		ticketsBaseURL: opts.TicketsBaseURL,

		selfOnboarding: opts.SelfOnboarding,
		noticeEvery:    opts.NoticeEvery,
		siteButton:     opts.SiteButton,
		onbForms:       &onbFormCache{m: map[string]onbFormEntry{}},
		feed:           newFeedMemory(),
		menuKB:         newMenuShown(),
	}
	b.sessions = newSessionDialogs(b.dialogs)
	b.onb = newOnbDialogs(b.dialogs)
	b.serial = newUserSerial(logger)
	b.route = b.routeUpdate
	if b.texts == nil {
		b.texts = NewTexts(nil)
	}
	var refs RefIO = opts.Arena
	if opts.Refs != nil {
		refs = opts.Refs
	}
	b.wizard = NewWizard(b.texts, refs).WithPosterHints(true)
	tgOpts := []tgbot.Option{
		tgbot.WithDefaultHandler(b.handleUpdate),
		tgbot.WithSkipGetMe(),
		// One reader that calls handleUpdate synchronously, in the order
		// Telegram delivered the updates. handleUpdate only queues the update
		// in its sender's lane (serial.go), so handlers of different accounts
		// still run concurrently while one account's updates — "e-mail after
		// /start", "next page after page 1" — never overtake each other.
		// Without WithNotAsyncHandlers the library starts a goroutine per
		// update and that order is lost before handleUpdate runs.
		tgbot.WithWorkers(1),
		tgbot.WithNotAsyncHandlers(),
	}
	if opts.TelegramServerURL != "" {
		tgOpts = append(tgOpts, tgbot.WithServerURL(opts.TelegramServerURL))
	}
	// Every request to Telegram first writes the dialog step its handler
	// registered (reply_hook.go), so the step is stored before the person sees
	// the question. Without opts.HTTPClient this is the library's own default:
	// a one-minute poll on a client with the same timeout.
	if opts.HTTPClient != nil {
		tgOpts = append(tgOpts, tgbot.WithHTTPClient(15*time.Second, saveBeforeReplyClient{opts.HTTPClient}))
	} else {
		tgOpts = append(tgOpts, tgbot.WithHTTPClient(time.Minute, saveBeforeReplyClient{&http.Client{Timeout: time.Minute}}))
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
	if b.selfOnboarding {
		go b.onbNoticeLoop(ctx)
	}
	go b.webNoticeLoop(ctx)
	go b.dialogSweepLoop(ctx)
	go b.draftSweepLoop(ctx)
	go b.recoverLostRefundSends(ctx)
	b.tg.Start(ctx)
	done := make(chan struct{})
	b.bg.close()
	go func() { b.bg.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(backgroundDrain):
		b.logger.Warn("eventbot: background work still running at shutdown")
	}
	return nil
}

// rateLimitFrom turns Options.MessageRateLimit into the limiter's limit:
// 0 is the default, a negative value is "no limit" (the limiter admits
// everything when its limit is not positive).
func rateLimitFrom(opt int) int {
	if opt == 0 {
		return messageRateLimit
	}
	return opt
}

func (b *Bot) publishCommands(ctx context.Context) {
	for _, locale := range SupportedLocales {
		cmds := []models.BotCommand{
			{Command: "start", Description: b.texts.T(locale, "bot.cmd_start", nil)},
			{Command: "menu", Description: b.texts.T(locale, "bot.cmd_menu", nil)},
			{Command: "events", Description: b.texts.T(locale, "bot.cmd_events", nil)},
			{Command: "new", Description: b.texts.T(locale, "bot.cmd_new", nil)},
			{Command: "team", Description: b.texts.T(locale, "bot.cmd_team", nil)},
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
		b.publishAbout(ctx, locale)
	}
}

// publishAbout sets what Telegram shows in the EMPTY chat, above the Start
// button, before the person has pressed anything: a long description and the
// short one of the bot's profile. It is the first screen a person sees when
// they open the invitation link, so it says what the bot does and how to get
// in. Telegram caps them at 512 and 120 characters (checked in the tests).
func (b *Bot) publishAbout(ctx context.Context, locale string) {
	code := ""
	if locale != "en" {
		code = locale
	}
	if _, err := b.tg.SetMyDescription(ctx, &tgbot.SetMyDescriptionParams{
		Description:  b.texts.T(locale, "bot.about_description", nil),
		LanguageCode: code,
	}); err != nil {
		b.logger.Warn("eventbot: setMyDescription failed", slog.String("locale", locale), slog.String("error", err.Error()))
	}
	if _, err := b.tg.SetMyShortDescription(ctx, &tgbot.SetMyShortDescriptionParams{
		ShortDescription: b.texts.T(locale, "bot.about_short", nil),
		LanguageCode:     code,
	}); err != nil {
		b.logger.Warn("eventbot: setMyShortDescription failed", slog.String("locale", locale), slog.String("error", err.Error()))
	}
}

// ─── update dispatch ──────────────────────────────────────────────────────────

// handleUpdate is the single entry point for every Telegram update. The
// library calls it on its one reader goroutine, in delivery order, so it must
// not block: it applies the rate limit (a map lookup) and queues the rest of
// the work in the sender's lane, where it runs after that account's earlier
// updates and never at the same time as them.
func (b *Bot) handleUpdate(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	from := updateSender(u)
	if from == nil {
		return
	}
	// The limit is checked here, before anything is queued, so a flood costs
	// one map lookup per update rather than a database read and an API call.
	allowed, first := b.allow(u, from)
	err := b.serial.submit(from.ID, func() {
		tctx := withTurn(ctx, from.ID)
		if !allowed {
			b.refuse(tctx, u, from, first)
			return
		}
		b.route(tctx, u)
	})
	if err != nil {
		b.logger.Warn("eventbot: update dropped", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
	}
}

// updateSender is the account an update comes from, or nil for an update the
// bot ignores (a bot's own message, a group chat, an update kind it does not
// handle).
func updateSender(u *models.Update) *models.User {
	switch {
	case u.CallbackQuery != nil:
		return &u.CallbackQuery.From
	case u.Message != nil && u.Message.From != nil && !u.Message.From.IsBot:
		if string(u.Message.Chat.Type) != "private" {
			return nil // the bot works in private chats only
		}
		return u.Message.From
	}
	return nil
}

// routeUpdate handles one admitted update; it runs inside the sender's turn.
func (b *Bot) routeUpdate(ctx context.Context, u *models.Update) {
	if u.CallbackQuery != nil {
		b.handleCallback(ctx, u.CallbackQuery)
		return
	}
	b.handleMessage(ctx, u.Message)
}

func (b *Bot) handleMessage(ctx context.Context, m *models.Message) {
	text := strings.TrimSpace(m.Text)
	from := m.From
	chatID := m.Chat.ID
	// Whatever the bot answers is a new message; a new keyboard then takes
	// the buttons from the older screens (feed.go).
	ctx = withFeedAction(ctx, textAction(chatID))
	if b.isMenuButton(text) {
		// The "☰ Menu" button under the message field (menu_keyboard.go).
		b.openMenu(ctx, chatID, from, false)
		return
	}
	if strings.HasPrefix(text, "/") {
		cmd, arg, _ := strings.Cut(text, " ")
		cmd = strings.ToLower(strings.TrimSpace(cmd))
		if i := strings.Index(cmd, "@"); i > 0 {
			cmd = cmd[:i]
		}
		if b.menuCommand(ctx, cmd, strings.TrimSpace(arg), from) {
			// /menu and a plain /start leave every dialog themselves, and
			// ask first when a refund waits for its confirmation.
			b.openMenu(ctx, chatID, from, cmd == "/start")
			return
		}
		// Every command ends every dialog (dialog_owners.go): a word typed
		// after it is never an answer to a question asked before it.
		b.leaveForCommand(ctx, from.ID)
		switch cmd {
		case "/start":
			arg = strings.TrimSpace(arg)
			if strings.HasPrefix(arg, "inv_") {
				b.startInvitation(ctx, chatID, from, strings.TrimPrefix(arg, "inv_"))
				return
			}
			if arg == "apply" && b.onbOn() {
				if _, _, err := b.resolveIdentity(ctx, from.ID); errors.Is(err, ErrNotLinked) {
					b.onbApplyDirect(ctx, chatID, from)
					return
				}
			}
			b.showHome(ctx, chatID, nil, from, "")
			return
		case "/events":
			b.showEventsFresh(ctx, chatID, nil, from, "")
			return
		case "/new":
			b.wizardStart(ctx, chatID, nil, from, false)
			return
		case "/team":
			b.showTeam(ctx, chatID, nil, from, "")
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
	// A typed text goes to the newest open dialog that takes it.
	if text != "" && b.routeDialogText(ctx, chatID, from, text) {
		return
	}
	// A picture or a location answers an open Materials question first
	// (materials_input.go): a dialog is open only while its screen is the
	// latest one the person pressed.
	if (m.Document != nil || len(m.Photo) > 0) && b.materialsPhoto(ctx, chatID, from, m) {
		return
	}
	if m.Location != nil && b.materialsLocation(ctx, chatID, from, m.Location) {
		return
	}
	if m.Document != nil || len(m.Photo) > 0 {
		if b.wizardPoster(ctx, chatID, from, m) {
			return
		}
	} else if text != "" && b.wizardText(ctx, chatID, from, text) {
		return
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		if b.onbOn() && errors.Is(err, ErrNotLinked) {
			b.onbMessage(ctx, chatID, from, m)
			return
		}
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	b.send(ctx, chatID, b.texts.T(id.Locale(), "bot.unknown_input", nil), b.homeKeyboard(ctx, id, jwt))
}

func (b *Bot) handleCallback(ctx context.Context, cq *models.CallbackQuery) {
	if cq.Message.Message == nil {
		_, _ = b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID})
		return
	}
	chatID := cq.Message.Message.Chat.ID
	msgID := cq.Message.Message.ID
	// A button of a message that is no longer the live one does nothing
	// (feed.go): the person is told to carry on below.
	if !b.feed.admit(chatID, msgID, int64(cq.Message.Message.Date)) {
		b.refuseStalePress(ctx, cq)
		return
	}
	_, _ = b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID})
	ctx = withFeedAction(ctx, pressAction(cq))
	from := &cq.From
	parts := strings.Split(cq.Data, ":")
	// A press ends every dialog it does not belong to (dialog_owners.go).
	b.leaveForeignDialogs(ctx, from.ID, cq.Data)
	switch parts[0] {
	case "el":
		b.eventsCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "el:"))
	case "ec":
		b.ecCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "ec:"))
	case "web":
		b.webCallback(ctx, chatID, from, strings.TrimPrefix(cq.Data, "web:"))
	case "or":
		b.ordersCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "or:"))
	case "iv":
		b.inviteCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "iv:"))
	case "rq":
		b.queueCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "rq:"))
	case "pm":
		b.promoCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "pm:"))
	case "pr":
		b.promoterCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "pr:"))
	case "mt":
		b.materialsCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "mt:"))
	case "ct":
		b.categoryCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "ct:"))
	case "og":
		b.orgDataCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "og:"))
	case "home":
		b.showHome(ctx, chatID, &msgID, from, "")
	case menuCallback:
		b.menuPress(ctx, chatID, from, strings.TrimPrefix(cq.Data, menuCallback+":"))
	case "help":
		b.showHelp(ctx, chatID, &msgID, from)
	case "scanner":
		b.showScanner(ctx, chatID, &msgID, from)
	case "events":
		page := 0 // 0: the page the list was on
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
	case "sample":
		if len(parts) > 1 {
			b.sampleCallback(ctx, chatID, from, parts[1])
		}
	case "onb":
		b.onbCallback(ctx, chatID, msgID, from, cq.Data)
	case "ses":
		b.sessionsCallback(ctx, chatID, msgID, from, strings.TrimPrefix(cq.Data, "ses:"))
	case "team":
		b.teamCallback(ctx, chatID, msgID, from, strings.TrimPrefix(strings.TrimPrefix(cq.Data, "team"), ":"))
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
	// The first thing a person reads in the bot: where they are and the three
	// steps to a published event, with the language one tap away in case the
	// guess from Telegram's own language was wrong.
	b.send(ctx, chatID, b.texts.T(res.Locale, "bot.invite_accepted", map[string]any{
		"Org":   Esc(res.OrgName),
		"Role":  b.texts.T(res.Locale, roleKey, nil),
		"Owner": res.Role == "owner",
	}), languageRow())
	b.notifyInviter(ctx, res, from)
	b.showHome(ctx, chatID, nil, from, "")
}

// notifyInviter tells whoever issued an accepted invitation that the person
// has joined, in the inviter's own language. Best effort: an issuer without a
// bot link (the superadmin's API call) is simply not told, and a failed send
// never touches the new member's own flow.
func (b *Bot) notifyInviter(ctx context.Context, res AcceptInvitationResponse, from *models.User) {
	userID, err := uuid.Parse(res.UserID)
	if err != nil {
		return
	}
	orgID, err := uuid.Parse(res.OrgID)
	if err != nil {
		return
	}
	inviter, err := b.queries.GetBotInvitationInviterLink(ctx, userID, orgID, from.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			b.logger.Warn("eventbot: inviter lookup failed", slog.String("error", err.Error()))
		}
		return
	}
	loc := NormalizeLocale(inviter.Locale)
	roleKey := "bot.role_manager"
	if res.Role == "owner" {
		roleKey = "bot.role_owner"
	}
	b.send(ctx, inviter.TelegramUserID, b.texts.T(loc, "bot.invite_accepted_notice", map[string]any{
		"Email": Esc(res.Email),
		"Org":   Esc(res.OrgName),
		"Role":  b.texts.T(loc, roleKey, nil),
	}), nil)
}

func looksLikeEmail(s string) bool {
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\n") && strings.Contains(s[at:], ".")
}

// ─── screens ──────────────────────────────────────────────────────────────────

func (b *Bot) showHome(ctx context.Context, chatID int64, editMsgID *int, from *models.User, prefix string) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	// A linked person always has the "☰ Menu" button (menu_keyboard.go).
	b.ensureMenuKeyboard(ctx, chatID, id.Locale(), false)
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	text := prefix + b.texts.T(id.Locale(), "bot.menu_title", map[string]any{"Org": Esc(id.Current.OrgName)}) + b.paymentsNote(ctx, id, jwt)
	b.reply(ctx, chatID, editMsgID, text, b.homeKeyboard(ctx, id, jwt))
}

// homeKeyboard is the main menu. An owner who is still alone in the
// organization sees "+ Team member" (straight into the invite dialog)
// instead of a "Team" screen that would list only themselves; once a
// colleague exists the button opens the team screen.
func (b *Bot) homeKeyboard(ctx context.Context, id *Identity, jwt string) *models.InlineKeyboardMarkup {
	loc := id.Locale()
	rows := [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.wz.new_event_btn", nil), CallbackData: "wz:new"}},
		{{Text: b.texts.T(loc, "bot.btn_events", nil), CallbackData: "el:new"}},
	}
	if canViewSales(id) {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ord.btn", nil), CallbackData: "or:new"}})
	}
	if row := b.queueMenuRow(loc, id); row != nil { // refunds (EC-23)
		rows = append(rows, row)
	}
	if canInvite(id) {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.inv.btn", nil), CallbackData: "iv:new"}})
	}
	if row := b.promoMenuRow(loc, id); row != nil { // promo codes (EC-11)
		rows = append(rows, row)
	}
	if row := b.promoterMenuRow(loc, id); row != nil { // promoters (EC-14)
		rows = append(rows, row)
	}
	if row := b.orgDataMenuRow(loc, id); row != nil { // organization data (HP-4a)
		rows = append(rows, row)
	}
	if isOwner(id) {
		if b.ownerIsAlone(ctx, id, jwt) {
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_team_add", nil), CallbackData: "team:invite"}})
		} else {
			rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_team", nil), CallbackData: "team"}})
		}
	} else if isTeamReader(id) { // the manager reads the team, never changes it (V-11)
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_team", nil), CallbackData: "team"}})
	}
	if len(id.Memberships) > 1 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_org", nil), CallbackData: "org"}})
	}
	if row := b.webMenuRow(loc, id); row != nil { // the web event center (spec 37 §4.3)
		rows = append(rows, row)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ec.notif_btn", nil), CallbackData: "ec:nt"}})
	rows = append(rows, b.scannerRow(loc))
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
		languageRow().InlineKeyboard[0],
		{{Text: b.texts.T(id.Locale(), "bot.btn_home", nil), CallbackData: "home"}},
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(id.Locale(), "bot.lang_choose", nil), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// languageRow is the one row of buttons that switches the bot's language; the
// names are written in their own language on purpose.
func languageRow() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "English", CallbackData: "lang:en"},
		{Text: "Русский", CallbackData: "lang:ru"},
		{Text: "Español", CallbackData: "lang:es"},
	}}}
}

func (b *Bot) switchLang(ctx context.Context, chatID int64, editMsgID *int, from *models.User, raw string) {
	locale := NormalizeLocale(raw)
	if err := b.queries.UpdateBotTelegramLinkLocale(ctx, from.ID, locale); err != nil {
		b.logger.Error("eventbot: set locale failed", slog.String("error", err.Error()))
	} else {
		// The menu button carries its label: send it in the new language.
		b.ensureMenuKeyboard(ctx, chatID, locale, true)
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
		if b.onbOn() {
			b.onbWelcome(ctx, chatID, from)
			return
		}
		b.send(ctx, chatID, b.texts.T(locale, "bot.not_invited", nil), nil)
		return
	}
	b.logger.Error("eventbot: resolve identity failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
	b.send(ctx, chatID, b.texts.T(locale, "bot.error_generic", nil), nil)
}

// reply answers in the chat. editMsgID names the message the answer
// belongs to (the pressed one, or a screen a dialog remembers): the answer is
// a NEW message at the bottom and that message loses its keyboard, except
// where feed.go allows a redraw in place (a tick in a list, a page, a
// message this same action already sent).
func (b *Bot) reply(ctx context.Context, chatID int64, editMsgID *int, text string, kb *models.InlineKeyboardMarkup) {
	if err := b.replyChecked(ctx, chatID, editMsgID, text, kb); err != nil {
		b.logger.Warn("eventbot: sendMessage failed", slog.Int64("chat_id", chatID), slog.String("error", err.Error()))
	}
}

// send is sendChecked for the callers that cannot do anything with a failure
// but log it.
func (b *Bot) send(ctx context.Context, chatID int64, text string, kb *models.InlineKeyboardMarkup) {
	if err := b.sendChecked(ctx, chatID, text, kb); err != nil {
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
