//go:build integration

package httpserver

// End-to-end proof of the bot's side of the hall plan intake (spec 64, wave 1)
// through the real router, the real bot and the stub Telegram:
//
//   - the wizard, a venue without a plan: the organizer sends a PDF and a photo,
//     presses "Done", ONE intake with two sources waits for an administrator and
//     the draft is paused on it; the administrator claims it, posts an SVG draft
//     and returns it, the notice loop tells the organizer, who opens the review
//     (a picture), presses "Correct", and the wizard goes on with the new plan:
//     the saved date is sold by that plan version and the final screen offers
//     "Split seats by price";
//   - a valid SVG goes straight to the review, an open intake is shown instead of
//     a second one, "Fix it" stores the organizer's text and sends the intake back
//     to an administrator, a broken SVG becomes an intake for an administrator;
//   - a rejected intake tells the organizer the reason.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

const planIntakeE2EOwnerTG = int64(91901)

// planUpSVG is a plan with ONE category and three seats (the same shape the
// older plan upload test used, and the plan seating_plan_verify_integration_test.go
// reads). It has no Legend group, so the importer warns.
const planUpSVG = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 200 100">` +
	`<g id="PriceCategory"><circle inkscape:label="#Standard" cx="1" cy="1" r="1" style="fill:#ff0000"/></g>` +
	`<g inkscape:label="#Parter"><title>1</title>` +
	`<circle cx="10" cy="10" r="3" style="fill:#ff0000"><title>1</title></circle>` +
	`<circle cx="20" cy="10" r="3" style="fill:#ff0000"><title>2</title></circle>` +
	`<circle cx="30" cy="10" r="3" style="fill:#ff0000"><title>3</title></circle>` +
	`</g></svg>`

// piBadSVG parses as an <svg> but names no categories: the importer refuses it.
const piBadSVG = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 100 100">` +
	`<g inkscape:label="#Parter"><title>1</title><circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>1</title></circle></g></svg>`

// piEnv is one organizer in the bot with a venue that has no plan, and a
// platform administrator for the API.
type piEnv struct {
	t         *testing.T
	pool      *pgxpool.Pool
	f         *botInviteFixture
	api       *httptest.Server
	srv       *Server
	tg        *stubTelegram
	owner     ecDriver
	ownerTG   int64
	venue     uuid.UUID
	venueName string
	suffix    string
	super     string
	country   uuid.UUID
	city      uuid.UUID
}

func newPIEnv(t *testing.T, ownerTG int64) *piEnv {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	t.Cleanup(api.Close)
	srv.cfg.AppPublicURL = api.URL
	linkECBotUser(t, f, srv, ownerTG, "owner")
	ctx := context.Background()
	e := &piEnv{t: t, pool: pool, f: f, api: api, srv: srv, ownerTG: ownerTG, suffix: uuid.NewString()[:6]}

	t.Cleanup(func() {
		c := context.Background()
		for _, st := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM bot_drafts WHERE org_id = $1`,
			`DELETE FROM venue_plan_intakes WHERE org_id = $1`,
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
				t.Logf("plan intake cleanup %q: %v", st, err)
			}
		}
	})

	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&e.country); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&e.city); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}
	e.venue = uuid.New()
	e.venueName = "Intake hall " + e.suffix
	if _, err := pool.Exec(ctx, `INSERT INTO venues (id, org_id, name, city_id, timezone) VALUES ($1, $2, $3, $4, 'Europe/Tallinn')`,
		e.venue, f.orgID, e.venueName, e.city); err != nil {
		t.Fatalf("venue: %v", err)
	}

	// A platform administrator for the admin routes.
	q := gen.New(pool)
	admin, err := q.InsertUser(ctx, "pi-admin-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
	           SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, admin.ID); err != nil {
		t.Fatalf("superadmin role: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1`, admin.ID) })
	e.super, _, err = auth.IssueJWT(botTestJWTSecret, admin.ID, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	e.tg = newStubTelegram(t)
	// The notice loop runs for real, every 150 ms.
	startSeatLayoutBot(t, pool, api, e.tg, func(o *eventbot.Options) { o.NoticeEvery = 150 * time.Millisecond })
	e.owner = ecDriver{t, e.tg, ownerTG}
	return e
}

