//go:build integration

// metainsights_routes_integration_test.go - the Meta ads report routes (spec
// 66, stage M2) through the real router: report.read per organization, the
// membership check of {org_id}, an API key with report.read inside its own
// organization, the event tied to the path organization, the refresh
// throttle and the exact numbers of the linking rules.
//
//	DATABASE_URL=... go.exe test -tags integration -count=1 -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run TestMetaAdsRoutes_
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// metaAdsSeed writes the rows of one organization and removes them after
// the test (before the env's own cleanup of the organizations).
type metaAdsSeed struct {
	t   *testing.T
	e   *sec1Env
	org uuid.UUID
}

func newMetaAdsSeed(t *testing.T, e *sec1Env, org uuid.UUID) *metaAdsSeed {
	t.Helper()
	s := &metaAdsSeed{t: t, e: e, org: org}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM worker_jobs WHERE payload ->> 'org_id' = $1::text`,
			`DELETE FROM refunds WHERE org_id = $1`,
			`DELETE FROM payment_intents WHERE org_id = $1`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE venue_id IN (SELECT id FROM venues WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM ad_spend_daily WHERE org_id = $1`,
			`DELETE FROM ad_campaigns WHERE org_id = $1`,
			`DELETE FROM meta_insights_state WHERE org_id = $1`,
			`DELETE FROM meta_graph_calls WHERE org_id = $1`,
			`DELETE FROM org_meta_assets WHERE org_id = $1`,
			`DELETE FROM org_meta_connections WHERE org_id = $1`,
		} {
			if _, err := e.srv.pgxPool.Exec(context.Background(), sql, org); err != nil {
				t.Logf("cleanup %q: %v", sql, err)
			}
		}
	})
	return s
}

func (s *metaAdsSeed) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.e.srv.pgxPool.Exec(context.Background(), sql, args...); err != nil {
		s.t.Fatalf("seed %q: %v", sql, err)
	}
}

// connect makes an active Meta connection with a selected ad account.
func (s *metaAdsSeed) connect(account, currency string) {
	s.exec(`INSERT INTO org_meta_connections (org_id, meta_user_id, access_token_enc, status) VALUES ($1, $2, 'v1:not-a-real-sealed-token', 'active')`, s.org, "mu-"+s.org.String()[:8])
	s.exec(`INSERT INTO org_meta_assets (org_id, kind, meta_id, name, currency, timezone, selected)
	        VALUES ($1, 'ad_account', $2, 'Main account', $3, 'UTC', true)`, s.org, account, currency)
}

func (s *metaAdsSeed) spend(account, campaign, ad, day, currency string, spend, impressions int64) {
	s.exec(`INSERT INTO ad_spend_daily (org_id, ad_account_id, day, campaign_id, campaign_name, adset_id, ad_id, currency,
	                                    spend_minor, impressions, clicks, link_clicks, meta_purchases, meta_purchase_value_minor)
	        VALUES ($1, $2, $3::date, $4, $5, 'as-' || $4, $6, $7, $8, $9, 10, 8, 1, 2500)`,
		s.org, account, day, campaign, "Campaign "+campaign, ad, currency, spend, impressions)
}

type metaAdsEvent struct {
	id, session, channel, testChannel uuid.UUID
}

