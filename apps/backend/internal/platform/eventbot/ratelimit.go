package eventbot

// ratelimit.go — at most 30 messages a minute from one Telegram account
// (spec 35 §4.4, carried over from spec 28). Every update costs the bot a
// database read, a JWT and usually an arena-api call, and the bot runs ONE
// worker, so one person holding a key down (or a script) would make every
// other organizer wait. Updates over the limit are dropped; the person hears
// "too many messages" once per burst, not once per dropped update, because an
// answer to every one of them would itself be the flood.
//
// Typed messages and button presses have SEPARATE windows. A person walking
// the event wizard presses calendar pages, categories and "next" far faster
// than anyone types, so one shared 30-a-minute budget would throttle an
// honest organizer half-way through an event; buttons get a wider budget of
// their own instead.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
)

const (
	// messageRateLimit is how many messages (text, photos, documents) one
	// Telegram account may send within messageRateWindow.
	messageRateLimit  = 30
	messageRateWindow = time.Minute
	// callbackRateLimit is how many button presses one account may make in
	// the same window, counted apart from its messages.
	callbackRateLimit = 120
	// ratePruneMinCalls is the least number of calls between two prunes of
	// idle accounts; together with "at least as many calls as there are
	// accounts" it keeps pruning O(1) amortised per call.
	ratePruneMinCalls = 256
)

// userRateLimiter is a sliding-window limit per Telegram account. The bot
// runs one worker, but the mutex keeps it correct should that ever change.
type userRateLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	users      map[int64]*rateWindow
	sincePrune int
}

// rateWindow holds the instants of one account's admitted updates within the
// window (at most limit of them, oldest first) and whether the account has
// already been told it is over the limit.
type rateWindow struct {
	stamps  []time.Time
	refused bool
}

func newUserRateLimiter(limit int, window time.Duration) *userRateLimiter {
	return &userRateLimiter{limit: limit, window: window, users: map[int64]*rateWindow{}}
}

// Allow admits one update from id at now. A refused update answers
// allowed=false, and firstRefusal is true only for the first refusal since
// the account was last admitted, so the caller says "wait a minute" once.
// A limit of zero or less admits everything.
func (l *userRateLimiter) Allow(id int64, now time.Time) (allowed bool, firstRefusal bool) {
	if l == nil || l.limit <= 0 {
		return true, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maybePrune(now)

	w := l.users[id]
	if w == nil {
		w = &rateWindow{stamps: make([]time.Time, 0, l.limit)}
		l.users[id] = w
	}
	cutoff := now.Add(-l.window)
	keep := 0
	for keep < len(w.stamps) && !w.stamps[keep].After(cutoff) {
		keep++
	}
	if keep > 0 {
		// Shift in place so the backing array never grows past limit.
		w.stamps = w.stamps[:copy(w.stamps, w.stamps[keep:])]
	}
	if len(w.stamps) >= l.limit {
		first := !w.refused
		w.refused = true
		return false, first
	}
	w.stamps = append(w.stamps, now)
	w.refused = false
	return true, false
}

// maybePrune forgets accounts with nothing left in their window. It runs once
// at least as many calls have passed as there are accounts, so its O(n) scan
// is paid for by those calls. The caller holds mu.
func (l *userRateLimiter) maybePrune(now time.Time) {
	l.sincePrune++
	if l.sincePrune < ratePruneMinCalls || l.sincePrune < len(l.users) {
		return
	}
	l.sincePrune = 0
	cutoff := now.Add(-l.window)
	for id, w := range l.users {
		if len(w.stamps) == 0 || !w.stamps[len(w.stamps)-1].After(cutoff) {
			delete(l.users, id)
		}
	}
}

// admit applies the per-account limit to one update — the button window for a
// callback query, the message window for everything else. It answers false
// when the update must be dropped; on the first refusal of a burst it has
// already told the person to wait, and a refused button press always has its
// spinner stopped so Telegram does not show it loading forever.
func (b *Bot) admit(ctx context.Context, u *models.Update, from *models.User) bool {
	limiter := b.limiter
	if u.CallbackQuery != nil {
		limiter = b.cbLimiter
	}
	allowed, first := limiter.Allow(from.ID, time.Now())
	if allowed {
		return true
	}
	chatID := from.ID // a private chat's id is the user's id
	if cq := u.CallbackQuery; cq != nil {
		_, _ = b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID})
		if cq.Message.Message != nil {
			chatID = cq.Message.Message.Chat.ID
		}
	} else if u.Message != nil {
		chatID = u.Message.Chat.ID
	}
	if first {
		b.send(ctx, chatID, b.texts.T(b.limitLocale(ctx, from), "bot.too_many_messages", nil), nil)
	}
	return false
}

// limitLocale is the language of the "too many messages" answer: the linked
// account's own, else the one Telegram reports, as for any stranger. It reads
// only the bot's table — no /v1/me call for an update that is being dropped.
func (b *Bot) limitLocale(ctx context.Context, from *models.User) string {
	if b.queries != nil {
		link, err := b.queries.GetBotTelegramLink(ctx, from.ID)
		if err == nil && link.RevokedAt == nil {
			return NormalizeLocale(link.Locale)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			b.logger.Debug("eventbot: rate-limit locale lookup failed", slog.String("error", err.Error()))
		}
	}
	return NormalizeLocale(from.LanguageCode)
}