func (e *piEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// intake is the newest intake of the venue: its id and state.
func (e *piEnv) intake() (id, state string) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id::text, state FROM venue_plan_intakes WHERE org_id = $1 AND venue_id = $2 ORDER BY created_at DESC LIMIT 1`,
		e.f.orgID, e.venue).Scan(&id, &state); err != nil {
		e.t.Fatalf("the intake: %v", err)
	}
	return id, state
}

// admin calls an administrator's route.
func (e *piEnv) admin(method, path, reason, body string) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.api.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.super)
	req.Header.Set("X-Admin-Reason", reason)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := e.api.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

func (e *piEnv) adminOK(method, path, reason, body string) {
	e.t.Helper()
	if status, raw := e.admin(method, path, reason, body); status/100 != 2 {
		e.t.Fatalf("%s %s: %d %s", method, path, status, raw)
	}
}

// doc sends a document the way Telegram delivers one: the stub serves the bytes
// staged for its file id.
func (e *piEnv) doc(fileID, name string, data []byte, expect string) string {
	e.t.Helper()
	e.tg.stageFile(fileID, data)
	mark := e.tg.mark()
	e.tg.push(fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"document":{"file_id":%q,"file_unique_id":"u-%s","file_name":%q,"file_size":%d}}`,
		time.Now().UnixNano()%100000, e.ownerTG, e.ownerTG, fileID, fileID, name, len(data)))
	return e.tg.waitSince(e.t, mark, expect)
}

