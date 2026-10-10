//go:build integration

package httpserver

// End-to-end proof of the category screens (spec 35 EC-15) through the real
// router, the real bot and the stub Telegram: the owner reaches the categories
// from the Sessions screen and from the event card, closes and opens one,
// changes a price (confirmed with a press, visible in the API, applied to the
// ACTIVE scheduled window when the category follows a schedule), sets the sale
// window from the calendar and takes a limit off again, resizes a category and
// is refused plainly below what is sold; a chain category keeps only its price
// (the forbidden presses change nothing); the manager does the same with
// tier.update from migration 0129; a user of ANOTHER organization gets "not
// found" and a bot restart in the middle of a question loses nothing. The stub
// records message TEXT only, so every effect is read from the database or the
// API.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

const (
	catOwnerTG    = int64(91501)
	catManagerTG  = int64(91502)
	catOutsiderTG = int64(91503)
)

type catTierRow struct {
	Price    int64
	Open     bool
	Capacity *int32
	Start    *time.Time
	End      *time.Time
}

func readCatTier(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) catTierRow {
	t.Helper()
	var r catTierRow
	err := pool.QueryRow(context.Background(),
		`SELECT price_amount, is_open, capacity, sale_window_start, sale_window_end FROM ticket_tiers WHERE id = $1`, id).
		Scan(&r.Price, &r.Open, &r.Capacity, &r.Start, &r.End)
	if err != nil {
		t.Fatalf("read tier %s: %v", id, err)
	}
	return r
}

