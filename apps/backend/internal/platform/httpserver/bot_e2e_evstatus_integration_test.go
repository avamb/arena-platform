//go:build integration

package httpserver

// End-to-end proof of the event card's status buttons (spec 35 EC-10) through
// the real router, the real bot and the stub Telegram: publish a draft, take an
// event off sale and archive it (one confirmation press each, the screen says
// sold tickets stay valid), delete a never-sold event by typing the word (a
// wrong word first, the English word for the manager), the refusal for an event
// with a paid order (the database untouched, the archive offered instead), a
// sale that lands between the question and the word, an expired and a
// forgotten question, a user of ANOTHER organization, and a bot restart in the
// middle of the delete question.
//
// The stub records message TEXT only, so every effect is checked in the
// database; which buttons a status shows is the unit test's business
// (evstatus_test.go).

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	evsOwnerTG    = int64(7821)
	evsManagerTG  = int64(7822)
	evsOutsiderTG = int64(7823)
)

type evsSeed struct {
	pool   *pgxpool.Pool
	orgID  uuid.UUID
	suffix string
	venue  uuid.UUID
	chann  uuid.UUID
}

// seedEvsOrg prepares an organization that events can be added to.
func seedEvsOrg(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) *evsSeed {
	t.Helper()
	s := &evsSeed{pool: pool, orgID: orgID, suffix: uuid.NewString()[:6], venue: uuid.New(), chann: uuid.New()}
	s.exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, s.venue, orgID, "EVS Hall "+s.suffix)
	s.exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, s.chann, orgID, "EVS Channel "+s.suffix)
	t.Cleanup(func() {
		c := context.Background()
		sessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`
		for _, sql := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM tickets WHERE session_id IN ` + sessions,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE id IN ` + sessions,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
		} {
			if _, err := pool.Exec(c, sql, orgID); err != nil {
				t.Logf("evs bot cleanup: %s: %v", sql, err)
			}
		}
	})
	return s
}

func (s *evsSeed) exec(sql string, args ...any) {
	if _, err := s.pool.Exec(context.Background(), sql, args...); err != nil {
		panic("evs seed " + sql + ": " + err.Error())
	}
}

// event inserts an event with `dates` sessions, each with one priced category.
func (s *evsSeed) event(status, label string, dates int) (id uuid.UUID, name string, sessions []uuid.UUID) {
	id = uuid.New()
	name = "EVS " + label + " " + s.suffix
	s.exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, $4, 'public', $5)`,
		id, s.orgID, name, status, "evs-"+uuid.NewString()[:8])
	for i := 0; i < dates; i++ {
		sid := uuid.New()
		sessions = append(sessions, sid)
		s.exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		        VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 5, 'scheduled', 'EUR', 'override')`,
			sid, id, s.venue, time.Now().UTC().Add(time.Duration(30+i)*24*time.Hour).Truncate(time.Hour))
		s.exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
		        VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 5, 1, true)`, uuid.New(), sid)
	}
	return id, name, sessions
}

