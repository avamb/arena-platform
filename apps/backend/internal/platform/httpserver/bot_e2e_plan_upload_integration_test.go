//go:build integration

package httpserver

// End-to-end proof of "send the hall plan" in the "+ Event" wizard through the
// real router, the real bot and the stub Telegram. A venue without a plan used
// to skip the plan question for good; now the bot offers to take an SVG file:
//
//   - a file that is not an SVG (told apart by its content, not its name), a
//     photo, a file that is too big and an SVG the importer refuses leave the
//     wizard where it is and write nothing: no plan, no media object;
//   - an SVG with one colour makes a plan with ONE category and says so;
//   - the plan, its stored file and its first version exist afterwards, owned by
//     the organization, and the date is sold by that version;
//   - a venue made in the wizard gets the same offer, a plan with several
//     categories lists each with its places, and "without a plan" still works.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

const wzPlanUpOwnerTG = int64(91801)

// planUpSVG is a hall with ONE colour: a single category. It has no Legend
// group on purpose, so the importer's advisory warning reaches the person too.
const planUpSVG = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 200 100">` +
	`<g id="PriceCategory"><circle inkscape:label="#Standard" cx="1" cy="1" r="1" style="fill:#ff0000"/></g>` +
	`<g inkscape:label="#Parter"><title>1</title>` +
	`<circle cx="10" cy="10" r="3" style="fill:#ff0000"><title>1</title></circle>` +
	`<circle cx="20" cy="10" r="3" style="fill:#ff0000"><title>2</title></circle>` +
	`<circle cx="30" cy="10" r="3" style="fill:#ff0000"><title>3</title></circle>` +
	`</g></svg>`

// planUpBadSVG parses as XML and is an <svg>, but names no categories.
const planUpBadSVG = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 100 100">` +
	`<g inkscape:label="#Parter"><title>1</title><circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>1</title></circle></g></svg>`

// planUpFile sends a document the way Telegram delivers one: the stub serves
// the staged bytes for its file id.
func planUpFile(t *testing.T, tg *stubTelegram, userID int64, name string, data []byte, declared int, expect string) string {
	t.Helper()
	tg.mu.Lock()
	tg.poster = data
	tg.mu.Unlock()
	size := len(data)
	if declared > 0 {
		size = declared
	}
	mark := tg.mark()
	tg.push(fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"document":{"file_id":"plan-file","file_unique_id":"plan-u","file_name":%q,"mime_type":"image/svg+xml","file_size":%d}}`,
		time.Now().UnixNano()%100000, userID, userID, name, size))
	return tg.waitSince(t, mark, expect)
}

func planUpPhoto(t *testing.T, tg *stubTelegram, userID int64, expect string) string {
	t.Helper()
	mark := tg.mark()
	tg.push(fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"photo":[{"file_id":"ph","file_unique_id":"phu","width":640,"height":480,"file_size":1234}]}`,
		time.Now().UnixNano()%100000, userID, userID))
	return tg.waitSince(t, mark, expect)
}

