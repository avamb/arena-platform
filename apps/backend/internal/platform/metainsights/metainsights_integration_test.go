//go:build integration

package metainsights

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

// graphStub answers /v25.0/{act}/campaigns and /v25.0/{act}/insights per ad
// account and remembers the time_range and the token of each call.
type graphStub struct {
	mu        sync.Mutex
	insights  map[string]stubAnswer
	campaigns map[string]stubAnswer
	ranges    map[string]string
	tokens    map[string]string
}

type stubAnswer struct {
	status int
	body   string
}

func newGraphStub() *graphStub {
	return &graphStub{insights: map[string]stubAnswer{}, campaigns: map[string]stubAnswer{}, ranges: map[string]string{}, tokens: map[string]string{}}
}

func (g *graphStub) set(account string, insights stubAnswer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.insights[account] = insights
}

func (g *graphStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v25.0" {
		http.NotFound(w, r)
		return
	}
	account, edge := parts[1], parts[2]
	g.tokens[account] = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var a stubAnswer
	switch edge {
	case "insights":
		g.ranges[account] = r.URL.Query().Get("time_range")
		a = g.insights[account]
	case "campaigns":
		var ok bool
		if a, ok = g.campaigns[account]; !ok {
			a = stubAnswer{200, `{"data":[{"id":"c1","name":"Camp 1","effective_status":"ACTIVE","objective":"OUTCOME_SALES"}]}`}
		}
	default:
		http.NotFound(w, r)
		return
	}
	if a.status == 0 {
		a = stubAnswer{200, `{"data":[]}`}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	_, _ = w.Write([]byte(a.body))
}

func insightRow(ad, campaign, day, spend string) string {
	return fmt.Sprintf(`{"ad_id":%q,"ad_name":"Ad %s","adset_id":"s1","adset_name":"Set","campaign_id":%q,"campaign_name":"Camp","spend":%q,
		"impressions":"100","clicks":"10","inline_link_clicks":"7","account_currency":"EUR","date_start":%q,
		"actions":[{"action_type":"omni_purchase","value":"1"},{"action_type":"purchase","value":"1"}],
		"action_values":[{"action_type":"omni_purchase","value":"20.00"}]}`, ad, ad, campaign, spend, day)
}