func (s *metaAdsSeed) event(name string) metaAdsEvent {
	ev := metaAdsEvent{id: uuid.New(), session: uuid.New(), channel: uuid.New(), testChannel: uuid.New()}
	venue := uuid.New()
	nonce := ev.id.String()[:8]
	s.exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'UTC')`, venue, s.org, "Hall "+nonce)
	s.exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, ev.id, s.org, name)
	s.exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	        VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'EUR', 'override')`, ev.session, ev.id, venue)
	s.exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider) VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, ev.channel, s.org, "Ch "+nonce)
	s.exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider, is_test) VALUES ($1, $2, $3, 'direct_merchant', 'stripe', true)`, ev.testChannel, s.org, "Test "+nonce)
	return ev
}

// checkout makes a checkout of the event created at; campaign "" = no
// utm_campaign. It returns the checkout and reservation ids.
func (s *metaAdsSeed) checkout(ev metaAdsEvent, channel uuid.UUID, campaign, at string) (uuid.UUID, uuid.UUID) {
	resv, cs := uuid.New(), uuid.New()
	attr := `{}`
	if campaign != "" {
		b, _ := json.Marshal(map[string]string{"utm_source": "facebook", "utm_campaign": campaign})
		attr = string(b)
	}
	s.exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	        VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resv, s.org, channel, ev.session)
	s.exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, attribution, created_at)
	        VALUES ($1, $2, $3, $4, 'completed', $5::jsonb, $6::timestamptz)`, cs, s.org, channel, resv, attr, at)
	return cs, resv
}

// order makes a checkout and its order.
func (s *metaAdsSeed) order(ev metaAdsEvent, channel uuid.UUID, campaign, source, currency string, total int64, at string) uuid.UUID {
	cs, resv := s.checkout(ev, channel, campaign, at)
	id := uuid.New()
	s.exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                            source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, created_at)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'paid', $9, $10, 0, 0, $10, 'B', 'b@example.test', $11::timestamptz)`,
		id, s.org, channel, ev.id, ev.session, cs, resv, source, currency, total, at)
	return id
}

// refund adds a provider refund of the order in the given state.
func (s *metaAdsSeed) refund(order uuid.UUID, amount int64, state string) {
	pi, rf := uuid.New(), uuid.New()
	s.exec(`INSERT INTO payment_intents (id, org_id, provider, amount, currency, state) VALUES ($1, $2, 'stripe', $3, 'EUR', 'succeeded')`, pi, s.org, amount)
	s.exec(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, payment_intent_id, succeeded_at)
	        VALUES ($1, $2, $3, 'EUR', $4, 'provider', $5, $6, now())`, rf, s.org, amount, state, order, pi)
}

func metaAdsJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v %s", err, body)
	}
	return m
}

func metaAdsNum(t *testing.T, m map[string]any, path string) any {
	t.Helper()
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s: %v is not an object", path, cur)
		}
		cur = obj[k]
	}
	return cur
}

func wantField(t *testing.T, m map[string]any, path string, want any) {
	t.Helper()
	got := metaAdsNum(t, m, path)
	if f, ok := got.(float64); ok {
		if w, ok := want.(int); ok && f == float64(w) {
			return
		}
	}
	if got != want {
		t.Errorf("%s = %#v, want %#v", path, got, want)
	}
}

func campaignByID(t *testing.T, m map[string]any, id string) map[string]any {
	t.Helper()
	list, _ := m["campaigns"].([]any)
	for _, c := range list {
		if cm, ok := c.(map[string]any); ok && cm["campaign_id"] == id {
			return cm
		}
	}
	t.Fatalf("campaign %s missing in %v", id, m["campaigns"])
	return nil
}

