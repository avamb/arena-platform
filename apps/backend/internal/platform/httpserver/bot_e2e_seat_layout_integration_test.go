//go:build integration

package httpserver

// End-to-end proof of the bot's "Seat layout" screens (task S3c of spec 40)
// through the real router, the real bot, the real allocator and the stub
// Telegram: the owner opens the date's card of a seated session, chooses the
// way of cutting the hall, says where the stage is, looks at the proposal
// (which writes NOTHING), applies exactly that proposal (a hall that changed
// while the owner looked shows the new picture and applies nothing), and takes
// the change back. Sold and held seats keep their category throughout. The
// manager does the same; a user of ANOTHER organization finds nothing; the
// "Seat map" link button appears only when the setting asks for it. The stub
// records message text and callback data, so effects are read from the
// database.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

const (
	slOwnerTG    = int64(91601)
	slManagerTG  = int64(91602)
	slOutsiderTG = int64(91603)
)

// startSeatLayoutBot is startPromoTestBot with room for the bot's options.
func startSeatLayoutBot(t *testing.T, pool *pgxpool.Pool, api *httptest.Server, tg *stubTelegram, mutate func(*eventbot.Options)) (stop func()) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg.restartGap()
	opts := eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		MessageRateLimit:  -1,
	}
	if mutate != nil {
		mutate(&opts)
	}
	bot, err := eventbot.New(opts)
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("bot.Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("bot did not stop after cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

type slSeat struct {
	Key  string
	Y    float64
	Tier uuid.UUID
	Stat string
}

// slHall reads the seats of the session as the database holds them now.
func slHall(t *testing.T, pool *pgxpool.Pool, session uuid.UUID, y map[string]float64) map[string]slSeat {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT seat_key, tier_id, status FROM session_seats WHERE session_id = $1 AND kind = 'seat'`, session)
	if err != nil {
		t.Fatalf("read seats: %v", err)
	}
	defer rows.Close()
	out := map[string]slSeat{}
	for rows.Next() {
		var s slSeat
		if err := rows.Scan(&s.Key, &s.Tier, &s.Stat); err != nil {
			t.Fatal(err)
		}
		s.Y = y[s.Key]
		out[s.Key] = s
	}
	return out
}

func TestBotE2E_SeatLayout(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, slOwnerTG, "owner")
	linkECBotUser(t, f, srv, slManagerTG, "manager")
	linkECBotUser(t, other, srv, slOutsiderTG, "owner")
	ctx := context.Background()
	suffix := uuid.NewString()[:6]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
	}

	// ── fixtures: Palác Akropolis bound to a seated session of three
	// categories, a few seats sold and held ─────────────────────────────────
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
	seats := layoutgen.Seats(geometry)
	y := map[string]float64{}
	for _, s := range seats {
		y[s.Key] = s.Y
	}

	venue, event, channel := uuid.New(), uuid.New(), uuid.New()
	plan, version, session := uuid.New(), uuid.New(), uuid.New()
	stalls, balcony, gallery := uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Add(96 * time.Hour).Truncate(time.Hour)
	t.Cleanup(func() {
		c := context.Background()
		for _, st := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'session_id' = '` + session.String() + `'`,
			`DELETE FROM session_seat_batches WHERE session_id = '` + session.String() + `'`,
			`DELETE FROM session_seats WHERE session_id = '` + session.String() + `'`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id = '` + session.String() + `'`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
			`DELETE FROM seating_plans WHERE owner_org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
		} {
			args := []any{f.orgID}
			if !strings.Contains(st, "$1") {
				args = nil
			}
			if _, err := pool.Exec(c, st, args...); err != nil {
				t.Logf("seat layout bot cleanup %q: %v", st, err)
			}
		}
	})
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venue, f.orgID, "Akropolis "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'public')`, event, f.orgID, "Layout Show "+suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channel, f.orgID, "Layout Channel "+suffix)
	exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
		plan, venue, f.orgID, "Akropolis "+suffix)
	exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, capacity_standing)
	      VALUES ($1, $2, 1, $3::jsonb, $4, $5, 0)`, version, plan, string(geoJSON), checksum, geometry.SeatCount())
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source, admission_mode, seating_plan_version_id)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'EUR', 'override', 'assigned_seats', $6)`,
		session, event, venue, start, geometry.SeatCount(), version)
	for i, tier := range []struct {
		id    uuid.UUID
		name  string
		price int
	}{{stalls, "Stalls", 9000}, {balcony, "Balcony", 5000}, {gallery, "Gallery", 2500}} {
		exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open, sort_order)
		      VALUES ($1, $2, $3, 'fixed', $4, 'EUR', $5, true, $5)`, tier.id, session, tier.name, tier.price, i+1)
	}
	// The hall starts as the plan's colours would: the first seats in the
	// balcony, the next in the gallery, the rest in the stalls.
	for i, s := range seats {
		tier := stalls
		switch {
		case i < 20:
			tier = balcony
		case i < 40:
			tier = gallery
		}
		sec := geometry.Sections[s.Section]
		exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, kind)
		      VALUES ($1, $2, $3, $4, $5, $6, 'seat')`, session, s.Key, sec.Name, sec.Rows[s.Row].Name, s.Number, tier)
	}
	heldRes := uuid.New()
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
	      VALUES ($1, $2, $3, $4, 3, 'active', now() + interval '1 hour')`, heldRes, f.orgID, channel, session)
	soldKeys, heldKeys := []string{seats[100].Key, seats[101].Key, seats[102].Key}, []string{seats[200].Key, seats[201].Key}
	for _, k := range soldKeys {
		exec(`UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2`, session, k)
	}
	for _, k := range heldKeys {
		exec(`UPDATE session_seats SET status = 'held', reservation_id = $3 WHERE session_id = $1 AND seat_key = $2`, session, k, heldRes)
	}
	before := slHall(t, pool, session, y)
	batches := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seat_batches WHERE session_id = $1`, session).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	sameHall := func(a, b map[string]slSeat) bool {
		for k, s := range a {
			if b[k].Tier != s.Tier || b[k].Stat != s.Stat {
				return false
			}
		}
		return len(a) == len(b)
	}

	tg := newStubTelegram(t)
	stop := startSeatLayoutBot(t, pool, api, tg, nil)
	owner := ecDriver{t, tg, slOwnerTG}
	manager := ecDriver{t, tg, slManagerTG}
	outsider := ecDriver{t, tg, slOutsiderTG}
	findData := func(d ecDriver, prefix string) string {
		t.Helper()
		_, _, data := tg.lastOf(d.id)
		for _, x := range data {
			if strings.HasPrefix(x, prefix) {
				return x
			}
		}
		t.Fatalf("no button %q on the screen, buttons: %v", prefix, data)
		return ""
	}

	// ── the date's card offers the layout, and no link button by default ─────
	owner.press("ses:list:"+event.String(), "Layout Show")
	owner.press("ses:o:0", "Layout Show")
	id, _, data := tg.lastOf(slOwnerTG)
	if !hasPrefixData(data, "ses:sl") {
		t.Fatalf("the seated date's card must offer the layout, buttons: %v", data)
	}
	if urls := tg.urlsOf(id); len(urls) != 0 {
		t.Errorf("no seat map link is shown while BOT_SCHEME_BUTTON is off: %v", urls)
	}

	// ── start screen: the three categories, one way of cutting ───────────────
	text := owner.press("ses:sl", "Раскладка мест")
	for _, n := range []string{"Stalls", "Balcony", "Gallery"} {
		if !strings.Contains(text, n) {
			t.Errorf("the start screen lacks %q:\n%s", n, text)
		}
	}
	findData(owner, "sl:a:desirability")

	// ── options: the stage is asked, never guessed ───────────────────────────
	owner.press("sl:a:desirability", "Где сцена")
	findData(owner, "sl:pm:0:2") // the algorithm's own choice: three ways of sharing the seats
	findData(owner, "sl:pm:1:1") // ...and its switch
	owner.press("sl:go", "Сначала выберите, где сцена")
	if batches() != 0 || !sameHall(before, slHall(t, pool, session, y)) {
		t.Fatal("asking for the proposal without a stage must write nothing")
	}
	owner.press("sl:sd:top", "Где сцена")
	owner.press("sl:pm:1:1", "Где сцена") // cheaper row edges on

	// ── the proposal writes nothing ──────────────────────────────────────────
	text = owner.press("sl:go", "Предлагаемая раскладка")
	for _, n := range []string{"Stalls", "Balcony", "Gallery", "продано 3", "в брони 2"} {
		if !strings.Contains(text, n) {
			t.Errorf("the preview lacks %q:\n%s", n, text)
		}
	}
	if batches() != 0 || !sameHall(before, slHall(t, pool, session, y)) {
		t.Fatal("the preview must write nothing")
	}
	oldApply := findData(owner, "sl:ap:")

	// ── a hall that changed meanwhile: a new picture, nothing applied ────────
	changedKey := seats[150].Key
	exec(`UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2`, session, changedKey)
	soldKeys = append(soldKeys, changedKey)
	mid := slHall(t, pool, session, y)
	owner.press(oldApply, "Пока вы смотрели")
	if batches() != 0 || !sameHall(mid, slHall(t, pool, session, y)) {
		t.Fatal("a stale preview must apply nothing")
	}
	newApply := findData(owner, "sl:ap:")
	if newApply == oldApply {
		t.Error("the new preview must carry a new fingerprint")
	}

	// ── apply exactly what is shown ──────────────────────────────────────────
	text = owner.press(newApply, "Готово")
	if batches() != 1 {
		t.Fatalf("one batch expected, got %d", batches())
	}
	var kind string
	if err := pool.QueryRow(ctx, `SELECT kind FROM session_seat_batches WHERE session_id = $1`, session).Scan(&kind); err != nil || kind != "apply_layout" {
		t.Fatalf("batch kind = %q (%v)", kind, err)
	}
	after := slHall(t, pool, session, y)
	frozen := append(append([]string{}, soldKeys...), heldKeys...)
	for _, k := range frozen {
		if after[k].Tier != mid[k].Tier || after[k].Stat != mid[k].Stat {
			t.Errorf("seat %s was sold or held and must not move: %+v -> %+v", k, mid[k], after[k])
		}
	}
	mean := func(tier uuid.UUID) float64 {
		var sum float64
		var n int
		for k, s := range after {
			if s.Tier == tier && s.Stat == "available" && !slContains(frozen, k) {
				sum += s.Y
				n++
			}
		}
		if n == 0 {
			t.Fatalf("category %s has no free seat after the layout", tier)
		}
		return sum / float64(n)
	}
	if !(mean(stalls) < mean(balcony) && mean(balcony) < mean(gallery)) {
		t.Errorf("the dearest category must sit nearest the stage (top): mean y %v / %v / %v", mean(stalls), mean(balcony), mean(gallery))
	}
	// A second press of the same button applies nothing.
	owner.press(newApply, "устарела")
	if batches() != 1 {
		t.Fatalf("a second press must not apply again, batches = %d", batches())
	}

	// ── undo: the seats go back; a second undo is told it is done ────────────
	undo := findData(owner, "sl:rv:")
	// the start screen after "Done" is reached by the stored press; the undo
	// button of the result names the batch.
	owner.press(undo, "Отменено")
	if got := slHall(t, pool, session, y); !sameHall(mid, got) {
		t.Error("undo must put every seat back")
	}
	owner.press(undo, "уже отменено")

	// ── the manager reaches the same screens; an outsider finds nothing ──────
	manager.press("ses:list:"+event.String(), "Layout Show")
	manager.press("ses:o:0", "Layout Show")
	manager.press("ses:sl", "Раскладка мест")
	outsider.press("sl:go", "Экран устарел")
	if batches() != 2 { // the apply and its undo
		t.Errorf("batches = %d, want 2", batches())
	}

	// ── the setting turns on the link button ─────────────────────────────────
	stop()
	tg2 := newStubTelegram(t)
	stop2 := startSeatLayoutBot(t, pool, api, tg2, func(o *eventbot.Options) {
		o.SchemeButton, o.EventCenterURL = "url", "https://events.example.test"
	})
	owner2 := ecDriver{t, tg2, slOwnerTG}
	owner2.press("ses:list:"+event.String(), "Layout Show")
	owner2.press("ses:o:0", "Layout Show")
	id2, _, _ := tg2.lastOf(slOwnerTG)
	want := fmt.Sprintf("https://events.example.test/o/%s/events/%s/sessions/%s/seats?embed=1", f.orgID, event, session)
	if urls := tg2.urlsOf(id2); len(urls) != 1 || urls[0] != want {
		t.Errorf("seat map link = %v, want %s", urls, want)
	}

	// ── web_app: the Mini App opens /tg, which signs in and goes on to the screen ─
	stop2()
	tg3 := newStubTelegram(t)
	startSeatLayoutBot(t, pool, api, tg3, func(o *eventbot.Options) {
		o.SchemeButton, o.EventCenterURL = "web_app", "https://events.example.test"
	})
	owner3 := ecDriver{t, tg3, slOwnerTG}
	owner3.press("ses:list:"+event.String(), "Layout Show")
	owner3.press("ses:o:0", "Layout Show")
	id3, _, _ := tg3.lastOf(slOwnerTG)
	path := fmt.Sprintf("/o/%s/events/%s/sessions/%s/seats?embed=1", f.orgID, event, session)
	wantApp := "https://events.example.test/tg?next=" + url.QueryEscape(path)
	if apps := tg3.webAppsOf(id3); len(apps) != 1 || apps[0] != wantApp {
		t.Errorf("web_app link = %v, want %s", apps, wantApp)
	}
	if urls := tg3.urlsOf(id3); len(urls) != 0 {
		t.Errorf("a web_app button is not a url button: %v", urls)
	}
}

func slContains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

const (
	slSplitOwnerTG   = int64(91611)
	slSplitManagerTG = int64(91612)
)

// TestBotE2E_SeatLayoutSplitOneCategory proves the bot's split of a date that
// has ONE category (spec 64 section 2.4): the owner answers 3 categories and
// the top and bottom price, looks at the preview (nothing is written), applies,
// and the same batch creates the categories and repaints the free seats while
// the sold and the held seat keep the old category, with one seat status
// version bump; the undo puts the seats back and the new categories stay. A
// manager (no tier.create) is refused with a clear word and nothing changes.
func TestBotE2E_SeatLayoutSplitOneCategory(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, slSplitOwnerTG, "owner")
	linkECBotUser(t, f, srv, slSplitManagerTG, "manager")
	ctx := context.Background()
	suffix := uuid.NewString()[:6]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
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
	seats := layoutgen.Seats(geometry)
	y := map[string]float64{}
	for _, s := range seats {
		y[s.Key] = s.Y
	}

	venue, event, channel := uuid.New(), uuid.New(), uuid.New()
	plan, version, session := uuid.New(), uuid.New(), uuid.New()
	stalls := uuid.New()
	start := time.Now().UTC().Add(96 * time.Hour).Truncate(time.Hour)
	t.Cleanup(func() {
		c := context.Background()
		for _, st := range []string{
			`DELETE FROM bot_dialogs WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'session_id' = '` + session.String() + `'`,
			`DELETE FROM session_seat_batches WHERE session_id = '` + session.String() + `'`,
			`DELETE FROM session_seats WHERE session_id = '` + session.String() + `'`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id = '` + session.String() + `'`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
			`DELETE FROM seating_plans WHERE owner_org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
		} {
			args := []any{f.orgID}
			if !strings.Contains(st, "$1") {
				args = nil
			}
			if _, err := pool.Exec(c, st, args...); err != nil {
				t.Logf("seat layout split cleanup %q: %v", st, err)
			}
		}
	})
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venue, f.orgID, "Akropolis "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'public')`, event, f.orgID, "Split Show "+suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channel, f.orgID, "Split Channel "+suffix)
	exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
		plan, venue, f.orgID, "Akropolis "+suffix)
	exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, capacity_standing)
	      VALUES ($1, $2, 1, $3::jsonb, $4, $5, 0)`, version, plan, string(geoJSON), checksum, geometry.SeatCount())
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source, admission_mode, seating_plan_version_id)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'EUR', 'override', 'assigned_seats', $6)`,
		session, event, venue, start, geometry.SeatCount(), version)
	// ONE category holds the whole hall.
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open, sort_order)
	      VALUES ($1, $2, 'Stalls', 'fixed', 9000, 'EUR', 1, true, 1)`, stalls, session)
	for _, s := range seats {
		sec := geometry.Sections[s.Section]
		exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, kind)
		      VALUES ($1, $2, $3, $4, $5, $6, 'seat')`, session, s.Key, sec.Name, sec.Rows[s.Row].Name, s.Number, stalls)
	}
	heldRes := uuid.New()
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
	      VALUES ($1, $2, $3, $4, 1, 'active', now() + interval '1 hour')`, heldRes, f.orgID, channel, session)
	soldKey, heldKey := seats[100].Key, seats[200].Key
	exec(`UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2`, session, soldKey)
	exec(`UPDATE session_seats SET status = 'held', reservation_id = $3 WHERE session_id = $1 AND seat_key = $2`, session, heldKey, heldRes)

	before := slHall(t, pool, session, y)
	batches := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seat_batches WHERE session_id = $1`, session).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tierCount := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1 AND deleted_at IS NULL`, session).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	statusVersion := func() int64 {
		var v int64
		if err := pool.QueryRow(ctx, `SELECT seat_status_version FROM sessions WHERE id = $1`, session).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	sameHall := func(a, b map[string]slSeat) bool {
		for k, s := range a {
			if b[k].Tier != s.Tier || b[k].Stat != s.Stat {
				return false
			}
		}
		return len(a) == len(b)
	}

	tg := newStubTelegram(t)
	startSeatLayoutBot(t, pool, api, tg, nil)
	owner := ecDriver{t, tg, slSplitOwnerTG}
	manager := ecDriver{t, tg, slSplitManagerTG}
	findData := func(d ecDriver, prefix string) string {
		t.Helper()
		_, _, data := tg.lastOf(d.id)
		for _, x := range data {
			if strings.HasPrefix(x, prefix) {
				return x
			}
		}
		t.Fatalf("no button %q on the screen, buttons: %v", prefix, data)
		return ""
	}

	// ── the "Split seats by price" entry opens the same screen as the card ───
	owner.press("sl:o:"+session.String(), "На этом сеансе одна категория")
	owner.press("sl:o:"+uuid.NewString(), "Экран устарел")
	owner.press("ses:list:"+event.String(), "Split Show")
	owner.press("ses:o:0", "Split Show")
	text := owner.press("ses:sl", "На этом сеансе одна категория")
	if !strings.Contains(text, "Stalls") {
		t.Errorf("the start screen lacks the category:\n%s", text)
	}

	// ── the questions: how many categories, the top and the bottom price ─────
	owner.press("sl:a:desirability", "На сколько категорий")
	findData(owner, "sl:nc:2")
	findData(owner, "sl:nc:6")
	owner.press("sl:nc:3", "Какая цена у лучших мест")
	owner.say("abc", "Не понимаю цену")
	owner.say("120", "самых дешёвых мест")
	owner.say("200", "должна быть ниже")
	owner.say("40", "Где сцена")
	owner.press("sl:sd:top", "Где сцена")

	// ── the preview writes nothing and shows the new categories ──────────────
	text = owner.press("sl:go", "Предлагаемая раскладка")
	for _, want := range []string{"Категория 1", "новая", "Реалистичная оценка", "120 EUR", "40 EUR", "продано 1", "в брони 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("the preview lacks %q:\n%s", want, text)
		}
	}
	if batches() != 0 || tierCount() != 1 || !sameHall(before, slHall(t, pool, session, y)) {
		t.Fatal("the preview must write nothing and create no category")
	}
	apply := findData(owner, "sl:ap:")

	// ── apply: categories and seats in ONE batch, one version bump ───────────
	version0 := statusVersion()
	text = owner.press(apply, "Готово")
	for _, want := range []string{"Новые категории:", "Категория 1", "Категория 3", "Новые категории останутся"} {
		if !strings.Contains(text, want) {
			t.Errorf("the applied message lacks %q:\n%s", want, text)
		}
	}
	if batches() != 1 || tierCount() != 4 {
		t.Fatalf("batches = %d (want 1), categories = %d (want 4)", batches(), tierCount())
	}
	if got := statusVersion(); got != version0+1 {
		t.Errorf("seat_status_version = %d, want %d (one bump)", got, version0+1)
	}
	after := slHall(t, pool, session, y)
	if after[soldKey].Tier != stalls || after[soldKey].Stat != "sold" || after[heldKey].Tier != stalls || after[heldKey].Stat != "held" {
		t.Errorf("the sold and the held seat must keep the old category: %+v %+v", after[soldKey], after[heldKey])
	}
	newTiers := map[uuid.UUID]int64{}
	rows, err := pool.Query(ctx, `SELECT id, price_amount FROM ticket_tiers WHERE session_id = $1 AND id <> $2 AND deleted_at IS NULL`, session, stalls)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		var price int64
		if err := rows.Scan(&id, &price); err != nil {
			t.Fatal(err)
		}
		newTiers[id] = price
	}
	rows.Close()
	var hi, lo int64 = 0, 1 << 60
	for _, p := range newTiers {
		if p > hi {
			hi = p
		}
		if p < lo {
			lo = p
		}
	}
	if len(newTiers) != 3 || hi != 12000 || lo != 4000 {
		t.Errorf("new categories = %v, want 3 from 4000 to 12000", newTiers)
	}
	for k, s := range after {
		if k == soldKey || k == heldKey {
			continue
		}
		if _, ok := newTiers[s.Tier]; !ok {
			t.Errorf("free seat %s stayed in %s: it must be repainted into a new category", k, s.Tier)
			break
		}
	}

	// ── undo: the seats go back, the new categories stay and the text says so ─
	undo := findData(owner, "sl:rv:")
	text = owner.press(undo, "Отменено")
	if !strings.Contains(text, "Новые категории остались") {
		t.Errorf("the undo message must say that the new categories stay:\n%s", text)
	}
	if !sameHall(before, slHall(t, pool, session, y)) {
		t.Error("undo must put every seat back")
	}
	if tierCount() != 4 {
		t.Errorf("the new categories stay after the undo, got %d", tierCount())
	}

	// ── a manager has no tier.create: a clear refusal, nothing changes ───────
	exec(`UPDATE ticket_tiers SET is_open = false WHERE session_id = $1 AND id <> $2`, session, stalls)
	batches0 := batches()
	manager.press("ses:list:"+event.String(), "Split Show")
	manager.press("ses:o:0", "Split Show")
	manager.press("ses:sl", "На этом сеансе одна категория")
	manager.press("sl:a:desirability", "На сколько категорий")
	manager.press("sl:nc:2", "Какая цена у лучших мест")
	manager.say("100", "самых дешёвых мест")
	manager.say("50", "Где сцена")
	manager.press("sl:sd:bottom", "Где сцена")
	text = manager.press("sl:go", "Предлагаемая раскладка")
	// The old names are taken by the closed categories: the new ones get other numbers.
	if strings.Contains(text, "Категория 1 (новая)") {
		t.Errorf("a name that a closed category holds must not be proposed again:\n%s", text)
	}
	manager.press(findData(manager, "sl:ap:"), "владелец или администратор")
	if batches() != batches0 || tierCount() != 4 {
		t.Errorf("a refused batch must change nothing: batches %d -> %d, categories %d", batches0, batches(), tierCount())
	}
}
