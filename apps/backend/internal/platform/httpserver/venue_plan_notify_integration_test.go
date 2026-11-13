//go:build integration

package httpserver

// venue_plan_notify_integration_test.go — spec 08_architecture/64 section 3.2
// and 4.4: every platform administrator hears personally (e-mail, and Telegram
// for a live bot link) of four events — a venue was created, an intake came,
// an intake went to an administrator, an organizer approved a plan — and the
// bot claims what the organizer must be told. Each event is driven through the
// real routes; the venue_plan.notify job is then run by its handler on exactly
// the payload this test's request queued.

import (
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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/notify"
)

const vpnAppURL = "https://admin.example.test"

type vpnEnv struct {
	*vpiEnv
	orgName string

	organizer      uuid.UUID
	organizerToken string
	organizerEmail string
	organizerChat  int64

	liveEmail, revokedEmail, superEmail string
	liveChat, revokedChat               int64
	staff                               []uuid.UUID
}

const (
	vpnFirstName = "Zinaida"
	vpnLastName  = "Quixote"
)

func newVPNEnv(t *testing.T) *vpnEnv {
	t.Helper()
	e := &vpnEnv{vpiEnv: newVPIEnv(t)}
	e.srv.SetVenuePlanNotifier(notify.Notifier{})
	ctx := context.Background()
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, e.orgA).Scan(&e.orgName); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UnixNano()%1_000_000_000 + 6_000_000_000
	e.liveChat, e.revokedChat, e.organizerChat = base, base+1, base+2

	link := func(user uuid.UUID, chat int64, revoked bool) {
		rev := "NULL"
		if revoked {
			rev = "now()"
		}
		e.exec(t, `INSERT INTO bot_telegram_links (telegram_user_id, user_id, revoked_at) VALUES ($1, $2, `+rev+`)`, chat, user)
	}
	staff := func(label string) (uuid.UUID, string) {
		id := e.user(t, label)
		e.exec(t, `INSERT INTO user_roles (user_id, role_id, org_id)
		           SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, id)
		var email string
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, id).Scan(&email); err != nil {
			t.Fatal(err)
		}
		e.staff = append(e.staff, id)
		return id, email
	}
	var live, revoked uuid.UUID
	live, e.liveEmail = staff("notify-live")
	link(live, e.liveChat, false)
	revoked, e.revokedEmail = staff("notify-revoked")
	link(revoked, e.revokedChat, true)
	e.staff = append(e.staff, e.superID)
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, e.superID).Scan(&e.superEmail); err != nil {
		t.Fatal(err)
	}

	// The organizer has a name, an e-mail and a live bot link: none of it may
	// reach an administrator's message.
	e.organizer, e.organizerToken = e.member(t, e.orgA, "org_admin")
	e.exec(t, `UPDATE users SET first_name = $2, last_name = $3 WHERE id = $1`, e.organizer, vpnFirstName, vpnLastName)
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, e.organizer).Scan(&e.organizerEmail); err != nil {
		t.Fatal(err)
	}
	link(e.organizer, e.organizerChat, false)

	t.Cleanup(func() {
		c := context.Background()
		pool := e.srv.pgxPool
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND (
			payload->>'intake_id' IN (SELECT id::text FROM venue_plan_intakes WHERE org_id = ANY($1))
			OR payload->>'venue_id' IN (SELECT id::text FROM venues WHERE org_id = ANY($1)))`, []uuid.UUID{e.orgA, e.orgB})
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type IN ('admin_notify.email', 'admin_notify.telegram') AND payload::text LIKE '%' || $1 || '%'`, e.orgName)
		_, _ = pool.Exec(c, `DELETE FROM venue_plan_notify_log WHERE subject_id IN (
			SELECT id FROM venue_plan_intakes WHERE org_id = ANY($1) UNION SELECT id FROM venues WHERE org_id = ANY($1))`, []uuid.UUID{e.orgA, e.orgB})
		_, _ = pool.Exec(c, `DELETE FROM bot_telegram_links WHERE telegram_user_id = ANY($1)`, []int64{e.liveChat, e.revokedChat, e.organizerChat})
		for _, id := range e.staff {
			if id != e.superID {
				_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = $1`, id)
				_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1`, id)
			}
		}
	})
	return e
}

func (e *vpnEnv) handler() func(context.Context, []byte) error {
	return notify.NewHandler(notify.Options{Pool: e.srv.pgxPool, AppURL: vpnAppURL, Telegram: true})
}

// job returns the ONE venue_plan.notify payload of this kind about this subject.
func (e *vpnEnv) job(t *testing.T, kind, key, id string) []byte {
	t.Helper()
	rows, err := e.srv.pgxPool.Query(context.Background(),
		`SELECT payload::text FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'kind' = $1 AND payload->>$2 = $3`, kind, key, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, []byte(p))
	}
	if len(out) != 1 {
		t.Fatalf("venue_plan.notify jobs of %s about %s = %d, want 1", kind, id, len(out))
	}
	return out[0]
}

func (e *vpnEnv) noJob(t *testing.T, kind, key, id string) {
	t.Helper()
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'kind' = $1 AND payload->>$2 = $3`, kind, key, id); n != 0 {
		t.Fatalf("%d venue_plan.notify jobs of %s about %s, want none", n, kind, id)
	}
}

