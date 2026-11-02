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

// analyticsQueries is the one read tracking makes (who is this account, cached);
// unit tests put a fake behind it.
type analyticsQueries interface {
	GetBotTelegramLink(ctx context.Context, telegramUserID int64) (gen.BotTelegramLinkRow, error)
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

	mu    sync.Mutex
	who   map[int64]whoEntry
	steps map[int64]*stepState
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
		steps:  map[int64]*stepState{},
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
			if !botCommands[prefix] {
				prefix = "other"
			}
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

// botCommands are the commands the bot registers (publishCommands). Any other
// "/word" a person types is stored as prefix "other": a typed "/john.smith"
// must not put a name into the table.
var botCommands = map[string]bool{
	"start": true, "menu": true, "events": true, "new": true,
	"team": true, "org": true, "lang": true, "help": true,
}

// begin snapshots an admitted update and its step (from memory, no database)
// and returns the function that records bot_update. The caller runs it AFTER
// the handler, so the identity lookup on a cache miss never delays the reply.
func (a *botAnalytics) begin(u *models.Update, from *models.User) func(ctx context.Context) {
	noop := func(context.Context) {}
	if a == nil || from == nil {
		return noop
	}
	kind, prefix := updateKind(u)
	if kind == "" {
		return noop
	}
	props := map[string]any{"kind": kind}
	if prefix != "" {
		props["prefix"] = prefix
	}
	if step := a.stepOf(from.ID); step != "" {
		props["step"] = step
	}
	tg := from.ID
	return func(ctx context.Context) { a.track(ctx, tg, nil, "bot_update", props) }
}

// stepNote is a step and when it was noted.
type stepNote struct {
	step string
	at   time.Time
}

// stepState is what is remembered about where one account is: its open wizard
// draft and its latest dialog.
type stepState struct {
	draft      stepNote
	dialog     stepNote
	dialogKind string
}

// noteDraft remembers the step of the account's wizard draft.
func (a *botAnalytics) noteDraft(tg int64, step string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stateLocked(tg).draft = stepNote{step: step, at: a.now()}
}

// clearDraft forgets the draft step: the draft was deleted.
func (a *botAnalytics) clearDraft(tg int64) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, ok := a.steps[tg]; ok {
		st.draft = stepNote{}
	}
}

// noteDialog remembers the latest dialog as "<kind>:<step>".
func (a *botAnalytics) noteDialog(tg int64, kind, step string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.stateLocked(tg)
	st.dialog = stepNote{step: kind + ":" + step, at: a.now()}
	st.dialogKind = kind
}

// clearDialog forgets the dialog when it is the one noted last.
func (a *botAnalytics) clearDialog(tg int64, kind string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, ok := a.steps[tg]; ok && st.dialogKind == kind {
		st.dialog, st.dialogKind = stepNote{}, ""
	}
}

func (a *botAnalytics) stateLocked(tg int64) *stepState {
	if st, ok := a.steps[tg]; ok {
		return st
	}
	if len(a.steps) >= analyticsWhoMax {
		a.steps = map[int64]*stepState{}
	}
	st := &stepState{}
	a.steps[tg] = st
	return st
}

// stepOf is the step the account is on, "" when unknown: the latest dialog
// while it can still be live (dialogTTL), else the wizard draft (draftKeepFor).
// It is kept in memory by the writers of dialogs and drafts, so it costs no
// database read; a restart forgets it until the next answer.
func (a *botAnalytics) stepOf(tg int64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.steps[tg]
	if !ok {
		return ""
	}
	now := a.now()
	if st.dialog.step != "" && now.Sub(st.dialog.at) < dialogTTL {
		return st.dialog.step
	}
	if st.draft.step != "" && now.Sub(st.draft.at) < draftKeepFor {
		return st.draft.step
	}
	return ""
}

// stepDialogs notes every dialog write in the analytics step memory.
type stepDialogs struct {
	DialogStore
	an *botAnalytics
}

func (s stepDialogs) Save(ctx context.Context, tg int64, orgID *uuid.UUID, kind, step string, state any, ttl time.Duration) error {
	err := s.DialogStore.Save(ctx, tg, orgID, kind, step, state, ttl)
	if err == nil {
		s.an.noteDialog(tg, kind, step)
	}
	return err
}

func (s stepDialogs) Delete(ctx context.Context, tg int64, kind string) error {
	err := s.DialogStore.Delete(ctx, tg, kind)
	if err == nil {
		s.an.clearDialog(tg, kind)
	}
	return err
}
