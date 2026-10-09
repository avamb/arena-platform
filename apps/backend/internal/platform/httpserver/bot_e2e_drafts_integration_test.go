//go:build integration

package httpserver

// End-to-end proof of the idle-draft rules (spec 28 §5.3, spec 35 §4.1): a
// wizard draft nobody answers for a day is announced once in the owner's
// language, and one that waited seven days is deleted with a goodbye. The
// bot's sweep runs here every 200 ms against a real database. Run against a
// migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/ -run BotE2E_Draft

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// startDraftTestBot is startDialogTestBot with the draft sweep sped up to
// the test's pace.
func startDraftTestBot(t *testing.T, pool *pgxpool.Pool, api *httptest.Server, tg *stubTelegram) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		DraftSweepEvery:   200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("bot.Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("bot did not stop after cancel")
		}
	})
}

// countSince is how many messages after mark contain needle.
func (s *stubTelegram) countSince(mark int, needle string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.sent[mark:] {
		if strings.Contains(m, needle) {
			n++
		}
	}
	return n
}

func TestBotE2E_DraftReminderThenExpiry(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	const ownerTG = int64(783)
	linkDialogTestOwner(t, f, srv, ownerTG)

	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bot_drafts WHERE telegram_user_id = $1`, ownerTG)
	})

	// A draft the owner left on the very first answer, as the wizard stores it.
	d := eventbot.NewDraft()
	d.Event.Name = "Jazz night"
	state, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("encode draft: %v", err)
	}
	if _, err := gen.New(pool).UpsertBotDraft(ctx, ownerTG, f.orgID, eventbot.ModeCreate, nil, d.Step, int32(d.Version), state, nil); err != nil {
		t.Fatalf("store draft: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE bot_drafts SET updated_at = now() - interval '25 hours' WHERE telegram_user_id = $1`, ownerTG); err != nil {
		t.Fatalf("age the draft: %v", err)
	}

	tg := newStubTelegram(t)
	m := tg.mark()
	startDraftTestBot(t, pool, api, tg)

	// A day without an answer: one reminder, in the owner's language, naming
	// the event, and the draft is marked so it is not repeated.
	reminder := tg.waitSince(t, m, "Вы не закончили ивент")
	if !strings.Contains(reminder, "Jazz night") || !strings.Contains(reminder, "7 дней") {
		t.Fatalf("the reminder must name the event and the 7 days:\n%s", reminder)
	}
	var remindedSet bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx,
			`SELECT reminded_at IS NOT NULL FROM bot_drafts WHERE telegram_user_id = $1`, ownerTG).Scan(&remindedSet); err == nil && remindedSet {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !remindedSet {
		t.Fatal("reminded_at must be set once the reminder was sent")
	}
	time.Sleep(800 * time.Millisecond) // four more sweeps
	if n := tg.countSince(m, "Вы не закончили ивент"); n != 1 {
		t.Fatalf("the reminder must be sent once, was sent %d times", n)
	}

	// "Continue" is the wizard's own resume button: the draft opens again.
	m = tg.mark()
	tg.push(e2eCallbackAs(ownerTG, "wz:resume"))
	tg.waitSince(t, m, "") // any screen of the wizard
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_drafts WHERE telegram_user_id = $1`, ownerTG).Scan(&left); err != nil || left != 1 {
		t.Fatalf("resuming must keep the draft: %d rows (%v)", left, err)
	}

	// A week without an answer: the draft is deleted and the owner is told.
	if _, err := pool.Exec(ctx,
		`UPDATE bot_drafts SET updated_at = now() - interval '8 days' WHERE telegram_user_id = $1`, ownerTG); err != nil {
		t.Fatalf("age the draft further: %v", err)
	}
	m = tg.mark()
	tg.waitSince(t, m, "Черновик ивента удалён")
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_drafts WHERE telegram_user_id = $1`, ownerTG).Scan(&left); err != nil || left != 0 {
		t.Fatalf("an expired draft must be gone: %d rows (%v)", left, err)
	}
}
