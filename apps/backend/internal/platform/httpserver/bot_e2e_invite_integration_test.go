//go:build integration

package httpserver

// End-to-end proof of the bot's "Invitations" screens (spec 35 EC-12) through
// the real router, the real bot and the stub Telegram: an organization with a
// general-admission date (two categories with real places) and a seated one.
// An owner and a manager walk event -> date -> category -> quantity -> guests
// -> confirmation and issue invitations (one issuance per guest, each with the
// guest's name and e-mail, a delivery job and a taken place); lines that are
// not valid are named and nothing from their message is kept; the quantity
// cap (50) and the free places refuse in words; the list pages newest first
// with valid / annulled / used states; annulling takes the typed word (a wrong
// word annuls nothing) and returns the place; a ticket scanned at the door
// cannot be annulled; a category that runs out half way leaves the guests
// issued before it and names the ones left; a user of ANOTHER organization
// gets nothing, by the bot and by the API; a bot restart in the middle of the
// dialog loses nothing and a second press of "Send" issues nothing twice.
//
// The stub records message TEXT only, so every effect is checked in the
// database (issuances, tickets, delivery jobs, the places) and through the
// API; what a button shows is covered by the unit tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

// invBotSeed is what the e2e put into the organization.
type invBotSeed struct {
	suffix                     string
	orgID                      uuid.UUID
	event, session             uuid.UUID
	seatedEvent, seatedSession uuid.UUID
	seatedTier                 uuid.UUID
	vip, std                   uuid.UUID // VIP owns 6 places, Standard 3
	eventName, seatedName      string
}

// seedInviteBotOrg creates a general-admission date with two categories that
// own real places (migration 0101) and a seated date, and registers the
// cleanup of everything the dialog may add (tickets, issuances, jobs).
func seedInviteBotOrg(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) *invBotSeed {
	t.Helper()
	ctx := context.Background()
	s := &invBotSeed{
		suffix: uuid.NewString()[:6], orgID: orgID,
		event: uuid.New(), session: uuid.New(), seatedEvent: uuid.New(), seatedSession: uuid.New(),
		vip: uuid.New(), std: uuid.New(),
	}
	s.eventName = "Invite Show " + s.suffix
	s.seatedName = "Seated Show " + s.suffix
	plan, version, seatedTier := uuid.New(), uuid.New(), uuid.New()
	s.seatedTier = seatedTier
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("invite seed %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		// The staff invitations (orders, reservations, checkout sessions, jobs)
		// the dialog wrote, then the guests' customer rows no other organization knows.
		var customerIDs []uuid.UUID
		if rows, err := pool.Query(c, `SELECT customer_id FROM customer_org_links WHERE org_id = $1`, orgID); err == nil {
			for rows.Next() {
				var id uuid.UUID
				if rows.Scan(&id) == nil {
					customerIDs = append(customerIDs, id)
				}
			}
			rows.Close()
		}
		for _, sid := range []uuid.UUID{s.session, s.seatedSession} {
			for _, sql := range inv02SessionCleanupSQL {
				if _, err := pool.Exec(c, sql, sid); err != nil {
					t.Logf("invite bot cleanup: %v", err)
				}
			}
		}
		for _, sql := range []string{
			`DELETE FROM customer_org_links WHERE org_id = $1`,
			`DELETE FROM outbox_events WHERE aggregate_id = $1::text`,
			`DELETE FROM agent_feed_tokens WHERE sales_channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1)`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(c, sql, orgID); err != nil {
				t.Logf("invite bot cleanup: %s: %v", sql, err)
			}
		}
		if len(customerIDs) > 0 {
			_, _ = pool.Exec(c, `DELETE FROM customers c WHERE c.id = ANY($1::uuid[])
				AND NOT EXISTS (SELECT 1 FROM customer_org_links l WHERE l.customer_id = c.id)
				AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.customer_id = c.id)`, customerIDs)
		}
		sessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`
		tickets := `(SELECT id FROM tickets WHERE session_id IN ` + sessions + `)`
		for _, sql := range []string{
			`DELETE FROM worker_jobs WHERE payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE session_id IN ` + sessions + `)`,
			`DELETE FROM delivery_jobs WHERE ticket_id IN ` + tickets,
			`DELETE FROM barcodes WHERE ticket_id IN ` + tickets,
			`DELETE FROM ticket_credentials WHERE ticket_id IN ` + tickets,
			`DELETE FROM tickets WHERE session_id IN ` + sessions,
			`DELETE FROM complimentary_issuances WHERE org_id = $1`,
			`DELETE FROM session_seats WHERE session_id IN ` + sessions,
			`DELETE FROM inventory_ledger WHERE session_id IN ` + sessions,
			`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions,
			`DELETE FROM sessions WHERE id IN ` + sessions,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
			`DELETE FROM seating_plans WHERE owner_org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
		} {
			if _, err := pool.Exec(c, sql, orgID); err != nil {
				t.Logf("invite bot cleanup: %s: %v", sql, err)
			}
		}
	})

	venue := uuid.New()
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, venue, orgID, "Invite Hall "+s.suffix)
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, s.event, orgID, s.eventName)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 9, 'scheduled', 'general_admission', 'EUR', 'override')`, s.session, s.event, venue, start)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order, capacity, unit_seq, is_open)
	      VALUES ($1, $2, 'VIP', 'fixed', 5000, 'EUR', 0, 6, 1, true), ($3, $2, 'Standard', 'fixed', 2500, 'EUR', 1, 3, 2, true)`, s.vip, s.session, s.std)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 9)`, s.session)
	exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
	      SELECT $1, 'ga|t1|' || lpad(gs::text, 6, '0'), '', '', '', $2, 'available', 'ga_unit' FROM generate_series(1, 6) gs`, s.session, s.vip)
	exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
	      SELECT $1, 'ga|t2|' || lpad(gs::text, 6, '0'), '', '', '', $2, 'available', 'ga_unit' FROM generate_series(1, 3) gs`, s.session, s.std)

	// The seated date: a plan, its seats, no general-admission category.
	exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
	      VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`, plan, venue, orgID, "Invite Plan "+s.suffix)
	exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
	      VALUES ($1, $2, 1, '{}'::jsonb, $3, 2)`, version, plan, "inv-"+version.String()[:8])
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, s.seatedEvent, orgID, s.seatedName)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source, seating_plan_version_id)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 2, 'scheduled', 'assigned_seats', 'EUR', 'override', $5)`,
		s.seatedSession, s.seatedEvent, venue, start.Add(10*24*time.Hour), version)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order) VALUES ($1, $2, 'Seated', 'fixed', 5000, 'EUR', 0)`, seatedTier, s.seatedSession)
	exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
	      VALUES ($1, 'A|1|1', 'A', '1', '1', $2, 'available', 'seat'), ($1, 'A|1|2', 'A', '1', '2', $2, 'available', 'seat')`, s.seatedSession, seatedTier)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 2)`, s.seatedSession)
	return s
}

