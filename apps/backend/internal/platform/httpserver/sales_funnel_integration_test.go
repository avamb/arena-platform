//go:build integration

package httpserver

// sales_funnel_integration_test.go — spec 50, stage A7: the buyer sales funnel,
// through the REAL router and the real checkout and payment handlers (only
// Stripe itself is stubbed):
//
//	POST /v1/public/funnel                                         the beacon
//	POST /v1/public/feeds/{token}/checkout/start + payment webhook  the money
//	GET  /v1/organizations/{org_id}/events/{event_id}/funnel        the read
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run TestSalesFunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

const (
	funnelBrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15"
	funnelBotUA     = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
)

type funnelFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	srv      *Server // checkout, webhook and the public beacon (no sign-in)
	readSrv  *Server // the organizer's side: sign-in, API keys, the funnel read
	f        *hostedOrgFixture
	stripe   *stubStripe
	userID   uuid.UUID
	orgSlug  string
	evSlug   string
	linkCode string
}

func newFunnelFixture(t *testing.T) *funnelFixture {
	t.Helper()
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "fn")
	t.Cleanup(f.cleanup) // runs last: the funnel cleanup below is registered after and runs first
	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	readSrv, _ := productionIntegrationServer(t)
	q := gen.New(pool)
	run := strings.ToLower(uuid.NewString()[:8])

	user, err := q.InsertUser(ctx, "funnel-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	// The hosted page of the event: a slug and a channel with the page on.
	var orgSlug string
	if err := pool.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, f.orgID).Scan(&orgSlug); err != nil {
		t.Fatalf("org slug: %v", err)
	}
	evSlug := "funnel-event-" + run
	if _, err := pool.Exec(ctx, `UPDATE events SET slug = $2 WHERE id = $1`, f.eventID, evSlug); err != nil {
		t.Fatalf("event slug: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = '{"hosted_page":{"enabled":true}}'::jsonb WHERE id = $1`, f.channelID); err != nil {
		t.Fatalf("hosted page: %v", err)
	}
	// A tracked sales link of the organization (code: 7 characters of [a-z2-9]).
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // the sales-link code alphabet: no i, l, o
	raw := make([]byte, 7)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	code := make([]byte, 7)
	for i, b := range raw {
		code[i] = alphabet[int(b)%len(alphabet)]
	}
	fx := &funnelFixture{t: t, pool: pool, srv: srv, readSrv: readSrv, f: f, stripe: stripe, userID: user.ID, orgSlug: orgSlug, evSlug: evSlug, linkCode: string(code)}
	if _, err := pool.Exec(ctx, `INSERT INTO sales_links (org_id, code, target_kind, event_id, label)
		VALUES ($1, $2, 'event', $3, 'Funnel poster')`, f.orgID, fx.linkCode, f.eventID); err != nil {
		t.Fatalf("sales link: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, s := range []string{
			// the gateway fixture rows, children first
			`DELETE FROM refunds WHERE org_id = $1`,
			`DELETE FROM orders WHERE org_id = $1 AND source = 'bil24_gateway'`,
			`DELETE FROM checkout_sessions WHERE org_id = $1 AND channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1 AND settings ? 'gateway')`,
			`DELETE FROM reservations WHERE org_id = $1 AND channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1 AND settings ? 'gateway')`,
			`DELETE FROM sales_channels WHERE org_id = $1 AND settings ? 'gateway'`,
			// orders point at the link, the link points at the event
			`UPDATE orders SET sales_link_id = NULL WHERE org_id = $1`,
			`DELETE FROM sales_links WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(bg, s, f.orgID); err != nil {
				t.Logf("funnel cleanup (%.50s): %v", s, err)
			}
		}
		_, _ = pool.Exec(bg, `DELETE FROM api_keys WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, user.ID)
	})
	return fx
}

func (fx *funnelFixture) beacon(body map[string]any, ua string) (int, string) {
	fx.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/funnel", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	fx.srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (fx *funnelFixture) widgetBeacon(step string, session *uuid.UUID, utmSource, utmCampaign, ua string) int {
	fx.t.Helper()
	body := map[string]any{
		"feed_token": fx.f.feedToken, "event_id": fx.f.eventID.String(),
		"step": step, "surface": "widget", "utm_source": utmSource, "utm_campaign": utmCampaign,
	}
	if session != nil {
		body["session_id"] = session.String()
	}
	code, resp := fx.beacon(body, ua)
	if code != http.StatusNoContent {
		fx.t.Logf("beacon %s: %d %s", step, code, resp)
	}
	return code
}

