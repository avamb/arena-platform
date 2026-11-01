package eventbot

// bot_analytics.go — the bot's behaviour events for the platform's own
// product analytics (spec 08_architecture/50_product_analytics_ru.md §4.4).
//
// What is recorded: that an update arrived (kind and the button's prefix), that
// the wizard moved from one step to another and how long it stayed, why it
// refused an answer (a reason CODE), and how a wizard ended. What is never
// recorded: any text a person typed, any value of a field, any id of an event,
// venue or order. The dictionary (analytics.Events) drops everything else, and
// the callers below only ever pass constants and step names.
//
// Tracking never blocks and never fails the update: analytics.Writer.Track only
// puts the event into a buffer, the person is resolved from a short in-memory
// cache, and a nil botAnalytics (analytics off, or a unit test with no
// database) is a no-op on every method.

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
)

const (
	// analyticsWhoTTL is how long who a Telegram account is (linked user or
	// stranger) and which organization it works in is remembered. Short, so a
	// freshly accepted invitation or a switched organization shows up soon.
	analyticsWhoTTL = 30 * time.Second
	// analyticsLookupTimeout bounds the few indexed reads tracking may make.
	analyticsLookupTimeout = 2 * time.Second
	// analyticsWhoMax caps the cache; when it is full it is simply emptied.
	analyticsWhoMax = 20000
)

// analyticsQueries is the slice of gen.Queries tracking reads; unit tests put
// a fake behind it.
type analyticsQueries interface {
	GetBotTelegramLink(ctx context.Context, telegramUserID int64) (gen.BotTelegramLinkRow, error)
	GetBotDraft(ctx context.Context, telegramUserID int64, orgID uuid.UUID) (gen.BotDraftRow, error)
	ListBotDialogKindsByRecency(ctx context.Context, telegramUserID int64) ([]string, error)
	GetBotDialog(ctx context.Context, telegramUserID int64, kind string) (gen.BotDialogRow, error)
}

// whoEntry is who a Telegram account is, as analytics identifies them.
type whoEntry struct {
	kind  string
	value string
	org   *uuid.UUID
	until time.Time
}

// botAnalytics writes the bot's events through an analytics.Tracker.
type botAnalytics struct {
	sink   analytics.Tracker
	hasher *analytics.TGHasher
	q      analyticsQueries
	now    func() time.Time

	mu  sync.Mutex
	who map[int64]whoEntry
}

// newBotAnalytics returns nil (every method a no-op) when there is no sink.
// A typed-nil *analytics.Writer counts as no sink as well.
func newBotAnalytics(sink analytics.Tracker, signingSecret string, q analyticsQueries) *botAnalytics {
	if sink == nil {
		return nil
	}
	if w, ok := sink.(*analytics.Writer); ok && w == nil {
		return nil
	}
	return &botAnalytics{
		sink:   sink,
		hasher: analytics.NewTGHasher(signingSecret),
		q:      q,
		now:    time.Now,
		who:    map[int64]whoEntry{},
	}
}

// identify says who a Telegram account is: the linked platform user, else the
// keyed hash of the Telegram id. A failed read falls back to the hash.
func (a *botAnalytics) identify(ctx context.Context, tg int64) whoEntry {
	now := a.now()
	a.mu.Lock()
	if e, ok := a.who[tg]; ok && now.Before(e.until) {
		a.mu.Unlock()
		return e
	}
	a.mu.Unlock()

	e := whoEntry{kind: analytics.IDKindTG, value: a.hasher.Hash(tg), until: now.Add(analyticsWhoTTL)}
	if a.q != nil {
		lctx, cancel := context.WithTimeout(ctx, analyticsLookupTimeout)
		link, err := a.q.GetBotTelegramLink(lctx, tg)
		cancel()
		switch {
		case err == nil && link.RevokedAt == nil && link.UserID != uuid.Nil:
			e.kind, e.value, e.org = analytics.IDKindUser, link.UserID.String(), link.CurrentOrgID
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			// Not cached: the next event asks again.
			e.until = now
		}
	}
	a.mu.Lock()
	if len(a.who) >= analyticsWhoMax {
		a.who = map[int64]whoEntry{}
	}
	a.who[tg] = e
	a.mu.Unlock()
	return e
}

