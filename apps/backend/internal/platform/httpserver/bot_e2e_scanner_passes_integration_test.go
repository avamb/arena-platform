//go:build integration

package httpserver

// End-to-end proof of the bot's controller screens (spec 69, MA-12): a linked
// manager opens the passes of an event from the card, makes a pass in four
// steps (name, dates, phones), gets the link with its QR picture, pairs a phone
// through the real pairing route, removes that phone, extends the window,
// turns the pass off and back on with a new link (the old link dies), pairs
// his own phone from "Control app" and removes it. An agent has no buttons and
// a forged press changes nothing. Telegram is the stub, the API is the real
// router.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	scManagerTG = int64(7871)
	scAgentTG   = int64(7872)
)

var scCodeRe = regexp.MustCompile(`<code>([A-Z0-9]{8})</code>`)

// scCodeIn finds the 8-character pairing code in the lines the bot sent.
func scCodeIn(t *testing.T, lines []string) string {
	t.Helper()
	for _, l := range lines {
		if m := scCodeRe.FindStringSubmatch(l); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no pairing code in %q", lines)
	return ""
}

// scPair exchanges a code for a device through the real, unauthenticated route.
func scPair(t *testing.T, api *httptest.Server, code, label, controller string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "platform": "android", "device_label": label, "controller_name": controller})
	res, err := api.Client().Post(api.URL+"/v1/scanner-app/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.String()
}

// scHasButton reports whether any message of the chat carries the button.
func scHasButton(tg *stubTelegram, chat int64, data string) bool {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	for _, m := range tg.msgs {
		if m.chat != chat {
			continue
		}
		for _, d := range m.data {
			if d == data {
				return true
			}
		}
	}
	return false
}