func TestBotE2E_Categories(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, catOwnerTG, "owner")
	linkECBotUser(t, f, srv, catManagerTG, "manager")
	linkECBotUser(t, other, srv, catOutsiderTG, "owner")
	ctx := context.Background()
	suffix := uuid.NewString()[:6]

	// The truth about the roles: the owner and the manager hold tier.read and
	// tier.update (the manager's since migration 0129).
	for _, role := range []string{"org_admin", "organizer"} {
		for _, perm := range []string{"tier.read", "tier.update"} {
			var n int
			if err := pool.QueryRow(ctx, `
				SELECT count(*) FROM role_permissions rp
				  JOIN roles r ON r.id = rp.role_id AND r.org_id IS NULL
				  JOIN permissions p ON p.id = rp.permission_id
				 WHERE r.name = $1 AND p.name = $2`, role, perm).Scan(&n); err != nil || n != 1 {
				t.Fatalf("role %s must hold %s: n=%d err=%v", role, perm, n, err)
			}
		}
	}

	// ── fixtures: a published event with ONE date (so the event card offers the
	// categories), four categories made through the real handlers, three places
	// of the first sold, and a two-step chain.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
	}
	g := &ga46Fixture{t: t, pool: pool, orgID: f.orgID, venueID: uuid.New(), eventID: uuid.New()}
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, g.venueID, f.orgID, "Cat Hall "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, 'published', 'public', $4)`,
		g.eventID, f.orgID, "Cat Show "+suffix, "cat-show-"+suffix)
	body := ga46SessionBody(g.venueID, 40)
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
	standing, balcony, early, late := mkTier("Standing", 2500, 20), mkTier("Balcony", 4000, 10), mkTier("Early", 1000, 5), mkTier("Late", 1500, 5)
	exec(`INSERT INTO ticket_tier_chain (tier_id, next_tier_id) VALUES ($1, $2)`, early, late)
	exec(`UPDATE session_seats SET status = 'sold' WHERE id IN
	        (SELECT id FROM session_seats WHERE session_id = $1 AND tier_id = $2 AND kind = 'ga_unit' ORDER BY seat_key LIMIT 3)`, sessionID, standing)
	// Balcony follows a schedule: one window is in force now, one starts later.
	exec(`INSERT INTO ticket_tier_prices (tier_id, valid_from, valid_to, price_amount) VALUES
	        ($1, now() - interval '1 hour', now() + interval '48 hours', 4000),
	        ($1, now() + interval '48 hours', NULL, 5000)`, balcony)
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM ticket_tier_chain WHERE tier_id IN (SELECT t.id FROM ticket_tiers t JOIN sessions s ON s.id = t.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM ticket_tier_prices WHERE tier_id IN (SELECT t.id FROM ticket_tiers t JOIN sessions s ON s.id = t.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM audit_events WHERE metadata->>'session_id' = '` + sessionID.String() + `'`,
		} {
			args := []any{f.orgID}
			if !strings.Contains(sql, "$1") {
				args = nil
			}
			if _, err := pool.Exec(c, sql, args...); err != nil {
				t.Logf("category bot cleanup: %s: %v", sql, err)
			}
		}
		g.cleanup()
	})

	// A key to read the categories back through the API.
	q := gen.New(pool)
	_, rawKey, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: f.orgID, Name: "cat-e2e-" + suffix, Scopes: []string{"tier.read"}, CreatedBy: f.ownerID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	apiTier := func(id uuid.UUID) map[string]any {
		t.Helper()
		resp := integDoRequest(t, api.Client(), http.MethodGet,
			api.URL+"/v1/organizations/"+f.orgID.String()+"/events/"+g.eventID.String()+"/sessions/"+sessionID.String()+"/tiers", rawKey, "")
		raw := integReadBody(t, resp)
		var out struct {
			Tiers []map[string]any `json:"tiers"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode tiers %q: %v", raw, err)
		}
		for _, tier := range out.Tiers {
			if tier["id"] == id.String() {
				return tier
			}
		}
		t.Fatalf("tier %s is not in the API list: %s", id, raw)
		return nil
	}
	places := func(id uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND tier_id = $2 AND kind = 'ga_unit'`, sessionID, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	tg := newStubTelegram(t)
	stop := startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, catOwnerTG}
	manager := ecDriver{t, tg, catManagerTG}
	outsider := ecDriver{t, tg, catOutsiderTG}
	ct := func(kind string, id uuid.UUID) string { return "ct:" + kind + ":" + id.String() }
	prague, _ := time.LoadLocation("Europe/Prague")
	midnight := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, prague) }

	// ── in through the Sessions screen: event -> dates -> the date's card -> Categories
	owner.press("ses:list:"+g.eventID.String(), "Cat Show")
	owner.press("ses:o:0", "Cat Show")
	text := owner.press("ses:ct", "Категорий: 4")
	for _, n := range []string{"Standing", "Balcony", "Early", "Late", "продано 3"} {
		if !strings.Contains(text, n) {
			t.Errorf("the list lacks %q:\n%s", n, text)
		}
	}
	// ...and in through the event card of an event with one date.
	owner.press("ct:e:"+g.eventID.String(), "Категорий: 4")

	// ── close, then open ──────────────────────────────────────────────────────
	text = owner.press(ct("v", standing), "Standing")
	if !strings.Contains(text, "в продаже") || !strings.Contains(text, "25 EUR") {
		t.Errorf("the card must show the state and the price:\n%s", text)
	}
	owner.press(ct("cl", standing), "Закрыто")
	if r := readCatTier(t, pool, standing); r.Open {
		t.Error("the category must be closed")
	}
	if got := apiTier(standing)["is_open"]; got != false {
		t.Errorf("API is_open = %v, want false", got)
	}
	owner.press(ct("op", standing), "Снова открыто")
	if r := readCatTier(t, pool, standing); !r.Open {
		t.Error("the category must be open again")
	}

	// ── a price: refused text, confirmation, nothing before the press ─────────
	owner.press(ct("pr", standing), "Новая цена")
	owner.say("дёшево", "Это не цена")
	owner.say("0", "Это не цена")
	text = owner.say("30", "25 EUR → 30 EUR")
	if !strings.Contains(text, "новых покупок") {
		t.Errorf("the confirmation must say the price is for new purchases only:\n%s", text)
	}
	if r := readCatTier(t, pool, standing); r.Price != 2500 {
		t.Fatalf("nothing is written before the press, price = %d", r.Price)
	}
	owner.press(ct("ps", standing), "Цена сохранена")
	if r := readCatTier(t, pool, standing); r.Price != 3000 {
		t.Errorf("price = %d, want 3000", r.Price)
	}
	if tier := apiTier(standing); tier["price_amount"] != float64(3000) || tier["current_price"] != float64(3000) {
		t.Errorf("the API must show the new price: %v / %v", tier["price_amount"], tier["current_price"])
	}
	owner.press(ct("ps", standing), "Standing") // a press with no question open
	if r := readCatTier(t, pool, standing); r.Price != 3000 {
		t.Errorf("a confirm with no question open changed the price: %d", r.Price)
	}

	// ── a category on a schedule: the window in force now is the one changed ──
	owner.press(ct("pr", balcony), "Новая цена")
	owner.say("45,50", "40 EUR → 45,50 EUR")
	owner.press(ct("ps", balcony), "по расписанию")
	var base int64
	var inForce, later int64
	if err := pool.QueryRow(ctx, `SELECT price_amount FROM ticket_tiers WHERE id = $1`, balcony).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT price_amount FROM ticket_tier_prices WHERE tier_id = $1 AND valid_from <= now() AND (valid_to IS NULL OR valid_to > now())`, balcony).Scan(&inForce); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT price_amount FROM ticket_tier_prices WHERE tier_id = $1 AND valid_from > now()`, balcony).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if inForce != 4550 || base != 4000 || later != 5000 {
		t.Errorf("window in force = %d (want 4550), base = %d (want 4000), later window = %d (want 5000)", inForce, base, later)
	}
	if got := apiTier(balcony)["current_price"]; got != float64(4550) {
		t.Errorf("the API current price = %v, want 4550", got)
	}

	// ── the sale window from the calendar ─────────────────────────────────────
	owner.press(ct("wn", standing), "Окно продаж")
	owner.press(ct("ws", standing), "Первый день продаж")
	owner.press("ct:d:2099-10-05", "Окно продаж сохранено")
	if r := readCatTier(t, pool, standing); r.Start == nil || !r.Start.Equal(midnight(2099, 10, 5)) || r.End != nil {
		t.Errorf("start = %v end = %v, want 05.10.2099 00:00 Prague and no end", r.Start, r.End)
	}
	owner.press(ct("wn", standing), "с 05.10.2099")
	owner.press(ct("we", standing), "Последний день продаж")
	owner.press("ct:d:2099-10-31", "Окно продаж сохранено")
	if r := readCatTier(t, pool, standing); r.End == nil || !r.End.Equal(midnight(2099, 11, 1)) {
		t.Errorf("the last day is inclusive, so the window ends at the midnight after it: %v", r.End)
	}
	text = owner.press(ct("v", standing), "с 05.10.2099 по 31.10.2099")
	if !strings.Contains(text, "20.11.2099") && !strings.Contains(text, "Вся дата перестаёт продаваться") {
		t.Errorf("the card must say when the whole date stops selling:\n%s", text)
	}
	// A last day before the first day is refused by the API, in words.
	owner.press(ct("we", standing), "Последний день продаж")
	owner.press("ct:d:2099-09-01", "последний день должен быть позже первого")
	if r := readCatTier(t, pool, standing); r.End == nil || !r.End.Equal(midnight(2099, 11, 1)) {
		t.Errorf("a refused window must change nothing: %v", r.End)
	}
	// A typed date asks to be confirmed, then saves.
	owner.press(ct("ws", standing), "Первый день продаж")
	owner.say("07.10.2099", "2099")
	owner.press("ct:dc:2099-10-07", "Окно продаж сохранено")
	if r := readCatTier(t, pool, standing); r.Start == nil || !r.Start.Equal(midnight(2099, 10, 7)) {
		t.Errorf("typed start = %v", r.Start)
	}
	// A limit comes off with one press.
	owner.press(ct("we", standing), "Последний день продаж")
	owner.press(ct("wx", standing), "Ограничение убрано")
	if r := readCatTier(t, pool, standing); r.End != nil || r.Start == nil {
		t.Errorf("only the end must be gone: start=%v end=%v", r.Start, r.End)
	}

	// ── the quantity: below the sold places is refused plainly ────────────────
	owner.press(ct("qt", standing), "Новое число мест")
	owner.say("двадцать", "целое число мест")
	text = owner.say("2", "Не сохранено")
	if !strings.Contains(text, "3") {
		t.Errorf("the refusal must name the floor (3 sold):\n%s", text)
	}
	if n := places(standing); n != 20 {
		t.Fatalf("a refused quantity must change nothing: %d places", n)
	}
	owner.say("25", "Сохранено: в категории теперь 25")
	if n := places(standing); n != 25 {
		t.Errorf("places = %d, want 25", n)
	}
	if r := readCatTier(t, pool, standing); r.Capacity == nil || *r.Capacity != 25 {
		t.Errorf("capacity = %v, want 25", r.Capacity)
	}
	if got := apiTier(standing)["quantity"]; got != float64(25) {
		t.Errorf("API quantity = %v", got)
	}

	// ── a chain category keeps only its price ─────────────────────────────────
	text = owner.press(ct("v", early), "Early")
	if !strings.Contains(text, "Late") || !strings.Contains(text, "сама передаёт") {
		t.Errorf("a chain head must say it hands its places to the next one:\n%s", text)
	}
	owner.press(ct("cl", early), "Early") // not offered: the press only redraws the card
	owner.press(ct("qt", early), "Early")
	owner.press(ct("wn", early), "Early")
	if r := readCatTier(t, pool, early); !r.Open || r.Capacity == nil || *r.Capacity != 5 || r.Start != nil {
		t.Errorf("forbidden presses changed a chain category: %+v", r)
	}
	if n := places(early); n != 5 {
		t.Errorf("chain places = %d, want 5", n)
	}
	owner.press(ct("pr", early), "Новая цена") // its price is still its own

	// ── the manager, with tier.update from migration 0129 ─────────────────────
	manager.press("ct:e:"+g.eventID.String(), "Категорий: 4")
	manager.press(ct("cl", balcony), "Закрыто")
	if r := readCatTier(t, pool, balcony); r.Open {
		t.Error("the manager must be able to close a category")
	}
	manager.press(ct("op", balcony), "Снова открыто")
	manager.press(ct("pr", balcony), "Новая цена")
	manager.say("50", "→ 50 EUR")
	manager.press(ct("ps", balcony), "Цена сохранена")
	if err := pool.QueryRow(ctx, `SELECT price_amount FROM ticket_tier_prices WHERE tier_id = $1 AND valid_from <= now() AND (valid_to IS NULL OR valid_to > now())`, balcony).Scan(&inForce); err != nil || inForce != 5000 {
		t.Errorf("manager price: window in force = %d (%v), want 5000", inForce, err)
	}

	// ── another organization: its own event list, a foreign event is not found ─
	before := readCatTier(t, pool, standing)
	outsider.press("ct:e:"+g.eventID.String(), "Не найдено")
	outsider.press(ct("cl", standing), "Этот экран уже закрыт") // no dialog of his own
	outsider.press("ses:ct", "")                                // no sessions dialog either: nothing to answer
	if after := readCatTier(t, pool, standing); after.Price != before.Price || after.Open != before.Open {
		t.Errorf("a foreign press changed the category: %+v -> %+v", before, after)
	}

	// ── a restart in the middle of a question loses nothing ───────────────────
	owner.press(ct("v", standing), "Standing")
	owner.press(ct("pr", standing), "Новая цена")
	stop()
	time.Sleep(400 * time.Millisecond) // let the stub's long poll end, so no update goes to the dead poller
	startPromoTestBot(t, pool, api, tg)
	owner.say("33", "→ 33 EUR")
	owner.press(ct("ps", standing), "Цена сохранена")
	if r := readCatTier(t, pool, standing); r.Price != 3300 {
		t.Errorf("after the restart price = %d, want 3300", r.Price)
	}

	// ── an answer after the dialog ran out is reported once ───────────────────
	owner.press(ct("pr", standing), "Новая цена")
	exec(`UPDATE bot_dialogs SET expires_at = now() - interval '1 hour' WHERE telegram_user_id = $1 AND kind = 'category'`, catOwnerTG)
	owner.say("99", "устарел")
	if r := readCatTier(t, pool, standing); r.Price != 3300 {
		t.Errorf("an expired dialog must not reprice: %d", r.Price)
	}

	// ── the audit row of a price change names the Telegram bot ────────────────
	var via *string
	if err := pool.QueryRow(ctx, `SELECT metadata->>'via' FROM audit_events WHERE action = 'v1.tier.price.update' AND resource_id = $1 ORDER BY occurred_at DESC LIMIT 1`, standing.String()).Scan(&via); err != nil || via == nil || *via != "telegram_bot" {
		t.Errorf("audit via = %v (%v), want telegram_bot", via, err)
	}
}