// sell puts a paid order with one ticket on the session.
func (s *evsSeed) sell(eventID, sessionID uuid.UUID) {
	res, cs, order := uuid.New(), uuid.New(), uuid.New()
	s.exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	        VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, res, s.orgID, s.chann, sessionID)
	s.exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, cs, s.orgID, s.chann, res)
	s.exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                            source, status, currency, subtotal, discount, charge, total, buyer_email)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'evs-buyer@example.test')`,
		order, s.orgID, s.chann, eventID, sessionID, cs, res)
	s.exec(`INSERT INTO tickets (id, checkout_session_id, session_id, holder_email, order_id) VALUES ($1, $2, $3, 'evs-buyer@example.test', $4)`,
		uuid.New(), cs, sessionID, order)
}

func (s *evsSeed) status(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var st string
	if err := s.pool.QueryRow(context.Background(), `SELECT status FROM events WHERE id = $1`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (s *evsSeed) deleted(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var d bool
	if err := s.pool.QueryRow(context.Background(), `SELECT deleted_at IS NOT NULL FROM events WHERE id = $1`, id).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (s *evsSeed) dialogs(t *testing.T, tg int64) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'evstatus'`, tg).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (s *evsSeed) audits(t *testing.T, id uuid.UUID, action string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2`, action, id.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func evsData(id uuid.UUID, act string) string { return "ec:ev:" + id.String() + ":" + act }

func TestBotE2E_EventStatusButtons(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedEvsOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, evsOwnerTG, "owner")
	linkECBotUser(t, f, srv, evsManagerTG, "manager")
	linkECBotUser(t, other, srv, evsOutsiderTG, "owner")

	// The truth about the roles: both membership roles hold both permissions,
	// so the owner and the manager may use every button.
	for _, role := range []string{"org_admin", "organizer"} {
		for _, perm := range []string{"event.publish", "event.delete"} {
			var n int
			if err := pool.QueryRow(context.Background(), `
				SELECT count(*) FROM role_permissions rp
				  JOIN roles r ON r.id = rp.role_id AND r.org_id IS NULL
				  JOIN permissions p ON p.id = rp.permission_id
				 WHERE r.name = $1 AND p.name = $2`, role, perm).Scan(&n); err != nil || n != 1 {
				t.Fatalf("role %s must hold %s: n=%d err=%v", role, perm, n, err)
			}
		}
	}

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, evsOwnerTG}
	manager := ecDriver{t, tg, evsManagerTG}
	outsider := ecDriver{t, tg, evsOutsiderTG}

	// ── publish a draft, take it off sale, publish again, archive ─────────────
	draft, _, _ := seed.event("draft", "Draft", 1)
	owner.press(evsData(draft, "pub"), "опубликован и в продаже")
	if seed.status(t, draft) != "published" || seed.audits(t, draft, "v1.event.status_update") != 1 {
		t.Fatalf("publish: status=%s audits=%d", seed.status(t, draft), seed.audits(t, draft, "v1.event.status_update"))
	}

	// The question changes nothing and says the sold tickets stay valid.
	ask := owner.press(evsData(draft, "off"), "Снять «")
	if !strings.Contains(ask, "Уже проданные билеты остаются действительными") {
		t.Errorf("take-off-sale question must say sold tickets stay valid:\n%s", ask)
	}
	if seed.status(t, draft) != "published" {
		t.Fatal("the question must not take the event off sale")
	}
	done := owner.press(evsData(draft, "offy"), "снят с продажи")
	if !strings.Contains(done, "остаются действительными") || seed.status(t, draft) != "draft" {
		t.Errorf("take off sale: status=%s\n%s", seed.status(t, draft), done)
	}
	// A stale "take off sale" on an event that is already off sale is a no-op:
	// it says so, changes nothing and writes no second audit row.
	owner.press(evsData(draft, "offy"), "снят с продажи")
	if seed.status(t, draft) != "draft" || seed.audits(t, draft, "v1.event.status_update") != 2 {
		t.Fatalf("a stale button must change nothing: status=%s audits=%d", seed.status(t, draft), seed.audits(t, draft, "v1.event.status_update"))
	}
	owner.press(evsData(draft, "pub"), "опубликован и в продаже")

	ask = owner.press(evsData(draft, "arc"), "в архив?")
	if !strings.Contains(ask, "Уже проданные билеты остаются действительными") || !strings.Contains(ask, "нельзя") {
		t.Errorf("archive question must say tickets stay valid and that it is final:\n%s", ask)
	}
	if seed.status(t, draft) != "published" {
		t.Fatal("the archive question must not archive")
	}
	owner.press(evsData(draft, "arcy"), "в архиве")
	if seed.status(t, draft) != "archived" {
		t.Fatalf("archive: status=%s", seed.status(t, draft))
	}
	owner.press(evsData(draft, "pub"), "невозможно")
	if seed.status(t, draft) != "archived" {
		t.Fatal("an archived event cannot be published again")
	}
	if got := seed.audits(t, draft, "v1.event.status_update"); got != 4 { // pub, off, pub, arc
		t.Errorf("status audits = %d, want 4", got)
	}

	// A draft with no date cannot be published; it says why.
	empty := uuid.New()
	seed.exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'public')`, empty, f.orgID, "EVS Empty "+seed.suffix)
	owner.press(evsData(empty, "pub"), "нет ни одной даты")
	if seed.status(t, empty) != "draft" {
		t.Fatal("a refused publish changes nothing")
	}

	// The manager publishes too.
	mgrDraft, _, _ := seed.event("draft", "Manager Draft", 1)
	manager.press(evsData(mgrDraft, "pub"), "опубликован и в продаже")
	if seed.status(t, mgrDraft) != "published" {
		t.Fatalf("the manager must be able to publish: %s", seed.status(t, mgrDraft))
	}

	// ── delete: a never-sold event, by the typed word ─────────────────────────
	live, liveName, _ := seed.event("published", "Live", 2)
	q := owner.press(evsData(live, "del"), "Последний шаг")
	if !strings.Contains(q, liveName) || !strings.Contains(q, "УДАЛИТЬ") || !strings.Contains(q, "дат — 2") {
		t.Errorf("delete question must name the event, the dates and the word:\n%s", q)
	}
	if seed.deleted(t, live) || seed.dialogs(t, evsOwnerTG) != 1 {
		t.Fatalf("the question deletes nothing and opens a dialog: deleted=%v dialogs=%d", seed.deleted(t, live), seed.dialogs(t, evsOwnerTG))
	}
	if w := owner.say("да", "Это не то слово"); !strings.Contains(w, "УДАЛИТЬ") {
		t.Errorf("a wrong word must repeat the word:\n%s", w)
	}
	if seed.deleted(t, live) || seed.dialogs(t, evsOwnerTG) != 1 {
		t.Fatal("a wrong word deletes nothing and keeps the question")
	}

	// Going elsewhere ends the question: the word typed afterwards deletes nothing.
	owner.press("ec:es:"+live.String(), "Сводка")
	if seed.dialogs(t, evsOwnerTG) != 0 {
		t.Fatal("leaving the card must end the delete question")
	}
	owner.say("УДАЛИТЬ", "Я не понял")
	if seed.deleted(t, live) {
		t.Fatal("a delete word typed with no question must delete nothing")
	}

	owner.press(evsData(live, "del"), "Последний шаг")
	owner.say("УДАЛИТЬ", "удалён")
	if !seed.deleted(t, live) || seed.dialogs(t, evsOwnerTG) != 0 || seed.audits(t, live, "v1.event.delete") != 1 {
		t.Errorf("typed delete: deleted=%v dialogs=%d audits=%d", seed.deleted(t, live), seed.dialogs(t, evsOwnerTG), seed.audits(t, live, "v1.event.delete"))
	}

	// The manager deletes with the English word (accepted in any language).
	mgrLive, _, _ := seed.event("published", "Manager Live", 1)
	manager.press(evsData(mgrLive, "del"), "Последний шаг")
	manager.say("нет", "Это не то слово")
	manager.say("delete", "удалён")
	if !seed.deleted(t, mgrLive) {
		t.Fatal("the manager must be able to delete with the English word")
	}

	// ── an event that has sold: no delete, the archive instead ────────────────
	sold, soldName, soldSessions := seed.event("published", "Sold", 1)
	seed.sell(sold, soldSessions[0])
	blocked := owner.press(evsData(sold, "del"), "удалить нельзя")
	if !strings.Contains(blocked, soldName) || !strings.Contains(blocked, "Оплаченных заказов: 1") || !strings.Contains(blocked, "проданных билетов: 1") || !strings.Contains(blocked, "в архив") {
		t.Errorf("the refusal must name the event, the counts and the archive:\n%s", blocked)
	}
	if seed.dialogs(t, evsOwnerTG) != 0 {
		t.Fatal("a refused delete must not open the typed-word step")
	}
	owner.say("УДАЛИТЬ", "Я не понял")
	if seed.deleted(t, sold) || seed.status(t, sold) != "published" {
		t.Fatal("an event with a paid order must be untouched")
	}
	var tickets, orders int
	count := func() {
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM tickets WHERE session_id = $1`, soldSessions[0]).Scan(&tickets)
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE event_id = $1`, sold).Scan(&orders)
	}
	// "Archive instead": allowed, and the tickets and the order stay.
	owner.press(evsData(sold, "arc"), "в архив?")
	owner.press(evsData(sold, "arcy"), "в архиве")
	count()
	if seed.status(t, sold) != "archived" || tickets != 1 || orders != 1 {
		t.Errorf("archive of a sold event: status=%s tickets=%d orders=%d", seed.status(t, sold), tickets, orders)
	}
	// Archived and sold: still not deletable, and the screen no longer offers the archive.
	again := owner.press(evsData(sold, "del"), "удалить нельзя")
	if strings.Contains(again, "Можно отправить") {
		t.Errorf("an archived event must not be offered the archive:\n%s", again)
	}
	if seed.deleted(t, sold) {
		t.Fatal("an archived event with sales must not be deleted")
	}

	// ── a sale lands between the question and the word ────────────────────────
	race, _, raceSessions := seed.event("published", "Race", 1)
	owner.press(evsData(race, "del"), "Последний шаг")
	seed.sell(race, raceSessions[0])
	owner.say("УДАЛИТЬ", "удалить нельзя")
	if seed.deleted(t, race) || seed.dialogs(t, evsOwnerTG) != 0 {
		t.Fatalf("a sale in between: deleted=%v dialogs=%d", seed.deleted(t, race), seed.dialogs(t, evsOwnerTG))
	}

	// ── another organization gets nothing ─────────────────────────────────────
	victim, _, victimSessions := seed.event("published", "Victim", 1)
	seed.sell(victim, victimSessions[0])
	empty2, _, _ := seed.event("published", "Victim Free", 1)
	for _, act := range []string{"pub", "off", "offy", "arc", "arcy", "del"} {
		got := outsider.press(evsData(victim, act), "Не найдено")
		if strings.Contains(got, "EVS") {
			t.Errorf("%s leaked the other organization's event:\n%s", act, got)
		}
	}
	outsider.press(evsData(empty2, "del"), "Не найдено")
	outsider.say("УДАЛИТЬ", "Я не понял")
	if seed.status(t, victim) != "published" || seed.deleted(t, victim) || seed.deleted(t, empty2) || seed.dialogs(t, evsOutsiderTG) != 0 {
		t.Fatal("a user of another organization must not touch the event")
	}

	// ── the question survives a bot restart; an expired one is reported once ──
	restart, _, _ := seed.event("published", "Restart", 1)
	owner.press(evsData(restart, "del"), "Последний шаг")
	stop()
	time.Sleep(400 * time.Millisecond) // the stub's long poll
	startDialogTestBot(t, pool, api, tg)
	owner.say("УДАЛИТЬ", "удалён")
	if !seed.deleted(t, restart) {
		t.Fatal("the delete question must survive a bot restart")
	}

	lapse, _, _ := seed.event("published", "Lapse", 1)
	owner.press(evsData(lapse, "del"), "Последний шаг")
	if _, err := pool.Exec(context.Background(),
		`UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'evstatus'`, evsOwnerTG); err != nil {
		t.Fatal(err)
	}
	owner.say("УДАЛИТЬ", "Этот диалог устарел")
	if seed.deleted(t, lapse) {
		t.Fatal("an expired question must delete nothing")
	}
	m := tg.mark()
	tg.push(e2eMessageAs(evsOwnerTG, "УДАЛИТЬ"))
	if got := tg.waitSince(t, m, "Я не понял"); strings.Contains(got, "устарел") {
		t.Errorf("an expired dialog is reported once:\n%s", got)
	}
	if seed.deleted(t, lapse) {
		t.Fatal("the word after the lapse must delete nothing")
	}
}