// invE2E is the shared set-up of the scenarios.
type invE2E struct {
	t     *testing.T
	pool  *pgxpool.Pool
	f     *botInviteFixture
	other *botInviteFixture
	srv   *Server
	api   *httptest.Server
	seed  *invBotSeed
	tg    *stubTelegram
	stop  func()
}

func newInviteE2E(t *testing.T) *invE2E { return newInviteE2EWith(t, nil, nil) }

// newInviteE2EWith is newInviteE2E with a hook on the API config and one on
// the bot's options (the Mini App scenario switches the flag and the address on).
func newInviteE2EWith(t *testing.T, mutate func(*config.Config), tweak func(*eventbot.Options)) *invE2E {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	e := &invE2E{t: t, pool: pool}
	e.f = newBotInviteFixture(t, pool)
	e.other = newBotInviteFixture(t, pool)
	e.srv = buildBotIntegrationServerCfg(t, pool, mutate)
	e.api = httptest.NewServer(e.srv.router)
	t.Cleanup(e.api.Close)
	e.seed = seedInviteBotOrg(t, pool, e.f.orgID)
	linkECBotUser(t, e.f, e.srv, ecOwnerTG, "owner")
	linkECBotUser(t, e.f, e.srv, ecManagerTG, "manager")
	linkECBotUser(t, e.other, e.srv, ecOutsiderTG, "owner")
	e.tg = newStubTelegram(t)
	if tweak != nil {
		e.stop = textFmtBot(t, pool, e.api, e.tg, 3*time.Second, tweak)
	} else {
		e.stop = startDialogTestBot(t, pool, e.api, e.tg)
	}
	t.Cleanup(func() { e.stop() })
	return e
}

func (e *invE2E) restart() {
	e.t.Helper()
	e.stop = restartDialogTestBot(e.t, e.pool, e.api, e.tg, e.stop)
}

func (e *invE2E) driver(tgID int64) ecDriver { return ecDriver{e.t, e.tg, tgID} }

// userOf is the arena user a Telegram account is linked to.
func (e *invE2E) userOf(tgID int64) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT user_id FROM bot_telegram_links WHERE telegram_user_id = $1`, tgID).Scan(&id); err != nil {
		e.t.Fatalf("user of telegram %d: %v", tgID, err)
	}
	return id
}

// call makes an API request as the user behind a Telegram account.
func (e *invE2E) call(tgID int64, method, path string, body any) (int, []byte) {
	e.t.Helper()
	tok, err := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(e.userOf(tgID))
	if err != nil {
		e.t.Fatal(err)
	}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.srv.router.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (e *invE2E) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func (e *invE2E) places(tier uuid.UUID, status string) int {
	return e.count(`SELECT count(*) FROM session_seats WHERE session_id = $1 AND tier_id = $2 AND status = $3 AND kind = 'ga_unit'`, e.seed.session, tier, status)
}

// staffOrders counts the staff invitations (spec 46 orders) of the organization.
func (e *invE2E) staffOrders() int {
	return e.count(`SELECT count(*) FROM orders WHERE org_id = $1 AND source = 'complimentary' AND issued_via = 'staff'`, e.seed.orgID)
}

// mintTickets does the worker's job for every staff invitation that has no
// tickets yet (the e2e runs no worker).
func (e *invE2E) mintTickets() {
	e.t.Helper()
	ctx := context.Background()
	rows, err := e.pool.Query(ctx, `SELECT checkout_session_id FROM orders o WHERE o.org_id = $1 AND o.source = 'complimentary'
		AND NOT EXISTS (SELECT 1 FROM tickets t WHERE t.checkout_session_id = o.checkout_session_id)`, e.seed.orgID)
	if err != nil {
		e.t.Fatal(err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		cs, err := gen.New(e.pool).GetCheckoutSessionByID(ctx, id)
		if err != nil {
			e.t.Fatal(err)
		}
		if _, err := e.srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs); err != nil {
			e.t.Fatalf("IssueTicketsForCheckout: %v", err)
		}
	}
}

// staffInv is one staff invitation order with its (single) ticket.
type staffInv struct {
	orderID                              uuid.UUID
	sysID                                int64
	email, name, delivery, origin, state string
	ticketID                             uuid.UUID
	ticketStatus, seatKey                string
}

// staffInvs reads the organization's staff invitations keyed by the guest's
// e-mail (the guest of a name-less line is named by the address).
func (e *invE2E) staffInvs() map[string]staffInv {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT o.id, o.system_id, coalesce(o.buyer_email, ''), coalesce(o.buyer_name, ''), coalesce(o.metadata->'invitation'->>'delivery', ''),
		       coalesce((SELECT oi.seat_origin FROM order_items oi WHERE oi.order_id = o.id LIMIT 1), ''), o.status,
		       coalesce(t.id, '00000000-0000-0000-0000-000000000000'::uuid), coalesce(t.status, ''), coalesce(t.seat_key, '')
		FROM orders o LEFT JOIN tickets t ON t.checkout_session_id = o.checkout_session_id
		WHERE o.org_id = $1 AND o.source = 'complimentary'`, e.seed.orgID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]staffInv{}
	for rows.Next() {
		var c staffInv
		if err := rows.Scan(&c.orderID, &c.sysID, &c.email, &c.name, &c.delivery, &c.origin, &c.state, &c.ticketID, &c.ticketStatus, &c.seatKey); err != nil {
			e.t.Fatal(err)
		}
		out[c.email] = c
	}
	return out
}

func (e *invE2E) issuances() int {
	return e.count(`SELECT count(*) FROM complimentary_issuances WHERE org_id = $1`, e.seed.orgID)
}

