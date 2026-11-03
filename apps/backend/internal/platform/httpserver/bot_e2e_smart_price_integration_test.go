//go:build integration

package httpserver

// The bot's "Smart price" screen (spec 08_architecture/51_4_2_smart_price_ru.md)
// end to end: the real router, the real bot over the stub Telegram, a real
// database. A preset is previewed (nothing written), turned on with the press
// under the preview, stopped from its own question, and turned off by a price
// set by hand.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	spOwnerTG    = int64(91601)
	spManagerTG  = int64(91602)
	spOutsiderTG = int64(91603)
)

func TestBotE2E_SmartPrice(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, spOwnerTG, "owner")
	linkECBotUser(t, f, srv, spManagerTG, "manager")
	linkECBotUser(t, other, srv, spOutsiderTG, "owner")
	ctx := context.Background()
	suffix := uuid.NewString()[:6]

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
	}
	g := &ga46Fixture{t: t, pool: pool, orgID: f.orgID, venueID: uuid.New(), eventID: uuid.New()}
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, g.venueID, f.orgID, "SP Hall "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, 'published', 'public', $4)`,
		g.eventID, f.orgID, "SP Show "+suffix, "sp-show-"+suffix)
	body := ga46SessionBody(g.venueID, 200)
	body["start_at"], body["end_at"] = "2099-11-20T18:00:00Z", "2099-11-20T21:00:00Z"
	sessionID, w := g.createSession(srv, body)
	if sessionID == uuid.Nil {
		t.Fatalf("create session: %d %s", w.Code, w.Body.String())
	}
	mkTier := func(name string, price, capacity int) uuid.UUID {
		out, w := g.createTier(srv, sessionID, map[string]any{"name": name, "pricing_mode": "fixed", "price_amount": price, "capacity": capacity})
		if w.Code != http.StatusCreated {
			t.Fatalf("create tier %s: %d %s", name, w.Code, w.Body.String())
		}
		return uuid.MustParse(out.Tier.ID)
	}
	standing, balcony := mkTier("Standing", 2000, 100), mkTier("Balcony", 1000, 50)
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM smart_price_rules WHERE org_id = $1`,
			`DELETE FROM ticket_tier_chain WHERE tier_id IN (SELECT t.id FROM ticket_tiers t JOIN sessions s ON s.id = t.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM ticket_tier_prices WHERE tier_id IN (SELECT t.id FROM ticket_tiers t JOIN sessions s ON s.id = t.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT t.id FROM ticket_tiers t JOIN sessions s ON s.id = t.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
		} {
			if _, err := pool.Exec(c, sql, f.orgID); err != nil {
				t.Logf("smart price bot cleanup: %s: %v", sql, err)
			}
		}
		g.cleanup()
	})

	status := func(tierID uuid.UUID) string {
		t.Helper()
		var s string
		err := pool.QueryRow(ctx, `SELECT status FROM smart_price_rules WHERE tier_id = $1 ORDER BY created_at DESC LIMIT 1`, tierID).Scan(&s)
		if err != nil {
			return ""
		}
		return s
	}
	places := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND kind = 'ga_unit'`, sessionID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	hall := places()

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, spOwnerTG}
	manager := ecDriver{t, tg, spManagerTG}
	outsider := ecDriver{t, tg, spOutsiderTG}
	ct := func(kind string, id uuid.UUID) string { return "ct:" + kind + ":" + id.String() }

	// ── the card offers the screen, the screen offers the three presets ───────
	owner.press("ct:e:"+g.eventID.String(), "Категорий: 2")
	owner.press(ct("v", standing), "Standing")
	owner.press(ct("sp", standing), "Умная цена")
	_, _, data := tg.lastOf(spOwnerTG)
	for _, want := range []string{ct("ss", standing), ct("sd", standing), ct("sl", standing)} {
		if !hasPrefixData(data, want) {
			t.Errorf("the screen lacks the button %q: %v", want, data)
		}
	}

	// ── the proposed numbers: the server's table, nothing written ─────────────
	text := owner.press(ct("ss", standing), "Пока ничего не сохранено")
	if !strings.Contains(text, "50 билетов: 20 EUR") || !strings.Contains(text, "остальные, 50: 22 EUR") {
		t.Errorf("the default preview:\n%s", text)
	}
	// ── other numbers: a typo is refused; numbers that do not fit the places say
	// what to do and ask again; the preview writes nothing ─────────────────────
	owner.press(ct("sn", standing), "Введите три числа")
	owner.say("десять", "не те числа")
	text = owner.say("10 200 50", "не хватает")
	if !strings.Contains(text, "Введите три числа") {
		t.Errorf("a refusal other numbers mend must ask them:\n%s", text)
	}
	if status(standing) != "" {
		t.Fatal("a refused preview wrote a preset")
	}
	text = owner.say("10 30 50", "Пока ничего не сохранено")
	for _, want := range []string{"30 билетов: 20 EUR", "30 билетов: 22 EUR", "30 билетов: 24 EUR", "остальные, 10: 26 EUR"} {
		if !strings.Contains(text, want) {
			t.Errorf("the preview lacks %q:\n%s", want, text)
		}
	}
	if status(standing) != "" {
		t.Fatal("the preview wrote a preset")
	}
	owner.press(ct("se", standing), "Умная цена включена")
	if s := status(standing); s != "active" {
		t.Fatalf("after the press the preset is %q, want active", s)
	}
	if got := places(); got != hall {
		t.Errorf("turning smart price on changed the hall: %d places, want %d", got, hall)
	}
	text = owner.press(ct("sp", standing), "Включена")
	if !strings.Contains(text, "ступень 1 из 4") || !strings.Contains(text, "ещё 30") {
		t.Errorf("the screen must say where the preset stands:\n%s", text)
	}
	owner.press(ct("se", standing), "Включена") // a second press turns nothing on twice

	// ── stop: one question, one press ─────────────────────────────────────────
	owner.press(ct("sx", standing), "до конца продаж")
	if status(standing) != "active" {
		t.Fatal("the question alone stopped the preset")
	}
	owner.press(ct("sz", standing), "больше не растёт")
	if s := status(standing); s != "stopped" {
		t.Errorf("after stop the preset is %q, want stopped", s)
	}
	owner.press(ct("sp", standing), "уже продаётся ступенями")

	// ── the manager: a rise by days, then a price set by hand turns it off ────
	manager.press("ct:e:"+g.eventID.String(), "Категорий")
	manager.press(ct("sp", balcony), "Умная цена")
	manager.press(ct("sd", balcony), "Пока ничего не сохранено")
	manager.press(ct("se", balcony), "Умная цена включена")
	if s := status(balcony); s != "active" {
		t.Fatalf("the manager's preset is %q, want active", s)
	}
	manager.press(ct("pr", balcony), "Новая цена")
	text = manager.say("15", "→ 15 EUR")
	if !strings.Contains(text, "выключит умную цену") {
		t.Errorf("the confirmation must warn that a hand-set price turns smart price off:\n%s", text)
	}
	manager.press(ct("ps", balcony), "Цена сохранена")
	if s := status(balcony); s != "overridden" {
		t.Errorf("after a hand-set price the preset is %q, want overridden", s)
	}
	manager.press(ct("sp", balcony), "цену изменили вручную")

	// ── another organization's owner changes nothing ──────────────────────────
	outsider.press(ct("sz", balcony), "Этот экран уже закрыт")
	if s := status(balcony); s != "overridden" {
		t.Errorf("a foreign press changed the preset: %q", s)
	}
}