// Roles and isolation: owner and manager read, an agent and the owner of
// another organization do not, a foreign event is 404, an API key reads
// only with report.read and only its own organization.
func TestMetaAdsRoutes_IsolationAndRoles(t *testing.T) {
	e := metaRtEnv(t)
	orgA, orgB := e.org(t, "adsA"), e.org(t, "adsB")
	seedA, seedB := newMetaAdsSeed(t, e, orgA), newMetaAdsSeed(t, e, orgB)
	seedA.connect("act_A"+orgA.String()[:6], "EUR")
	evA := seedA.event("Event A")
	evB := seedB.event("Event B")

	ownerA, mgrA, agentA, ownerB := e.user(t, "ownerA"), e.user(t, "mgrA"), e.user(t, "agentA"), e.user(t, "ownerB")
	e.member(t, ownerA, orgA, "org_admin")
	e.member(t, mgrA, orgA, "organizer")
	e.member(t, agentA, orgA, "agent")
	e.member(t, ownerB, orgB, "org_admin")
	tokOwner, tokMgr, tokAgent, tokB := e.token(t, ownerA), e.token(t, mgrA), e.token(t, agentA), e.token(t, ownerB)
	keyA, keyNoReport, keyB := e.key(t, orgA, "report.read"), e.key(t, orgA, "event.read"), e.key(t, orgB, "report.read")

	baseA := "/v1/organizations/" + orgA.String()
	eventPath := baseA + "/events/" + evA.id.String() + "/ads/meta"
	routes := []struct{ method, path string }{
		{http.MethodGet, eventPath},
		{http.MethodGet, baseA + "/ads/meta/summary"},
		{http.MethodPost, baseA + "/ads/meta/refresh"},
	}
	for _, who := range []struct{ name, bearer string }{{"owner", tokOwner}, {"manager", tokMgr}, {"API key with report.read", keyA}} {
		for _, r := range routes {
			want := http.StatusOK
			if r.method == http.MethodPost {
				want = http.StatusAccepted
			}
			if st, out := e.do(t, r.method, r.path, who.bearer, "", ""); st != want {
				t.Errorf("%s %s %s: got %d %s, want %d", who.name, r.method, r.path, st, out, want)
			}
		}
	}
	for _, who := range []struct{ name, bearer string }{{"agent", tokAgent}, {"owner of B", tokB}, {"API key without report.read", keyNoReport}, {"API key of B", keyB}} {
		for _, r := range routes {
			if st, out := e.do(t, r.method, r.path, who.bearer, "", ""); st != http.StatusForbidden {
				t.Errorf("%s %s %s: got %d %s, want 403", who.name, r.method, r.path, st, out)
			}
		}
	}
	if st, _ := e.do(t, http.MethodGet, eventPath, "", "", ""); st != http.StatusUnauthorized {
		t.Errorf("no credentials: %d, want 401", st)
	}
	// Organization A's path with organization B's event, and the reverse.
	foreign := baseA + "/events/" + evB.id.String() + "/ads/meta"
	for _, bearer := range []string{tokOwner, keyA} {
		if st, out := e.do(t, http.MethodGet, foreign, bearer, "", ""); st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
			t.Errorf("foreign event: %d %s, want 404 event.not_found", st, out)
		}
	}
	if st, out := e.do(t, http.MethodGet, "/v1/organizations/"+orgB.String()+"/events/"+evA.id.String()+"/ads/meta", tokB, "", ""); st != http.StatusNotFound {
		t.Errorf("B path with A's event: %d %s, want 404", st, out)
	}
	if st, _ := e.do(t, http.MethodGet, baseA+"/events/"+uuid.NewString()+"/ads/meta", tokOwner, "", ""); st != http.StatusNotFound {
		t.Errorf("unknown event: %d, want 404", st)
	}
	// Bad periods.
	for _, q := range []string{"?from=2026-02-30", "?to=x", "?from=2026-03-02&to=2026-03-01", "?from=2025-01-01&to=2026-02-05"} {
		for _, p := range []string{eventPath, baseA + "/ads/meta/summary"} {
			if st, out := e.do(t, http.MethodGet, p+q, tokOwner, "", ""); st != http.StatusBadRequest {
				t.Errorf("GET %s%s: %d %s, want 400", p, q, st, out)
			}
		}
	}
	if st, _ := e.do(t, http.MethodGet, eventPath+"?from=2025-01-01&to=2026-02-04", tokOwner, "", ""); st != http.StatusOK {
		t.Errorf("400 days: %d, want 200", st)
	}
	// Default period: the last 30 days in the account timezone (UTC here).
	out := e.expect(t, http.StatusOK, http.MethodGet, eventPath, tokOwner, "", "")
	m := metaAdsJSON(t, out)
	today := time.Now().UTC()
	wantField(t, m, "period.to", today.Format("2006-01-02"))
	wantField(t, m, "period.from", today.AddDate(0, 0, -29).Format("2006-01-02"))
	wantField(t, m, "sync.status", "ok")
	wantField(t, m, "mer", nil)
	wantField(t, m, "url_parameters_template", "utm_source=facebook&utm_medium=paid_social&utm_campaign={{campaign.id}}&utm_term={{adset.id}}&utm_content={{ad.id}}")
	// B has no connection.
	out = e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+orgB.String()+"/ads/meta/summary", tokB, "", "")
	wantField(t, metaAdsJSON(t, out), "sync.status", "not_connected")
}