func (e *invE2E) email(label string) string { return label + "-" + e.seed.suffix + "@example.test" }

// invState is the invite dialog as it is stored.
type invState struct {
	Step                          string
	EventIDs, SessionIDs, TierIDs []uuid.UUID
	ListIDs, NOrderIDs            []uuid.UUID
	Qty                           int
	Rcpt                          []map[string]any
	Done                          []int64
}

func (e *invE2E) dialog(tg int64) (invState, bool) {
	e.t.Helper()
	var raw []byte
	var step string
	err := e.pool.QueryRow(context.Background(), `SELECT step, state FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'invite'`, tg).Scan(&step, &raw)
	if err != nil {
		return invState{}, false
	}
	var st struct {
		EventIDs   []uuid.UUID      `json:"event_ids"`
		SessionIDs []uuid.UUID      `json:"session_ids"`
		TierIDs    []uuid.UUID      `json:"tier_ids"`
		ListIDs    []uuid.UUID      `json:"list_ids"`
		NOrderIDs  []uuid.UUID      `json:"n_order_ids"`
		Qty        int              `json:"qty"`
		Rcpt       []map[string]any `json:"rcpt"`
		Done       []int64          `json:"done"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		e.t.Fatal(err)
	}
	return invState{Step: step, EventIDs: st.EventIDs, SessionIDs: st.SessionIDs, TierIDs: st.TierIDs, ListIDs: st.ListIDs, NOrderIDs: st.NOrderIDs, Qty: st.Qty, Rcpt: st.Rcpt, Done: st.Done}, true
}

// toCategory walks the dialog to the quantity question of the category
// (0 = VIP, 1 = Standard) of the general-admission date.
func (e *invE2E) toCategory(d ecDriver, tier int) {
	e.t.Helper()
	d.press("iv:new", "Бесплатные билеты для гостей")
	d.press("iv:i", "Выберите мероприятие")
	st, ok := e.dialog(d.id)
	if !ok || st.Step != "ev" || len(st.EventIDs) != 2 {
		e.t.Fatalf("events step = %+v %v, want the two events of the organization", st, ok)
	}
	if st.EventIDs[0] != e.seed.event {
		e.t.Fatalf("the soonest event comes first: %v, want %v", st.EventIDs, e.seed.event)
	}
	d.press("iv:eo:0", "Выберите дату")
	d.press("iv:so:0", "Выберите категорию")
	st, _ = e.dialog(d.id)
	if len(st.TierIDs) != 2 || st.TierIDs[0] != e.seed.vip || st.TierIDs[1] != e.seed.std {
		e.t.Fatalf("categories on screen = %v, want VIP then Standard", st.TierIDs)
	}
	d.press(fmt.Sprintf("iv:to:%d", tier), "Сколько билетов?")
}

type compTicket struct {
	id               uuid.UUID
	email, name      string
	sysID            int64
	status, seatKey  string
	delivery, worker int
}

// ticketsOfOrg reads every complimentary ticket of the organization by e-mail.
func (e *invE2E) tickets() map[string]compTicket {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT t.id, coalesce(t.holder_email, ''), coalesce(t.holder_name, ''), t.system_ticket_id, t.status, coalesce(t.seat_key, ''),
		       (SELECT count(*) FROM delivery_jobs d WHERE d.ticket_id = t.id),
		       (SELECT count(*) FROM worker_jobs w WHERE w.job_type = 'ticket.deliver' AND w.payload->>'ticket_id' = t.id::text)
		FROM tickets t JOIN complimentary_issuances ci ON ci.id = t.complimentary_issuance_id
		WHERE ci.org_id = $1`, e.seed.orgID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]compTicket{}
	for rows.Next() {
		var c compTicket
		if err := rows.Scan(&c.id, &c.email, &c.name, &c.sysID, &c.status, &c.seatKey, &c.delivery, &c.worker); err != nil {
			e.t.Fatal(err)
		}
		out[c.email] = c
	}
	return out
}

// issueViaAPI issues n one-place staff invitations on the VIP category of the
// general-admission date through the real route, as the owner.
func (e *invE2E) issueViaAPI(n int, label string) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		code, body := e.call(ecOwnerTG, http.MethodPost, "/v1/organizations/"+e.seed.orgID.String()+"/sessions/"+e.seed.session.String()+"/invitations", map[string]any{
			"request_id": uuid.NewString(),
			"ga":         []map[string]any{{"tier_id": e.seed.vip.String(), "qty": 1}},
			"guest":      map[string]any{"name": fmt.Sprintf("%s %d", label, i), "email": e.email(fmt.Sprintf("%s%d", label, i))},
			"delivery":   "guest_email",
		})
		if code != http.StatusCreated {
			e.t.Fatalf("issue %d: %d %s", i, code, body)
		}
	}
}

