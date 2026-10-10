//go:build integration

package httpserver

// End-to-end proof of the promoter screens (spec 35 EC-14) through the real
// router, the real bot and the stub Telegram: the owner opens the list and a
// card from the menu and from the event card, changes every field one question
// at a time (a taken name, a malformed phone, e-mail and website, a malformed
// and a taken page address are each refused in plain words), clears optional
// fields, confirms a page address change, restarts the bot in the middle of a
// question, archives with one confirming press and sees the event keep its
// promoter name; the manager does the same on a promoter of his own; a user of
// ANOTHER organization sees an empty list and cannot touch a foreign promoter
// by its id. The stub records message TEXT only, so every effect is read from
// the database.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	prOwnerTG    = int64(91401)
	prManagerTG  = int64(91402)
	prOutsiderTG = int64(91403)
)

type prRow struct {
	Name, Address, LegalID, Phone, Email, Website, Slug *string
	Archived                                            bool
}

func (r prRow) str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// sig is the row as one comparable list of strings.
func (r prRow) sig() []string {
	return []string{r.str(r.Name), r.str(r.Address), r.str(r.LegalID), r.str(r.Phone), r.str(r.Email), r.str(r.Website), r.str(r.Slug), fmt.Sprint(r.Archived)}
}

func readPromoter(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) prRow {
	t.Helper()
	var r prRow
	err := pool.QueryRow(context.Background(), `
		SELECT name, address, legal_id, phone, email, website, slug, archived_at IS NOT NULL
		  FROM org_promoters WHERE id = $1`, id).
		Scan(&r.Name, &r.Address, &r.LegalID, &r.Phone, &r.Email, &r.Website, &r.Slug, &r.Archived)
	if err != nil {
		t.Fatalf("read promoter %s: %v", id, err)
	}
	return r
}