func page(rows ...string) stubAnswer {
	return stubAnswer{200, `{"data":[` + strings.Join(rows, ",") + `]}`}
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	box  *secretbox.Box
	stub *graphStub
	srv  *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	box, err := secretbox.ParseKeys("v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("i", 32))))
	if err != nil {
		t.Fatal(err)
	}
	stub := newGraphStub()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	return &fixture{t: t, ctx: ctx, pool: pool, box: box, stub: stub, srv: srv}
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("fixture %q: %v", sql, err)
	}
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// org makes an organization with an active Meta connection whose token is
// "tok-<account>" and a selected ad account (EUR, Europe/Madrid).
func (f *fixture) org(account string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	nonce := id.String()[:8]
	f.exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, id, "Meta Insights "+nonce, "mins-"+nonce)
	f.t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM worker_jobs WHERE payload ->> 'org_id' = $1::text`,
			`DELETE FROM sales_notification_deliveries WHERE key LIKE 'ads:' || $1::text || ':%'`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE venue_id IN (SELECT id FROM venues WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := f.pool.Exec(context.Background(), sql, id); err != nil {
				f.t.Logf("cleanup %q: %v", sql, err)
			}
		}
	})
	sealed, err := metaconnect.SealToken(f.box, id, "tok-"+account)
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`INSERT INTO org_meta_connections (org_id, meta_user_id, access_token_enc, status) VALUES ($1, $2, $3, 'active')`, id, "mu-"+nonce, sealed)
	f.exec(`INSERT INTO org_meta_assets (org_id, kind, meta_id, name, currency, timezone, selected)
	        VALUES ($1, 'ad_account', $2, 'Account', 'EUR', 'Europe/Madrid', true)`, id, account)
	return id
}

func (f *fixture) syncer(a Announcer) *Syncer {
	return New(Options{Pool: f.pool, Box: f.box, Client: NewClient(f.srv.URL, "v25.0", nil), Announcer: a, Logger: slog.Default()})
}

// The run time: 05:00 in Madrid on 2026-11-23.
var runAt = time.Date(2026, 11, 23, 4, 0, 0, 0, time.UTC)

func TestRun_BackfillUpsertAndVanishedRows_LiveDB(t *testing.T) {
	f := newFixture(t)
	act := "act_" + uuid.NewString()[:8]
	org := f.org(act)
	f.stub.set(act, page(
		insightRow("a1", "c1", "2026-11-20", "10.00"),
		insightRow("a2", "c1", "2026-11-21", "5.50"),
		insightRow("a1", "c1", "2026-11-22", "3.21"),
	))
	s := f.syncer(nil)
	res, err := s.Run(f.ctx, org, ModeScheduled, runAt)
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("backfill: %+v %v", res, err)
	}
	if res.Mode != ModeBackfill || res.Since != "2026-08-26" || res.Until != "2026-11-23" || res.Rows != 3 {
		t.Fatalf("backfill result %+v", res)
	}
	if got := f.stub.ranges[act]; got != `{"since":"2026-08-26","until":"2026-11-23"}` {
		t.Errorf("time_range = %s", got)
	}
	if f.stub.tokens[act] != "tok-"+act {
		t.Errorf("the decrypted token did not reach Graph")
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1`, org); n != 3 {
		t.Fatalf("rows = %d", n)
	}
	var spend, value, purchases int64
	if err := f.pool.QueryRow(f.ctx, `SELECT spend_minor, meta_purchase_value_minor, meta_purchases FROM ad_spend_daily
		WHERE org_id = $1 AND ad_id = 'a2' AND day = '2026-11-21'`, org).Scan(&spend, &value, &purchases); err != nil {
		t.Fatal(err)
	}
	if spend != 550 || value != 2000 || purchases != 1 {
		t.Errorf("a2 = %d %d %d", spend, value, purchases)
	}
	st, err := LoadState(f.ctx, f.pool, org)
	if err != nil || st.AdAccountID != act || st.BackfillDoneAt == nil || st.LastSuccessAt == nil || st.LastError != "" {
		t.Fatalf("state %+v %v", st, err)
	}
	if n := f.count(`SELECT count(*) FROM ad_campaigns WHERE org_id = $1 AND campaign_id = 'c1' AND objective = 'OUTCOME_SALES'`, org); n != 1 {
		t.Errorf("campaigns = %d", n)
	}
	calls, errs, err := CallVolume(f.ctx, f.pool, org, runAt.AddDate(0, 0, -14))
	if err != nil || calls != 2 || errs != 0 {
		t.Errorf("call volume %d %d %v", calls, errs, err)
	}

	// A manual run reads 3 days: a1 of 11-22 changes, a2 of 11-21 is gone,
	// a1 of 11-20 is outside the range and stays.
	f.stub.set(act, page(insightRow("a1", "c1", "2026-11-22", "4.00")))
	res, err = s.Run(f.ctx, org, ModeManual, runAt.Add(time.Hour))
	if err != nil || res.Mode != ModeManual || res.Since != "2026-11-21" || res.Deleted != 1 {
		t.Fatalf("manual: %+v %v", res, err)
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1`, org); n != 2 {
		t.Fatalf("rows after the manual run = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1 AND ad_id = 'a1' AND day = '2026-11-22' AND spend_minor = 400`, org); n != 1 {
		t.Errorf("a1 of 11-22 was not updated")
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1 AND ad_id = 'a1' AND day = '2026-11-20'`, org); n != 1 {
		t.Errorf("a row outside the range was touched")
	}
	if calls, _, _ := CallVolume(f.ctx, f.pool, org, runAt.AddDate(0, 0, -14)); calls != 4 {
		t.Errorf("calls = %d", calls)
	}

	// The data-deletion hook removes the ad data.
	if err := DeleteOrgData(f.ctx, f.pool, org); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT (SELECT count(*) FROM ad_spend_daily WHERE org_id = $1) + (SELECT count(*) FROM ad_campaigns WHERE org_id = $1)
		+ (SELECT count(*) FROM meta_insights_state WHERE org_id = $1)`, org); n != 0 {
		t.Errorf("DeleteOrgData left %d rows", n)
	}
}