func TestBotE2E_ScannerPassesAndControlApp(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx := context.Background()
	seed := seedPromoOrg(t, pool, f.orgID)
	t.Cleanup(func() {
		// Runs before the fixture's cleanup (LIFO): devices, codes and passes first.
		for _, sql := range []string{
			`DELETE FROM scanner_pairing_codes WHERE org_id = $1`,
			`DELETE FROM scanner_devices WHERE org_id = $1`,
			`DELETE FROM scanner_passes WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(context.Background(), sql, f.orgID); err != nil {
				t.Logf("scanner bot cleanup: %s: %v", sql, err)
			}
		}
	})
	linkECBotUser(t, f, srv, scManagerTG, "manager")

	// An agent, linked to Telegram directly (the bot invitation never makes one).
	q := gen.New(pool)
	agent, err := q.InsertUser(ctx, f.newEmail("agent"), "x", "ru")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(ctx, agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	if _, err := q.UpsertBotTelegramLink(ctx, scAgentTG, agent.ID, nil, "ru", &f.orgID); err != nil {
		t.Fatalf("link agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, scAgentTG)
	})

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	manager := ecDriver{t, tg, scManagerTG}
	agentD := ecDriver{t, tg, scAgentTG}
	ev := seed.eventA.String()

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	// ── the card: the button is in Sales and orders, and the list is empty ────
	manager.press("ec:g:"+ev+":s", "Продажи")
	if !tg.waitButton(scManagerTG, "sp:l:"+ev, 3*time.Second) {
		t.Fatal("the Sales group of the card has no Controllers button")
	}
	manager.press("sp:l:"+ev, "Пропусков пока нет")

	// ── a new pass: name, dates (all on by default), two phones ──────────────
	manager.press("sp:n:"+ev, "Назовите новый пропуск")
	manager.say("Главный вход", "Включено дат: 3")
	manager.press("sp:t:0", "Включено дат: 2")
	manager.press("sp:t:0", "Включено дат: 3")
	manager.press("sp:nx", "Сколько телефонов")
	mark := tg.mark()
	manager.press("sp:d:2", "готов")
	link := qlSentSince(tg, mark, "Ссылка для контролёра")
	code := scCodeIn(t, link)
	if len(link) != 1 || !strings.HasPrefix(link[0], "[photo ") {
		t.Errorf("the link must go out once, as the caption of the QR picture: %q", link)
	}

	var passID uuid.UUID
	var maxDevices int
	if err := pool.QueryRow(ctx, `SELECT id, max_devices FROM scanner_passes WHERE org_id = $1 AND label = 'Главный вход' AND revoked_at IS NULL`, f.orgID).Scan(&passID, &maxDevices); err != nil {
		t.Fatalf("the pass was not made: %v", err)
	}
	if maxDevices != 2 || count(`SELECT count(*) FROM scanner_pass_sessions WHERE pass_id = $1`, passID) != 3 {
		t.Fatalf("the pass has %d phones and the wrong dates", maxDevices)
	}
	pid := passID.String()

	// ── a phone pairs with the link, the card counts it, a press removes it ──
	if status, body := scPair(t, api, code, "Pixel 8", "Ана"); status != http.StatusCreated {
		t.Fatalf("pair with the pass code: %d %s", status, body)
	}
	card := manager.press("sp:v:"+pid, "Принято телефонов: 1 из 2")
	if !strings.Contains(card, "Главный вход") || !strings.Contains(card, "включён") {
		t.Errorf("card:\n%s", card)
	}
	manager.press("sp:dv:"+pid, "Ана")
	var devID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM scanner_devices WHERE pass_id = $1 AND revoked_at IS NULL`, passID).Scan(&devID); err != nil {
		t.Fatalf("no live phone on the pass: %v", err)
	}
	manager.press("sp:rv:"+devID.String(), "Телефон убран")
	if n := count(`SELECT count(*) FROM scanner_devices WHERE id = $1 AND revoked_at IS NOT NULL`, devID); n != 1 {
		t.Fatalf("the phone was not removed")
	}
	manager.press("sp:rv:"+devID.String(), "уже убран") // a second press says so and changes nothing

	// ── extend the window by two hours ────────────────────────────────────────
	var before, after time.Time
	_ = pool.QueryRow(ctx, `SELECT valid_until FROM scanner_passes WHERE id = $1`, passID).Scan(&before)
	manager.press("sp:ex:"+pid+":2", "Пропуск работает до")
	_ = pool.QueryRow(ctx, `SELECT valid_until FROM scanner_passes WHERE id = $1`, passID).Scan(&after)
	if !after.Equal(before.Add(2 * time.Hour)) {
		t.Fatalf("valid_until moved from %s to %s, want +2h", before, after)
	}

	// ── turn off with one press, turn on with a new link; the old link dies ──
	off := manager.press("sp:off:"+pid, "Пропуск выключен")
	if !strings.Contains(off, "24 часа") || !strings.Contains(off, "Новая ссылка") {
		t.Errorf("the turn-off text must say the phones still upload for 24 hours and how to turn it on:\n%s", off)
	}
	if n := count(`SELECT count(*) FROM scanner_passes WHERE id = $1 AND revoked_at IS NOT NULL`, passID); n != 1 {
		t.Fatal("the pass is not turned off")
	}
	mark = tg.mark()
	manager.press("sp:ln:"+pid, "Новая ссылка готова")
	newCode := scCodeIn(t, qlSentSince(tg, mark, "Ссылка для контролёра"))
	if newCode == code {
		t.Fatal("a new link must carry a new code")
	}
	if n := count(`SELECT count(*) FROM scanner_passes WHERE id = $1 AND revoked_at IS NULL`, passID); n != 1 {
		t.Fatal("the new link did not turn the pass on again")
	}
	if status, _ := scPair(t, api, code, "Old", "Old"); status != http.StatusNotFound {
		t.Errorf("the old link still pairs: %d", status)
	}
	if status, body := scPair(t, api, newCode, "Pixel 9", "Борис"); status != http.StatusCreated {
		t.Errorf("the new link does not pair: %d %s", status, body)
	}

	// ── the list shows the pass; the card of a stranger's pass is "gone" ─────
	manager.press("sp:l:"+ev, "Главный вход")
	manager.press("sp:v:"+uuid.NewString(), "больше нет")

	// ── Control app: the manager's own phone ─────────────────────────────────
	manager.press("scanner", "Сканер билетов")
	if !tg.waitButton(scManagerTG, "sp:me", 3*time.Second) {
		t.Fatal("the Scanner screen has no Control app button for the manager")
	}
	manager.press("sp:me", "Привяжите свой телефон")
	mark = tg.mark()
	manager.press("sp:pc", "Код работает один раз")
	own := scCodeIn(t, qlSentSince(tg, mark, "Ваш телефон"))
	if status, body := scPair(t, api, own, "Мой Pixel", ""); status != http.StatusCreated {
		t.Fatalf("pair with the organizer code: %d %s", status, body)
	}
	if status, _ := scPair(t, api, own, "Twice", ""); status != http.StatusNotFound {
		t.Errorf("an organizer code worked twice: %d", status)
	}
	// "My devices" shows only the organizer's phone, not the controllers'.
	mine := manager.press("sp:md", "Ваши телефоны")
	if !strings.Contains(mine, "Мой Pixel") || strings.Contains(mine, "Борис") {
		t.Errorf("my devices:\n%s", mine)
	}
	var ownDev uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM scanner_devices WHERE org_id = $1 AND pass_id IS NULL AND revoked_at IS NULL`, f.orgID).Scan(&ownDev); err != nil {
		t.Fatalf("no organizer phone: %v", err)
	}
	manager.press("sp:rv:"+ownDev.String(), "Телефон убран")
	if n := count(`SELECT count(*) FROM scanner_devices WHERE id = $1 AND revoked_at IS NOT NULL`, ownDev); n != 1 {
		t.Fatal("the organizer's phone was not removed")
	}

	// ── an agent: no button, and forged presses change nothing ───────────────
	passes := count(`SELECT count(*) FROM scanner_passes WHERE org_id = $1`, f.orgID)
	devices := count(`SELECT count(*) FROM scanner_devices WHERE org_id = $1 AND revoked_at IS NULL`, f.orgID)
	agentD.press("scanner", "Сканер билетов")
	if scHasButton(tg, scAgentTG, "sp:me") {
		t.Error("an agent sees the Control app button")
	}
	for _, data := range []string{"sp:l:" + ev, "sp:n:" + ev, "sp:off:" + pid, "sp:ln:" + pid, "sp:pc", "sp:rv:" + uuid.NewString()} {
		agentD.press(data, "Ваша роль")
	}
	if n := count(`SELECT count(*) FROM scanner_passes WHERE org_id = $1`, f.orgID); n != passes {
		t.Errorf("an agent changed the passes: %d -> %d", passes, n)
	}
	if n := count(`SELECT count(*) FROM scanner_devices WHERE org_id = $1 AND revoked_at IS NULL`, f.orgID); n != devices {
		t.Errorf("an agent changed the devices: %d -> %d", devices, n)
	}
	if n := count(`SELECT count(*) FROM scanner_passes WHERE id = $1 AND revoked_at IS NULL`, passID); n != 1 {
		t.Error("an agent turned the pass off")
	}
}
