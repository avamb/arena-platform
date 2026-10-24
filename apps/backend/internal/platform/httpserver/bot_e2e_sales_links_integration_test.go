//go:build integration

package httpserver

// End-to-end proof of the bot's "Links and QR" dialog (spec 43, QL-8) through
// the real router, the real bot and the stub Telegram: from an event card the
// owner names the place ("Афиша у входа"), the link lands in sales_links with
// its event, and the chat gets the PNG as a photo, the SVG as a document and
// the card with the short address. One real GET on the short link is counted,
// and "Статистика" shows it. A retarget to a date asks one press and leaves
// an audit row that names the bot. The menu path with the organization's own
// address, the archive and the restore, a manager's own screens, a link of
// ANOTHER organization (reads as gone) and a dialog that ran out are checked
// too.
//
// The stub records message TEXT, so every effect is checked in the database.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	qlOwnerTG    = int64(7851)
	qlManagerTG  = int64(7852)
	qlOutsiderTG = int64(7853)

	qlShortBase   = "https://go.ql8.test"
	qlTicketsBase = "https://tickets.ql8.test"
)

type qlLinkRow struct {
	ID        uuid.UUID
	Code      string
	Kind      string
	EventID   *uuid.UUID
	SessionID *uuid.UUID
	URL       *string
	Label     string
	Placement string
	Status    string
	Scans     int64
}