func TestBotE2E_WizardPlanUpload(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	srv.cfg.AppPublicURL = api.URL
	linkECBotUser(t, f, srv, wzPlanUpOwnerTG, "owner")
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
			`DELETE FROM media_objects WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'owner_org_id' = $1 OR metadata->>'org_id' = $1`,
		} {
			args := []any{f.orgID}
			if strings.Contains(st, "audit_events") {
				args = []any{f.orgID.String()}
			}
			if _, err := pool.Exec(c, st, args...); err != nil {
				t.Logf("plan upload cleanup %q: %v", st, err)
			}
		}
	})

	var countryID, cityID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&countryID); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&cityID); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}
	venue := uuid.New()
	suffix := uuid.NewString()[:6]
	exec(`INSERT INTO venues (id, org_id, name, city_id, timezone) VALUES ($1, $2, $3, $4, 'Europe/Tallinn')`, venue, f.orgID, "Plain hall "+suffix, cityID)

	tg := newStubTelegram(t)
	startSeatLayoutBot(t, pool, api, tg, nil)
	owner := ecDriver{t, tg, wzPlanUpOwnerTG}

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	nothingStored := func(when string) {
		t.Helper()
		if n := count(`SELECT count(*) FROM seating_plans WHERE owner_org_id = $1`, f.orgID); n != 0 {
			t.Fatalf("%s: %d plans were written", when, n)
		}
		if n := count(`SELECT count(*) FROM media_objects WHERE org_id = $1 AND owner_type = 'seating_plan_svg'`, f.orgID); n != 0 {
			t.Fatalf("%s: %d plan files were stored", when, n)
		}
	}

	// ── the wizard, up to the venue question ────────────────────────────────
	eventName := "Plan upload show " + suffix
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

	// The venue has no plan: the bot offers to take one.
	if text := owner.press("wz:venue:"+venue.String(), "нет схемы зала"); !strings.Contains(text, "Plain hall "+suffix) {
		t.Fatalf("offer: %s", text)
	}
	owner.press("wz:plan:upload", "Пришлите схему зала файлом")

	// ── files that must not move the wizard ─────────────────────────────────
	// A picture named like a plan: told apart by its content.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01")
	planUpFile(t, tg, wzPlanUpOwnerTG, "hall.svg", png, 0, "Это не SVG")
	// A real SVG named like a picture is accepted by content, see below; a text
	// file is not.
	planUpFile(t, tg, wzPlanUpOwnerTG, "notes.svg", []byte("hello, not a plan"), 0, "Это не SVG")
	// Too big: refused on the declared size, before any download.
	planUpFile(t, tg, wzPlanUpOwnerTG, "huge.svg", []byte(planUpSVG), 4<<20, "слишком большой")
	// A photo is no plan.
	planUpPhoto(t, tg, wzPlanUpOwnerTG, "Это не файл")
	// An SVG the importer refuses: told in words, nothing stored.
	text := planUpFile(t, tg, wzPlanUpOwnerTG, "bad.svg", []byte(planUpBadSVG), 0, "Пока не могу использовать этот файл")
	if !strings.Contains(text, "PriceCategory") || !strings.Contains(text, "продолжите без схемы") {
		t.Fatalf("refusal text: %s", text)
	}
	nothingStored("after the refused files")
	// The wizard is still waiting for a file: a typed word does not move it.
	owner.say("привет", "Пришлите схему зала файлом")

	// ── a good file: one colour, one category ───────────────────────────────
	text = planUpFile(t, tg, wzPlanUpOwnerTG, "Hall_one.svg", []byte(planUpSVG), 0, "сохранена")
	for _, want := range []string{"Hall one", "мест — 3", "Standard: 3 мест", "Все места в одной категории", "нет группы «Legend»", "схема зала"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lacks %q:\n%s", want, text)
		}
	}
	var planID, versionID uuid.UUID
	var planType, planStatus, planName string
	var seats int
	if err := pool.QueryRow(ctx, `SELECT p.id, p.plan_type, p.status, p.name, v.id, v.capacity_seated
	        FROM seating_plans p JOIN seating_plan_versions v ON v.id = p.current_version_id
	        WHERE p.owner_org_id = $1 AND p.venue_id = $2`, f.orgID, venue).Scan(&planID, &planType, &planStatus, &planName, &versionID, &seats); err != nil {
		t.Fatalf("the plan: %v", err)
	}
	if planType != "assigned_seats" || planStatus != "active" || planName != "Hall one" || seats != 3 {
		t.Fatalf("plan = %s %s %q %d seats", planType, planStatus, planName, seats)
	}
	var mediaOrg, mediaOwner uuid.UUID
	var mediaType string
	var assetID *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT org_id, owner_id, content_type FROM media_objects WHERE owner_type = 'seating_plan_svg' AND org_id = $1`, f.orgID).Scan(&mediaOrg, &mediaOwner, &mediaType); err != nil {
		t.Fatalf("the stored file: %v", err)
	}
	if mediaOwner != planID || mediaType != "image/svg+xml" {
		t.Fatalf("stored file owner %s type %s", mediaOwner, mediaType)
	}
	if err := pool.QueryRow(ctx, `SELECT svg_asset_media_id FROM seating_plan_versions WHERE id = $1`, versionID).Scan(&assetID); err != nil || assetID == nil {
		t.Fatalf("the version does not link its file: %v %v", assetID, err)
	}

	// ── the date is sold by the new plan ────────────────────────────────────
	owner.press("wz:plan:0", "Категория «")
	owner.say("40", "Схема «")
	owner.press("wz:next", "Описание мероприятия")
	owner.press("wz:skip", "Валюта цен")
	owner.press("wz:cur:EUR", "Открыть продажи сразу")
	owner.press("wz:pub:now", "Проверьте и опубликуйте")
	owner.press("wz:publish", "Готово!")

	var bound uuid.UUID
	var capacity int
	if err := pool.QueryRow(ctx, `SELECT s.seating_plan_version_id, s.capacity_total FROM events e JOIN sessions s ON s.event_id = e.id
	        WHERE e.org_id = $1 AND e.name = $2`, f.orgID, eventName).Scan(&bound, &capacity); err != nil {
		t.Fatalf("event row: %v", err)
	}
	if bound != versionID || capacity != 3 {
		t.Fatalf("session = version %s capacity %d, want %s / 3", bound, capacity, versionID)
	}
	var tierPrice int64
	if err := pool.QueryRow(ctx, `SELECT tt.price_amount FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id JOIN events e ON e.id = s.event_id
	        WHERE e.org_id = $1 AND e.name = $2`, f.orgID, eventName).Scan(&tierPrice); err != nil || tierPrice != 4000 {
		t.Fatalf("the category price = %d (%v), want 4000", tierPrice, err)
	}
}

// A venue made in the wizard gets the same offer; a plan with several
// categories lists each one with its places; and "without a plan" goes on.
func TestBotE2E_WizardPlanUploadNewVenue(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	srv.cfg.AppPublicURL = api.URL
	const ownerTG = wzPlanUpOwnerTG + 1
	linkECBotUser(t, f, srv, ownerTG, "owner")
	ctx := context.Background()

	t.Cleanup(func() {
		c := context.Background()
		for _, st := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM bot_drafts WHERE org_id = $1`,
			`DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM venues WHERE org_id = $1)`,
			`UPDATE seating_plans SET current_version_id = NULL WHERE owner_org_id = $1`,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
			`DELETE FROM seating_plans WHERE owner_org_id = $1`,
			`DELETE FROM media_objects WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'owner_org_id' = $1 OR metadata->>'org_id' = $1`,
		} {
			args := []any{f.orgID}
			if strings.Contains(st, "audit_events") {
				args = []any{f.orgID.String()}
			}
			if _, err := pool.Exec(c, st, args...); err != nil {
				t.Logf("plan upload cleanup %q: %v", st, err)
			}
		}
	})

	var countryID, cityID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&countryID); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&cityID); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "domain", "seating", "testdata", "Palac_Akropolis_GA.svg"))
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	g, _, verrs := seating.ImportSVG(raw)
	if len(verrs) != 0 {
		t.Fatalf("import plan: %v", verrs)
	}
	// The places of each category: its seats, or its capacity for a standing zone.
	perCategory := map[int]int{}
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				perCategory[s.CategoryIndex]++
			}
		}
	}
	for _, c := range g.Categories {
		if c.IsGA() {
			perCategory[c.Index] = c.Capacity
		}
	}
	if len(perCategory) < 2 {
		t.Fatalf("the fixture plan must have several categories, has %d", len(perCategory))
	}

	tg := newStubTelegram(t)
	startSeatLayoutBot(t, pool, api, tg, nil)
	owner := ecDriver{t, tg, ownerTG}
	suffix := uuid.NewString()[:6]

	owner.press("wz:new", "Как называется")
	owner.say("New venue show "+suffix, "Возраст")
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
	owner.press("wz:venue:new", "Название площадки")
	owner.say("Made hall "+suffix, "Адрес площадки")
	owner.press("wz:skip", "Сколько мест")
	// The venue is made at once (Estonia has one zone) and has no plan.
	owner.say("200", "нет схемы зала")
	owner.press("wz:plan:upload", "Пришлите схему зала файлом")

	text := planUpFile(t, tg, ownerTG, "Palac_Akropolis_GA.svg", raw, 0, "сохранена")
	for idx, n := range perCategory {
		var name string
		for _, c := range g.Categories {
			if c.Index == idx {
				name = c.Name
			}
		}
		if want := fmt.Sprintf("%s: %d мест", name, n); !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Все места в одной категории") {
		t.Errorf("several categories must not carry the one-category advice:\n%s", text)
	}
	var venueID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM venues WHERE org_id = $1 AND name = $2`, f.orgID, "Made hall "+suffix).Scan(&venueID); err != nil {
		t.Fatalf("the new venue: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM seating_plans WHERE owner_org_id = $1 AND venue_id = $2 AND status = 'active' AND current_version_id IS NOT NULL`, f.orgID, venueID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("plans of the new venue = %d (%v)", n, err)
	}
	// Seats and a standing zone together make a mixed plan.
	var kind string
	if err := pool.QueryRow(ctx, `SELECT plan_type FROM seating_plans WHERE owner_org_id = $1 AND venue_id = $2`, f.orgID, venueID).Scan(&kind); err != nil || kind != "mixed" {
		t.Fatalf("plan type = %q (%v), want mixed", kind, err)
	}
	// Taking "without a plan" from the plan list: the places were answered
	// already, so the wizard goes straight to "one more date?".
	owner.press("wz:plan:none", "Добавить ещё один сеанс")
}