// emailJobs and telegramJobs count what the fan-out queued for this
// organization's message with this title.
func (e *vpnEnv) emailJobs(t *testing.T, addr, title string) int {
	return e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'admin_notify.email'
		AND payload->>'email' = $1 AND payload->>'title' = $2 AND payload::text LIKE '%' || $3 || '%'`, addr, title, e.orgName)
}

func (e *vpnEnv) telegramJobs(t *testing.T, chat int64, title string) int {
	return e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'admin_notify.telegram'
		AND (payload->>'chat_id')::bigint = $1 AND payload->>'text' LIKE '%' || $2 || '%' AND payload->>'text' LIKE '%' || $3 || '%'`, chat, title, e.orgName)
}

// run executes the job, checks the fan-out, runs it again and checks that
// nothing was added. It returns the e-mail payload of the live administrator.
func (e *vpnEnv) run(t *testing.T, payload []byte, title string, wantIn ...string) (details string, telegram string) {
	t.Helper()
	ctx := context.Background()
	if err := e.handler()(ctx, payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	check := func(label string) {
		for _, addr := range []string{e.superEmail, e.liveEmail, e.revokedEmail} {
			if n := e.emailJobs(t, addr, title); n != 1 {
				t.Errorf("%s: e-mail jobs for %s = %d, want 1", label, addr, n)
			}
		}
		if n := e.telegramJobs(t, e.liveChat, title); n != 1 {
			t.Errorf("%s: Telegram jobs for the live chat = %d, want 1", label, n)
		}
		if n := e.telegramJobs(t, e.revokedChat, title); n != 0 {
			t.Errorf("%s: Telegram jobs for the revoked link = %d, want 0", label, n)
		}
		// Nothing for the organization's own people.
		if n := e.emailJobs(t, e.organizerEmail, title); n != 0 {
			t.Errorf("%s: the organizer got %d e-mail jobs", label, n)
		}
		if n := e.telegramJobs(t, e.organizerChat, title); n != 0 {
			t.Errorf("%s: the organizer's chat got %d Telegram jobs", label, n)
		}
	}
	check("first run")
	if err := e.handler()(ctx, payload); err != nil {
		t.Fatalf("handler, second run: %v", err)
	}
	check("repeated run")

	var raw string
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT payload::text FROM worker_jobs WHERE job_type = 'admin_notify.email'
		AND payload->>'email' = $1 AND payload->>'title' = $2 AND payload::text LIKE '%' || $3 || '%'`, e.liveEmail, title, e.orgName).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var email struct {
		Title   string   `json:"title"`
		Details []string `json:"details"`
		Link    string   `json:"link"`
	}
	if err := json.Unmarshal([]byte(raw), &email); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT payload->>'text' FROM worker_jobs WHERE job_type = 'admin_notify.telegram'
		AND (payload->>'chat_id')::bigint = $1 AND payload->>'text' LIKE '%' || $2 || '%'`, e.liveChat, e.orgName).Scan(&telegram); err != nil {
		t.Fatal(err)
	}
	details = strings.Join(email.Details, "\n") + "\n" + email.Link
	for _, want := range append([]string{"Организация: " + e.orgName, "Площадка: "}, wantIn...) {
		if !strings.Contains(details, want) {
			t.Errorf("e-mail lacks %q:\n%s", want, details)
		}
		if strings.HasPrefix(want, "Организация") && !strings.Contains(telegram, want) {
			t.Errorf("Telegram text lacks %q:\n%s", want, telegram)
		}
	}
	for _, secret := range []string{e.organizerEmail, vpnFirstName, vpnLastName} {
		if strings.Contains(raw, secret) || strings.Contains(telegram, secret) {
			t.Errorf("the message carries %q of the organizer", secret)
		}
	}
	return details, telegram
}

