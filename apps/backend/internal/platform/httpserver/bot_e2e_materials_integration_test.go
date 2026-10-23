//go:build integration

package httpserver

// End-to-end proof of the Materials screens (task 7-3, spec 40 §5.7) through
// the real router, the real bot and the stub Telegram: the owner opens the
// menu from the event card's button, adds a photo (the bot downloads it from
// Telegram, stores it as an event_gallery media object and attaches it), moves
// and captions it, deletes it with a question, fills the gallery to its limit
// of six and hears the refusal in words; adds video links (a bad one refused),
// "where else to buy" links (seller defaults to the site, renamed), the map
// point as typed coordinates and as a Telegram location, and photo-report
// photos; a typed text where a photo is asked, and a photo where text is asked,
// change nothing; the manager adds a report photo; the agent sees the screens
// without a button that changes anything and a forged press writes nothing; a
// user of another organization cannot open the event's materials. The stub
// records message TEXT and callback data, so every effect is read from the
// database.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	mtOwnerTG    = int64(91701)
	mtManagerTG  = int64(91702)
	mtAgentTG    = int64(91703)
	mtOutsiderTG = int64(91704)
)

// e2ePhotoAs is a Telegram photo message (three sizes) from an account; the
// stub serves the staged bytes for its file id.
func e2ePhotoAs(userID int64, caption string) string {
	cap := ""
	if caption != "" {
		cap = fmt.Sprintf(`,"caption":%q`, caption)
	}
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"photo":[{"file_id":"p-s","file_unique_id":"u1","width":90,"height":112,"file_size":1000},{"file_id":"p-l","file_unique_id":"u2","width":1000,"height":1250,"file_size":4000}]%s}`,
		time.Now().UnixNano()%100000, userID, userID, cap)
}

func e2eLocationAs(userID int64, lat, lon float64) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"location":{"latitude":%v,"longitude":%v}}`,
		time.Now().UnixNano()%100000, userID, userID, lat, lon)
}