// The owner issues three invitations through the new route, the list and the
// card show them, a wrong word annuls nothing, the right one does and returns
// the place, a ticket scanned at the door cannot be annulled.
func TestBotE2E_Invitations_OwnerIssuesListsAndAnnuls(t *testing.T) {
	e := newInviteE2E(t)
	d := e.driver(ecOwnerTG)
	anna, boris, carl := e.email("anna"), e.email("boris"), e.email("carl")

	// ── the way to the quantity, and the refusals on the way ────────────────
	e.toCategory(d, 0)
	d.say("abc", "Это не число")
	d.say("51", "не больше 50 билетов")
	d.say("7", "осталось только 6 свободных мест")
	d.say("3", "Осталось указать: 3")
	if st, _ := e.dialog(d.id); st.Step != "rcpt" || st.Qty != 3 {
		t.Fatalf("dialog = %+v, want step rcpt with qty 3", st)
	}

	// ── guests: a message with a bad line is refused whole ──────────────────
	bad := d.say("Anna Test, "+anna+"\nnot an address\n"+anna+"\nBoris Test, broken@", "Из этого сообщения ничего не принято")
	for _, want := range []string{"Строка 2", "Строка 3", "Строка 4", "нет корректного e-mail", "этот e-mail уже есть в списке"} {
		if !strings.Contains(bad, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, bad)
		}
	}
	if st, _ := e.dialog(d.id); len(st.Rcpt) != 0 {
		t.Fatalf("a refused message kept guests: %+v", st.Rcpt)
	}
	// Two valid lines: accepted, one more is needed.
	got := d.say("Anna Test, "+anna+"\nBoris Test <"+boris+">", "Принято пока (2 из 3)")
	if !strings.Contains(got, "Anna Test") || !strings.Contains(got, boris) {
		t.Errorf("the accepted guests are not listed:\n%s", got)
	}
	// Too many for what is left; the same address twice.
	d.say("c1-"+e.seed.suffix+"@example.test\nc2-"+e.seed.suffix+"@example.test", "нужно ещё только 1")
	d.say(anna, "этот e-mail уже есть в списке")
	if st, _ := e.dialog(d.id); len(st.Rcpt) != 2 {
		t.Fatalf("guests = %+v, want the 2 accepted ones", st.Rcpt)
	}
	confirm := d.say("Carl Test, "+carl, "Отправить приглашения?")
	for _, want := range []string{"Билетов: 3", "Carl Test", anna, "Invite Show"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, confirm)
		}
	}
	// Nothing is issued before the last press.
	if n := e.staffOrders(); n != 0 {
		t.Fatalf("%d orders before the confirmation", n)
	}

	// ── send: one order per guest through the new route ─────────────────────
	done := d.press("iv:go", "Выдано приглашений: 3")
	inv := e.staffInvs()
	if len(inv) != 3 {
		t.Fatalf("orders = %+v, want 3", inv)
	}
	for _, want := range []struct{ email, name string }{{anna, "Anna Test"}, {boris, "Boris Test"}, {carl, "Carl Test"}} {
		c, ok := inv[want.email]
		if !ok || c.name != want.name || c.delivery != "guest_email" || c.origin != "available" || c.state != "paid" {
			t.Errorf("order of %s = %+v, want a paid order named %q, delivered to the guest, from a free place", want.email, c, want.name)
		}
		if !strings.Contains(done, fmt.Sprintf("№%d", c.sysID)) {
			t.Errorf("the result lacks the order number %d of %s:\n%s", c.sysID, want.email, done)
		}
	}
	if n := e.issuances(); n != 0 {
		t.Errorf("the legacy table got %d issuances, the bot must use the new route", n)
	}
	// The places are sold in the issue transaction itself.
	if sold, free := e.places(e.seed.vip, "sold"), e.places(e.seed.vip, "available"); sold != 3 || free != 3 {
		t.Errorf("VIP places sold/free = %d/%d, want 3/3", sold, free)
	}
	if _, ok := e.dialog(d.id); ok {
		t.Error("the dialog must end after the send")
	}
	// A second press of "Send" finds nothing to send and issues nothing.
	d.press("iv:go", "Не найдено")
	if n := e.staffOrders(); n != 3 {
		t.Errorf("a second press issued: %d orders", n)
	}
	e.mintTickets()
	inv = e.staffInvs()
	for _, em := range []string{anna, boris, carl} {
		if c := inv[em]; c.ticketStatus != "active" || !strings.HasPrefix(c.seatKey, "ga|t1|") {
			t.Errorf("ticket of %s = %+v, want an active ticket on a VIP place", em, c)
		}
	}

	// ── the list: every date at once, newest first ──────────────────────────
	d.press("iv:l", "Все сеансы, сначала новые")
	st, _ := e.dialog(d.id)
	if st.Step != "nlist" || len(st.NOrderIDs) != 3 {
		t.Fatalf("list dialog = %+v, want 3 rows", st)
	}
	code, body := e.call(ecOwnerTG, http.MethodGet, "/v1/organizations/"+e.seed.orgID.String()+"/invitations?limit=2", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var page struct {
		Items []struct {
			OrderID uuid.UUID `json:"order_id"`
			Session struct {
				EventName string `json:"event_name"`
			} `json:"session"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil || page.Items[0].OrderID != st.NOrderIDs[0] || page.Items[0].Session.EventName != e.seed.eventName {
		t.Errorf("API page = %s, want 2 rows and a cursor, newest first matching the bot's first row %v", body, st.NOrderIDs[0])
	}
	if st.NOrderIDs[0] != inv[carl].orderID {
		t.Errorf("the newest row is %v, want Carl's order %v", st.NOrderIDs[0], inv[carl].orderID)
	}

	// ── the card and the annul word ─────────────────────────────────────────
	if card := d.press("iv:no:0", "Места:"); !strings.Contains(card, "✉ Отправляем письмо") {
		t.Errorf("the card of a guest_email invitation shows its queued letter: %q", card)
	}
	d.press("iv:nrv", "Аннулировать это приглашение?")
	d.say("нет", "Текст не совпал")
	if e.count(`SELECT count(*) FROM tickets WHERE status = 'cancelled' AND checkout_session_id IN (SELECT checkout_session_id FROM orders WHERE org_id = $1)`, e.seed.orgID) != 0 {
		t.Fatal("a wrong word annulled something")
	}
	d.say("аннулировать", "аннулировано: билетов 1")
	inv = e.staffInvs()
	if inv[carl].ticketStatus != "cancelled" || inv[anna].ticketStatus != "active" || inv[boris].ticketStatus != "active" {
		t.Errorf("annulled the wrong ticket: carl %q anna %q boris %q", inv[carl].ticketStatus, inv[anna].ticketStatus, inv[boris].ticketStatus)
	}
	if inv[carl].state != "paid" {
		t.Errorf("an annulled invitation stays a paid order (it stays billed): %q", inv[carl].state)
	}
	if sold, free := e.places(e.seed.vip, "sold"), e.places(e.seed.vip, "available"); sold != 2 || free != 4 {
		t.Errorf("VIP places sold/free after the annul = %d/%d, want 2/4", sold, free)
	}
	// An annulled invitation offers no second annul: back to the list.
	d.press("iv:l", "Все сеансы, сначала новые")
	d.press("iv:no:0", "Места:") // Carl's card, annulled
	d.press("iv:nrv", "Все сеансы, сначала новые")

	// ── a ticket scanned at the door cannot be annulled ─────────────────────
	if _, err := e.pool.Exec(context.Background(), `UPDATE tickets SET used_at = now() WHERE id = $1`, inv[boris].ticketID); err != nil {
		t.Fatal(err)
	}
	d.press("iv:l", "Все сеансы, сначала новые")
	st, _ = e.dialog(d.id)
	row := -1
	for i, id := range st.NOrderIDs {
		if id == inv[boris].orderID {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("Boris's order is not on the list: %v", st.NOrderIDs)
	}
	d.press(fmt.Sprintf("iv:no:%d", row), "Места:")
	d.press("iv:nrv", "Аннулировать это приглашение?")
	d.say("аннулировать", "уже использовано")
	if inv = e.staffInvs(); inv[boris].ticketStatus != "active" {
		t.Errorf("a scanned ticket was annulled: %q", inv[boris].ticketStatus)
	}
}

// The new list pages by cursor, and what was issued before the new
// invitations (complimentary_issuances) stays reachable, read-only except for
// its own annul.
func TestBotE2E_Invitations_ListPagesAndEarlierInvitationsStayManageable(t *testing.T) {
	e := newInviteE2E(t)
	d := e.driver(ecOwnerTG)

	// Without any invitation the list is empty and there is no "earlier" button.
	d.press("iv:l", "Приглашений пока не выдавали")
	if st, _ := e.dialog(d.id); st.Step != "nlist" || len(st.NOrderIDs) != 0 {
		t.Fatalf("empty list dialog = %+v", st)
	}

	e.issueViaAPI(6, "paged")
	d.press("iv:l", "страница 1")
	st, _ := e.dialog(d.id)
	if len(st.NOrderIDs) != 5 {
		t.Fatalf("page 1 has %d rows, want 5", len(st.NOrderIDs))
	}
	first := st.NOrderIDs[0]
	d.press("iv:np:2", "страница 2")
	st, _ = e.dialog(d.id)
	if len(st.NOrderIDs) != 1 || st.NOrderIDs[0] == first {
		t.Fatalf("page 2 rows = %v, want the one oldest order", st.NOrderIDs)
	}
	d.press("iv:np:1", "страница 1")
	if st, _ = e.dialog(d.id); len(st.NOrderIDs) != 5 || st.NOrderIDs[0] != first {
		t.Fatalf("back on page 1: %v", st.NOrderIDs)
	}

	// An invitation of the old kind (the general-admission date still takes
	// the old route): the list shows the way to it, and it can be annulled.
	code, body := e.call(ecOwnerTG, http.MethodPost, "/v1/organizations/"+e.seed.orgID.String()+"/complimentary", map[string]any{
		"session_id": e.seed.session, "tier_id": e.seed.std, "qty": 1, "batch_id": "old-" + e.seed.suffix, "recipients": []string{e.email("old")},
	})
	if code != http.StatusCreated {
		t.Fatalf("old issue: %d %s", code, body)
	}
	d.press("iv:l", "страница 1")
	d.press("iv:lg", "Прежние приглашения")
	if st, _ = e.dialog(d.id); st.Step != "list" || len(st.ListIDs) != 1 {
		t.Fatalf("earlier invitations dialog = %+v, want the one old issuance", st)
	}
	d.press("iv:lo:0", "действует")
	d.press("iv:rv", "Аннулировать это приглашение?")
	d.say("аннулировать", "аннулировано. Место вернулось в продажу")
	if n := e.count(`SELECT count(*) FROM complimentary_issuances WHERE org_id = $1 AND status = 'revoked'`, e.seed.orgID); n != 1 {
		t.Errorf("revoked old issuances = %d, want 1", n)
	}
}

// The manager has the same hands: issues one invitation to an address only
// (the address names the guest on the ticket) and annuls it.
func TestBotE2E_Invitations_ManagerIssuesAndAnnuls(t *testing.T) {
	e := newInviteE2E(t)
	d := e.driver(ecManagerTG)
	guest := e.email("manager-guest")

	e.toCategory(d, 1)
	d.press("iv:q:1", "Кто эти 1 гостей?")
	d.say(guest, "Отправить приглашения?")
	d.press("iv:go", "Выдано приглашений: 1")
	e.mintTickets()
	c := e.staffInvs()[guest]
	if c.ticketStatus != "active" || c.name != guest || !strings.HasPrefix(c.seatKey, "ga|t2|") || c.delivery != "guest_email" {
		t.Fatalf("order = %+v, want an active ticket on a Standard place named by the address", c)
	}
	if e.places(e.seed.std, "sold") != 1 {
		t.Error("Standard must have one place sold")
	}
	d.press("iv:l", "Все сеансы, сначала новые")
	d.press("iv:no:0", "Места:")
	d.press("iv:nrv", "Аннулировать это приглашение?")
	d.say("ANNUL", "аннулировано")
	if e.places(e.seed.std, "sold") != 0 || e.staffInvs()[guest].ticketStatus != "cancelled" {
		t.Error("the annul must return the place and cancel the ticket")
	}
	// The event card carries the entry point too.
	d.press("iv:e:"+e.seed.event.String(), "Выберите дату")
	if st, _ := e.dialog(d.id); st.Step != "se" || len(st.SessionIDs) != 1 || st.SessionIDs[0] != e.seed.session {
		t.Errorf("from the event card: %+v", st)
	}
}

// A category that runs out half way: the guests before it stay issued, the
// answer says so and names the guests that were not.
func TestBotE2E_Invitations_CategoryRunsOutHalfWay(t *testing.T) {
	e := newInviteE2E(t)
	d := e.driver(ecOwnerTG)
	first, second := e.email("first"), e.email("second")

	e.toCategory(d, 1) // Standard: 3 places
	d.say("2", "Осталось указать: 2")
	d.say("First Guest, "+first+"\nSecond Guest, "+second, "Отправить приглашения?")
	// Somebody else takes two of the three places before the press.
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND tier_id = $2 AND seat_key IN ('ga|t2|000001', 'ga|t2|000002')`, e.seed.session, e.seed.std); err != nil {
		t.Fatal(err)
	}
	res := d.press("iv:go", "Выдано 1 из 2")
	for _, want := range []string{"в категории не хватает свободных мест", "Выдано:", "First Guest", "Не выдано:", "Second Guest", second} {
		if !strings.Contains(res, want) {
			t.Errorf("the partial answer lacks %q:\n%s", want, res)
		}
	}
	inv := e.staffInvs()
	if len(inv) != 1 || inv[first].name != "First Guest" {
		t.Errorf("orders = %+v, want only the first guest", inv)
	}
	if _, ok := e.dialog(d.id); ok {
		t.Error("a refusal for want of places ends the dialog")
	}
	// The category now shows no free places at all (one more taken).
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND tier_id = $2`, e.seed.session, e.seed.std); err != nil {
		t.Fatal(err)
	}
	d.press("iv:i", "Выберите мероприятие")
	d.press("iv:eo:0", "Выберите дату")
	d.press("iv:so:0", "Выберите категорию")
	d.press("iv:to:1", "не осталось свободных мест")
}

// The dialog is on disk: a restart in the middle of the guests loses nothing,
// the Home button ends it, a dialog that ran out is reported once, and a seated
// date shows the way to the seat map in words (the flag is off here).
func TestBotE2E_Invitations_RestartHomeExpiryAndSeatedDate(t *testing.T) {
	e := newInviteE2E(t)
	d := e.driver(ecOwnerTG)
	g1, g2 := e.email("restart-one"), e.email("restart-two")

	e.toCategory(d, 0)
	d.say("2", "Осталось указать: 2")
	d.say("One, "+g1, "Принято пока (1 из 2)")
	e.restart()
	// A typed answer still reaches the dialog kept on disk.
	d.say("Two, "+g2, "Отправить приглашения?")
	e.restart()
	// The confirmation was sent before the restart: its button is out of
	// date and issues nothing (owner decision 2026-10-14).
	e.tg.pressStale(t, d.id, "iv:go")
	if n := e.staffOrders(); n != 0 {
		t.Fatalf("the old screen's button issued %d invitation(s)", n)
	}
	// From the menu the person starts again on new screens.
	d.say("☰ Меню", "Что будем делать")
	e.toCategory(d, 0)
	d.say("2", "Осталось указать: 2")
	d.say("One, "+g1, "Принято пока (1 из 2)")
	d.say("Two, "+g2, "Отправить приглашения?")
	d.press("iv:go", "Выдано приглашений: 2")
	if inv := e.staffInvs(); len(inv) != 2 || inv[g1].name != "One" || inv[g2].name != "Two" {
		t.Fatalf("orders = %+v", inv)
	}
	if n := e.staffOrders(); n != 2 {
		t.Errorf("orders = %d, want 2", n)
	}

	// Home ends a dialog: a text typed afterwards is nobody's answer.
	e.toCategory(d, 0)
	d.say("1", "Осталось указать: 1")
	d.press("home", "Что будем делать")
	if _, ok := e.dialog(d.id); ok {
		t.Fatal("Home must end the invitations dialog")
	}
	m := e.tg.mark()
	e.tg.push(e2eMessageAs(d.id, "stray-"+e.seed.suffix+"@example.test"))
	e.tg.waitSince(t, m, "Я не понял")
	if n := e.staffOrders(); n != 2 {
		t.Errorf("a stray address issued: %d", n)
	}

	// A dialog that ran out is reported once, on a text or on a press.
	e.toCategory(d, 0)
	d.say("1", "Осталось указать: 1")
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'invite'`, d.id); err != nil {
		t.Fatal(err)
	}
	d.say("late-"+e.seed.suffix+"@example.test", "Этот диалог устарел")
	if n := e.staffOrders(); n != 2 {
		t.Errorf("an expired dialog issued: %d", n)
	}
	d.press("iv:go", "Не найдено")

	// The seated date: the seats are picked on the map. With the Mini App
	// flag off the screen is a text, with the link to the web screen when the
	// bot knows the event center's address (it does not here).
	d.press("iv:i", "Выберите мероприятие")
	d.press("iv:eo:1", "Выберите дату")
	got := d.press("iv:so:0", "есть схема зала")
	if !strings.Contains(got, "веб-ивент-центре") {
		t.Errorf("the seated screen does not point at the web event center:\n%s", got)
	}
	if st, _ := e.dialog(d.id); st.Step != "ti" || len(st.TierIDs) != 0 {
		t.Errorf("seated date dialog = %+v, want the category step with no category to pick", st)
	}
	if n := e.count(`SELECT count(*) FROM web_login_codes WHERE purpose = 'miniapp' AND user_id = $1`, e.userOf(d.id)); n != 0 {
		t.Errorf("a code was drawn with the flag off: %d", n)
	}
}

