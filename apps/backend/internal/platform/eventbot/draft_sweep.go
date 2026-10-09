package eventbot

// draft_sweep.go — what happens to a wizard draft nobody touches (spec 28
// §5.3, spec 35 §4.1, EC-01). A draft lives in bot_drafts and is kept for
// seven days after the last answer; a day after the last answer the person is
// reminded once, with a button that takes them back into the wizard. A draft
// that ran out is deleted and its owner told, so "where did my event go" has
// an answer in the chat and not only in a table.
//
// The decision of WHAT to say and to WHOM is the pure functions below; the
// sweeper only moves rows and sends the result, which is what lets the unit
// tests run it without a Telegram.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	// draftReminderAfter is how long a draft may sit unanswered before its
	// owner is reminded of it.
	draftReminderAfter = 24 * time.Hour
	// draftKeepFor is how long a draft survives without an answer.
	draftKeepFor = 7 * 24 * time.Hour
	// draftSweepEvery is how often the sweep runs. A reminder therefore
	// arrives up to ten minutes after the 24-hour mark, which nobody notices.
	draftSweepEvery = 10 * time.Minute
	// draftSweepBatch caps the reminders of one pass, so a bot that was down
	// for a day does not send a burst that Telegram's own limits would refuse.
	// Whatever is left is picked up by the next pass.
	draftSweepBatch = 100
	// draftNameMax keeps a long event name from crowding the reminder.
	draftNameMax = 80
)

// draftQueries is the slice of gen.Queries the sweep uses; the unit tests put
// an in-memory table behind it, so the idle rules under test are the ones the
// bot runs with.
type draftQueries interface {
	ListBotDraftsForReminder(ctx context.Context, idleBefore time.Time, limit int32) ([]gen.BotDraftReminderRow, error)
	MarkBotDraftReminded(ctx context.Context, id uuid.UUID) error
	DeleteBotDraftsIdleBefore(ctx context.Context, idleBefore time.Time) ([]gen.BotDraftReminderRow, error)
}

// draftSender delivers one message to a private chat (its id is the Telegram
// user id). A nil error means Telegram accepted it.
type draftSender func(ctx context.Context, chatID int64, text string, kb *models.InlineKeyboardMarkup) error

// errChatUnreachable is a send that can never succeed — the person blocked the
// bot or deleted the chat. The reminder is then marked as sent anyway, or the
// sweep would knock on the same closed door every ten minutes until the draft
// itself expires.
var errChatUnreachable = errors.New("eventbot: chat unreachable")

// draftNotice is one message for one chat.
type draftNotice struct {
	ChatID   int64
	Text     string
	Keyboard *models.InlineKeyboardMarkup
}

// draftReminderNotice is the one-day reminder. It names the event when the
// draft already has a name, and offers the two things a person can want:
// carry on, or throw the draft away. "Delete" is the wizard's own cancel
// screen ("wz:cancel"), which asks before it deletes — a reminder must not
// turn one stray tap into the loss of an hour of answers.
func draftReminderNotice(t *Texts, row gen.BotDraftReminderRow) draftNotice {
	loc := NormalizeLocale(row.Locale)
	key := "bot.draft_reminder"
	var data map[string]any
	if name := draftName(row.State); name != "" {
		key = "bot.draft_reminder_named"
		data = map[string]any{"Name": Esc(name)}
	}
	return draftNotice{
		ChatID: row.TelegramUserID,
		Text:   t.T(loc, key, data),
		Keyboard: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: t.T(loc, "bot.wz.resume_btn", nil), CallbackData: "wz:resume"}},
			{{Text: t.T(loc, "bot.wz.cancel_drop_btn", nil), CallbackData: "wz:cancel"}},
		}},
	}
}

// draftDeletedNotice tells the owner the draft is gone, with the way to begin
// a new one. An unknown or empty locale reads in English, as everywhere else.
func draftDeletedNotice(t *Texts, row gen.BotDraftReminderRow) draftNotice {
	loc := NormalizeLocale(row.Locale)
	return draftNotice{
		ChatID: row.TelegramUserID,
		Text:   t.T(loc, "bot.draft_deleted", nil),
		Keyboard: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: t.T(loc, "bot.wz.new_event_btn", nil), CallbackData: "wz:new"}},
		}},
	}
}

// draftName reads the event name out of a stored draft without depending on
// the draft's schema version: a draft the wizard can no longer open still
// has the name the person typed. "" when there is none or the state is not
// readable.
func draftName(state json.RawMessage) string {
	var s struct {
		Event struct {
			Name string `json:"name"`
		} `json:"event"`
	}
	if json.Unmarshal(state, &s) != nil {
		return ""
	}
	return truncate(s.Event.Name, draftNameMax)
}