func TestBotE2E_Materials(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	ctx := context.Background()
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, mtOwnerTG, "owner")
	linkECBotUser(t, f, srv, mtManagerTG, "manager")
	linkECBotUser(t, other, srv, mtOutsiderTG, "owner")
	q := gen.New(pool)
	agent, err := q.InsertUser(ctx, f.newEmail("agent"), "x", "ru")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(ctx, agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	if _, err := q.UpsertBotTelegramLink(ctx, mtAgentTG, agent.ID, nil, "ru", &f.orgID); err != nil {
		t.Fatalf("link agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, mtAgentTG)
	})

	// The roles' truth: the owner and the manager hold event.update, the
	// agent does not (migrations 0074 and 0115).
	for role, want := range map[string]int{"org_admin": 1, "organizer": 1, "agent": 0} {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM role_permissions rp
			  JOIN roles r ON r.id = rp.role_id AND r.org_id IS NULL
			  JOIN permissions p ON p.id = rp.permission_id
			 WHERE r.name = $1 AND p.name = 'event.update'`, role).Scan(&n); err != nil || n != want {
			t.Fatalf("role %s: event.update count = %d (want %d) err=%v", role, n, want, err)
		}
	}

	suffix := uuid.NewString()[:6]
	eventID := uuid.New()
	eventName := "MT Show " + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, 'published', 'public', $4)`,
		eventID, f.orgID, eventName, "mt-show-"+suffix); err != nil {
		t.Fatalf("event fixture: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM events WHERE org_id = $1`, // the materials cascade with the event
			`DELETE FROM media_objects WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
		} {
			if _, err := pool.Exec(c, sql, f.orgID); err != nil {
				t.Logf("materials cleanup: %s: %v", sql, err)
			}
		}
	})

	tg := newStubTelegram(t)
	tg.poster = e2eWizardPoster(t) // every file id serves this picture
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, mtOwnerTG}
	manager := ecDriver{t, tg, mtManagerTG}
	agentU := ecDriver{t, tg, mtAgentTG}
	outsider := ecDriver{t, tg, mtOutsiderTG}

	count := func(table string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE event_id = $1`, eventID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s *string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("query %q: %v", sql, err)
		}
		if s == nil {
			return "<nil>"
		}
		return *s
	}
	photo := func(d ecDriver, caption, expect string) string {
		t.Helper()
		m := tg.mark()
		tg.push(e2ePhotoAs(d.id, caption))
		return tg.waitSince(t, m, expect)
	}
	labels := func(d ecDriver) []string {
		t.Helper()
		id, _, _ := tg.lastOf(d.id)
		return tg.labelsOf(id)
	}
	hasLabel := func(d ecDriver, want string) bool {
		for _, l := range labels(d) {
			if strings.Contains(l, want) {
				return true
			}
		}
		return false
	}
	noButton := func(d ecDriver, who string, data ...string) {
		t.Helper()
		_, _, kb := tg.lastOf(d.id)
		for _, want := range data {
			if hasPrefixData(kb, want) {
				t.Errorf("%s must not see the button %q: %v", who, want, kb)
			}
		}
	}

	// ── the menu, with counters ───────────────────────────────────────────────
	text := owner.press("ec:mt:"+eventID.String(), "Материалы")
	if !strings.Contains(text, eventName) {
		t.Errorf("menu lacks the event name:\n%s", text)
	}
	for _, want := range []string{"Фото 0/6", "Видео 0/4", "Где ещё купить 0/3", "Карта: не задана", "Фотоотчёт 0/30"} {
		if !hasLabel(owner, want) {
			t.Errorf("menu lacks the button %q: %v", want, labels(owner))
		}
	}

	// ── the gallery: add, caption in the same message, an album ───────────────
	owner.press("mt:s:g", "Фото пока нет")
	owner.press("mt:a", "Пришлите фото")
	owner.say("просто текст", "Сейчас я жду фото")
	if n := count("event_gallery_items"); n != 0 {
		t.Fatalf("a text where a photo is asked must add nothing, got %d", n)
	}
	photo(owner, "Первый ряд", "Добавлено. 1 из 6")
	photo(owner, "", "Добавлено. 2 из 6")
	if n := count("event_gallery_items"); n != 2 {
		t.Fatalf("gallery rows = %d, want 2", n)
	}
	if got := str(`SELECT m.owner_type || '/' || m.content_type FROM event_gallery_items g JOIN media_objects m ON m.id = g.media_id WHERE g.event_id = $1 AND g.position = 0`, eventID); got != "event_gallery/image/png" {
		t.Errorf("first photo media = %s", got)
	}
	if got := str(`SELECT caption FROM event_gallery_items WHERE event_id = $1 AND position = 0`, eventID); got != "Первый ряд" {
		t.Errorf("caption of the first photo = %s", got)
	}
	text = owner.press("mt:s:g", "Фото идут на странице")
	for _, want := range []string{"1. Первый ряд", "2. без подписи", "2 из 6"} {
		if !strings.Contains(text, want) {
			t.Errorf("gallery list lacks %q:\n%s", want, text)
		}
	}

	// ── one photo's card: move, caption, remove the caption, delete ───────────
	owner.press("mt:o:1", "Фото 2 из 2")
	owner.press("mt:u", "Перенесено")
	if got := str(`SELECT caption FROM event_gallery_items WHERE event_id = $1 AND position = 1`, eventID); got != "Первый ряд" {
		t.Errorf("after moving the second photo up, the captioned one is second, got %s", got)
	}
	owner.press("mt:c", "Пришлите подпись")
	owner.say(strings.Repeat("я", 201), "не больше 200")
	owner.say("Новая   подпись", "Сохранено")
	if got := str(`SELECT caption FROM event_gallery_items WHERE event_id = $1 AND position = 0`, eventID); got != "Новая подпись" {
		t.Errorf("caption = %q", got)
	}
	owner.press("mt:cx", "Сохранено")
	if got := str(`SELECT caption FROM event_gallery_items WHERE event_id = $1 AND position = 0`, eventID); got != "<nil>" {
		t.Errorf("a removed caption must be NULL, got %q", got)
	}
	owner.press("mt:x", "Удалить это фото")
	if n := count("event_gallery_items"); n != 2 {
		t.Fatalf("the question alone must delete nothing, rows = %d", n)
	}
	owner.press("mt:y", "Удалено")
	if n := count("event_gallery_items"); n != 1 {
		t.Fatalf("gallery rows after delete = %d, want 1", n)
	}
	if got := str(`SELECT position::text FROM event_gallery_items WHERE event_id = $1`, eventID); got != "0" {
		t.Errorf("the gap closes: remaining position = %s", got)
	}
	// A second confirm press has nothing to confirm.
	owner.press("mt:y", "из 6")
	if n := count("event_gallery_items"); n != 1 {
		t.Fatalf("a repeated confirm deleted again: %d", n)
	}

	// ── fill to the limit of six, then the refusal in words ───────────────────
	owner.press("mt:a", "Пришлите фото")
	for i := 0; i < 4; i++ {
		photo(owner, "", "Добавлено. ")
	}
	photo(owner, "", "Добавлено.") // the sixth: the part is full, the list is shown
	if n := count("event_gallery_items"); n != 6 {
		t.Fatalf("gallery rows = %d, want 6", n)
	}
	noButton(owner, "owner with a full gallery", "mt:a")
	owner.press("mt:a", "Этот раздел заполнен") // a stale or forged press
	if n := count("event_gallery_items"); n != 6 {
		t.Fatalf("a full gallery took another photo: %d", n)
	}

	// ── videos ────────────────────────────────────────────────────────────────
	owner.press("mt:s:v", "Видео пока нет")
	owner.press("mt:a", "Пришлите ссылку на видео")
	owner.say("не ссылка", "не подходит")
	owner.say("youtu.be/dQw4w9WgXcQ", "Добавлено")
	if got := str(`SELECT provider || ' ' || url FROM event_videos WHERE event_id = $1`, eventID); got != "youtube https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("video = %q", got)
	}
	for i := 2; i <= 4; i++ {
		owner.press("mt:a", "Пришлите ссылку на видео")
		owner.say(fmt.Sprintf("https://example.com/v%d", i), "Добавлено")
	}
	if n := count("event_videos"); n != 4 {
		t.Fatalf("videos = %d, want 4", n)
	}
	owner.press("mt:a", "Этот раздел заполнен")
	owner.press("mt:o:0", "Видео 1 из 4")
	owner.press("mt:x", "Удалить это видео")
	owner.press("mt:y", "Удалено")
	if n := count("event_videos"); n != 3 {
		t.Fatalf("videos after delete = %d, want 3", n)
	}

	// ── where else to buy: the seller defaults to the site, then is renamed ───
	owner.press("mt:s:p", "Ссылок пока нет")
	owner.press("mt:a", "Пришлите ссылку на страницу")
	owner.say("ticketportal.cz/event/12", "Добавлено")
	if got := str(`SELECT url || ' ' || seller_name FROM event_partner_links WHERE event_id = $1`, eventID); got != "https://ticketportal.cz/event/12 ticketportal.cz" {
		t.Errorf("partner link = %q", got)
	}
	owner.press("mt:o:0", "Продавец: ticketportal.cz")
	owner.press("mt:n", "Пришлите название продавца")
	owner.say("Ticketportal", "Сохранено")
	if got := str(`SELECT seller_name FROM event_partner_links WHERE event_id = $1`, eventID); got != "Ticketportal" {
		t.Errorf("seller = %q", got)
	}
	for i := 2; i <= 3; i++ {
		owner.press("mt:s:p", "Где ещё купить")
		owner.press("mt:a", "Пришлите ссылку на страницу")
		owner.say(fmt.Sprintf("https://tickets%d.example.com/e", i), "Добавлено")
	}
	owner.press("mt:a", "Этот раздел заполнен")
	if n := count("event_partner_links"); n != 3 {
		t.Fatalf("partner links = %d, want 3", n)
	}

	// ── the map: typed coordinates, a Telegram location, delete ───────────────
	owner.press("mt:s:m", "Точки пока нет")
	owner.press("mt:a", "широту и долготу")
	owner.say("где-то в Праге", "Не получилось прочитать точку")
	owner.say("50,0755; 14,4378", "Широта 50.075500")
	if got := str(`SELECT latitude::text || ' ' || longitude::text FROM event_map_points WHERE event_id = $1`, eventID); got != "50.075500 14.437800" {
		t.Errorf("map point = %q", got)
	}
	_, mapText, _ := tg.lastOf(mtOwnerTG)
	if !strings.Contains(mapText, "openstreetmap.org/?mlat=50.075500&amp;mlon=14.437800") {
		t.Errorf("the card must carry the OpenStreetMap link:\n%s", mapText)
	}
	owner.press("mt:a", "широту и долготу")
	m := tg.mark()
	tg.push(e2eLocationAs(mtOwnerTG, 41.4036, 2.1744))
	tg.waitSince(t, m, "Широта 41.403600")
	if got := str(`SELECT latitude::text || ' ' || longitude::text FROM event_map_points WHERE event_id = $1`, eventID); got != "41.403600 2.174400" {
		t.Errorf("map point after a location = %q", got)
	}
	owner.press("mt:x", "Удалить точку")
	owner.press("mt:y", "Точки пока нет")
	if n := count("event_map_points"); n != 0 {
		t.Fatalf("map points after delete = %d", n)
	}

	// ── the photo report, with the hint that buyers see it after the date ─────
	text = owner.press("mt:s:r", "пока нет фото")
	if !strings.Contains(text, "после даты мероприятия") {
		t.Errorf("the report list must say buyers see it after the date:\n%s", text)
	}
	owner.press("mt:a", "Пришлите фото с мероприятия")
	owner.say("привет", "Сейчас я жду фото")
	photo(owner, "Финал", "Добавлено. 1 из 30")
	if got := str(`SELECT m.owner_type || ' ' || r.caption FROM event_report_photos r JOIN media_objects m ON m.id = r.media_id WHERE r.event_id = $1`, eventID); got != "event_report Финал" {
		t.Errorf("report photo = %q", got)
	}

	// A press elsewhere ends the dialog: a photo sent after it is not taken.
	owner.press("home", "")
	var dialogs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'materials'`, mtOwnerTG).Scan(&dialogs); err != nil || dialogs != 0 {
		t.Fatalf("the materials dialog must end with a press elsewhere: n=%d err=%v", dialogs, err)
	}
	tg.push(e2ePhotoAs(mtOwnerTG, ""))
	time.Sleep(600 * time.Millisecond)
	if n := count("event_report_photos"); n != 1 {
		t.Fatalf("a photo after the dialog ended was added: %d", n)
	}

	// ── the manager changes materials too ─────────────────────────────────────
	manager.press("ec:mt:"+eventID.String(), "Материалы")
	manager.press("mt:s:r", "Фотоотчёт")
	manager.press("mt:a", "Пришлите фото с мероприятия")
	photo(manager, "", "Добавлено. 2 из 30")
	if n := count("event_report_photos"); n != 2 {
		t.Fatalf("the manager's report photo is missing: %d", n)
	}

	// ── the agent looks, a forged press writes nothing ────────────────────────
	text = agentU.press("ec:mt:"+eventID.String(), "Вы можете смотреть материалы")
	if !strings.Contains(text, eventName) {
		t.Errorf("agent menu lacks the event:\n%s", text)
	}
	agentU.press("mt:s:r", "Фотоотчёт")
	noButton(agentU, "an agent", "mt:a")
	agentU.press("mt:o:0", "Фото отчёта 1 из 2")
	noButton(agentU, "an agent", "mt:x", "mt:u", "mt:d", "mt:c")
	for _, forged := range []string{"mt:x", "mt:y", "mt:u", "mt:cx"} {
		agentU.press(forged, "Вы можете смотреть материалы")
	}
	agentU.press("mt:a", "Вы можете смотреть материалы")
	tg.push(e2ePhotoAs(mtAgentTG, "подделка"))
	time.Sleep(600 * time.Millisecond)
	if n := count("event_report_photos"); n != 2 {
		t.Fatalf("an agent changed the report: %d rows", n)
	}
	if got := str(`SELECT string_agg(coalesce(caption, '-'), ',' ORDER BY position) FROM event_report_photos WHERE event_id = $1`, eventID); got != "Финал,-" {
		t.Errorf("report captions after the agent's forged presses = %q", got)
	}

	// ── another organization cannot open this event's materials ───────────────
	outsider.press("ec:mt:"+eventID.String(), "Не найдено")
	if n := count("event_gallery_items"); n != 6 {
		t.Fatalf("gallery rows changed: %d", n)
	}
}