// A seated date with BOT_MINIAPP_INVITES_ENABLED on: the entry is a callback
// button, no code exists until it is pressed, and the press draws exactly one
// one-time code and turns that button into a web_app button that opens
// /tg/invitations for THIS session and event. The code is in the fragment.
func TestBotE2E_Invitations_SeatedDateOpensTheMiniApp(t *testing.T) {
	e := newInviteE2EWith(t, func(c *config.Config) { c.EventCenterPublicURL = "https://events.example.test" },
		func(o *eventbot.Options) {
			o.MiniappInvites = true
			o.EventCenterURL = "https://events.example.test"
		})
	d := e.driver(ecOwnerTG)
	user := e.userOf(d.id)
	openCodes := func() int {
		return e.count(`SELECT count(*) FROM web_login_codes WHERE user_id = $1 AND purpose = 'miniapp' AND used_at IS NULL AND revoked_at IS NULL`, user)
	}

	d.press("iv:new", "Бесплатные билеты для гостей")
	d.press("iv:i", "Выберите мероприятие")
	d.press("iv:eo:1", "Выберите дату")
	d.press("iv:so:0", "Нажмите кнопку")
	screen, _, _ := e.tg.lastOf(d.id)
	if n := openCodes(); n != 0 {
		t.Fatalf("%d codes drawn when the screen was drawn, want none until the press", n)
	}
	// The press draws one code and the button becomes a web_app button.
	d.press("iv:ma", "Ссылка действует 5 минут")
	if n := openCodes(); n != 1 {
		t.Fatalf("%d open codes after one press, want 1", n)
	}
	apps := e.tg.webAppsOf(screen)
	prefix := "https://events.example.test/tg/invitations?session=" + e.seed.seatedSession.String() + "&event=" + e.seed.seatedEvent.String() + "#code="
	if len(apps) != 1 || !strings.HasPrefix(apps[0], prefix) || len(apps[0]) <= len(prefix)+20 {
		t.Fatalf("web_app buttons of the screen: %v, want one starting with %s", apps, prefix)
	}
	// The code opens the page: it exchanges for a session of the same user.
	code := strings.TrimPrefix(apps[0], prefix)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/web/link", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	e.srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("the code does not sign in: %d %s", rec.Code, rec.Body.String())
	}
	// A GA date of the same organization has no Mini App button: it goes on to categories.
	d.press("iv:i", "Выберите мероприятие")
	d.press("iv:eo:0", "Выберите дату")
	d.press("iv:so:0", "Выберите категорию")
}

