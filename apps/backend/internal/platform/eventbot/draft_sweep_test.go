package eventbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// memDrafts stands in for bot_drafts + bot_telegram_links: the three queries
// the sweep uses, with the same idle and reminded_at rules as the SQL.
type memDrafts struct {
	mu       sync.Mutex
	rows     []gen.BotDraftReminderRow
	reminded map[uuid.UUID]bool
}

func newMemDrafts(rows ...gen.BotDraftReminderRow) *memDrafts {
	return &memDrafts{rows: rows, reminded: map[uuid.UUID]bool{}}
}

func (m *memDrafts) ListBotDraftsForReminder(_ context.Context, idleBefore time.Time, limit int32) ([]gen.BotDraftReminderRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []gen.BotDraftReminderRow
	for _, r := range m.rows {
		if r.UpdatedAt.Before(idleBefore) && !m.reminded[r.ID] && int32(len(out)) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memDrafts) MarkBotDraftReminded(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reminded[id] = true
	return nil
}

func (m *memDrafts) DeleteBotDraftsIdleBefore(_ context.Context, idleBefore time.Time) ([]gen.BotDraftReminderRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var gone, kept []gen.BotDraftReminderRow
	for _, r := range m.rows {
		if r.UpdatedAt.Before(idleBefore) {
			gone = append(gone, r)
		} else {
			kept = append(kept, r)
		}
	}
	m.rows = kept
	return gone, nil
}

func (m *memDrafts) isReminded(id uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reminded[id]
}

// sentMessage is what the fake Telegram received.
type sentMessage struct {
	chatID int64
	text   string
	kb     *models.InlineKeyboardMarkup
}

// fakeSender records messages and fails on demand per chat.
type fakeSender struct {
	mu   sync.Mutex
	sent []sentMessage
	fail map[int64]error
}

func (f *fakeSender) send(_ context.Context, chatID int64, text string, kb *models.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[chatID]; err != nil {
		return err
	}
	f.sent = append(f.sent, sentMessage{chatID, text, kb})
	return nil
}

func (f *fakeSender) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func draftRow(tg int64, locale, name string, updated time.Time) gen.BotDraftReminderRow {
	state, _ := json.Marshal(map[string]any{"version": 2, "event": map[string]any{"name": name}})
	return gen.BotDraftReminderRow{ID: uuid.New(), TelegramUserID: tg, OrgID: uuid.New(), Mode: "create", State: state, Locale: locale, UpdatedAt: updated}
}

func newTestSweeper(t *testing.T, q draftQueries, s *fakeSender) *draftSweeper {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	return &draftSweeper{q: q, texts: NewTexts(bundle), send: s.send, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func callbacks(kb *models.InlineKeyboardMarkup) []string {
	var out []string
	if kb == nil {
		return out
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

var sweepNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// A draft idle for a day is reminded once; the row is marked only after the
// message was delivered, so the next pass leaves it alone.
func TestDraftSweep_ReminderSentOnce(t *testing.T) {
	t.Parallel()
	idle := draftRow(11, "ru", "Концерт <джаз>", sweepNow.Add(-25*time.Hour))
	fresh := draftRow(12, "ru", "Свежий", sweepNow.Add(-2*time.Hour))
	q := newMemDrafts(idle, fresh)
	snd := &fakeSender{}
	sw := newTestSweeper(t, q, snd)

	res := sw.sweep(context.Background(), sweepNow)
	if res.Reminded != 1 || res.Failed != 0 || res.Deleted != 0 {
		t.Fatalf("first pass = %+v, want exactly one reminder", res)
	}
	msgs := snd.messages()
	if len(msgs) != 1 || msgs[0].chatID != 11 {
		t.Fatalf("messages = %+v, want one to chat 11", msgs)
	}
	if !strings.Contains(msgs[0].text, "Концерт &lt;джаз&gt;") || !strings.Contains(msgs[0].text, "7 дней") {
		t.Errorf("reminder must name the (escaped) event and the 7 days: %q", msgs[0].text)
	}
	if got := callbacks(msgs[0].kb); len(got) != 2 || got[0] != "wz:resume" || got[1] != "wz:cancel" {
		t.Errorf("reminder buttons = %v, want continue and delete", got)
	}
	if !q.isReminded(idle.ID) || q.isReminded(fresh.ID) {
		t.Errorf("only the idle draft may be marked reminded")
	}

	res = sw.sweep(context.Background(), sweepNow.Add(time.Minute))
	if res.Reminded != 0 || len(snd.messages()) != 1 {
		t.Errorf("a reminded draft must not be reminded again: %+v, %d messages", res, len(snd.messages()))
	}
}

// A draft without a name reads well too, and a failed send leaves the draft
// unmarked so the next pass retries it.
func TestDraftSweep_FailedSendIsRetried(t *testing.T) {
	t.Parallel()
	unnamed := draftRow(21, "en", "", sweepNow.Add(-30*time.Hour))
	other := draftRow(22, "en", "Second", sweepNow.Add(-30*time.Hour))
	q := newMemDrafts(unnamed, other)
	snd := &fakeSender{fail: map[int64]error{21: errors.New("telegram is down")}}
	sw := newTestSweeper(t, q, snd)

	res := sw.sweep(context.Background(), sweepNow)
	if res.Failed != 1 || res.Reminded != 1 {
		t.Fatalf("pass 1 = %+v, want one failure and one reminder (a failure must not stop the batch)", res)
	}
	if q.isReminded(unnamed.ID) || !q.isReminded(other.ID) {
		t.Fatalf("reminded flags wrong after a failure: unnamed=%v other=%v", q.isReminded(unnamed.ID), q.isReminded(other.ID))
	}

	delete(snd.fail, 21)
	res = sw.sweep(context.Background(), sweepNow.Add(10*time.Minute))
	if res.Reminded != 1 || !q.isReminded(unnamed.ID) {
		t.Fatalf("pass 2 = %+v, want the failed reminder retried", res)
	}
	for _, m := range snd.messages() {
		if m.chatID == 21 && (strings.Contains(m.text, "{{") || strings.Contains(m.text, "“”") || strings.Contains(m.text, "bot.draft")) {
			t.Errorf("an unnamed draft must read cleanly: %q", m.text)
		}
	}
}

// A person who blocked the bot is recorded as reminded, or the sweep would
// retry the closed chat every pass for a week.
func TestDraftSweep_UnreachableChatIsNotRetried(t *testing.T) {
	t.Parallel()
	row := draftRow(31, "en", "Gone", sweepNow.Add(-26*time.Hour))
	q := newMemDrafts(row)
	snd := &fakeSender{fail: map[int64]error{31: fmt.Errorf("%w: forbidden", errChatUnreachable)}}
	sw := newTestSweeper(t, q, snd)

	res := sw.sweep(context.Background(), sweepNow)
	if res.Unreachable != 1 || res.Failed != 0 || !q.isReminded(row.ID) {
		t.Fatalf("result = %+v reminded=%v, want recorded without a retry", res, q.isReminded(row.ID))
	}
}

// A draft past its seven days is deleted and its owner told in their own
// language; it does not also get a reminder.
func TestDraftSweep_DeletionNoticeInOwnersLocale(t *testing.T) {
	t.Parallel()
	old := draftRow(41, "es", "Viejo", sweepNow.Add(-8*24*time.Hour))
	q := newMemDrafts(old)
	snd := &fakeSender{}
	sw := newTestSweeper(t, q, snd)

	res := sw.sweep(context.Background(), sweepNow)
	if res.Deleted != 1 || res.Reminded != 0 {
		t.Fatalf("result = %+v, want one deletion and no reminder", res)
	}
	msgs := snd.messages()
	if len(msgs) != 1 || msgs[0].chatID != 41 {
		t.Fatalf("messages = %+v", msgs)
	}
	if !strings.Contains(msgs[0].text, "borrador") || !strings.Contains(msgs[0].text, "7 días") {
		t.Errorf("deletion notice must be Spanish: %q", msgs[0].text)
	}
	if got := callbacks(msgs[0].kb); len(got) != 1 || got[0] != "wz:new" {
		t.Errorf("deletion notice buttons = %v, want the + Event button", got)
	}
	if len(q.rows) != 0 {
		t.Errorf("the draft must be gone from the table")
	}
}

// An empty or unsupported locale falls back to English, never to a key name.
func TestDraftSweep_UnknownLocaleFallsBack(t *testing.T) {
	t.Parallel()
	for _, loc := range []string{"", "xx", "he"} {
		row := draftRow(51, loc, "", sweepNow.Add(-8*24*time.Hour))
		n := draftDeletedNotice(newTestSweeper(t, newMemDrafts(), &fakeSender{}).texts, row)
		if !strings.Contains(n.Text, "Your event draft was deleted") {
			t.Errorf("locale %q: notice = %q, want English", loc, n.Text)
		}
		r := draftReminderNotice(newTestSweeper(t, newMemDrafts(), &fakeSender{}).texts, row)
		if strings.Contains(r.Text, "bot.draft") || !strings.Contains(r.Text, "7 days") {
			t.Errorf("locale %q: reminder = %q, want English", loc, r.Text)
		}
	}
}

// More due drafts than one batch: the rest wait for the next pass.
func TestDraftSweep_BatchLimit(t *testing.T) {
	t.Parallel()
	var rows []gen.BotDraftReminderRow
	for i := 0; i < draftSweepBatch+5; i++ {
		rows = append(rows, draftRow(int64(1000+i), "en", "", sweepNow.Add(-30*time.Hour)))
	}
	q := newMemDrafts(rows...)
	snd := &fakeSender{}
	sw := newTestSweeper(t, q, snd)

	if res := sw.sweep(context.Background(), sweepNow); res.Reminded != draftSweepBatch {
		t.Fatalf("pass 1 reminded %d, want %d", res.Reminded, draftSweepBatch)
	}
	if res := sw.sweep(context.Background(), sweepNow); res.Reminded != 5 {
		t.Fatalf("pass 2 reminded %d, want the remaining 5", res.Reminded)
	}
}

func TestDraftName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"event":{"name":"  Jazz night "}}`: "Jazz night",
		`{"event":{}}`:                       "",
		`not json`:                           "",
		``:                                   "",
	}
	for in, want := range cases {
		if got := draftName(json.RawMessage(in)); got != want {
			t.Errorf("draftName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("я", 200)
	if got := draftName(json.RawMessage(`{"event":{"name":"` + long + `"}}`)); len([]rune(got)) != draftNameMax {
		t.Errorf("a long name must be cut to %d runes, got %d", draftNameMax, len([]rune(got)))
	}
}