// (a) venue_created through the venue route.
func TestVenuePlanNotify_VenueCreated(t *testing.T) {
	e := newVPNEnv(t)
	name := "VPN New Hall " + uuid.NewString()[:8]
	body := fmt.Sprintf(`{"name":%q,"timezone":"Europe/Madrid"}`, name)
	r := e.jsonDo(t, http.MethodPost, "/v1/organizations/"+e.orgA.String()+"/venues", e.organizerToken, "", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create venue: %d %s", r.status, r.body)
	}
	venueID, _ := r.json(t)["venue"].(map[string]any)["id"].(string)
	payload := e.job(t, "venue_created", "venue_id", venueID)

	details, telegram := e.run(t, payload, "Организация добавила площадку", "Площадка: "+name)
	if !strings.Contains(details, vpnAppURL+"/venues") || !strings.Contains(telegram, vpnAppURL+"/venues") {
		t.Errorf("no link to the venues page:\n%s\n%s", details, telegram)
	}

	// A refused create (the same name again) queues nothing more.
	if r := e.jsonDo(t, http.MethodPost, "/v1/organizations/"+e.orgA.String()+"/venues", e.organizerToken, "", body); r.status != http.StatusConflict {
		t.Fatalf("duplicate venue: %d %s", r.status, r.body)
	}
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'kind' = 'venue_created'
		AND payload->>'venue_id' IN (SELECT id::text FROM venues WHERE org_id = $1)`, e.orgA); n != 1 {
		t.Errorf("venue_created jobs of the organization = %d, want 1", n)
	}
}

// (b) A PDF waits for the generator job: the create tells nobody. A worker
// without a generator sends it to an administrator (no_generator), and only
// then the administrators hear of it, once.
func TestVenuePlanNotify_IntakeWithoutGeneratorEscalates(t *testing.T) {
	e := newVPNEnv(t)
	res := e.create(t, e.orgA, e.venueA, e.organizerToken, nil, vpiFile{"plan.pdf", vpiPDF()})
	if res.status != http.StatusAccepted {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	id := res.intake(t)["id"].(string)
	e.noJob(t, "intake_created", "intake_id", id)
	e.noJob(t, "intake_escalated", "intake_id", id)
	e.toOperator(t, id)
	e.noJob(t, "intake_created", "intake_id", id)
	payload := e.job(t, "intake_escalated", "intake_id", id)

	e.run(t, payload, "Заявка на схему зала ждёт администратора",
		"Площадка: VPI Hall A", "Файлов: 1 (pdf)", "генератор схем не подключён", vpnAppURL+"/venue-plans/"+id)
}

// (b2) intake_created: a clean SVG is draft 1 at once; the message says the
// organizer checks it.
func TestVenuePlanNotify_IntakeCreated(t *testing.T) {
	e := newVPNEnv(t)
	res := e.create(t, e.orgA, e.venueA, e.organizerToken, nil, vpiFile{"plan.svg", vpiPlanSVG(t)})
	if res.status != http.StatusAccepted {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	id := res.intake(t)["id"].(string)
	e.noJob(t, "intake_escalated", "intake_id", id)
	payload := e.job(t, "intake_created", "intake_id", id)
	e.run(t, payload, "Новая заявка на схему зала",
		"Площадка: VPI Hall A", "Файлов: 1 (svg)", "Организатор проверяет черновик сам", vpnAppURL+"/venue-plans/"+id)
}

// (c) intake_escalated: the organizer calls an administrator, with a text.
func TestVenuePlanNotify_IntakeEscalated(t *testing.T) {
	e := newVPNEnv(t)
	res := e.create(t, e.orgA, e.venueA, e.organizerToken, nil, vpiFile{"plan.svg", vpiPlanSVG(t)})
	if res.status != http.StatusAccepted {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	id := res.intake(t)["id"].(string)
	e.noJob(t, "intake_escalated", "intake_id", id)

	r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/escalate", e.organizerToken, "", `{"text":"Балкон нарисован неверно, помогите"}`)
	if r.status != http.StatusOK {
		t.Fatalf("escalate: %d %s", r.status, r.body)
	}
	payload := e.job(t, "intake_escalated", "intake_id", id)
	var p notify.Payload
	if err := json.Unmarshal(payload, &p); err != nil || p.Reason != "organizer_request" || p.Nonce == "" || p.VenueID != e.venueA.String() {
		t.Fatalf("payload %s: %v", payload, err)
	}
	e.run(t, payload, "Заявка на схему зала ждёт администратора",
		"Файлов: 1 (svg)", "организатор позвал администратора", "Текст организатора: Балкон нарисован неверно, помогите", vpnAppURL+"/venue-plans/"+id)

	// A second job for the same intake is a new message: the nonce differs.
	var again notify.Payload
	_ = json.Unmarshal(payload, &again)
	if err := notify.Enqueue(context.Background(), e.srv.pgxPool, notify.Payload{Kind: notify.KindIntakeEscalated, IntakeID: id, VenueID: e.venueA.String(), Reason: "organizer_request"}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'kind' = 'intake_escalated' AND payload->>'intake_id' = $1 AND payload->>'nonce' <> $2`, id, again.Nonce); n != 1 {
		t.Fatalf("a new job has no new nonce: %d", n)
	}
}