// Somebody from ANOTHER organization gets nothing — from the bot and from the
// API — and a press from a group chat is refused.
func TestBotE2E_Invitations_OtherOrganizationAndGroupChat(t *testing.T) {
	e := newInviteE2E(t)
	owner := e.driver(ecOwnerTG)
	out := e.driver(ecOutsiderTG)
	guest := e.email("isolated")

	e.toCategory(owner, 0)
	owner.press("iv:q:1", "Кто эти 1 гостей?")
	owner.say(guest, "Отправить приглашения?")
	owner.press("iv:go", "Выдано приглашений: 1")
	e.mintTickets()
	order := e.staffInvs()[guest]

	// The outsider's own organization has no events: nothing to invite to.
	out.press("iv:i", "нет предстоящей даты")
	// An event of the first organization by id: not found.
	out.press("iv:e:"+e.seed.event.String(), "Не найдено")
	// Presses with no dialog behind them, aimed at the first organization's rows.
	for _, c := range []string{"iv:eo:0", "iv:so:0", "iv:to:0", "iv:q:1", "iv:go", "iv:lo:0", "iv:rv", "iv:lp:1", "iv:no:0", "iv:nrv", "iv:np:2", "iv:ma"} {
		got := out.press(c, "Не найдено")
		if strings.Contains(got, guest) || strings.Contains(got, e.seed.eventName) {
			t.Errorf("%s leaked to another organization:\n%s", c, got)
		}
	}
	out.say(guest, "Я не понял")
	// The outsider's list is their own (empty), never ours.
	got := out.press("iv:l", "Приглашений пока не выдавали")
	if strings.Contains(got, guest) {
		t.Errorf("the other organization's list shows our guest:\n%s", got)
	}

	// The API: the outsider cannot cancel, list or issue in our organization,
	// and sees nothing in their own list.
	org := e.seed.orgID.String()
	if code, body := e.call(ecOutsiderTG, http.MethodPost, "/v1/organizations/"+org+"/invitations/"+order.orderID.String()+"/cancel", map[string]any{}); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("foreign cancel: %d %s, want 403 or 404", code, body)
	}
	if code, _ := e.call(ecOutsiderTG, http.MethodGet, "/v1/organizations/"+org+"/invitations", nil); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("foreign list: %d, want 403 or 404", code)
	}
	if code, _ := e.call(ecOutsiderTG, http.MethodPost, "/v1/organizations/"+org+"/sessions/"+e.seed.session.String()+"/invitations", map[string]any{
		"request_id": uuid.NewString(), "ga": []map[string]any{{"tier_id": e.seed.vip.String(), "qty": 1}},
		"guest": map[string]any{"name": "x"}, "delivery": "pdf_only",
	}); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("foreign issue: %d, want 403 or 404", code)
	}
	if e.staffInvs()[guest].ticketStatus != "active" || e.staffOrders() != 1 {
		t.Error("the outsider changed our invitations")
	}

	// A press from a group chat (the chat is not the person's own) is refused
	// before anything is read or shown.
	m := e.tg.mark()
	e.tg.push(fmt.Sprintf(`"callback_query":{"id":"cb-grp","from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"chat_instance":"x","data":"iv:l","message":{"message_id":5,"date":%d,"chat":{"id":-1001234,"type":"supergroup"},"text":"menu"}}`, ecOwnerTG, e2ePressDate()))
	e.tg.waitSince(t, m, "только в личном чате")
}