func TestRun_GraphErrors_LiveDB(t *testing.T) {
	f := newFixture(t)
	s := f.syncer(nil)

	dead := "act_" + uuid.NewString()[:8]
	orgDead := f.org(dead)
	f.stub.set(dead, stubAnswer{400, `{"error":{"message":"Error validating access token","code":190,"type":"OAuthException"}}`})
	res, err := s.Run(f.ctx, orgDead, ModeScheduled, runAt)
	if err != nil || res.ErrorCode != "graph_190" {
		t.Fatalf("190: %+v %v", res, err)
	}
	var status string
	var tokenGone bool
	if err := f.pool.QueryRow(f.ctx, `SELECT status, access_token_enc IS NULL FROM org_meta_connections WHERE org_id = $1`, orgDead).Scan(&status, &tokenGone); err != nil {
		t.Fatal(err)
	}
	if status != metaconnect.StatusError || !tokenGone {
		t.Errorf("190 left the connection %s, token wiped %v", status, tokenGone)
	}
	if st, _ := LoadState(f.ctx, f.pool, orgDead); st.LastError != "graph_190" || st.LastSuccessAt != nil {
		t.Errorf("state %+v", st)
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1`, orgDead); n != 0 {
		t.Errorf("rows written after 190: %d", n)
	}
	if _, errs, _ := CallVolume(f.ctx, f.pool, orgDead, runAt.AddDate(0, 0, -1)); errs != 1 {
		t.Errorf("errors counted = %d", errs)
	}
	// The next run makes no Graph call at all.
	if res, _ := s.Run(f.ctx, orgDead, ModeManual, runAt.Add(time.Hour)); res.ErrorCode != CodeNotConnected {
		t.Errorf("after 190: %+v", res)
	}

	denied := "act_" + uuid.NewString()[:8]
	orgDenied := f.org(denied)
	f.stub.set(denied, stubAnswer{403, `{"error":{"message":"no permission","code":10,"type":"OAuthException"}}`})
	res, err = s.Run(f.ctx, orgDenied, ModeScheduled, runAt)
	if err != nil || res.ErrorCode != "graph_10" {
		t.Fatalf("10: %+v %v", res, err)
	}
	var lastErr *string
	if err := f.pool.QueryRow(f.ctx, `SELECT status, access_token_enc IS NULL, last_error FROM org_meta_connections WHERE org_id = $1`, orgDenied).Scan(&status, &tokenGone, &lastErr); err != nil {
		t.Fatal(err)
	}
	if status != metaconnect.StatusActive || tokenGone || lastErr == nil || *lastErr != "graph_10" {
		t.Errorf("permission error: %s %v %v", status, tokenGone, lastErr)
	}

	limited := "act_" + uuid.NewString()[:8]
	orgLimited := f.org(limited)
	f.stub.set(limited, stubAnswer{400, `{"error":{"message":"too many calls","code":80004,"type":"OAuthException"}}`})
	if res, _ := s.Run(f.ctx, orgLimited, ModeScheduled, runAt); res.ErrorCode != CodeGraphRateLimited {
		t.Errorf("rate limit: %+v", res)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM org_meta_connections WHERE org_id = $1`, orgLimited).Scan(&status); err != nil || status != metaconnect.StatusActive {
		t.Errorf("rate limit changed the connection: %s %v", status, err)
	}

	// No selected ad account: no call, no_ad_account.
	none := f.org("act_" + uuid.NewString()[:8])
	f.exec(`UPDATE org_meta_assets SET selected = false WHERE org_id = $1`, none)
	if res, _ := s.Run(f.ctx, none, ModeManual, runAt); res.ErrorCode != CodeNoAdAccount {
		t.Errorf("no ad account: %+v", res)
	}
	if calls, _, _ := CallVolume(f.ctx, f.pool, none, runAt.AddDate(0, 0, -1)); calls != 0 {
		t.Errorf("calls without an ad account: %d", calls)
	}
}