func (fx *funnelFixture) funnelRows() (hits, suspect int64, rows int) {
	fx.t.Helper()
	err := fx.pool.QueryRow(context.Background(),
		`SELECT coalesce(sum(hits),0), coalesce(sum(suspect_hits),0), count(*) FROM sales_funnel_daily WHERE event_id = $1`,
		fx.f.eventID).Scan(&hits, &suspect, &rows)
	if err != nil {
		fx.t.Fatalf("funnelRows: %v", err)
	}
	return
}

func (fx *funnelFixture) key(orgID uuid.UUID, scopes ...string) string {
	fx.t.Helper()
	_, raw, err := apikeys.Issue(context.Background(), apikeys.NewStoreFromQueries(gen.New(fx.pool)), apikeys.IssueInput{
		OrgID: orgID, Name: "funnel-" + uuid.NewString()[:6], Scopes: scopes, CreatedBy: fx.userID,
	})
	if err != nil {
		fx.t.Fatalf("apikeys.Issue: %v", err)
	}
	return raw
}

func (fx *funnelFixture) read(orgID, eventID uuid.UUID, query, key string) (int, []byte) {
	fx.t.Helper()
	path := fmt.Sprintf("/v1/organizations/%s/events/%s/funnel%s", orgID, eventID, query)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	fx.readSrv.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

type funnelStepJSON struct {
	Step            string   `json:"step"`
	Count           *int64   `json:"count"`
	SuspectCount    int64    `json:"suspect_count"`
	ShareOfPrevious *float64 `json:"share_of_previous"`
}

type funnelMoneyJSON struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

type funnelJSON struct {
	SessionID *string `json:"session_id"`
	Sessions  []struct {
		ID       string  `json:"id"`
		Timezone *string `json:"timezone"`
	} `json:"sessions"`
	Steps   []funnelStepJSON  `json:"steps"`
	Revenue []funnelMoneyJSON `json:"revenue"`
	Sources []struct {
		UTMSource   string            `json:"utm_source"`
		UTMCampaign string            `json:"utm_campaign"`
		Steps       []funnelStepJSON  `json:"steps"`
		Revenue     []funnelMoneyJSON `json:"revenue"`
	} `json:"sources"`
	SalesLinks []struct {
		Code          *string           `json:"code"`
		Label         *string           `json:"label"`
		CheckoutStart int64             `json:"checkout_start"`
		Paid          int64             `json:"paid"`
		Revenue       []funnelMoneyJSON `json:"revenue"`
	} `json:"sales_links"`
	Gateway *struct {
		Steps   []funnelStepJSON  `json:"steps"`
		Revenue []funnelMoneyJSON `json:"revenue"`
	} `json:"gateway"`
}

func decodeFunnel(t *testing.T, raw []byte) funnelJSON {
	t.Helper()
	var out funnelJSON
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode funnel: %v (body: %s)", err, raw)
	}
	return out
}

func stepCount(t *testing.T, steps []funnelStepJSON, name string) int64 {
	t.Helper()
	for _, s := range steps {
		if s.Step == name {
			if s.Count == nil {
				t.Fatalf("step %s has no count", name)
			}
			return *s.Count
		}
	}
	t.Fatalf("no step %s in %+v", name, steps)
	return 0
}

// startCheckout drives the real checkout/start with an attribution and returns
// the checkout session id.
func (fx *funnelFixture) startCheckout(email string, qty int, attribution map[string]any) uuid.UUID {
	fx.t.Helper()
	payload := map[string]any{
		"session_id": fx.f.sessionID.String(),
		"tier_id":    fx.f.tierID.String(),
		"qty":        qty,
		"buyer":      map[string]any{"email": email, "name": "Funnel Buyer", "phone": fmt.Sprintf("+3633%07d", time.Now().UnixNano()%10_000_000)},
		"return_url": hostedTicketsBaseURL + "/shows/funnel",
	}
	if attribution != nil {
		payload["attribution"] = attribution
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+fx.f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("checkout/start = %d: %s", rec.Code, rec.Body.String())
	}
	var start hostedStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		fx.t.Fatalf("decode start: %v", err)
	}
	id, err := uuid.Parse(start.CheckoutSession.ID)
	if err != nil {
		fx.t.Fatalf("checkout session id: %v", err)
	}
	return id
}

