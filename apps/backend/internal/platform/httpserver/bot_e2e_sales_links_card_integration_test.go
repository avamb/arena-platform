//go:build integration

package httpserver

// End-to-end proof of the link card's texts after the owner's first live run
// (spec 43, QL-8b). The card always says where the code leads RIGHT NOW; when
// that is not the target it names the reason the server gives
// (destination_reason) and advises "Redirect" only where another target would
// change the address: an event taken off sale is mended by publishing it, a
// cancelled date by another date, a missing page address by nobody but the
// platform. A retarget to the very same target changes nothing and says so
// without a call; "a new page" is said only when the page really changed.
//
// The stub records message TEXT, so every effect is checked in the database.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	qlCardOwnerTG  = int64(7861)
	qlNoBaseTG     = int64(7862)
	qlCardFallback = "https://home.ql8.test"
)

func qlRetargetAudits(t *testing.T, pool *pgxpool.Pool, linkID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'v1.sales_link.retarget' AND resource_id = $1`, linkID.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func qlSlugs(t *testing.T, pool *pgxpool.Pool, orgID, eventID uuid.UUID) (orgSlug, eventSlug string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT slug FROM organizations WHERE id = $1`, orgID).Scan(&orgSlug); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT slug FROM events WHERE id = $1`, eventID).Scan(&eventSlug); err != nil {
		t.Fatal(err)
	}
	return orgSlug, eventSlug
}

// mustHave fails when a screen lacks a wanted phrase or carries a forbidden one.
func qlScreen(t *testing.T, label, text string, want, forbid []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("%s: lacks %q:\n%s", label, w, text)
		}
	}
	for _, f := range forbid {
		if strings.Contains(text, f) {
			t.Errorf("%s: must not say %q:\n%s", label, f, text)
		}
	}
}

func TestBotE2E_SalesLinkCardTexts(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.ShortLinkBaseURL = qlShortBase
		c.PublicTicketsBaseURL = qlTicketsBase
		c.ShortLinkFallbackURL = qlCardFallback
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedPromoOrg(t, pool, f.orgID)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = $1)`, f.orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM sales_links WHERE org_id = $1`, f.orgID)
	})
	linkECBotUser(t, f, srv, qlCardOwnerTG, "owner")
	orgSlug, slugA := qlSlugs(t, pool, f.orgID, seed.eventA)
	orgPage := qlTicketsBase + "/" + orgSlug
	eventPage := orgPage + "/" + slugA // the event has no promoter: the organization's slug leads
	leadsTo := func(url string) string { return "Сейчас ведёт на: " + url }
	const retargetAdvice = "нажмите «Перенацелить»"

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, qlCardOwnerTG}

	// A link to a published event: the card says where the code leads, and
	// has no reason to explain anything.
	owner.press("ql:e:"+seed.eventA.String(), "Где вы разместите код")
	owner.press("ql:pk:poster", "Напечатайте короткое название места")
	card := owner.say("Афиша в фойе", "Ссылка создана")
	link := qlOne(t, pool, f.orgID, "Афиша в фойе")
	qlScreen(t, "card of a live event", card,
		[]string{leadsTo(eventPage)},
		[]string{"не опубликовано", "запасной адрес", retargetAdvice, "выберите другую цель"})

	// ── (1) the event is taken off sale: the reason, no retarget advice ───────
	if _, err := pool.Exec(context.Background(), `UPDATE events SET status = 'draft' WHERE id = $1`, seed.eventA); err != nil {
		t.Fatal(err)
	}
	card = owner.press("ql:v:"+link.ID.String(), "не опубликовано")
	qlScreen(t, "card of an event off sale", card,
		[]string{leadsTo(orgPage), "Мероприятие не опубликовано", "Опубликуйте мероприятие", "Перенацеливать не нужно"},
		[]string{retargetAdvice, "выберите другую цель", "Адрес страницы пока неизвестен", "запасной адрес"})

	// ── (2) the same target again: nothing changed, no call, no audit row ─────
	owner.press("ql:rt:"+link.ID.String(), "Куда должна вести ссылка")
	text := owner.press("ql:t:ev", "Цель не изменилась")
	qlScreen(t, "retarget to the same event", text,
		[]string{"ведёт туда же", leadsTo(orgPage)},
		[]string{"теперь ведёт на новую страницу", "Напечатанный код не меняется"})
	if n := qlRetargetAudits(t, pool, link.ID); n != 0 {
		t.Errorf("a retarget to the same target wrote %d audit rows, want 0", n)
	}
	if got := qlOne(t, pool, f.orgID, "Афиша в фойе"); got.Kind != "event" {
		t.Errorf("the link moved: %+v", got)
	}
	var dialogs int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'sales_link'`, qlCardOwnerTG).Scan(&dialogs)
	if dialogs != 0 {
		t.Errorf("sales_link dialogs left after the same-target answer = %d, want 0", dialogs)
	}

	// ── back on sale, moved to a date: the page really changed ────────────────
	if _, err := pool.Exec(context.Background(), `UPDATE events SET status = 'published' WHERE id = $1`, seed.eventA); err != nil {
		t.Fatal(err)
	}
	owner.press("ql:rt:"+link.ID.String(), "Куда должна вести ссылка")
	owner.press("ql:t:ss", "Выберите сеанс мероприятия")
	owner.press("ql:so:0", "Напечатанный код не меняется")
	text = owner.press("ql:rt:ok:"+link.ID.String(), "теперь ведёт на новую страницу")
	qlScreen(t, "retarget to a date", text, []string{leadsTo(eventPage + "?session=" + seed.sessA[0].String())}, []string{"не опубликовано"})
	if n := qlRetargetAudits(t, pool, link.ID); n != 1 {
		t.Errorf("retarget audit rows = %d, want 1", n)
	}

	// ── (1b) the date is cancelled: here another date WOULD change the address ─
	if _, err := pool.Exec(context.Background(), `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, seed.sessA[0]); err != nil {
		t.Fatal(err)
	}
	card = owner.press("ql:v:"+link.ID.String(), "Сеанс отменён")
	qlScreen(t, "card of a cancelled date", card,
		[]string{leadsTo(eventPage), "ведёт на страницу мероприятия", retargetAdvice},
		[]string{"не опубликовано", "выберите другую цель"})

	// ── (2b) a changed target that lands on the same page says so ─────────────
	owner.press("ql:rt:"+link.ID.String(), "Куда должна вести ссылка")
	owner.press("ql:t:ev", "Напечатанный код не меняется")
	text = owner.press("ql:rt:ok:"+link.ID.String(), "остался прежним")
	qlScreen(t, "retarget from the cancelled date to its event", text,
		[]string{"Цель изменена", leadsTo(eventPage)},
		[]string{"теперь ведёт на новую страницу", "Сеанс отменён"})
	if got := qlOne(t, pool, f.orgID, "Афиша в фойе"); got.Kind != "event" || got.SessionID != nil {
		t.Errorf("after the retarget to the event: %+v", got)
	}
	if n := qlRetargetAudits(t, pool, link.ID); n != 2 {
		t.Errorf("retarget audit rows = %d, want 2", n)
	}
}

// Without PUBLIC_TICKETS_BASE_URL no page address exists at all: the card says
// the code leads to the platform's fallback page and names the cause, and
// never advises a retarget that would change nothing. The redirect itself
// proves the text: the scan goes to the fallback page.
func TestBotE2E_SalesLinkCardNoPageAddress(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.ShortLinkBaseURL = qlShortBase
		c.PublicTicketsBaseURL = ""
		c.ShortLinkFallbackURL = qlCardFallback
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedPromoOrg(t, pool, f.orgID)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = $1)`, f.orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM sales_links WHERE org_id = $1`, f.orgID)
	})
	linkECBotUser(t, f, srv, qlNoBaseTG, "owner")

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, qlNoBaseTG}

	owner.press("ql:e:"+seed.eventA.String(), "Где вы разместите код")
	owner.press("ql:pk:sign", "Напечатайте короткое название места")
	card := owner.say("Вывеска без адреса", "Ссылка создана")
	link := qlOne(t, pool, f.orgID, "Вывеска без адреса")
	qlScreen(t, "card without a page address", card,
		[]string{"Сейчас ведёт на запасной адрес платформы", "не настроен адрес страниц продаж", "Напишите в поддержку"},
		[]string{"Сейчас ведёт на: ", "нажмите «Перенацелить»", "выберите другую цель", "Адрес страницы пока неизвестен", "не опубликовано"})

	req := httptest.NewRequest(http.MethodGet, "/go/"+link.Code, nil)
	req.Header.Set("User-Agent", goBrowserUA)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != qlCardFallback {
		t.Errorf("GET /go/%s = %d %q, want 302 to the fallback page %s", link.Code, rec.Code, rec.Header().Get("Location"), qlCardFallback)
	}
}