// The API on its own: the checks on the request, the guest's name on the
// ticket and in the presentation, the batch id that makes a retry safe, the
// paged and filtered list.
func TestComplimentaryAPI_EC12_ValidationNamesReplayAndList(t *testing.T) {
	e := newInviteE2E(t)
	org := e.seed.orgID.String()
	issue := func(body map[string]any) (int, []byte) {
		return e.call(ecOwnerTG, http.MethodPost, "/v1/organizations/"+org+"/complimentary", body)
	}
	base := func(batch string, extra map[string]any) map[string]any {
		m := map[string]any{"session_id": e.seed.session, "tier_id": e.seed.vip, "qty": 1, "batch_id": batch + e.seed.suffix}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	errCode := func(body []byte) string {
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &env)
		return env.Error.Code
	}

	for name, c := range map[string]struct {
		body map[string]any
		code string
	}{
		"more than 50":         {base("v1-", map[string]any{"qty": 51}), "complimentary.qty_too_large"},
		"not an address":       {base("v2-", map[string]any{"recipients": []string{"nobody"}}), "complimentary.invalid_recipient"},
		"more guests than qty": {base("v3-", map[string]any{"recipients": []string{"a@example.test", "b@example.test"}}), "complimentary.too_many_recipients"},
		"name too long":        {base("v4-", map[string]any{"recipients": []string{"a@example.test"}, "recipient_names": []string{strings.Repeat("n", 201)}}), "complimentary.invalid_recipient_name"},
		"tier missing":         {map[string]any{"session_id": e.seed.session, "qty": 1, "batch_id": "v5-" + e.seed.suffix}, "tier.required"},
	} {
		if code, body := issue(c.body); code != http.StatusBadRequest || errCode(body) != c.code {
			t.Errorf("%s: %d %s, want 400 %s", name, code, body, c.code)
		}
	}
	if e.issuances() != 0 {
		t.Fatalf("a refused request created %d issuances", e.issuances())
	}

	// The name lands on the ticket and wins in the presentation the PDF prints.
	guest := e.email("named")
	code, body := issue(base("ok-", map[string]any{"recipients": []string{guest}, "recipient_names": []string{"  Greta Named  "}}))
	if code != http.StatusCreated {
		t.Fatalf("issue: %d %s", code, body)
	}
	var created struct {
		Issuance struct{ ID uuid.UUID } `json:"issuance"`
		Tickets  []struct {
			ID             uuid.UUID `json:"id"`
			HolderName     *string   `json:"holder_name"`
			SystemTicketID int64     `json:"system_ticket_id"`
		} `json:"tickets"`
		Replay bool `json:"idempotent_replay"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Tickets) != 1 || created.Tickets[0].HolderName == nil || *created.Tickets[0].HolderName != "Greta Named" || created.Tickets[0].SystemTicketID <= 0 || created.Replay {
		t.Fatalf("created = %+v", created)
	}
	pres, err := gen.New(e.pool).GetTicketPresentationByID(context.Background(), created.Tickets[0].ID)
	if err != nil || pres.HolderName == nil || *pres.HolderName != "Greta Named" {
		t.Errorf("presentation holder name = %v (%v), want the name typed on the invitation", pres.HolderName, err)
	}

	// The same batch id again: the first result, no second ticket, no second letter.
	code, body = issue(base("ok-", map[string]any{"recipients": []string{guest}, "recipient_names": []string{"Greta Named"}}))
	if code != http.StatusOK || !strings.Contains(string(body), `"idempotent_replay":true`) {
		t.Errorf("replay: %d %s, want 200 with idempotent_replay", code, body)
	}
	if e.tickets()[guest].delivery != 1 || e.tickets()[guest].worker != 1 || e.count(`SELECT count(*) FROM tickets WHERE holder_email = $1`, guest) != 1 {
		t.Errorf("a replay created a second ticket or letter: %+v", e.tickets()[guest])
	}

	// A list of five issuances: paging, state filter and session filter.
	for i := 0; i < 4; i++ {
		if code, body := issue(base(fmt.Sprintf("l%d-", i), map[string]any{"recipients": []string{e.email(fmt.Sprintf("list%d", i))}})); code != http.StatusCreated {
			t.Fatalf("issue %d: %d %s", i, code, body)
		}
	}
	list := func(query string) (total int, ids []uuid.UUID, hasMore bool) {
		code, body := e.call(ecOwnerTG, http.MethodGet, "/v1/organizations/"+org+"/complimentary"+query, nil)
		if code != http.StatusOK {
			t.Fatalf("list%s: %d %s", query, code, body)
		}
		var r struct {
			Issuances []struct{ ID uuid.UUID } `json:"issuances"`
			Total     int                      `json:"total"`
			HasMore   bool                     `json:"has_more"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatal(err)
		}
		for _, it := range r.Issuances {
			ids = append(ids, it.ID)
		}
		return r.Total, ids, r.HasMore
	}
	total, first, more := list("?limit=2")
	total2, second, _ := list("?limit=2&offset=2")
	total3, last, more3 := list("?limit=2&offset=4")
	if total != 5 || total2 != 5 || total3 != 5 || !more || more3 || len(first) != 2 || len(second) != 2 || len(last) != 1 {
		t.Fatalf("paging: totals %d %d %d, pages %d %d %d, more %v %v", total, total2, total3, len(first), len(second), len(last), more, more3)
	}
	if last[0] != created.Issuance.ID {
		t.Errorf("the oldest issuance must be last: %v, want %v", last[0], created.Issuance.ID)
	}
	if code, _ := e.call(ecOwnerTG, http.MethodPost, "/v1/complimentary/"+first[0].String()+"/revoke", map[string]string{}); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	if n, _, _ := list("?state=revoked"); n != 1 {
		t.Errorf("state=revoked total = %d, want 1", n)
	}
	if n, _, _ := list("?state=valid"); n != 4 {
		t.Errorf("state=valid total = %d, want 4", n)
	}
	if n, _, _ := list("?session_id=" + uuid.NewString()); n != 0 {
		t.Errorf("another session total = %d, want 0", n)
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?offset=-1", "?state=nope", "?session_id=x"} {
		if code, _ := e.call(ecOwnerTG, http.MethodGet, "/v1/organizations/"+org+"/complimentary"+bad, nil); code != http.StatusBadRequest {
			t.Errorf("list%s: %d, want 400", bad, code)
		}
	}
	// A second revoke of the same issuance: 409 already revoked, in the envelope.
	code, body = e.call(ecOwnerTG, http.MethodPost, "/v1/complimentary/"+first[0].String()+"/revoke", map[string]string{})
	if code != http.StatusConflict || errCode(body) != "complimentary.already_revoked" {
		t.Errorf("second revoke: %d %s", code, body)
	}
}