func qlLinks(t *testing.T, pool *pgxpool.Pool, org uuid.UUID) []qlLinkRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, code, target_kind, event_id, session_id, url, label, placement_kind, status, scan_count
		  FROM sales_links WHERE org_id = $1 ORDER BY created_at`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []qlLinkRow
	for rows.Next() {
		var r qlLinkRow
		if err := rows.Scan(&r.ID, &r.Code, &r.Kind, &r.EventID, &r.SessionID, &r.URL, &r.Label, &r.Placement, &r.Status, &r.Scans); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func qlOne(t *testing.T, pool *pgxpool.Pool, org uuid.UUID, label string) qlLinkRow {
	t.Helper()
	for _, r := range qlLinks(t, pool, org) {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no sales link named %q", label)
	return qlLinkRow{}
}

// qlSentSince returns the sent lines after a mark that contain needle.
func qlSentSince(tg *stubTelegram, mark int, needle string) []string {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	var out []string
	for _, m := range tg.sent[mark:] {
		if strings.Contains(m, needle) {
			out = append(out, m)
		}
	}
	return out
}

func TestBotE2E_SalesLinks(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.ShortLinkBaseURL = qlShortBase
		c.PublicTicketsBaseURL = qlTicketsBase
		c.ShortLinkFallbackURL = "https://home.ql8.test"
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedPromoOrg(t, pool, f.orgID)
	foreign := seedPromoOrg(t, pool, other.orgID)
	_ = foreign
	t.Cleanup(func() {
		ctx := context.Background()
		for _, org := range []uuid.UUID{f.orgID, other.orgID} {
			_, _ = pool.Exec(ctx, `DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = $1)`, org)
			_, _ = pool.Exec(ctx, `DELETE FROM sales_links WHERE org_id = $1`, org)
		}
	})
	linkECBotUser(t, f, srv, qlOwnerTG, "owner")
	linkECBotUser(t, f, srv, qlManagerTG, "manager")
	linkECBotUser(t, other, srv, qlOutsiderTG, "owner")

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, qlOwnerTG}
	manager := ecDriver{t, tg, qlManagerTG}
	outsider := ecDriver{t, tg, qlOutsiderTG}

	// ── from the event card: the poster at the entrance ───────────────────────
	owner.press("ql:e:"+seed.eventA.String(), "Где вы разместите код")
	owner.press("ql:pk:poster", "Напечатайте короткое название места")
	owner.say(strings.Repeat("я", 121), "слишком длинное")
	mark := tg.mark()
	card := owner.say("Афиша у входа", "Ссылка создана")

	link := qlOne(t, pool, f.orgID, "Афиша у входа")
	if link.Kind != "event" || link.EventID == nil || *link.EventID != seed.eventA || link.SessionID != nil {
		t.Errorf("the link target = %s event=%v session=%v, want event A", link.Kind, link.EventID, link.SessionID)
	}
	if link.Placement != "poster" || link.Status != "active" {
		t.Errorf("placement/status = %s/%s", link.Placement, link.Status)
	}
	short := qlShortBase + "/" + link.Code
	if !strings.Contains(card, short) {
		t.Errorf("the card lacks the short address %s:\n%s", short, card)
	}
	if !strings.Contains(card, qlTicketsBase+"/") || strings.Contains(card, "выше, чем его цель") {
		t.Errorf("the card must show where the code leads, not degraded:\n%s", card)
	}
	if got := qlSentSince(tg, mark, "[photo "+link.Code+".png]"); len(got) != 1 {
		t.Errorf("the PNG did not arrive as a photo once: %v", got)
	}
	if got := qlSentSince(tg, mark, "[document "+link.Code+".svg]"); len(got) != 1 {
		t.Errorf("the SVG did not arrive as a document once: %v", got)
	}
	if png, ok := tg.document(link.Code + ".png"); !ok || len(png) < 8 || string(png[1:4]) != "PNG" {
		t.Errorf("the photo is not a PNG (%d bytes, stored=%v)", len(png), ok)
	}
	if svg, ok := tg.document(link.Code + ".svg"); !ok || !strings.Contains(string(svg), "<svg") {
		t.Errorf("the document is not an SVG (stored=%v)", ok)
	}
	var via string
	if err := pool.QueryRow(context.Background(),
		`SELECT metadata->>'via' FROM audit_events WHERE action = 'v1.sales_link.create' AND resource_id = $1`, link.ID.String()).Scan(&via); err != nil || via != "telegram_bot" {
		t.Errorf("create audit via = %q (%v), want telegram_bot", via, err)
	}
	var dialogs int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'sales_link'`, qlOwnerTG).Scan(&dialogs)
	if dialogs != 1 {
		t.Errorf("sales_link dialogs stored = %d, want 1", dialogs)
	}

	// ── one real scan, then the statistics ────────────────────────────────────
	req := httptest.NewRequest(http.MethodGet, "/go/"+link.Code, nil)
	req.Header.Set("User-Agent", goBrowserUA)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), qlTicketsBase+"/") {
		t.Fatalf("GET /go/%s = %d %q", link.Code, rec.Code, rec.Header().Get("Location"))
	}
	// The redirect counts the scan after the answer, in the background.
	deadline := time.Now().Add(10 * time.Second)
	for qlOne(t, pool, f.orgID, "Афиша у входа").Scans < 1 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := qlOne(t, pool, f.orgID, "Афиша у входа").Scans; got != 1 {
		t.Fatalf("scan_count = %d, want 1", got)
	}
	stats := owner.press("ql:st:"+link.ID.String(), "Сканов:")
	for _, want := range []string{"Сканов: 1.", "За 7 дней: 1", "Сегодня: 1", "Оплаченных заказов пока нет", "UTC"} {
		if !strings.Contains(stats, want) {
			t.Errorf("statistics lack %q:\n%s", want, stats)
		}
	}

	// ── send the code again ───────────────────────────────────────────────────
	mark = tg.mark()
	owner.press("ql:qr:"+link.ID.String(), "Короткий адрес")
	if got := qlSentSince(tg, mark, "[photo "+link.Code+".png]"); len(got) != 1 {
		t.Errorf("the code did not arrive again as a photo: %v", got)
	}

	// ── retarget to the first date: one press, an audit row, no typed word ────
	owner.press("ql:rt:"+link.ID.String(), "Куда должна вести ссылка")
	owner.press("ql:t:ss", "Выберите сеанс мероприятия")
	owner.press("ql:so:0", "Напечатанный код не меняется")
	if got := qlOne(t, pool, f.orgID, "Афиша у входа"); got.Kind != "event" {
		t.Errorf("nothing may move before the confirming press, kind = %s", got.Kind)
	}
	// An old confirmation of ANOTHER link moves nothing.
	owner.press("ql:rt:ok:"+uuid.NewString(), "больше нет в списке")
	owner.press("ql:rt:ok:"+link.ID.String(), "теперь ведёт на новую страницу")
	moved := qlOne(t, pool, f.orgID, "Афиша у входа")
	if moved.Kind != "session" || moved.SessionID == nil || *moved.SessionID != seed.sessA[0] {
		t.Errorf("after the retarget kind=%s session=%v, want the first date %s", moved.Kind, moved.SessionID, seed.sessA[0])
	}
	var retargets int
	var retargetVia string
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(max(metadata->>'via'), '') FROM audit_events WHERE action = 'v1.sales_link.retarget' AND resource_id = $1`,
		link.ID.String()).Scan(&retargets, &retargetVia); err != nil || retargets != 1 || retargetVia != "telegram_bot" {
		t.Errorf("retarget audit rows = %d via %q (%v), want 1 by telegram_bot", retargets, retargetVia, err)
	}
	// The confirming button of the finished retarget moves nothing again.
	owner.press("ql:rt:ok:"+link.ID.String(), "Сканов: ")

	// ── archive and restore ───────────────────────────────────────────────────
	owner.press("ql:ar:"+link.ID.String(), "Ссылка в архиве")
	if got := qlOne(t, pool, f.orgID, "Афиша у входа").Status; got != "archived" {
		t.Errorf("status after the archive press = %s", got)
	}
	owner.press("ql:rt:"+link.ID.String(), "Сначала верните её из архива")
	owner.press("ql:ua:"+link.ID.String(), "снова работает")
	if got := qlOne(t, pool, f.orgID, "Афиша у входа").Status; got != "active" {
		t.Errorf("status after the restore press = %s", got)
	}

	// ── the menu: an address of one's own, typed ──────────────────────────────
	owner.press("ql:menu", "Фильтр: Активные")
	owner.press("ql:new", "Куда должна вести ссылка")
	owner.press("ql:t:url", "Напечатайте адрес страницы")
	owner.say("example.com/shop", "должен начинаться с https://")
	owner.say("https://example.com/shop", "Где вы разместите код")
	owner.press("ql:pk:flyer", "Напечатайте короткое название места")
	owner.say("Флаер на фестивале", "Ссылка создана")
	flyer := qlOne(t, pool, f.orgID, "Флаер на фестивале")
	if flyer.Kind != "url" || flyer.URL == nil || *flyer.URL != "https://example.com/shop" || flyer.Placement != "flyer" {
		t.Errorf("flyer link = %+v", flyer)
	}

	// ── the menu: pick another event from the list, then the page of all ──────
	owner.press("ql:menu", "Фильтр: Активные")
	owner.press("ql:new", "Куда должна вести ссылка")
	text := owner.press("ql:t:oe", "Выберите мероприятие")
	if !strings.Contains(text, "Страница 1 из 1") {
		t.Errorf("events choice:\n%s", text)
	}
	owner.press("ql:eo:1", "Мероприятие:") // the second event on the list
	owner.press("ql:t:pg", "Где вы разместите код")
	owner.press("ql:pk:social", "Напечатайте короткое название места")
	owner.say("Пост в соцсетях", "Ссылка создана")
	post := qlOne(t, pool, f.orgID, "Пост в соцсетях")
	if post.Kind != "org_page" || post.EventID != nil {
		t.Errorf("all-events link = %+v, want org_page with no event", post)
	}

	// ── the manager sees the same screens ─────────────────────────────────────
	manager.press("ql:e:"+seed.eventB.String(), "Где вы разместите код")
	manager.press("ql:pk:sign", "Напечатайте короткое название места")
	manager.say("Вывеска у кассы", "Ссылка создана")
	if got := qlOne(t, pool, f.orgID, "Вывеска у кассы"); got.EventID == nil || *got.EventID != seed.eventB {
		t.Errorf("the manager's link = %+v", got)
	}

	// ── another organization: a foreign link reads as gone, an event too ──────
	outsider.press("ql:v:"+link.ID.String(), "больше нет в списке")
	outsider.press("ql:st:"+link.ID.String(), "больше нет в списке")
	outsider.press("ql:e:"+seed.eventA.String(), "Не найдено")
	if got := qlOne(t, pool, f.orgID, "Афиша у входа").Status; got != "active" {
		t.Errorf("a stranger changed the link: status = %s", got)
	}

	// ── a dialog that ran out is reported, not guessed at ─────────────────────
	owner.press("ql:e:"+seed.eventA.String(), "Где вы разместите код")
	if _, err := pool.Exec(context.Background(), `UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'sales_link'`, qlOwnerTG); err != nil {
		t.Fatal(err)
	}
	owner.press("ql:pk:poster", "Этот диалог устарел")
	if n := len(qlLinks(t, pool, f.orgID)); n != 4 {
		t.Errorf("links after the expired press = %d, want 4", n)
	}
}