// photo sends a photo (a JPEG of the largest size Telegram offers).
func (e *piEnv) photo(fileID string, data []byte, expect string) string {
	e.t.Helper()
	e.tg.stageFile(fileID, data)
	mark := e.tg.mark()
	e.tg.push(fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"photo":[{"file_id":%q,"file_unique_id":"u-%s","width":640,"height":480,"file_size":%d}]}`,
		time.Now().UnixNano()%100000, e.ownerTG, e.ownerTG, fileID, fileID, len(data)))
	return e.tg.waitSince(e.t, mark, expect)
}

// keyboardsHave reports whether any message of the chat carries a button with
// this callback data now.
func (e *piEnv) keyboardsHave(data string) bool {
	e.tg.mu.Lock()
	defer e.tg.mu.Unlock()
	for _, m := range e.tg.msgs {
		if m.chat != e.ownerTG {
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

func (e *piEnv) draftState() (step string, paused bool, intakeID string) {
	e.t.Helper()
	var raw []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT state FROM bot_drafts WHERE org_id = $1`, e.f.orgID).Scan(&raw); err != nil {
		e.t.Fatalf("the draft: %v", err)
	}
	var d struct {
		Step         string `json:"step"`
		Paused       bool   `json:"paused"`
		PlanIntakeID string `json:"plan_intake_id"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		e.t.Fatal(err)
	}
	return d.Step, d.Paused, d.PlanIntakeID
}

// wizardToVenue walks the wizard to the venue question and picks the venue.
func (e *piEnv) wizardToVenue(eventName string) {
	o := e.owner
	o.press("wz:new", "Как называется")
	o.say(eventName, "Возраст")
	o.press("wz:age:16+", "Под каким брендом")
	o.press("wz:prom:org", "афишу")
	o.press("wz:skip", "Дата сеанса 1")
	o.say("15.12.2099", "Вы написали")
	o.press("wz:dc:2099-12-15", "Время начала")
	o.press("wz:default", "Продажа билетов закроется в начале")
	o.press("wz:se:60", "Во сколько открывается вход")
	o.press("wz:dr:30", "В какой части мира")
	o.press("wz:region:europe", "В какой стране")
	o.press("wz:country:"+e.country.String(), "В каком городе")
	o.press("wz:city:"+e.city.String(), "Где проходит")
	if text := o.press("wz:venue:"+e.venue.String(), "нет схемы зала"); !strings.Contains(text, e.venueName) {
		e.t.Fatalf("offer: %s", text)
	}
}

// (a) The wizard waits for an administrator and goes on with the plan they built.
func TestBotE2E_PlanIntake_WizardWaitsForAnAdministrator(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG)
	eventName := "Plan intake show " + e.suffix
	e.wizardToVenue(eventName)
	o := e.owner

	o.press("wz:plan:upload", "Пришлите файлы схемы")
	// Text is no file.
	o.say("привет", "Это не файл")
	e.doc("pi-pdf", "hall.pdf", vpiPDF(), "Файл 1 получен")
	text := e.photo("pi-jpg", vpiJPEG(t), "Файл 2 получен")
	if !strings.Contains(text, "Фото получено") {
		t.Fatalf("the photo advice is missing:\n%s", text)
	}
	if n := e.count(`SELECT count(*) FROM venue_plan_intakes WHERE org_id = $1`, e.f.orgID); n != 0 {
		t.Fatalf("%d intakes exist before \"Done\"", n)
	}

	text = o.press("pi:done", "Файл получен. Мы строим схему зала")
	if !strings.Contains(text, "несколько минут") || strings.Contains(text, "администратор") || !strings.Contains(text, "Черновик мероприятия сохранён") {
		t.Fatalf("the received text:\n%s", text)
	}
	id, state := e.intake()
	if state != "received" {
		t.Fatalf("intake state = %s, want received (waiting for the generator job)", state)
	}
	if n := e.count(`SELECT count(*) FROM venue_plan_intake_sources WHERE intake_id = $1`, id); n != 2 {
		t.Fatalf("sources = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM venue_plan_intakes WHERE id = $1 AND channel = 'telegram_bot'`, id); n != 1 {
		t.Fatal("the intake is not from the bot")
	}
	if step, paused, intake := e.draftState(); step != "s_plan_file" || !paused || intake != id {
		t.Fatalf("draft = %s paused=%v intake=%s", step, paused, intake)
	}

	// A worker without a generator sends it to an administrator, who builds a
	// draft and returns it.
	runVenuePlanGenerateJobs(t, e.pool, nil, nil, id)
	if _, state := e.intake(); state != "operator" {
		t.Fatalf("after the generator job: state %s, want operator", state)
	}
	adm := "/v1/admin/plan-intakes/" + id
	e.adminOK(http.MethodPost, adm+"/claim", "taking it", "")
	body, _ := json.Marshal(map[string]any{"svg": planUpSVG, "note": "drawn by hand"})
	e.adminOK(http.MethodPost, adm+"/drafts", "drawn", string(body))
	mark := e.tg.mark()
	e.adminOK(http.MethodPost, adm+"/return", "ready", `{"note":"Проверьте балкон"}`)

	// The notice loop tells the organizer, with the note and the button.
	notice := e.tg.waitSince(t, mark, "Администратор поправил схему зала")
	if !strings.Contains(notice, e.venueName) || !strings.Contains(notice, "Проверьте балкон") {
		t.Fatalf("the notice:\n%s", notice)
	}
	if !e.keyboardsHave("pi:rv:" + id) {
		t.Fatal("the notice has no \"Check the plan\" button")
	}

	// The review is a picture with a summary.
	review := o.press("pi:rv:"+id, "[photo plan.png]")
	if !strings.Contains(review, "Мест: 3") || !strings.Contains(review, "Рядов: 1") {
		t.Fatalf("the review caption:\n%s", review)
	}
	pngBytes, ok := e.tg.document("plan.png")
	if !ok {
		t.Fatal("no picture was sent")
	}
	if img, err := png.Decode(bytes.NewReader(pngBytes)); err != nil || img.Bounds().Dx() < 50 {
		t.Fatalf("the picture is not a PNG: %v", err)
	}
	for _, d := range []string{"pi:ok:" + id + ":1", "pi:fix:" + id + ":1", "pi:adm:" + id} {
		if !e.keyboardsHave(d) {
			t.Errorf("the review lacks the button %s", d)
		}
	}

	// "Correct": the plan is saved and the wizard is unpaused on the plan question.
	o.press("pi:ok:"+id+":1", "Схема зала сохранена: 3 мест")
	if _, state := e.intake(); state != "approved" {
		t.Fatalf("intake state = %s, want approved", state)
	}
	var planID, versionID uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT p.id, p.current_version_id FROM seating_plans p
	        JOIN venue_plan_intakes i ON i.plan_id = p.id WHERE i.id = $1`, id).Scan(&planID, &versionID); err != nil {
		t.Fatalf("the plan: %v", err)
	}
	if step, paused, intake := e.draftState(); step != "s_plan" || paused || intake != "" {
		t.Fatalf("draft = %s paused=%v intake=%q, want s_plan unpaused", step, paused, intake)
	}

	// The wizard finishes; the date is sold by the new plan version.
	o.press("wz:plan:0", "Категория «")
	o.say("40", "Нужны ещё ценовые категории")
	o.press("wz:extra:done", "Схема «")
	o.press("wz:next", "Описание мероприятия")
	o.press("wz:skip", "Валюта цен")
	o.press("wz:cur:EUR", "Открыть продажи сразу")
	o.press("wz:pub:now", "Проверьте и опубликуйте")
	o.press("wz:publish", "Готово!")

	var sessionID, bound uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT s.id, s.seating_plan_version_id FROM events ev JOIN sessions s ON s.event_id = ev.id
	        WHERE ev.org_id = $1 AND ev.name = $2`, e.f.orgID, eventName).Scan(&sessionID, &bound); err != nil {
		t.Fatalf("event row: %v", err)
	}
	if bound != versionID {
		t.Fatalf("the session is sold by version %s, want %s", bound, versionID)
	}
	// The final screen offers "Split seats by price" for the date.
	deadline := time.Now().Add(10 * time.Second)
	for !e.keyboardsHave("sl:o:"+sessionID.String()) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !e.keyboardsHave("sl:o:" + sessionID.String()) {
		t.Fatal("the saved event has no \"Split seats by price\" button")
	}
}