// (d) plan_approved: the organizer approves the draft; the plan waits for
// verification.
func TestVenuePlanNotify_PlanApproved(t *testing.T) {
	e := newVPNEnv(t)
	res := e.create(t, e.orgA, e.venueA, e.organizerToken, nil, vpiFile{"plan.svg", vpiPlanSVG(t)})
	if res.status != http.StatusAccepted {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	id := res.intake(t)["id"].(string)
	e.noJob(t, "plan_approved", "intake_id", id)
	if r := e.jsonDo(t, http.MethodPost, orgIntakePath(e.orgA, id)+"/approve", e.organizerToken, "", `{"draft":1}`); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	payload := e.job(t, "plan_approved", "intake_id", id)
	e.run(t, payload, "Схема зала утверждена и ждёт верификации",
		"Мест на схеме: 90", "Статус: ждёт верификации", vpnAppURL+"/venue-plans/"+id)
}

// (e) The bot claims what the organizer must be told: a notice comes once, with
// the creator's live chats; a creator without a bot link still gets the notice
// claimed, with an empty list.
func TestVenuePlanNotify_BotClaimsOrganizerNotices(t *testing.T) {
	e := newVPNEnv(t)
	const token = "vpn-bot-service-token"
	// The token is read when the routes are mounted, so the bot's calls go to a
	// second server of the same database that was built with it.
	botServer, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.BotServiceToken = token })
	botSrv := httptest.NewServer(botServer.Router())
	t.Cleanup(botSrv.Close)
	bot := botSrv.URL

	// Two intakes in operator state, one by the organizer with a live link and
	// one by a member without any link. An administrator returns both.
	_, noLinkToken := e.member(t, e.orgA, "org_admin")
	ids := map[string]string{}
	for label, tok := range map[string]string{"linked": e.organizerToken, "unlinked": noLinkToken} {
		res := e.create(t, e.orgA, e.venueA, tok, nil, vpiFile{"plan.pdf", vpiPDF()})
		if res.status != http.StatusAccepted {
			t.Fatalf("create %s: %d %s", label, res.status, res.body)
		}
		id := res.intake(t)["id"].(string)
		ids[label] = id
		e.toOperator(t, id)
		adm := "/v1/admin/plan-intakes/" + id
		if r := e.jsonDo(t, http.MethodPost, adm+"/claim", e.super, "taking it", ""); r.status != http.StatusOK {
			t.Fatalf("claim: %d %s", r.status, r.body)
		}
		draft, _ := json.Marshal(map[string]any{"svg": string(vpiPlanSVG(t)), "note": "drawn"})
		if r := e.jsonDo(t, http.MethodPost, adm+"/drafts", e.super, "drawn", string(draft)); r.status != http.StatusCreated {
			t.Fatalf("draft: %d %s", r.status, r.body)
		}
		if r := e.jsonDo(t, http.MethodPost, adm+"/return", e.super, "ready", `{"note":"Please check the balcony"}`); r.status != http.StatusOK {
			t.Fatalf("return: %d %s", r.status, r.body)
		}
	}

	if r := e.botDo(t, bot, "wrong-token"); r.status != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d %s", r.status, r.body)
	}
	if r := e.botDo(t, bot, ""); r.status != http.StatusUnauthorized {
		t.Fatalf("no token: %d %s", r.status, r.body)
	}

	r := e.botDo(t, bot, token)
	if r.status != http.StatusOK {
		t.Fatalf("claim: %d %s", r.status, r.body)
	}
	mine := map[string]map[string]any{}
	escalated := 0
	for _, raw := range r.json(t)["notices"].([]any) {
		n := raw.(map[string]any)
		for label, id := range ids {
			if n["intake_id"] != id {
				continue
			}
			// The worker without a generator left an escalated notice first.
			if n["kind"] == "escalated" {
				escalated++
				continue
			}
			mine[label] = n
		}
	}
	if escalated != 2 {
		t.Errorf("escalated notices claimed: %d, want 2", escalated)
	}
	if len(mine) != 2 {
		t.Fatalf("claimed notices of the test: %v in %s", mine, r.body)
	}
	for label, n := range mine {
		if n["kind"] != "returned" || n["note"] != "Please check the balcony" || n["org_id"] != e.orgA.String() ||
			n["venue_id"] != e.venueA.String() || n["venue_name"] != "VPI Hall A" || n["current_draft"] != float64(1) ||
			n["id"] == "" || n["created_at"] == "" {
			t.Errorf("%s notice: %v", label, n)
		}
	}
	linked, _ := mine["linked"]["telegram_user_ids"].([]any)
	if len(linked) != 1 || linked[0] != float64(e.organizerChat) {
		t.Errorf("linked chats = %v, want [%d]", linked, e.organizerChat)
	}
	unlinked, ok := mine["unlinked"]["telegram_user_ids"].([]any)
	if !ok || len(unlinked) != 0 {
		t.Errorf("unlinked chats = %v, want an empty list", mine["unlinked"]["telegram_user_ids"])
	}

	// Each notice comes once.
	again := e.botDo(t, bot, token)
	if again.status != http.StatusOK {
		t.Fatalf("second claim: %d %s", again.status, again.body)
	}
	for _, raw := range again.json(t)["notices"].([]any) {
		n := raw.(map[string]any)
		for _, id := range ids {
			if n["intake_id"] == id {
				t.Errorf("a notice came twice: %v", n)
			}
		}
	}
	if n := e.count(t, `SELECT count(*) FROM venue_plan_notices WHERE intake_id = ANY($1) AND claimed_at IS NOT NULL`, []uuid.UUID{uuid.MustParse(ids["linked"]), uuid.MustParse(ids["unlinked"])}); n != 4 {
		t.Errorf("claimed notices in the table = %d, want 4 (escalated and returned of each)", n)
	}
}

func (e *vpnEnv) botDo(t *testing.T, base, token string) vpiResp {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/v1/bot/venue-plan-notices/claim", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return vpiResp{status: resp.StatusCode, body: raw, header: resp.Header}
}