// The refresh is throttled: the second call within 10 minutes queues
// nothing and tells when the next one is accepted.
func TestMetaAdsRoutes_RefreshThrottle(t *testing.T) {
	e := metaRtEnv(t)
	org := e.org(t, "adsR")
	seed := newMetaAdsSeed(t, e, org)
	seed.connect("act_R"+org.String()[:6], "EUR")
	owner := e.user(t, "ownerR")
	e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)
	path := "/v1/organizations/" + org.String() + "/ads/meta/refresh"

	first := metaAdsJSON(t, e.expect(t, http.StatusAccepted, http.MethodPost, path, tok, "", ""))
	if first["queued"] != true || first["not_before"] == nil {
		t.Fatalf("first refresh: %v", first)
	}
	second := metaAdsJSON(t, e.expect(t, http.StatusAccepted, http.MethodPost, path, tok, "", ""))
	if second["queued"] != false || second["not_before"] != first["not_before"] {
		t.Fatalf("second refresh: %v (first %v)", second, first)
	}
	nb, err := time.Parse(time.RFC3339Nano, second["not_before"].(string))
	if err != nil || nb.Before(time.Now().Add(9*time.Minute)) {
		t.Fatalf("not_before = %v %v", second["not_before"], err)
	}
	var jobs int
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT count(*) FROM worker_jobs WHERE job_type = 'meta.insights_sync' AND payload ->> 'org_id' = $1`, org.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("sync jobs = %d %v, want 1", jobs, err)
	}
}

// Meta switched off: the three routes answer 503 meta.disabled.
func TestMetaAdsRoutes_DisabledIs503(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "adsOff")
	owner := e.user(t, "ownerOff")
	e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)
	base := "/v1/organizations/" + org.String()
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, base + "/events/" + uuid.NewString() + "/ads/meta"},
		{http.MethodGet, base + "/ads/meta/summary"},
		{http.MethodPost, base + "/ads/meta/refresh"},
	} {
		if st, out := e.do(t, r.method, r.path, tok, "", ""); st != http.StatusServiceUnavailable || sec1Code(out) != "meta.disabled" {
			t.Errorf("%s %s: %d %s, want 503 meta.disabled", r.method, r.path, st, out)
		}
	}
}

// The numbers: spend of the linked campaigns, our checkouts, paid orders
// and net revenue (a succeeded refund taken off, a failed one not),
// complimentary and test orders left out, MER, the untagged share and the
// shared flag of a campaign with checkouts on two events.
func TestMetaAdsRoutes_Numbers(t *testing.T) {
	e := metaRtEnv(t)
	org := e.org(t, "adsN")
	s := newMetaAdsSeed(t, e, org)
	act := "act_N" + org.String()[:6]
	s.connect(act, "EUR")
	owner := e.user(t, "ownerN")
	e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)

	// Meta: c1 1000+500 in the period and 9999 before it, c2 2000.
	s.spend(act, "c1", "a1", "2026-11-02", "EUR", 1000, 100)
	s.spend(act, "c1", "a1", "2026-11-03", "EUR", 500, 50)
	s.spend(act, "c1", "a1", "2026-10-15", "EUR", 9999, 999)
	s.spend(act, "c2", "a2", "2026-11-04", "EUR", 2000, 300)
	s.exec(`INSERT INTO ad_campaigns (org_id, campaign_id, ad_account_id, name, effective_status) VALUES ($1, 'c1', $2, 'Spring tour', 'ACTIVE')`, org, act)

	e1, e2 := s.event("Concert one"), s.event("Concert two")
	o1 := s.order(e1, e1.channel, "c1", "public_feed", "EUR", 3000, "2026-11-02 12:00:00+00")
	s.refund(o1, 1000, "succeeded")
	s.refund(o1, 700, "failed")
	s.order(e1, e1.channel, "c1", "public_feed", "EUR", 2000, "2026-11-05 12:00:00+00")
	s.order(e1, e1.channel, "", "public_feed", "EUR", 1500, "2026-11-06 12:00:00+00")         // untagged
	s.order(e1, e1.channel, "google-x", "public_feed", "EUR", 1000, "2026-11-06 13:00:00+00") // tagged, not Meta
	s.order(e1, e1.channel, "c1", "complimentary", "EUR", 0, "2026-11-06 14:00:00+00")        // never counted as paid
	s.order(e1, e1.testChannel, "c1", "public_feed", "EUR", 7000, "2026-11-06 15:00:00+00")   // test order
	s.checkout(e1, e1.channel, "c1", "2026-11-03 10:00:00+00")                                // abandoned
	s.order(e1, e1.channel, "c1", "public_feed", "EUR", 5000, "2026-11-20 12:00:00+00")       // after the period
	s.order(e2, e2.channel, "c2", "public_feed", "EUR", 4000, "2026-11-04 12:00:00+00")
	s.order(e2, e2.channel, "c1", "public_feed", "EUR", 1000, "2026-11-07 12:00:00+00") // c1 is shared
	s.exec(`INSERT INTO meta_graph_calls (day, org_id, endpoint, calls, errors) VALUES
	        ((now() AT TIME ZONE 'UTC')::date, $1, 'insights', 7, 1),
	        ((now() AT TIME ZONE 'UTC')::date - 3, $1, 'campaigns', 2, 0),
	        ((now() AT TIME ZONE 'UTC')::date - 20, $1, 'insights', 100, 50)`, org)

	q := "?from=2026-11-01&to=2026-11-10"
	base := "/v1/organizations/" + org.String()

	// Event one: only c1 (shared), spend 1500.
	m := metaAdsJSON(t, e.expect(t, http.StatusOK, http.MethodGet, base+"/events/"+e1.id.String()+"/ads/meta"+q, tok, "", ""))
	wantField(t, m, "period.from", "2026-11-01")
	wantField(t, m, "period.to", "2026-11-10")
	wantField(t, m, "meta.spend_minor", 1500)
	wantField(t, m, "meta.currency", "EUR")
	wantField(t, m, "meta.impressions", 150)
	wantField(t, m, "meta.link_clicks", 16)
	wantField(t, m, "meta.purchases", 2)
	wantField(t, m, "meta.purchase_value_minor", 5000)
	wantField(t, m, "ours.checkouts_started", 4) // o1, o2, the complimentary one and the abandoned one
	wantField(t, m, "ours.paid_orders", 2)
	wantField(t, m, "ours.revenue_net_minor", 4000)
	wantField(t, m, "ours.currency", "EUR")
	wantField(t, m, "mer", 2.67)
	wantField(t, m, "untagged.paid_orders", 4)
	wantField(t, m, "untagged.untagged_orders", 1)
	wantField(t, m, "shared_spend", true)
	if list, _ := m["campaigns"].([]any); len(list) != 1 {
		t.Fatalf("event one campaigns: %v", m["campaigns"])
	}
	c1 := campaignByID(t, m, "c1")
	if c1["shared"] != true || c1["campaign_name"] != "Spring tour" || c1["effective_status"] != "ACTIVE" ||
		c1["paid_orders"] != float64(2) || c1["revenue_net_minor"] != float64(4000) || c1["revenue_currency"] != "EUR" {
		t.Errorf("c1 of event one: %v", c1)
	}
	if evs, _ := c1["events"].([]any); len(evs) != 2 {
		t.Errorf("c1 events: %v", c1["events"])
	}
	// The raw answer carries MER as a number, never a string.
	raw := e.expect(t, http.StatusOK, http.MethodGet, base+"/events/"+e1.id.String()+"/ads/meta"+q, tok, "", "")
	if !strings.Contains(raw, `"mer":2.67`) {
		t.Errorf("mer is not a JSON number: %s", raw)
	}

	// Event two: c1 and c2, spend 3500, orders 4000 + 1000.
	m = metaAdsJSON(t, e.expect(t, http.StatusOK, http.MethodGet, base+"/events/"+e2.id.String()+"/ads/meta"+q, tok, "", ""))
	wantField(t, m, "meta.spend_minor", 3500)
	wantField(t, m, "ours.paid_orders", 2)
	wantField(t, m, "ours.revenue_net_minor", 5000)
	wantField(t, m, "mer", 1.43)
	wantField(t, m, "untagged.untagged_orders", 0)
	wantField(t, m, "shared_spend", true)
	if campaignByID(t, m, "c2")["shared"] != false {
		t.Errorf("c2 is not shared: %v", m["campaigns"])
	}

	// The organization summary.
	m = metaAdsJSON(t, e.expect(t, http.StatusOK, http.MethodGet, base+"/ads/meta/summary"+q, tok, "", ""))
	wantField(t, m, "meta.spend_minor", 3500)
	wantField(t, m, "api_volume.calls_15d", 9)
	wantField(t, m, "api_volume.errors_15d", 1)
	camps, _ := m["campaigns"].([]any)
	if len(camps) != 2 || camps[0].(map[string]any)["campaign_id"] != "c2" {
		t.Fatalf("summary campaigns (biggest spend first): %v", camps)
	}
	c1 = campaignByID(t, m, "c1")
	if c1["paid_orders"] != float64(3) || c1["revenue_net_minor"] != float64(5000) || c1["spend_minor"] != float64(1500) {
		t.Errorf("summary c1: %v", c1)
	}
	evs, _ := m["events"].([]any)
	if len(evs) != 2 {
		t.Fatalf("summary events: %v", evs)
	}
	first, second := evs[0].(map[string]any), evs[1].(map[string]any)
	if first["event_id"] != e2.id.String() || first["spend_minor"] != float64(3500) || first["paid_orders"] != float64(2) ||
		first["revenue_net_minor"] != float64(5000) || first["mer"] != 1.43 {
		t.Errorf("summary event two: %v", first)
	}
	if second["event_id"] != e1.id.String() || second["spend_minor"] != float64(1500) || second["revenue_net_minor"] != float64(4000) || second["mer"] != 2.67 {
		t.Errorf("summary event one: %v", second)
	}
}

// Spend in one currency and orders in another: MER is null.
func TestMetaAdsRoutes_DifferentCurrencyHasNoMER(t *testing.T) {
	e := metaRtEnv(t)
	org := e.org(t, "adsC")
	s := newMetaAdsSeed(t, e, org)
	act := "act_C" + org.String()[:6]
	s.connect(act, "USD")
	owner := e.user(t, "ownerC")
	e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)
	s.spend(act, "cx", "ax", "2026-11-02", "USD", 1000, 10)
	ev := s.event("Show")
	s.order(ev, ev.channel, "cx", "public_feed", "EUR", 3000, "2026-11-02 12:00:00+00")

	q := "?from=2026-11-01&to=2026-11-10"
	m := metaAdsJSON(t, e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+org.String()+"/events/"+ev.id.String()+"/ads/meta"+q, tok, "", ""))
	wantField(t, m, "meta.currency", "USD")
	wantField(t, m, "ours.currency", "EUR")
	wantField(t, m, "ours.revenue_net_minor", 3000)
	wantField(t, m, "mer", nil)
	m = metaAdsJSON(t, e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+org.String()+"/ads/meta/summary"+q, tok, "", ""))
	if evs, _ := m["events"].([]any); len(evs) != 1 || evs[0].(map[string]any)["mer"] != nil {
		t.Errorf("summary events: %v", m["events"])
	}
}