// forget drops what is remembered about an account, so the next event asks the
// database again: called when its link or its current organization changed.
func (a *botAnalytics) forget(tg int64) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.who, tg)
	a.mu.Unlock()
}

// track records one event of the Telegram account tg. org is the organization
// the person is working in when the caller knows it; nil takes the account's
// current organization.
func (a *botAnalytics) track(ctx context.Context, tg int64, org *uuid.UUID, name string, props map[string]any) {
	if a == nil {
		return
	}
	who := a.identify(ctx, tg)
	if org == nil {
		org = who.org
	}
	a.sink.Track(ctx, analytics.Event{
		At:      a.now(),
		Surface: analytics.SurfaceBot,
		Name:    name,
		IDKind:  who.kind,
		IDValue: who.value,
		OrgID:   org,
		Props:   props,
	})
}

// clock is the bot's time for analytics stamps: the injectable clock of the
// analytics recorder when there is one, else the wall clock.
func (b *Bot) clock() time.Time {
	if b.an != nil && b.an.now != nil {
		return b.an.now()
	}
	return time.Now()
}

var analyticsPrefixRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

// updateKind and updatePrefix describe an update for bot_update: the kind
// (message / callback / command) and, for a button, the part of its data before
// the first ':' (never the rest) or, for a command, the command word. A prefix
// that is not a short plain token is dropped.
func updateKind(u *models.Update) (kind, prefix string) {
	switch {
	case u.CallbackQuery != nil:
		kind = "callback"
		prefix, _, _ = strings.Cut(u.CallbackQuery.Data, ":")
	case u.Message != nil:
		text := strings.TrimSpace(u.Message.Text)
		if strings.HasPrefix(text, "/") {
			kind = "command"
			prefix, _, _ = strings.Cut(strings.TrimPrefix(text, "/"), " ")
			prefix, _, _ = strings.Cut(prefix, "@")
			prefix = strings.ToLower(prefix)
		} else {
			kind = "message"
		}
	default:
		return "", ""
	}
	if !analyticsPrefixRe.MatchString(prefix) {
		prefix = ""
	}
	return kind, prefix
}

// update records bot_update for an admitted update, with the step the person
// is on when it arrives. It runs inside the sender's lane, before the handler.
func (a *botAnalytics) update(ctx context.Context, u *models.Update, from *models.User) {
	if a == nil || from == nil {
		return
	}
	kind, prefix := updateKind(u)
	if kind == "" {
		return
	}
	props := map[string]any{"kind": kind}
	if prefix != "" {
		props["prefix"] = prefix
	}
	if step := a.currentStep(ctx, from.ID); step != "" {
		props["step"] = step
	}
	a.track(ctx, from.ID, nil, "bot_update", props)
}

// currentStep is the step the account is on: the most recent live dialog as
// "<kind>:<step>" (the team invite, sessions, the organizer application, ...),
// else the open wizard draft's step, else "".
func (a *botAnalytics) currentStep(ctx context.Context, tg int64) string {
	if a.q == nil {
		return ""
	}
	qctx, cancel := context.WithTimeout(ctx, analyticsLookupTimeout)
	defer cancel()
	if kinds, err := a.q.ListBotDialogKindsByRecency(qctx, tg); err == nil && len(kinds) > 0 {
		if row, err := a.q.GetBotDialog(qctx, tg, kinds[0]); err == nil && a.now().Before(row.ExpiresAt) {
			return kinds[0] + ":" + row.Step
		}
	}
	who := a.identify(ctx, tg)
	if who.org == nil {
		return ""
	}
	if row, err := a.q.GetBotDraft(qctx, tg, *who.org); err == nil {
		return row.Step
	}
	return ""
}