func TestRun_OrgIsolation_LiveDB(t *testing.T) {
	f := newFixture(t)
	s := f.syncer(nil)
	actA, actB := "act_"+uuid.NewString()[:8], "act_"+uuid.NewString()[:8]
	orgA, orgB := f.org(actA), f.org(actB)
	// Both answers carry the same ad id and day.
	f.stub.set(actA, page(insightRow("shared_ad", "cA", "2026-11-22", "10.00"), insightRow("onlyA", "cA", "2026-11-21", "1.00")))
	f.stub.set(actB, page(insightRow("shared_ad", "cB", "2026-11-22", "99.00")))
	if _, err := s.Run(f.ctx, orgA, ModeScheduled, runAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(f.ctx, orgB, ModeScheduled, runAt); err != nil {
		t.Fatal(err)
	}
	if f.stub.tokens[actB] != "tok-"+actB {
		t.Errorf("org B used another token")
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1 AND spend_minor = 1000 AND campaign_id = 'cA' AND ad_id = 'shared_ad'`, orgA); n != 1 {
		t.Errorf("org B's run changed org A's row")
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1`, orgA); n != 2 {
		t.Errorf("org A rows = %d", n)
	}
	// Org B's next answer is empty: its delete step must not reach org A.
	f.stub.set(actB, page())
	if res, err := s.Run(f.ctx, orgB, ModeManual, runAt.Add(time.Hour)); err != nil || res.Deleted != 1 {
		t.Fatalf("org B empty run: %+v %v", res, err)
	}
	if n := f.count(`SELECT count(*) FROM ad_spend_daily WHERE org_id = $1`, orgA); n != 2 {
		t.Errorf("org A rows after org B's empty run = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM ad_campaigns WHERE org_id = $1`, orgA); n != 1 {
		t.Errorf("org A campaigns = %d", n)
	}
	stA, _ := LoadState(f.ctx, f.pool, orgA)
	stB, _ := LoadState(f.ctx, f.pool, orgB)
	if stA.AdAccountID != actA || stB.AdAccountID != actB {
		t.Errorf("states mixed: %q %q", stA.AdAccountID, stB.AdAccountID)
	}
	callsA, _, _ := CallVolume(f.ctx, f.pool, orgA, runAt.AddDate(0, 0, -1))
	if callsA != 2 {
		t.Errorf("org A calls = %d", callsA)
	}
}

func TestEnqueueRefresh_Throttle_LiveDB(t *testing.T) {
	f := newFixture(t)
	org := f.org("act_" + uuid.NewString()[:8])
	jobs := func() int {
		return f.count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload ->> 'org_id' = $2 AND status = 'pending'`, SyncJobType, org.String())
	}
	now := time.Now().UTC().Truncate(time.Second)
	queued, nb, err := EnqueueRefresh(f.ctx, f.pool, org, now)
	if err != nil || !queued || !nb.Equal(now.Add(RefreshThrottle)) {
		t.Fatalf("first refresh: %v %s %v", queued, nb, err)
	}
	if n := jobs(); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	var mode string
	if err := f.pool.QueryRow(f.ctx, `SELECT payload ->> 'mode' FROM worker_jobs WHERE job_type = $1 AND payload ->> 'org_id' = $2`, SyncJobType, org.String()).Scan(&mode); err != nil || mode != ModeManual {
		t.Errorf("mode = %q %v", mode, err)
	}
	queued, nb2, err := EnqueueRefresh(f.ctx, f.pool, org, now.Add(time.Minute))
	if err != nil || queued || !nb2.Equal(nb) {
		t.Fatalf("throttled refresh: %v %s %v", queued, nb2, err)
	}
	// After the throttle: allowed, but the waiting job is joined, not doubled.
	queued, _, err = EnqueueRefresh(f.ctx, f.pool, org, now.Add(11*time.Minute))
	if err != nil || !queued || jobs() != 1 {
		t.Fatalf("after the throttle: %v %v, jobs %d", queued, err, jobs())
	}

	// The sweep never adds a second sync either.
	s := f.syncer(nil)
	if _, err := s.Sweep(f.ctx, now); err != nil {
		t.Fatal(err)
	}
	if n := jobs(); n != 1 {
		t.Errorf("jobs after the sweep = %d", n)
	}
	f.exec(`DELETE FROM worker_jobs WHERE job_type = $1 AND payload ->> 'org_id' = $2`, SyncJobType, org.String())
	if _, err := s.Sweep(f.ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT payload ->> 'mode' FROM worker_jobs WHERE job_type = $1 AND payload ->> 'org_id' = $2`, SyncJobType, org.String()).Scan(&mode); err != nil || mode != ModeBackfill {
		t.Errorf("sweep of a never-synced org: %q %v", mode, err)
	}
}

// No active connection: no throttle stamp, no job, queued false.
func TestEnqueueRefresh_NoConnection_LiveDB(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, status := range []string{"error", "gone"} {
		org := f.org("act_" + uuid.NewString()[:8])
		if status == "gone" {
			f.exec(`DELETE FROM org_meta_assets WHERE org_id = $1`, org)
			f.exec(`DELETE FROM org_meta_connections WHERE org_id = $1`, org)
		} else {
			f.exec(`UPDATE org_meta_connections SET status = 'error', access_token_enc = NULL WHERE org_id = $1`, org)
		}
		queued, nb, err := EnqueueRefresh(f.ctx, f.pool, org, now)
		if err != nil || queued || !nb.IsZero() {
			t.Fatalf("%s: %v %s %v", status, queued, nb, err)
		}
		if n := f.count(`SELECT count(*) FROM meta_insights_state WHERE org_id = $1`, org); n != 0 {
			t.Errorf("%s: throttle stamped (%d rows)", status, n)
		}
		if n := f.count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload ->> 'org_id' = $2`, SyncJobType, org.String()); n != 0 {
			t.Errorf("%s: jobs = %d", status, n)
		}
	}
}

// Many sweeps and refreshes at once still leave ONE waiting sync per org.
func TestEnqueueSync_ConcurrentOneJob_LiveDB(t *testing.T) {
	f := newFixture(t)
	org := f.org("act_" + uuid.NewString()[:8])
	now := time.Now().UTC().Truncate(time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = enqueueSync(f.ctx, f.pool, org, ModeInterval)
			} else {
				_, _, err = EnqueueRefresh(f.ctx, f.pool, org, now.Add(time.Duration(i)*RefreshThrottle))
			}
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload ->> 'org_id' = $2`, SyncJobType, org.String()); n != 1 {
		t.Fatalf("sync jobs = %d, want 1", n)
	}
}

// A failed run counts a consecutive failure; the sweep backs off 1 h, 2 h
// ... even with META_INSIGHTS_EVERY=15m; a success resets the count.
func TestRun_FailureBackoff_LiveDB(t *testing.T) {
	f := newFixture(t)
	act := "act_" + uuid.NewString()[:8]
	org := f.org(act)
	f.stub.set(act, page(insightRow("a1", "c1", "2026-11-22", "1.00")))
	s := New(Options{Pool: f.pool, Box: f.box, Client: NewClient(f.srv.URL, "v25.0", nil), Logger: slog.Default(), Every: 15 * time.Minute})
	day := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) // 13:00 Madrid
	if res, err := s.Run(f.ctx, org, ModeBackfill, day); err != nil || res.ErrorCode != "" {
		t.Fatalf("backfill: %+v %v", res, err)
	}
	f.stub.set(act, stubAnswer{400, `{"error":{"message":"too many calls","code":80004,"type":"OAuthException"}}`})
	t1 := day.Add(time.Hour)
	t2 := t1.Add(2 * time.Hour)
	for i, at := range []time.Time{t1, t2} {
		if res, err := s.Run(f.ctx, org, ModeInterval, at); err != nil || res.ErrorCode != CodeGraphRateLimited {
			t.Fatalf("failure %d: %+v %v", i+1, res, err)
		}
	}
	st, err := LoadState(f.ctx, f.pool, org)
	if err != nil || st.Failures != 2 || st.LastError != CodeGraphRateLimited {
		t.Fatalf("state after two failures: %+v %v", st, err)
	}
	pending := func() int {
		return f.count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload ->> 'org_id' = $2`, SyncJobType, org.String())
	}
	// 1 h 59 after the second failure: 2 h back-off, nothing queued.
	if _, err := s.Sweep(f.ctx, t2.Add(119*time.Minute)); err != nil || pending() != 0 {
		t.Fatalf("sweep inside the back-off: jobs %d %v", pending(), err)
	}
	if _, err := s.Sweep(f.ctx, t2.Add(121*time.Minute)); err != nil || pending() != 1 {
		t.Fatalf("sweep after the back-off: jobs %d %v", pending(), err)
	}
	f.stub.set(act, page(insightRow("a1", "c1", "2026-11-23", "1.00")))
	if res, err := s.Run(f.ctx, org, ModeInterval, t2.Add(3*time.Hour)); err != nil || res.ErrorCode != "" {
		t.Fatalf("success: %+v %v", res, err)
	}
	if st, _ := LoadState(f.ctx, f.pool, org); st.Failures != 0 || st.LastError != "" {
		t.Errorf("a success did not reset the back-off: %+v", st)
	}
}

type stubSender struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (s *stubSender) Send(_ context.Context, chat, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent[chat] = append(s.sent[chat], text)
	return nil
}

func TestRun_DailySummaryOnce_LiveDB(t *testing.T) {
	f := newFixture(t)
	act := "act_" + uuid.NewString()[:8]
	org := f.org(act)
	nonce := org.String()[:8]
	adsChat, ticketsChat := "-ads-"+nonce, "-tk-"+nonce
	f.exec(`INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds) VALUES ($1, 'ads', $2, ARRAY['tickets','ads'])`, org, adsChat)
	f.exec(`INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds) VALUES ($1, 'tickets', $2, ARRAY['tickets'])`, org, ticketsChat)

	// One paid order of yesterday (Madrid) tagged with campaign c1, 30.00 EUR.
	venueID, evtID, sessID, chanID, resvID, csID, orderID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, venueID, org, "Hall "+nonce)
	f.exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, org, "Event "+nonce)
	f.exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	        VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'EUR', 'override')`, sessID, evtID, venueID)
	f.exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider) VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, org, "Ch "+nonce)
	f.exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	        VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resvID, org, chanID, sessID)
	f.exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, attribution)
	        VALUES ($1, $2, $3, $4, 'completed', '{"utm_source":"facebook","utm_campaign":"c1"}')`, csID, org, chanID, resvID)
	f.exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                            source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone, created_at)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 3000, 0, 0, 3000, 'B', 'b@example.test', '+34600000000',
	                '2026-11-22 12:00:00+01')`, orderID, org, chanID, evtID, sessID, csID, resvID)

	f.stub.set(act, page(insightRow("a1", "c1", "2026-11-22", "12.00"), insightRow("a2", "c1", "2026-11-22", "3.00")))
	snd := &stubSender{sent: map[string][]string{}}
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(f.pool), snd, slog.Default())
	s := f.syncer(d)

	// The first run is the backfill: no summary.
	if res, err := s.Run(f.ctx, org, ModeBackfill, runAt); err != nil || res.SummarySent {
		t.Fatalf("backfill: %+v %v", res, err)
	}
	res, err := s.Run(f.ctx, org, ModeScheduled, runAt.Add(time.Hour))
	if err != nil || !res.SummarySent {
		t.Fatalf("nightly: %+v %v", res, err)
	}
	if len(snd.sent[adsChat]) != 1 || len(snd.sent[ticketsChat]) != 0 {
		t.Fatalf("sent = %v", snd.sent)
	}
	text := snd.sent[adsChat][0]
	for _, want := range []string{"Meta ads, 2026-11-22", "Spend: 15.00 EUR", "Impressions: 200", "Link clicks: 14",
		"Purchases reported by Meta: 2", "Our paid orders from these ads: 1, net 30.00 EUR", "MER (net revenue / spend): 2.00"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary misses %q:\n%s", want, text)
		}
	}
	if res, err := s.Run(f.ctx, org, ModeScheduled, runAt.Add(2*time.Hour)); err != nil || res.SummarySent || len(snd.sent[adsChat]) != 1 {
		t.Fatalf("second nightly run sent again: %+v %v %v", res, err, snd.sent)
	}
}
