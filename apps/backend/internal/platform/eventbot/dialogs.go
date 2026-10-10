package eventbot

// dialogs.go — where the bot's short multi-step dialogs live between two
// messages (spec 35 §4.1, EC-01). Until migration 0130 each dialog kept its
// own map in process memory, so a restart or a deploy dropped it without a
// word: an owner typed a colleague's e-mail after a deploy, the bot no longer
// knew it was waiting for one, and nothing reached the API. DialogStore puts
// that memory in bot_dialogs. One live dialog per Telegram account and kind,
// a 30-minute expiry that slides on every answer, and an expired dialog is
// reported ONCE ("this dialog has expired") instead of the answer silently
// falling through to whatever handles stray text.
//
// The event wizard is not a dialog in this sense: its draft is far richer,
// is kept for days and lives in bot_drafts (wizard_bot.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	// dialogTTL is how long a dialog waits for the next answer; every answer
	// starts the 30 minutes again.
	dialogTTL = 30 * time.Minute
	// dialogSweepEvery is how often expired dialogs are deleted.
	dialogSweepEvery = 10 * time.Minute
	// dialogKeepExpired is how long the sweep leaves an expired dialog in the
	// table. A person who comes back an hour later with the e-mail they were
	// asked for should still hear "this dialog has expired" rather than have
	// the address taken for a stray message, so only dialogs abandoned for a
	// day are swept; Load deletes the rest when it reports them.
	dialogKeepExpired = 24 * time.Hour
)

// DialogStore keeps the state of the bot's short dialogs between messages.
//
// Load answers a live dialog with found=true and its step, and decodes its
// state into `into` (a pointer; nil skips decoding). A dialog whose expiry has
// passed is deleted and reported once as expired=true, found=false; every
// later Load answers found=false, expired=false, as for a dialog that never
// existed. Save writes the whole state and always moves the expiry to
// now+ttl. Delete of a missing dialog is not an error.
type DialogStore interface {
	Save(ctx context.Context, tg int64, orgID *uuid.UUID, kind, step string, state any, ttl time.Duration) error
	Load(ctx context.Context, tg int64, kind string, into any) (step string, found, expired bool, err error)
	Delete(ctx context.Context, tg int64, kind string) error
}

// dialogQueries is the slice of gen.Queries the store uses; the unit tests
// stand an in-memory table behind it, so the expiry rules below are the ones
// under test.
type dialogQueries interface {
	UpsertBotDialog(ctx context.Context, telegramUserID int64, orgID *uuid.UUID, kind, step string, state json.RawMessage, expiresAt time.Time) (gen.BotDialogRow, error)
	GetBotDialog(ctx context.Context, telegramUserID int64, kind string) (gen.BotDialogRow, error)
	DeleteBotDialog(ctx context.Context, telegramUserID int64, kind string) error
}

// errNoDialogDatabase is what the store answers when the bot was built
// without a database. New refuses that already; the store stays defensive so
// a mis-wired test fails with a sentence, not a nil-pointer panic.
var errNoDialogDatabase = errors.New("eventbot: dialog store has no database")

// pgDialogStore is the production DialogStore over bot_dialogs.
type pgDialogStore struct {
	q   dialogQueries
	now func() time.Time
}

// newPGDialogStore wraps the bot's queries. A nil *gen.Queries leaves q a
// true nil interface, so every call answers errNoDialogDatabase.
func newPGDialogStore(q *gen.Queries) *pgDialogStore {
	s := &pgDialogStore{now: time.Now}
	if q != nil {
		s.q = q
	}
	return s
}

func (s *pgDialogStore) Save(ctx context.Context, tg int64, orgID *uuid.UUID, kind, step string, state any, ttl time.Duration) error {
	if s.q == nil {
		return errNoDialogDatabase
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("eventbot: encode %s dialog: %w", kind, err)
	}
	if _, err := s.q.UpsertBotDialog(ctx, tg, orgID, kind, step, raw, s.now().Add(ttl)); err != nil {
		return fmt.Errorf("eventbot: save %s dialog: %w", kind, err)
	}
	return nil
}

func (s *pgDialogStore) Load(ctx context.Context, tg int64, kind string, into any) (string, bool, bool, error) {
	if s.q == nil {
		return "", false, false, errNoDialogDatabase
	}
	row, err := s.q.GetBotDialog(ctx, tg, kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("eventbot: load %s dialog: %w", kind, err)
	}
	if !s.now().Before(row.ExpiresAt) {
		// Deleting is what makes "expired" a one-time answer: the next Load
		// finds nothing and the person's text goes wherever it would have gone
		// without a dialog.
		if err := s.q.DeleteBotDialog(ctx, tg, kind); err != nil {
			return "", false, false, fmt.Errorf("eventbot: drop expired %s dialog: %w", kind, err)
		}
		return "", false, true, nil
	}
	if into != nil && len(row.State) > 0 {
		if err := json.Unmarshal(row.State, into); err != nil {
			return "", false, false, fmt.Errorf("eventbot: decode %s dialog: %w", kind, err)
		}
	}
	return row.Step, true, false, nil
}

func (s *pgDialogStore) Delete(ctx context.Context, tg int64, kind string) error {
	if s.q == nil {
		return errNoDialogDatabase
	}
	if err := s.q.DeleteBotDialog(ctx, tg, kind); err != nil {
		return fmt.Errorf("eventbot: delete %s dialog: %w", kind, err)
	}
	return nil
}

// dialogSweepLoop deletes long-expired dialogs until ctx ends. Expired rows
// cost nothing but space, so a missed pass only means the next one does more.
func (b *Bot) dialogSweepLoop(ctx context.Context) {
	every := b.dialogSweepEvery
	if every <= 0 {
		every = dialogSweepEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.sweepDialogs(ctx, time.Now())
		}
	}
}

// sweepDialogs deletes the dialogs that expired more than dialogKeepExpired
// before now.
func (b *Bot) sweepDialogs(ctx context.Context, now time.Time) {
	if b.queries == nil {
		return
	}
	n, err := b.queries.DeleteExpiredBotDialogs(ctx, now.Add(-dialogKeepExpired))
	if err != nil {
		if ctx.Err() == nil {
			b.logger.Warn("eventbot: dialog sweep failed", slog.String("error", err.Error()))
		}
		return
	}
	if n > 0 {
		b.logger.Info("eventbot: expired dialogs swept", slog.Int64("count", n))
	}
}
