//go:build integration

package httpserver

// End-to-end proof of the "+ Event" wizard's seated date (task S3c of spec
// 40) through the real router, the real bot and the stub Telegram: the owner
// picks a venue that has a seating plan, chooses to sell by the plan, prices
// each plan category, publishes, and the import binds the plan (every seat of
// the plan becomes a place, every category a priced tier). Then the event is
// opened for editing: "Prices and categories" asks only the prices, and the
// change reaches the tiers while the places and the plan stay as they were.
// On top of the plan's categories the owner adds an extra price category
// (VIP, its colour): it is created with no places, keeps none after the edit,
// and the bot points to where the seats are painted.

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

const wzSeatedOwnerTG = int64(91701)

func TestBotE2E_WizardSeatedDate(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	srv.cfg.AppPublicURL = api.URL
	linkECBotUser(t, f, srv, wzSeatedOwnerTG, "owner")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
	}

	t.Cleanup(func() {
		c := context.Background()
		for _, st := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM bot_drafts WHERE org_id = $1`,
			`DELETE FROM compatibility_id_map WHERE platform_id IN (
			    SELECT tt.id FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM compatibility_id_map WHERE platform_id IN (
			    SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM venues WHERE org_id = $1)`,
			`DELETE FROM session_external_refs WHERE org_id = $1`,
			`DELETE FROM session_seat_batches WHERE session_id IN (
			    SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM session_seats WHERE session_id IN (
			    SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM inventory_ledger WHERE session_id IN (
			    SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM ticket_tiers WHERE session_id IN (
			    SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM event_promoters WHERE org_id = $1`,
			`DELETE FROM events WHERE org_id = $1`,
			`UPDATE seating_plans SET current_version_id = NULL WHERE owner_org_id = $1`,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
			`DELETE FROM seating_plans WHERE owner_org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'org_id' = $1`,
		} {
			args := []any{f.orgID}
			if strings.Contains(st, "$1") && strings.Contains(st, "audit_events") {
				args = []any{f.orgID.String()}
			}
			if _, err := pool.Exec(c, st, args...); err != nil {
				t.Logf("wizard seated cleanup %q: %v", st, err)
			}
		}
	})

	// The venue: in Tallinn (the wizard knows Estonia's zone), with one active
	// plan, the Palác Akropolis drawing.
	var countryID, cityID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&countryID); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&cityID); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "domain", "seating", "testdata", "Palac_Akropolis.svg"))
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	g, _, verrs := seating.ImportSVG(raw)
	if len(verrs) != 0 {
		t.Fatalf("import plan: %v", verrs)
	}
	geometry := seating.Canonicalize(g)
	geoJSON, _ := seating.CanonicalJSON(geometry)
	checksum, _ := seating.Checksum(geometry)
	perCategory := map[int]int{}
	for _, sec := range geometry.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				perCategory[s.CategoryIndex]++
			}
		}
	}
	categories := len(perCategory)
	if categories == 0 {
		t.Fatal("the plan has no categories with seats")
	}

	venue, plan, version := uuid.New(), uuid.New(), uuid.New()
	suffix := uuid.NewString()[:6]
	exec(`INSERT INTO venues (id, org_id, name, city_id, timezone) VALUES ($1, $2, $3, $4, 'Europe/Tallinn')`, venue, f.orgID, "Seated hall "+suffix, cityID)
	exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
		plan, venue, f.orgID, "Main plan "+suffix)
	exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, capacity_standing)
	      VALUES ($1, $2, 1, $3::jsonb, $4, $5, 0)`, version, plan, string(geoJSON), checksum, geometry.SeatCount())
	exec(`UPDATE seating_plans SET current_version_id = $2 WHERE id = $1`, plan, version)

	tg := newStubTelegram(t)
	startSeatLayoutBot(t, pool, api, tg, nil)
	owner := ecDriver{t, tg, wzSeatedOwnerTG}

	// ── create ──────────────────────────────────────────────────────────────
	eventName := "Seated show " + suffix
	owner.press("wz:new", "Как называется")
	owner.say(eventName, "Возраст")
	owner.press("wz:age:16+", "От чьего имени")
	owner.press("wz:prom:org", "афишу")
	owner.press("wz:skip", "Дата сеанса 1")
	owner.say("15.12.2099", "Вы написали")
	owner.press("wz:dc:2099-12-15", "Время начала")
	owner.press("wz:default", "Продажа билетов закроется в начале")
	owner.press("wz:se:60", "Во сколько открывается вход")
	owner.press("wz:dr:30", "В какой части мира")
	owner.press("wz:region:europe", "В какой стране")
	owner.press("wz:country:"+countryID.String(), "В каком городе")
	owner.press("wz:city:"+cityID.String(), "Где проходит")
	if text := owner.press("wz:venue:"+venue.String(), "схема зала"); !strings.Contains(text, "Seated hall "+suffix) {
		t.Fatalf("plan question: %s", text)
	}
	owner.press("wz:plan:0", "Категория «")
	if text := owner.say("не цена", "Категория «"); !strings.Contains(text, "1 из") && !strings.Contains(text, "цен") {
		t.Logf("refusal text: %s", text)
	}
	prices := []string{"90", "50", "30", "20", "15", "12", "10", "8", "6", "5", "4", "3", "2", "1", "1"}
	for i := 0; i < categories; i++ {
		expect := "Категория «"
		if i == categories-1 {
			expect = "Нужны ещё ценовые категории"
		}
		owner.say(prices[i], expect)
	}
	owner.press("wz:extra:add", "Название новой ценовой категории")
	owner.say("VIP", "Сколько стоит билет категории «VIP»")
	owner.say("120", "Каким цветом")
	if text := owner.press("wz:color:#dc2626", "Нужны ещё ценовые категории"); !strings.Contains(text, "🟥 VIP") {
		t.Fatalf("extra list: %s", text)
	}
	owner.press("wz:extra:done", "из них новых 1")
	owner.press("wz:next", "Описание мероприятия") // no shared ticket questions for a date sold by the plan
	owner.press("wz:skip", "Валюта цен")
	owner.press("wz:cur:EUR", "Открыть продажи сразу")
	summary := owner.press("wz:pub:now", "Проверьте и опубликуйте")
	if !strings.Contains(summary, eventName) {
		t.Fatalf("summary: %s", summary)
	}
	owner.press("wz:publish", "Готово!")

	var eventID, sessionID, boundVersion uuid.UUID
	var capacity int
	if err := pool.QueryRow(ctx, `SELECT e.id, s.id, s.seating_plan_version_id, s.capacity_total
	        FROM events e JOIN sessions s ON s.event_id = e.id WHERE e.org_id = $1 AND e.name = $2`, f.orgID, eventName).Scan(&eventID, &sessionID, &boundVersion, &capacity); err != nil {
		t.Fatalf("event row: %v", err)
	}
	if boundVersion != version || capacity != geometry.SeatCount() {
		t.Fatalf("session = version %s capacity %d, want %s / %d", boundVersion, capacity, version, geometry.SeatCount())
	}
	var seats, tiers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND kind = 'seat'`, sessionID).Scan(&seats); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1`, sessionID).Scan(&tiers); err != nil {
		t.Fatal(err)
	}
	if seats != geometry.SeatCount() || tiers != categories+1 {
		t.Fatalf("places = %d seats / %d tiers, want %d / %d", seats, tiers, geometry.SeatCount(), categories+1)
	}
	vipPlaces := func() (int, string) {
		t.Helper()
		var n int
		var color *string
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_seats ss WHERE ss.tier_id = tt.id), tt.color
		        FROM ticket_tiers tt WHERE tt.session_id = $1 AND tt.name = 'VIP'`, sessionID).Scan(&n, &color); err != nil {
			t.Fatalf("VIP tier: %v", err)
		}
		if color == nil {
			return n, ""
		}
		return n, *color
	}
	if n, color := vipPlaces(); n != 0 || color != "#dc2626" {
		t.Fatalf("VIP = %d places colour %q, want 0 and #dc2626", n, color)
	}
	tierPrice := func() map[string]int64 {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT name, price_amount FROM ticket_tiers WHERE session_id = $1`, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var n string
			var p int64
			if err := rows.Scan(&n, &p); err != nil {
				t.Fatal(err)
			}
			out[n] = p
		}
		return out
	}
	created := tierPrice()
	seen := map[int64]bool{}
	for _, p := range created {
		seen[p] = true
	}
	if !seen[9000] {
		t.Fatalf("the first category must cost 90.00: %v", created)
	}

	// ── edit: only the prices ───────────────────────────────────────────────
	// "Done!" is followed by the sample ticket and the menu, sent in the
	// background: a press that lands before them is answered as out of date.
	tg.waitFor(t, "sample-ticket.pdf")
	tg.waitFor(t, "Сеанс продаётся по схеме зала")
	owner.press("wz:edit:"+eventID.String(), "Правим")
	owner.press("wz:e:tickets", "Категория «")
	for i := 0; i < categories+1; i++ {
		expect := "Категория «"
		if i == categories {
			expect = "Правка мероприятия"
		}
		if i == 0 {
			owner.say("95", expect)
		} else {
			owner.press("wz:keep", expect)
		}
	}
	owner.press("wz:publish", "Что уйдёт на сайт")
	owner.press("wz:publish:go", "Сохранено")

	after := tierPrice()
	changed := 0
	for name, p := range after {
		if p != created[name] {
			changed++
			if p != 9500 {
				t.Errorf("tier %s = %d, want 9500", name, p)
			}
		}
	}
	if changed != 1 || len(after) != categories+1 {
		t.Fatalf("after the edit: %v (before %v), changed %d", after, created, changed)
	}
	var seatsAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND kind = 'seat'`, sessionID).Scan(&seatsAfter); err != nil {
		t.Fatal(err)
	}
	var versionAfter uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT seating_plan_version_id FROM sessions WHERE id = $1`, sessionID).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if seatsAfter != seats || versionAfter != version {
		t.Fatalf("the edit touched the places: %d seats, version %s", seatsAfter, versionAfter)
	}
	if n, _ := vipPlaces(); n != 0 {
		t.Fatalf("the edit gave the unpainted VIP category %d places, want 0", n)
	}
	var mode string
	if err := pool.QueryRow(ctx, `SELECT admission_mode FROM sessions WHERE id = $1`, sessionID).Scan(&mode); err != nil || mode != "assigned_seats" {
		t.Fatalf("admission mode after the edit %q %v, want assigned_seats", mode, err)
	}
}