// INV-08 (spec 46): the old route cannot name a seat, so a strictly seated
// session answers 422 complimentary.use_invitations_route and writes nothing,
// while the general-admission date of the same organization is served as ever.
func TestComplimentaryAPI_INV08_SeatedSessionUsesInvitationsRoute(t *testing.T) {
	e := newInviteE2E(t)
	org := e.seed.orgID.String()
	issue := func(session uuid.UUID, tier any, batch string) (int, []byte) {
		body := map[string]any{"session_id": session, "qty": 1, "batch_id": batch + e.seed.suffix, "recipients": []string{"inv08@example.test"}}
		if tier != nil {
			body["tier_id"] = tier
		}
		return e.call(ecOwnerTG, http.MethodPost, "/v1/organizations/"+org+"/complimentary", body)
	}
	envelope := func(body []byte) (code string, route string) {
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Route string `json:"route"`
				} `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &env)
		return env.Error.Code, env.Error.Details.Route
	}

	issuancesBefore := e.issuances()
	for _, tier := range []any{nil, e.seed.seatedTier} {
		code, body := issue(e.seed.seatedSession, tier, "inv08s-")
		wantRoute := "/v1/organizations/" + org + "/sessions/" + e.seed.seatedSession.String() + "/invitations"
		if gotCode, route := envelope(body); code != http.StatusUnprocessableEntity || gotCode != "complimentary.use_invitations_route" || route != wantRoute {
			t.Errorf("seated session, tier %v: %d %s, want 422 complimentary.use_invitations_route naming %s", tier, code, body, wantRoute)
		}
	}
	if n := e.issuances(); n != issuancesBefore {
		t.Errorf("a refused seated issuance wrote %d issuance rows", n-issuancesBefore)
	}
	var sold int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT capacity_sold FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`, e.seed.seatedSession).Scan(&sold); err != nil || sold != 0 {
		t.Errorf("seated ledger capacity_sold = %d (%v), want 0", sold, err)
	}

	// The general-admission date is unchanged.
	if code, body := issue(e.seed.session, e.seed.vip, "inv08g-"); code != http.StatusCreated {
		t.Errorf("general-admission date: %d %s, want 201", code, body)
	}
}