// draftSweeper applies the idle rules to bot_drafts.
type draftSweeper struct {
	q      draftQueries
	texts  *Texts
	send   draftSender
	logger *slog.Logger
}

// draftSweepResult counts what one pass did, for the log line and the tests.
type draftSweepResult struct {
	Deleted     int // drafts removed after draftKeepFor
	Reminded    int // reminders delivered and recorded
	Unreachable int // reminders recorded without delivery, see errChatUnreachable
	Failed      int // sends that failed and will be retried next pass
}

// sweep runs one pass at `now`: expire first, then remind. Doing them in this
// order means a draft that is already past its seven days is deleted with its
// goodbye and never gets a "you have a draft" message a moment earlier. One
// failing row never stops the rest.
func (s *draftSweeper) sweep(ctx context.Context, now time.Time) draftSweepResult {
	var res draftSweepResult

	gone, err := s.q.DeleteBotDraftsIdleBefore(ctx, now.Add(-draftKeepFor))
	if err != nil {
		s.warn(ctx, "eventbot: draft expiry failed", err)
	}
	for _, row := range gone {
		res.Deleted++
		n := draftDeletedNotice(s.texts, row)
		// The draft is already deleted, so a failed notice is not retried:
		// there is nothing left to key a retry on.
		if err := s.send(ctx, n.ChatID, n.Text, n.Keyboard); err != nil {
			s.warn(ctx, "eventbot: draft deleted notice failed", err, slog.Int64("telegram_user_id", row.TelegramUserID))
		}
	}

	due, err := s.q.ListBotDraftsForReminder(ctx, now.Add(-draftReminderAfter), draftSweepBatch)
	if err != nil {
		s.warn(ctx, "eventbot: draft reminder listing failed", err)
	}
	for _, row := range due {
		if ctx.Err() != nil {
			break
		}
		n := draftReminderNotice(s.texts, row)
		err := s.send(ctx, n.ChatID, n.Text, n.Keyboard)
		switch {
		case err == nil:
			res.Reminded++
		case errors.Is(err, errChatUnreachable):
			res.Unreachable++
		default:
			// Not marked: the next pass sends it again.
			res.Failed++
			s.warn(ctx, "eventbot: draft reminder failed", err, slog.Int64("telegram_user_id", row.TelegramUserID))
			continue
		}
		// Marked only after Telegram took the message (or never will), so a
		// crash between the two costs a duplicate reminder, never a lost one.
		if err := s.q.MarkBotDraftReminded(ctx, row.ID); err != nil {
			s.warn(ctx, "eventbot: draft reminder not recorded", err, slog.String("draft_id", row.ID.String()))
		}
	}

	if res.Deleted+res.Reminded+res.Unreachable+res.Failed > 0 {
		s.logger.Info("eventbot: idle drafts swept",
			slog.Int("deleted", res.Deleted), slog.Int("reminded", res.Reminded),
			slog.Int("unreachable", res.Unreachable), slog.Int("failed", res.Failed))
	}
	return res
}

// warn logs a failure unless it is only the bot shutting down.
func (s *draftSweeper) warn(ctx context.Context, msg string, err error, attrs ...any) {
	if ctx.Err() != nil {
		return
	}
	s.logger.Warn(msg, append(attrs, slog.String("error", err.Error()))...)
}

// draftSweepLoop reminds and expires idle drafts until ctx ends. A missed
// pass only means the next one has more to do.
func (b *Bot) draftSweepLoop(ctx context.Context) {
	every := b.draftSweepEvery
	if every <= 0 {
		every = draftSweepEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.sweepDrafts(ctx, time.Now())
		}
	}
}

// sweepDrafts is one pass of the loop at `now`.
func (b *Bot) sweepDrafts(ctx context.Context, now time.Time) {
	if b.queries == nil {
		return
	}
	s := &draftSweeper{q: b.queries, texts: b.texts, send: b.sendChecked, logger: b.logger}
	s.sweep(ctx, now)
}

// sendChecked is send() that reports the outcome: the sweep must know
// whether a reminder arrived before it records it as sent.
func (b *Bot) sendChecked(ctx context.Context, chatID int64, text string, kb *models.InlineKeyboardMarkup) error {
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
		// 403 is "bot was blocked by the user" and its relatives.
		if errors.Is(err, tgbot.ErrorForbidden) {
			return fmt.Errorf("%w: %v", errChatUnreachable, err)
		}
		return err
	}
	return nil
}