func (fx *funnelFixture) pay(csID uuid.UUID) {
	fx.t.Helper()
	intents, err := gen.New(fx.pool).ListPaymentIntentsByCheckout(fx.t.Context(), csID)
	if err != nil || len(intents) != 1 || intents[0].ProviderPaymentID == nil {
		fx.t.Fatalf("payment intents for %s: %v %v", csID, intents, err)
	}
	body := sessionCompletedEvent(*intents[0].ProviderPaymentID, "pi_funnel_"+uuid.NewString()[:8], "paid")
	if code, resp := postSignedWebhook(fx.t, fx.srv, body, fx.f.webhookSecret); code != http.StatusOK && code != http.StatusNoContent {
		fx.t.Fatalf("webhook = %d: %s", code, resp)
	}
}

func TestSalesFunnel_PaidWidgetPurchaseShowsAllSixSteps(t *testing.T) {
	fx := newFunnelFixture(t)
	sid := fx.f.sessionID

	// ── The beacon: counts, bots apart, duplicates dropped, strangers ignored ──
	if c := fx.widgetBeacon("view", nil, "ig", "spring", funnelBrowserUA); c != http.StatusNoContent {
		t.Fatalf("view = %d", c)
	}
	fx.widgetBeacon("session_open", &sid, "ig", "spring", funnelBrowserUA)
	fx.widgetBeacon("seats_chosen", &sid, "ig", "spring", funnelBrowserUA)
	// The same visitor's identical view again: dropped.
	fx.widgetBeacon("view", nil, "ig", "spring", funnelBrowserUA)
	// A bot is counted apart.
	fx.widgetBeacon("view", nil, "ig", "spring", funnelBotUA)
	// The hosted page names the event by its address.
	if code, resp := fx.beacon(map[string]any{
		"page": map[string]any{"slug": strings.ToUpper(fx.orgSlug), "event_slug": fx.evSlug},
		"step": "view", "surface": "hosted_page",
	}, funnelBrowserUA); code != http.StatusNoContent {
		t.Fatalf("page view = %d %s", code, resp)
	}
	hits, suspect, rows := fx.funnelRows()
	if hits != 4 || suspect != 1 || rows != 4 {
		t.Fatalf("after the beacons: hits=%d suspect=%d rows=%d; want 4 / 1 / 4 (the repeat was dropped, the bot shares the row but is counted apart)", hits, suspect, rows)
	}

	// Things that must not be counted, and must not tell why.
	foreign := uuid.New()
	if c := fx.widgetBeaconFor(foreign, "view", nil); c != http.StatusNoContent {
		t.Errorf("unknown event = %d; want 204", c)
	}
	stranger := uuid.New()
	if c := fx.widgetBeacon("session_open", &stranger, "", "", funnelBrowserUA); c != http.StatusNoContent {
		t.Errorf("unknown session = %d; want 204", c)
	}
	// A server step sent by a browser, an unknown surface, and a surface that
	// does not fit how the event is addressed: all answered 204, none counted.
	pageCtx := map[string]any{"slug": fx.orgSlug, "event_slug": fx.evSlug}
	for name, body := range map[string]map[string]any{
		"server step":             {"feed_token": fx.f.feedToken, "event_id": fx.f.eventID.String(), "step": "paid", "surface": "widget"},
		"unknown step":            {"feed_token": fx.f.feedToken, "event_id": fx.f.eventID.String(), "step": "nope", "surface": "widget"},
		"unknown surface":         {"feed_token": fx.f.feedToken, "event_id": fx.f.eventID.String(), "step": "view", "surface": "app"},
		"token with a page surf.": {"feed_token": fx.f.feedToken, "event_id": fx.f.eventID.String(), "step": "view", "surface": "hosted_page"},
		"token with tickets surf": {"feed_token": fx.f.feedToken, "event_id": fx.f.eventID.String(), "step": "view", "surface": "tickets_page"},
		"page with widget surf.":  {"page": pageCtx, "step": "view", "surface": "widget"},
		"both contexts":           {"feed_token": fx.f.feedToken, "event_id": fx.f.eventID.String(), "page": pageCtx, "step": "view", "surface": "widget"},
	} {
		if code, resp := fx.beacon(body, funnelBrowserUA); code != http.StatusNoContent {
			t.Errorf("%s = %d %s; want 204", name, code, resp)
		}
	}
	// Only an unreadable body is told it is wrong.
	req := httptest.NewRequest(http.MethodPost, "/v1/public/funnel", strings.NewReader("{nope"))
	req.Header.Set("Content-Type", "application/json")
	if rec := httptest.NewRecorder(); func() int { fx.srv.router.ServeHTTP(rec, req); return rec.Code }() != http.StatusBadRequest {
		t.Error("an unreadable body must be a 400")
	}
	if code, _ := fx.beacon(map[string]any{"feed_token": "no-such-token", "event_id": fx.f.eventID.String(), "step": "view", "surface": "widget"}, funnelBrowserUA); code != http.StatusNoContent {
		t.Errorf("unknown token = %d; want 204", code)
	}
	if h2, s2, r2 := fx.funnelRows(); h2 != hits || s2 != suspect || r2 != rows {
		t.Errorf("strangers changed the counters: %d/%d/%d", h2, s2, r2)
	}

	// ── The money: one paid, one left at the payment page ───────────────────
	paidCS := fx.startCheckout(fx.f.buyerMail, 2, map[string]any{
		"ref": fx.linkCode, "utm_source": "ig", "utm_campaign": "spring",
		"landed_at": time.Now().UTC().Format(time.RFC3339),
	})
	fx.pay(paidCS)
	var linked *uuid.UUID
	var attr string
	if err := fx.pool.QueryRow(t.Context(), `SELECT sales_link_id, attribution::text FROM orders WHERE checkout_session_id = $1`, paidCS).Scan(&linked, &attr); err != nil {
		t.Fatalf("order attribution: %v", err)
	}
	if linked == nil {
		t.Fatalf("the checkout did not tie the order to the sales link %s (attribution %s)", fx.linkCode, attr)
	}
	// A buyer who retries checkout/start (the first hold is taken, a second try
	// takes another) is ONE buyer in the funnel: otherwise the share of the
	// previous step could pass 100 % through retries alone.
	retryMail := "funnel-open-" + uuid.NewString()[:8] + "@arena-integration.test"
	fx.startCheckout(retryMail, 1, nil)
	fx.startCheckout(retryMail, 1, nil)
	var orderRows int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM orders WHERE event_id = $1`, fx.f.eventID).Scan(&orderRows); err != nil || orderRows != 3 {
		t.Fatalf("orders of the event = %d (%v); want 3: the paid one and two attempts of the retrying buyer", orderRows, err)
	}

	// ── The read ───────────────────────────────────────────────────────────
	key := fx.key(fx.f.orgID, "report.read")
	code, raw := fx.read(fx.f.orgID, fx.f.eventID, "", key)
	if code != http.StatusOK {
		t.Fatalf("funnel read = %d: %s", code, raw)
	}
	got := decodeFunnel(t, raw)
	want := map[string]int64{"view": 2, "session_open": 1, "seats_chosen": 1, "checkout_start": 2, "payment_open": 2, "paid": 1}
	if len(got.Steps) != 6 {
		t.Fatalf("steps = %d; want six: %s", len(got.Steps), raw)
	}
	for name, w := range want {
		if g := stepCount(t, got.Steps, name); g != w {
			t.Errorf("step %s = %d; want %d (%s)", name, g, w, raw)
		}
	}
	if got.Steps[0].SuspectCount != 1 {
		t.Errorf("view suspect_count = %d; want 1", got.Steps[0].SuspectCount)
	}
	if got.Steps[1].ShareOfPrevious == nil || *got.Steps[1].ShareOfPrevious != 0.5 {
		t.Errorf("session_open share = %v; want 0.5", got.Steps[1].ShareOfPrevious)
	}
	if len(got.Revenue) != 1 || got.Revenue[0].Currency != "EUR" || got.Revenue[0].AmountMinor <= 0 {
		t.Errorf("revenue = %+v; want the paid order in EUR", got.Revenue)
	}
	if got.Gateway != nil {
		t.Errorf("an event with no gateway site must report gateway null, got %+v", got.Gateway)
	}
	// The source breakdown: the ig / spring visit carried every step.
	var ig *struct {
		UTMSource   string            `json:"utm_source"`
		UTMCampaign string            `json:"utm_campaign"`
		Steps       []funnelStepJSON  `json:"steps"`
		Revenue     []funnelMoneyJSON `json:"revenue"`
	}
	for i := range got.Sources {
		if got.Sources[i].UTMSource == "ig" && got.Sources[i].UTMCampaign == "spring" {
			ig = &got.Sources[i]
		}
	}
	if ig == nil {
		t.Fatalf("no ig/spring source in %s", raw)
	}
	for name, w := range map[string]int64{"view": 1, "session_open": 1, "seats_chosen": 1, "checkout_start": 1, "payment_open": 1, "paid": 1} {
		if g := stepCount(t, ig.Steps, name); g != w {
			t.Errorf("ig/spring %s = %d; want %d", name, g, w)
		}
	}
	// The sales link of the paid order.
	if len(got.SalesLinks) != 1 || got.SalesLinks[0].Code == nil || *got.SalesLinks[0].Code != fx.linkCode ||
		got.SalesLinks[0].CheckoutStart != 1 || got.SalesLinks[0].Paid != 1 || len(got.SalesLinks[0].Revenue) != 1 {
		t.Errorf("sales_links = %+v", got.SalesLinks)
	}

	// The sessions come with the answer, each with the zone of its own venue.
	if len(got.Sessions) != 1 || got.Sessions[0].ID != sid.String() || got.Sessions[0].Timezone == nil || *got.Sessions[0].Timezone != "Europe/Prague" {
		t.Errorf("sessions = %+v; want the session with Europe/Prague", got.Sessions)
	}

	// ── A succeeded refund comes off the revenue, a requested one does not ───
	grossRevenue := got.Revenue[0].AmountMinor
	var piID uuid.UUID
	var orderID uuid.UUID
	if err := fx.pool.QueryRow(t.Context(), `SELECT pi.id, o.id FROM payment_intents pi JOIN orders o ON o.checkout_session_id = pi.checkout_session_id WHERE pi.checkout_session_id = $1`, paidCS).Scan(&piID, &orderID); err != nil {
		t.Fatalf("payment intent of the paid order: %v", err)
	}
	for _, r := range []struct {
		state  string
		amount int64
	}{{"requested", 700}, {"succeeded", 500}} {
		if _, err := fx.pool.Exec(t.Context(), `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, state) VALUES ($1, $2, $3, $4, 'EUR', $5)`,
			piID, fx.f.orgID, orderID, r.amount, r.state); err != nil {
			t.Fatalf("refund %s: %v", r.state, err)
		}
	}
	code, raw = fx.read(fx.f.orgID, fx.f.eventID, "", key)
	if code != http.StatusOK {
		t.Fatalf("funnel read after the refund = %d: %s", code, raw)
	}
	net := decodeFunnel(t, raw)
	if len(net.Revenue) != 1 || net.Revenue[0].AmountMinor != grossRevenue-500 {
		t.Errorf("revenue after a 500 refund = %+v; want %d (the requested refund does not count)", net.Revenue, grossRevenue-500)
	}
	if len(net.SalesLinks) != 1 || len(net.SalesLinks[0].Revenue) != 1 || net.SalesLinks[0].Revenue[0].AmountMinor != grossRevenue-500 {
		t.Errorf("sales link revenue = %+v; want it net as well", net.SalesLinks)
	}

	// ── One session: the view belongs to the event, so it has no count ───────
	code, raw = fx.read(fx.f.orgID, fx.f.eventID, "?session_id="+sid.String(), key)
	if code != http.StatusOK {
		t.Fatalf("session read = %d: %s", code, raw)
	}
	one := decodeFunnel(t, raw)
	if one.Steps[0].Count != nil || one.SessionID == nil {
		t.Errorf("session-limited view = %+v session=%v; want no count", one.Steps[0], one.SessionID)
	}
	if stepCount(t, one.Steps, "session_open") != 1 || stepCount(t, one.Steps, "paid") != 1 {
		t.Errorf("session-limited steps = %s", raw)
	}
	// Another session of nowhere has nothing.
	code, raw = fx.read(fx.f.orgID, fx.f.eventID, "?session_id="+uuid.NewString(), key)
	if code != http.StatusOK || stepCount(t, decodeFunnel(t, raw).Steps, "checkout_start") != 0 {
		t.Errorf("an unrelated session = %d %s; want an empty funnel", code, raw)
	}
	// A bad window is a 400.
	if code, _ = fx.read(fx.f.orgID, fx.f.eventID, "?from=2026-02-30", key); code != http.StatusBadRequest {
		t.Errorf("bad date = %d; want 400", code)
	}
	// Another window, in the past, is empty.
	code, raw = fx.read(fx.f.orgID, fx.f.eventID, "?from=2020-01-01&to=2020-01-31", key)
	if code != http.StatusOK || stepCount(t, decodeFunnel(t, raw).Steps, "view") != 0 {
		t.Errorf("window in the past = %d %s", code, raw)
	}
}

// widgetBeaconFor sends a widget view for an arbitrary event id with the
// fixture's valid token.
func (fx *funnelFixture) widgetBeaconFor(event uuid.UUID, step string, session *uuid.UUID) int {
	fx.t.Helper()
	body := map[string]any{"feed_token": fx.f.feedToken, "event_id": event.String(), "step": step, "surface": "widget"}
	if session != nil {
		body["session_id"] = session.String()
	}
	code, _ := fx.beacon(body, funnelBrowserUA)
	return code
}

func TestSalesFunnel_GatewayChannelStartsAtSeatsChosen(t *testing.T) {
	fx := newFunnelFixture(t)
	ctx := t.Context()
	f := fx.f

	// A WordPress site on the gateway protocol: a channel with a credential,
	// two carts and one paid order. No beacon, no widget order.
	gwChannel, gwRes1, gwRes2, gwCS, gwOrder := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sales_channels (id, org_id, name, provider, settings) VALUES ($1, $2, 'Funnel WP site', 'stripe',
			'{"gateway":{"enabled":true,"token_hash":"x"}}'::jsonb)`, []any{gwChannel, f.orgID}},
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
			VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour')`, []any{gwRes1, f.orgID, gwChannel, f.sessionID}},
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
			VALUES ($1, $2, $3, $4, 1, 'expired', now() + interval '1 hour')`, []any{gwRes2, f.orgID, gwChannel, f.sessionID}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
			[]any{gwCS, f.orgID, gwChannel, gwRes1}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id, source, status,
			currency, subtotal, discount, charge, total, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', 'paid', 'EUR', 1000, 0, 0, 1000, now())`,
			[]any{gwOrder, f.orgID, gwChannel, f.eventID, f.sessionID, gwCS, gwRes1}},
	}
	for i, s := range steps {
		if _, err := fx.pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("gateway fixture step %d: %v", i, err)
		}
	}

	key := fx.key(f.orgID, "report.read")
	code, raw := fx.read(f.orgID, f.eventID, "", key)
	if code != http.StatusOK {
		t.Fatalf("funnel read = %d: %s", code, raw)
	}
	got := decodeFunnel(t, raw)
	if got.Gateway == nil {
		t.Fatalf("gateway block missing: %s", raw)
	}
	if len(got.Gateway.Steps) != 3 || got.Gateway.Steps[0].Step != "seats_chosen" {
		t.Fatalf("the gateway funnel must start at seats_chosen, got %+v", got.Gateway.Steps)
	}
	if stepCount(t, got.Gateway.Steps, "seats_chosen") != 2 || stepCount(t, got.Gateway.Steps, "checkout_start") != 1 || stepCount(t, got.Gateway.Steps, "paid") != 1 {
		t.Errorf("gateway steps = %s", raw)
	}
	if len(got.Gateway.Revenue) != 1 || got.Gateway.Revenue[0].AmountMinor != 1000 {
		t.Errorf("gateway revenue = %+v", got.Gateway.Revenue)
	}
	// The site's order is NOT in the main funnel, and the main funnel shows no
	// invented zeros-as-failure: it is simply empty.
	if stepCount(t, got.Steps, "checkout_start") != 0 || stepCount(t, got.Steps, "paid") != 0 || len(got.Revenue) != 0 {
		t.Errorf("a gateway order leaked into the main funnel: %s", raw)
	}
}