func TestBotE2E_PromoterEdit(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, prOwnerTG, "owner")
	linkECBotUser(t, f, srv, prManagerTG, "manager")
	linkECBotUser(t, other, srv, prOutsiderTG, "owner")
	ctx := context.Background()
	suffix := uuid.NewString()[:6]

	// The truth about the roles: the owner and the manager hold promoter.read
	// and promoter.manage (migration 0115), whatever the spec table says.
	for _, role := range []string{"org_admin", "organizer"} {
		for _, perm := range []string{"promoter.read", "promoter.manage"} {
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

	// ── fixtures: three promoters (one without a page), a foreign one, and a
	// published event linked to the first.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
	}
	idA, idB, idC, idForeign, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nameA, nameB, nameC := "Alpha Agency "+suffix, "Beta Agency "+suffix, "Gamma Agency "+suffix
	slugA, slugB := "ec14-a-"+suffix, "ec14-b-"+suffix
	exec(`INSERT INTO org_promoters (id, org_id, name, slug, phone) VALUES ($1, $2, $3, $4, '+420 111 222 333')`, idA, f.orgID, nameA, slugA)
	exec(`INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`, idB, f.orgID, nameB, slugB)
	exec(`INSERT INTO org_promoters (id, org_id, name) VALUES ($1, $2, $3)`, idC, f.orgID, nameC)
	exec(`INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`, idForeign, other.orgID, "Foreign Agency "+suffix, "ec14-f-"+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, 'published', 'public', $4)`,
		eventID, f.orgID, "EC14 Show "+suffix, "ec14-show-"+suffix)
	exec(`INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, eventID, f.orgID, idA)
	t.Cleanup(func() {
		c := context.Background()
		for _, org := range []uuid.UUID{f.orgID, other.orgID} {
			for _, sql := range []string{
				`DELETE FROM bot_dialogs WHERE org_id = $1`,
				`DELETE FROM event_promoters WHERE org_id = $1`,
				`DELETE FROM events WHERE org_id = $1`,
				`DELETE FROM org_promoters WHERE org_id = $1`,
				`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
			} {
				if _, err := pool.Exec(c, sql, org); err != nil {
					t.Logf("promoter bot cleanup: %s: %v", sql, err)
				}
			}
		}
	})

	tg := newStubTelegram(t)
	stop := startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, prOwnerTG}
	manager := ecDriver{t, tg, prManagerTG}
	outsider := ecDriver{t, tg, prOutsiderTG}
	data := func(kind string, id uuid.UUID) string { return "pr:" + kind + ":" + id.String() }
	ask := func(field string, id uuid.UUID) string { return "pr:e:" + field + ":" + id.String() }
	dialogStep := func(tgID int64) string {
		var step string
		_ = pool.QueryRow(ctx, `SELECT step FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'promoter'`, tgID).Scan(&step)
		return step
	}

	// ── the list, from the menu: alphabetical, three rows ─────────────────────
	text := owner.press("pr:l", "Промоутеры")
	for _, n := range []string{"всего 3", "стр. 1/1"} {
		if !strings.Contains(text, n) {
			t.Errorf("list lacks %q:\n%s", n, text)
		}
	}
	owner.press("pr:o:0", nameA) // row 0 is the first by name
	if dialogStep(prOwnerTG) != "card" {
		t.Errorf("an open card is stored as step card, got %q", dialogStep(prOwnerTG))
	}

	// ── the name: a taken one is refused by the API, then a new one is saved ──
	owner.press(ask("n", idA), "Новое название")
	owner.say(strings.ToUpper(nameB), "уже есть такое название")
	if r := readPromoter(t, pool, idA); *r.Name != nameA {
		t.Fatalf("a refused name must change nothing: %s", *r.Name)
	}
	owner.say(strings.Repeat("я", 121), "слишком длинное")
	renamed := "Alpha Renamed " + suffix
	owner.say(renamed, "Сохранено")
	if r := readPromoter(t, pool, idA); *r.Name != renamed {
		t.Fatalf("name = %s, want %s", *r.Name, renamed)
	}
	if dialogStep(prOwnerTG) != "card" {
		t.Errorf("after a save the dialog is back on the card, got %q", dialogStep(prOwnerTG))
	}

	// ── address, tax id ───────────────────────────────────────────────────────
	owner.press(ask("a", idA), "Адрес")
	owner.say(strings.Repeat("x", 301), "от 1 до 300")
	owner.say("Dlouhá 12,\n110 00 Praha", "Сохранено")
	if r := readPromoter(t, pool, idA); r.str(r.Address) != "Dlouhá 12, 110 00 Praha" {
		t.Errorf("address = %s", r.str(r.Address))
	}
	owner.press(ask("t", idA), "Налоговый номер")
	owner.say("CZ 12345678", "Сохранено")
	if r := readPromoter(t, pool, idA); r.str(r.LegalID) != "CZ 12345678" {
		t.Errorf("legal_id = %s", r.str(r.LegalID))
	}

	// ── phone: malformed refused, good one saved, then cleared ────────────────
	owner.press(ask("p", idA), "Телефон")
	owner.say("позвоните мне", "не похоже на телефон")
	owner.say("+420 777 888 999", "Сохранено")
	if r := readPromoter(t, pool, idA); r.str(r.Phone) != "+420 777 888 999" {
		t.Errorf("phone = %s", r.str(r.Phone))
	}
	owner.press(ask("p", idA), "Сейчас: +420 777 888 999")
	owner.press("pr:x:p:"+idA.String(), "Очищено")
	if r := readPromoter(t, pool, idA); r.Phone != nil {
		t.Errorf("a cleared phone must be NULL, got %s", r.str(r.Phone))
	}

	// ── e-mail and website ────────────────────────────────────────────────────
	owner.press(ask("m", idA), "E-mail")
	owner.say("not-an-email", "не похоже на адрес e-mail")
	owner.say("Office@Partner.Example", "Сохранено")
	if r := readPromoter(t, pool, idA); r.str(r.Email) != "office@partner.example" {
		t.Errorf("email = %s", r.str(r.Email))
	}
	owner.press(ask("w", idA), "Сайт")
	owner.say("two words.example", "без пробелов")
	owner.say("partner.example", "Сохранено")
	if r := readPromoter(t, pool, idA); r.str(r.Website) != "https://partner.example" {
		t.Errorf("website = %s (the API adds https://)", r.str(r.Website))
	}

	// ── the page address: malformed and taken are refused by the API, a change
	// of an existing address waits for its press, a first address is saved at once.
	owner.press(ask("s", idA), "Адрес страницы")
	owner.say("Bad_Slug!", "от 2 до 64 символов") // reaches the API, 400 promoter.invalid_slug
	if r := readPromoter(t, pool, idA); r.str(r.Slug) != slugA {
		t.Fatalf("a malformed slug must change nothing: %s", r.str(r.Slug))
	}
	text = owner.say(slugB, "изменится") // the confirmation names both addresses
	if !strings.Contains(text, slugA) || !strings.Contains(text, slugB) {
		t.Errorf("the confirmation must name the old and the new address:\n%s", text)
	}
	if dialogStep(prOwnerTG) != "slug" {
		t.Errorf("a changed address waits on step slug, got %q", dialogStep(prOwnerTG))
	}
	owner.press(data("ok", idA), "уже занят") // 409 promoter.duplicate_slug from another promoter
	if r := readPromoter(t, pool, idA); r.str(r.Slug) != slugA {
		t.Fatalf("a taken slug must change nothing: %s", r.str(r.Slug))
	}
	var orgSlug string
	if err := pool.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, f.orgID).Scan(&orgSlug); err != nil {
		t.Fatal(err)
	}
	owner.say(orgSlug, "изменится")
	owner.press(data("ok", idA), "уже занят") // the namespace is shared with organizations
	newSlugA := "ec14-new-" + suffix
	owner.say("https://tickets.example/"+strings.ToUpper(newSlugA)+"/", "изменится") // a pasted link is cut to its last segment
	owner.press(data("ok", idA), "Публичная страница теперь открывается")
	if r := readPromoter(t, pool, idA); r.str(r.Slug) != newSlugA {
		t.Errorf("slug = %s, want %s", r.str(r.Slug), newSlugA)
	}
	// A press of "Save" with no question open does nothing.
	owner.press(data("ok", idA), renamed)
	// The promoter without a page gets one at once.
	owner.press(ask("s", idC), "Адрес страницы")
	firstSlug := "ec14-c-" + suffix
	owner.say(firstSlug, "Публичная страница теперь открывается")
	if r := readPromoter(t, pool, idC); r.str(r.Slug) != firstSlug {
		t.Errorf("first slug = %s", r.str(r.Slug))
	}

	// ── a restart in the middle of a question loses nothing ───────────────────
	owner.press(ask("t", idB), "Налоговый номер")
	stop()
	time.Sleep(400 * time.Millisecond) // let the stub's long poll end, so no update goes to the dead poller
	startPromoTestBot(t, pool, api, tg)
	owner.say("CZ 87654321", "Сохранено")
	if r := readPromoter(t, pool, idB); r.str(r.LegalID) != "CZ 87654321" {
		t.Errorf("after the restart legal_id = %s", r.str(r.LegalID))
	}

	// ── the event card carries the promoter line and opens the promoter ───────
	text = owner.press("ec:o:"+eventID.String(), "Промоутер:")
	if !strings.Contains(text, renamed) {
		t.Errorf("the event card must name the promoter:\n%s", text)
	}
	owner.press("pr:ev:"+eventID.String(), renamed)

	// ── archive: one question, one confirming press, the event keeps the name ─
	text = owner.press(data("ar", idA), "в архив")
	if !strings.Contains(text, "Ничего не удаляется") {
		t.Errorf("the archive question must say nothing is deleted:\n%s", text)
	}
	if r := readPromoter(t, pool, idA); r.Archived {
		t.Fatal("asking must not archive")
	}
	owner.press(data("ay", idA), "теперь в архиве")
	if r := readPromoter(t, pool, idA); !r.Archived {
		t.Fatal("the confirming press must archive")
	}
	var linked uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT promoter_id FROM event_promoters WHERE event_id = $1`, eventID).Scan(&linked); err != nil || linked != idA {
		t.Fatalf("the event keeps its promoter: %v %v", linked, err)
	}
	text = owner.press("ec:o:"+eventID.String(), "Промоутер:")
	if !strings.Contains(text, renamed) {
		t.Errorf("an archived promoter still shows on its event:\n%s", text)
	}
	// A stale confirm for an archive nobody asked about does nothing.
	owner.press(data("ay", idB), nameB)
	if r := readPromoter(t, pool, idB); r.Archived {
		t.Fatal("a confirm without its question must not archive")
	}

	// ── the manager works on the same organization's promoters ────────────────
	manager.press("pr:l", "Промоутеры")
	manager.press(ask("p", idB), "Телефон")
	manager.say("+34 600 111 222", "Сохранено")
	if r := readPromoter(t, pool, idB); r.str(r.Phone) != "+34 600 111 222" {
		t.Errorf("manager phone = %s", r.str(r.Phone))
	}

	// ── another organization: its own (empty) list, and a foreign id is "gone" ─
	text = outsider.press("pr:l", "Промоутеры")
	if !strings.Contains(text, "всего 1") || strings.Contains(text, nameB) {
		t.Errorf("the outsider must see only his own list:\n%s", text)
	}
	before := readPromoter(t, pool, idB)
	outsider.press(data("v", idB), "больше нет в списке")
	outsider.press(ask("n", idB), "больше нет в списке")
	outsider.press(data("ar", idB), "больше нет в списке")
	outsider.press(data("ay", idB), "больше нет в списке")
	if after := readPromoter(t, pool, idB); fmt.Sprint(after.sig()) != fmt.Sprint(before.sig()) {
		t.Errorf("a foreign press changed the promoter: %v -> %v", before.sig(), after.sig())
	}
	outsider.press("pr:ev:"+eventID.String(), "Не найдено") // a foreign event is "not found"

	// ── an answer after the dialog ran out is reported once ───────────────────
	owner.press(ask("n", idC), "Новое название")
	exec(`UPDATE bot_dialogs SET expires_at = now() - interval '1 hour' WHERE telegram_user_id = $1 AND kind = 'promoter'`, prOwnerTG)
	owner.say("Too Late "+suffix, "устарел")
	if r := readPromoter(t, pool, idC); *r.Name != nameC {
		t.Errorf("an expired dialog must not rename: %s", *r.Name)
	}

	// ── the audit row names the Telegram bot as the client ────────────────────
	var via *string
	if err := pool.QueryRow(ctx, `SELECT metadata->>'via' FROM audit_events WHERE action = 'v1.promoter.update' AND resource_id = $1 ORDER BY occurred_at DESC LIMIT 1`, idA.String()).Scan(&via); err != nil || via == nil || *via != "telegram_bot" {
		t.Errorf("audit via = %v (%v), want telegram_bot", via, err)
	}
}
