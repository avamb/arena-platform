//go:build integration

package httpserver

// The guest list of a session (owner decision 2026-11-02, package guestlist,
// migration 0187), through the real bot against the stub Telegram:
//
//   - "👥 Список гостей" on a date's card sends guest-list.csv as a document,
//     one row per active ticket, sorted by the guest's name;
//   - the toggle on the Notifications screen turns the automatic list on and
//     off (guest_list_subscriptions);
//   - the guest_list.schedule pass at "24 hours before the start" writes one
//     delivery, the bot's loop sends it to the owner's private chat, and a
//     second pass at the same moment sends nothing more. The manager who did
//     not turn it on gets nothing.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// startGuestListTestBot is startDialogTestBot with a fast notice loop, so the
// automatic list is claimed within a second.
func startGuestListTestBot(t *testing.T, pool *pgxpool.Pool, api *httptest.Server, tg *stubTelegram) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg.restartGap()
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		NoticeEvery:       200 * time.Millisecond,
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
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("bot did not stop after cancel")
		}
	})
}

func TestBotE2E_GuestList_ButtonToggleAndAutomaticSend(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedECBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM guest_list_deliveries WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM guest_list_subscriptions WHERE org_id = $1`, f.orgID)
	})

	tg := newStubTelegram(t)
	startGuestListTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, ecOwnerTG}

	// The button on a date's card (index 1 is the future date).
	owner.press("ses:list:"+seed.richEvent.String(), "Сеансы мероприятия")
	card := owner.press("ses:o:1", "Здесь можно перенести")
	_ = card
	before := tg.callCount("sendDocument")
	capText := owner.press("ses:gl", "[document guests_")
	tg.waitCall(t, "sendDocument", before)
	if !strings.Contains(capText, "Билетов в списке: 3") {
		t.Errorf("guest list caption: %s", capText)
	}
	body := guestListDoc(t, tg)
	if eventbot.CSVRows(body) != 3 {
		t.Fatalf("guest list rows = %d, want 3:\n%s", eventbot.CSVRows(body), body)
	}
	lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(string(body), "\xef\xbb\xbf"), "\r\n"), "\r\n")
	if !strings.HasPrefix(lines[0], `"Гость";"Категория";"Место";"Билет"`) {
		t.Errorf("header: %s", lines[0])
	}
	// Sorted by name: the two tickets of "Buyer Name", then the invitation.
	if !strings.HasPrefix(lines[1], `"Buyer Name"`) || !strings.HasPrefix(lines[3], `"Guest"`) ||
		!strings.HasSuffix(lines[3], `"yes"`) || !strings.HasSuffix(lines[1], `"no"`) {
		t.Errorf("rows not sorted by guest, or invitation column wrong:\n%s", strings.Join(lines, "\n"))
	}

	// The toggle on the Notifications screen.
	n := owner.press("ec:nt", "Список гостей")
	if !strings.Contains(n, "за 24 часа до начала") {
		t.Errorf("notifications screen lacks the guest list paragraph:\n%s", n)
	}
	owner.press("gl:on", "Список гостей")
	if !subscribed(t, pool, f.orgID.String(), f.ownerID.String()) {
		t.Fatal("gl:on did not subscribe the owner")
	}
	owner.press("gl:off", "Список гостей")
	if subscribed(t, pool, f.orgID.String(), f.ownerID.String()) {
		t.Fatal("gl:off did not unsubscribe the owner")
	}
	owner.press("gl:on", "Список гостей")

	// The automatic list: a pass at one minute past "24 hours before".
	var start time.Time
	if err := pool.QueryRow(ctx, `SELECT start_at FROM sessions WHERE id = $1`, seed.richFuture).Scan(&start); err != nil {
		t.Fatal(err)
	}
	sched := guestlist.NewScheduler(guestlist.Options{Pool: pool})
	m := tg.mark()
	written, _, err := sched.RunOnce(ctx, start.Add(-24*time.Hour+time.Minute))
	if err != nil || written < 1 {
		t.Fatalf("RunOnce: written=%d err=%v", written, err)
	}
	auto := tg.waitSince(t, m, "за 24 часа до начала")
	if !strings.Contains(auto, "[document guests_") {
		t.Errorf("automatic list is not a document: %s", auto)
	}
	waitSent(t, pool, f.orgID.String())

	// A second pass at the same moment writes and sends nothing.
	docs := tg.callCount("sendDocument")
	written, _, err = sched.RunOnce(ctx, start.Add(-24*time.Hour+2*time.Minute))
	if err != nil || written != 0 {
		t.Fatalf("second RunOnce: written=%d err=%v", written, err)
	}
	time.Sleep(800 * time.Millisecond)
	if got := tg.callCount("sendDocument"); got != docs {
		t.Errorf("a second pass sent %d more document(s)", got-docs)
	}
	// Only the owner turned it on: one delivery, for the owner.
	var rows, mine int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE user_id = $2)
		FROM guest_list_deliveries WHERE org_id = $1`, f.orgID, f.ownerID).Scan(&rows, &mine); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || mine != 1 {
		t.Errorf("deliveries = %d (owner %d), want exactly the owner's one", rows, mine)
	}
}

// guestListDoc returns the newest guests_*.csv document the stub received.
func guestListDoc(t *testing.T, tg *stubTelegram) []byte {
	t.Helper()
	var name string
	tg.mu.Lock()
	for n := range tg.docs {
		if strings.HasPrefix(n, "guests_") && strings.HasSuffix(n, ".csv") {
			name = n
		}
	}
	tg.mu.Unlock()
	body, ok := tg.document(name)
	if !ok {
		t.Fatalf("no guests_*.csv document received")
	}
	return body
}

func subscribed(t *testing.T, pool *pgxpool.Pool, orgID, userID string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM guest_list_subscriptions WHERE org_id = $1 AND user_id = $2)`, orgID, userID).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// waitSent waits until the bot has reported the delivery as sent.
func waitSent(t *testing.T, pool *pgxpool.Pool, orgID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM guest_list_deliveries WHERE org_id = $1 AND sent_at IS NOT NULL`, orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the automatic guest list was never reported as sent")
}