func TestSalesFunnel_AnotherOrganizationGets404(t *testing.T) {
	fx := newFunnelFixture(t)
	ctx := t.Context()
	other := newHostedOrgFixture(t, ctx, fx.pool, "fo")
	t.Cleanup(other.cleanup)
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM api_keys WHERE org_id = $1`, other.orgID)
	})

	// Organization B asks, in its own path, for organization A's event.
	keyB := fx.key(other.orgID, "report.read")
	if code, raw := fx.read(other.orgID, fx.f.eventID, "", keyB); code != http.StatusNotFound || !strings.Contains(string(raw), "event.not_found") {
		t.Errorf("foreign event in own org path = %d %s; want 404 event.not_found", code, raw)
	}
	// ... and in organization A's path: the key is not a member there.
	if code, raw := fx.read(fx.f.orgID, fx.f.eventID, "", keyB); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("foreign org path = %d %s; want 403 or 404", code, raw)
	}
	// A key without report.read is refused.
	keyNo := fx.key(fx.f.orgID, "event.read")
	if code, _ := fx.read(fx.f.orgID, fx.f.eventID, "", keyNo); code != http.StatusForbidden {
		t.Errorf("a key without report.read = %d; want 403", code)
	}
	// The owner of the event reads it.
	keyA := fx.key(fx.f.orgID, "report.read")
	if code, raw := fx.read(fx.f.orgID, fx.f.eventID, "", keyA); code != http.StatusOK {
		t.Errorf("own event = %d %s", code, raw)
	}
	// No credentials at all.
	if code, _ := fx.read(fx.f.orgID, fx.f.eventID, "", ""); code != http.StatusUnauthorized {
		t.Errorf("no key = %d; want 401", code)
	}
}

func TestSalesFunnel_UTMPairCapAndSessionOfAnotherEvent(t *testing.T) {
	fx := newFunnelFixture(t)
	ctx := t.Context()
	// The day of a hit is the day in the venue's zone (Europe/Prague).
	for i := 0; i < 200; i++ {
		if _, err := fx.pool.Exec(ctx, `INSERT INTO sales_funnel_daily (org_id, event_id, session_id, day, step, surface, utm_source, utm_campaign, hits)
			VALUES ($1, $2, NULL, (now() AT TIME ZONE 'Europe/Prague')::date, 'view', 'widget', $3, 'c', 1)`, fx.f.orgID, fx.f.eventID, fmt.Sprintf("s%d", i)); err != nil {
			t.Fatalf("seed pair %d: %v", i, err)
		}
	}
	// Two hundred pairs are there: a new one is counted without utm.
	fx.widgetBeacon("view", nil, "brand-new-source", "x", funnelBrowserUA)
	var named, folded int
	if err := fx.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE utm_source = 'brand-new-source'),
		count(*) FILTER (WHERE utm_source = '' AND utm_campaign = '' AND hits > 0)
		FROM sales_funnel_daily WHERE event_id = $1`, fx.f.eventID).Scan(&named, &folded); err != nil {
		t.Fatal(err)
	}
	if named != 0 || folded != 1 {
		t.Errorf("rows for the new pair = %d, folded into the empty pair = %d; want 0 and 1", named, folded)
	}
	// A session of ANOTHER event is not counted.
	other := newHostedOrgFixture(t, ctx, fx.pool, "fo2")
	t.Cleanup(other.cleanup)
	osid := other.sessionID
	h0, _, r0 := fx.funnelRows()
	fx.widgetBeacon("session_open", &osid, "", "", funnelBrowserUA)
	if h1, _, r1 := fx.funnelRows(); h1 != h0 || r1 != r0 {
		t.Errorf("a session of another event changed the counters: hits %d->%d rows %d->%d", h0, h1, r0, r1)
	}
}