// (b), (c) A valid SVG goes straight to the review, an open intake is shown
// instead of a second one, "Fix it" sends the text to an administrator, and a
// broken SVG becomes an intake for an administrator.
func TestBotE2E_PlanIntake_SVGReviewAndFix(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+1)
	o := e.owner

	// Outside the wizard: the press of "Upload a seating plan".
	o.press("pi:up:"+e.venue.String(), "Пришлите файлы схемы")
	e.doc("pi-svg", "plan.svg", []byte(planUpSVG), "Файл 1 получен")
	review := o.press("pi:done", "[photo plan.png]")
	if !strings.Contains(review, "Мест: 3") {
		t.Fatalf("the review caption:\n%s", review)
	}
	id, state := e.intake()
	if state != "review" {
		t.Fatalf("a valid SVG: state = %s, want review", state)
	}
	if n := e.count(`SELECT current_draft FROM venue_plan_intakes WHERE id = $1`, id); n != 1 {
		t.Fatalf("current draft = %d", n)
	}

	// A second upload is not started: the open intake is shown with its button.
	text := o.press("pi:up:"+e.venue.String(), "уже есть заявка")
	if !strings.Contains(text, "проверить") || !e.keyboardsHave("pi:rv:"+id) {
		t.Fatalf("the open intake:\n%s", text)
	}

	// "Fix it": a free text moves the intake back to an administrator.
	o.press("pi:rv:"+id, "[photo plan.png]")
	o.press("pi:fix:"+id+":1", "Напишите, что исправить")
	o.say("В ряду 1 должно быть 5 мест", "Администратор исправит схему")
	var reason, correction string
	if err := e.pool.QueryRow(context.Background(), `SELECT state, COALESCE(escalation_reason, '') FROM venue_plan_intakes WHERE id = $1`, id).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "operator" || reason != "organizer_text" {
		t.Fatalf("after the fix: state %s reason %s, want operator organizer_text", state, reason)
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT text FROM venue_plan_corrections WHERE intake_id = $1 AND kind = 'organizer_text'`, id).Scan(&correction); err != nil || correction != "В ряду 1 должно быть 5 мест" {
		t.Fatalf("the stored text = %q (%v)", correction, err)
	}

	// The intake is open (operator): the person may withdraw it.
	o.press("pi:up:"+e.venue.String(), "уже есть заявка")
	o.press("pi:cx:"+id, "Заявка отменена")
	if _, state := e.intake(); state != "cancelled" {
		t.Fatalf("state = %s, want cancelled", state)
	}

	// A broken SVG is no error: the server keeps it as a source for an administrator.
	o.press("pi:up:"+e.venue.String(), "Пришлите файлы схемы")
	e.doc("pi-bad", "bad.svg", []byte(piBadSVG), "Файл 1 получен")
	text = o.press("pi:done", "Файл получен. Мы строим схему зала")
	if !strings.Contains(text, "Этот SVG не подходит для продажи мест") {
		t.Fatalf("the SVG note is missing:\n%s", text)
	}
	badID, state := e.intake()
	if state != "received" || badID == id {
		t.Fatalf("a broken SVG: state %s intake %s", state, badID)
	}
	runVenuePlanGenerateJobs(t, e.pool, nil, nil, badID)
	if _, state := e.intake(); state != "operator" {
		t.Fatalf("a broken SVG after the generator job: state %s, want operator", state)
	}
	if n := e.count(`SELECT count(*) FROM venue_plan_intake_sources WHERE intake_id = $1 AND kind = 'svg'`, badID); n != 1 {
		t.Fatalf("svg sources = %d, want 1", n)
	}

	// (d) The administrator cannot build it: the organizer is told the reason.
	adm := "/v1/admin/plan-intakes/" + badID
	e.adminOK(http.MethodPost, adm+"/claim", "taking it", "")
	mark := e.tg.mark()
	e.adminOK(http.MethodPost, adm+"/reject", "no data", `{"reason":"На плане нет мест"}`)
	notice := e.tg.waitSince(t, mark, "Причина: На плане нет мест")
	if !strings.Contains(notice, e.venueName) || !strings.Contains(notice, "другой файл") {
		t.Fatalf("the rejected notice:\n%s", notice)
	}
	if !e.keyboardsHave("pi:again:"+badID) || e.keyboardsHave("pi:skip:"+badID) {
		t.Fatal("the rejected notice must offer other files, and no \"without a plan\" outside the wizard")
	}
}

// Files the server cannot read leave the person where they are, with a way on.
func TestBotE2E_PlanIntake_UnsupportedFileKeepsTheWizard(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+2)
	e.wizardToVenue("Plan intake bad file " + e.suffix)
	o := e.owner
	o.press("wz:plan:upload", "Пришлите файлы схемы")
	e.doc("pi-bin", "notes.bin", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x7f, 0x80}, "Файл 1 получен")
	text := o.press("pi:done", "Не получается прочитать файл «notes.bin»")
	// The files stay in the collection: the person can remove the refused one.
	if !e.keyboardsHave("pi:rm:1") {
		t.Fatal("the refusal does not offer to remove the refused file")
	}
	if !strings.Contains(text, "PDF") {
		t.Fatalf("the refusal does not say what to send:\n%s", text)
	}
	if n := e.count(`SELECT count(*) FROM venue_plan_intakes WHERE org_id = $1`, e.f.orgID); n != 0 {
		t.Fatalf("%d intakes were created for an unsupported file", n)
	}
	// The wizard stays on the file step and is not paused.
	if step, paused, intake := e.draftState(); step != "s_plan_file" || paused || intake != "" {
		t.Fatalf("draft = %s paused=%v intake=%q", step, paused, intake)
	}
	// Too big: refused on the declared size, before any download.
	e.tg.stageFile("pi-big", []byte("x"))
	mark := e.tg.mark()
	e.tg.push(fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"document":{"file_id":"pi-big","file_unique_id":"u-big","file_name":"huge.pdf","file_size":%d}}`,
		time.Now().UnixNano()%100000, e.ownerTG, e.ownerTG, 30<<20))
	e.tg.waitSince(t, mark, "Файл больше 20 МБ")
}
