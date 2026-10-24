//go:build integration

package httpserver

// page_stats_integration_test.go — task 2-3 (HP-2a): the hosted page's
// statistics beacon (POST /v1/public/pages/{slug}/{event_slug}/track), the
// organizer's read (GET .../events/{id}/page-stats and page-stats.csv) and the
// limits, all through the REAL router.
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run TestPageStats

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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
)

const (
	pageStatsBrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15"
	pageStatsBotUA     = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
)

type pageStatsFixture struct {
	srv       *Server
	ts        *httptest.Server
	q         *gen.Queries
	pool      *pgxpool.Pool
	userID    uuid.UUID
	orgA      uuid.UUID
	orgSlugA  string
	eventA    uuid.UUID
	eventSlug string
	tz        string
}

func newPageStatsFixture(t *testing.T) *pageStatsFixture {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
	// A private registry, so the counters of one test are not another's.
	metrics, err := observability.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	srv.typedMetrics = metrics
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	ctx := context.Background()
	q := gen.New(srv.pgxPool)
	run := strings.ToLower(uuid.NewString()[:8])

	user, err := q.InsertUser(ctx, "pagestats-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	org, err := q.InsertOrganization(ctx, "PageStats Org "+run, "pagestats-org-"+run, "ES", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	ev, err := q.InsertEvent(ctx, org.ID, "PageStats Event "+run, nil, "published", "public", nil, "text")
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	slug := "pagestats-event-" + run
	if _, err := q.UpdateEventMetadata(ctx, ev.ID, org.ID, &slug, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateEventMetadata: %v", err)
	}
	ch, err := q.InsertSalesChannel(ctx, org.ID, "PageStats Channel "+run, "direct_merchant", "stripe", nil, "0.00", nil, json.RawMessage(`{"hosted_page":{"enabled":true}}`))
	if err != nil {
		t.Fatalf("InsertSalesChannel: %v", err)
	}
	ft, err := q.InsertFeedToken(ctx, "pagestats-token-"+run, ch.ID, "page stats integration test")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}
	if _, err := q.PublishEvent(ctx, ev.ID, ft.ID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	// A venue far from UTC, so "the day follows the venue" is not a coincidence.
	tz := "Pacific/Auckland"
	venue, session := uuid.New(), uuid.New()
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'PageStats Hall', $3)`, venue, org.ID, tz); err != nil {
		t.Fatalf("venue: %v", err)
	}
	start := time.Now().UTC().Add(72 * time.Hour)
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, sales_end_at, capacity_total, status, currency, currency_source)
		VALUES ($1, $2, $3, $4, $5, $4, 50, 'scheduled', 'EUR', 'override')`, session, ev.ID, venue, start, start.Add(2*time.Hour)); err != nil {
		t.Fatalf("session: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM sessions WHERE event_id = $1`, ev.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM venues WHERE id = $1`, venue)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM event_publications WHERE event_id = $1`, ev.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, ch.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM sales_channels WHERE id = $1`, ch.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM api_keys WHERE org_id = $1`, org.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM events WHERE id = $1`, ev.ID) // cascades event_page_stats
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	return &pageStatsFixture{srv: srv, ts: ts, q: q, pool: srv.pgxPool, userID: user.ID,
		orgA: org.ID, orgSlugA: org.Slug, eventA: ev.ID, eventSlug: slug, tz: tz}
}

func (f *pageStatsFixture) key(t *testing.T, orgID uuid.UUID, scopes ...string) string {
	t.Helper()
	_, raw, err := apikeys.Issue(context.Background(), apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
		OrgID: orgID, Name: "pagestats-" + uuid.NewString()[:6], Scopes: scopes, CreatedBy: f.userID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	return raw
}

// track posts one beacon with the given User-Agent and returns the status, the
// body and the Retry-After header.
func (f *pageStatsFixture) track(t *testing.T, org, event, body, ua string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+"/v1/public/pages/"+org+"/"+event+"/track", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header.Get("Retry-After")
}

func (f *pageStatsFixture) get(t *testing.T, path, key string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

func (f *pageStatsFixture) rows(t *testing.T) (hits, suspect int64, n int) {
	t.Helper()
	err := f.pool.QueryRow(context.Background(),
		`SELECT coalesce(sum(hits),0), coalesce(sum(suspect_hits),0), count(*) FROM event_page_stats WHERE event_id = $1`, f.eventA).
		Scan(&hits, &suspect, &n)
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	return
}

func TestPageStats_CountsHitsAndSuspectsAndFollowsTheVenueDay(t *testing.T) {
	f := newPageStatsFixture(t)

	st, body, _ := f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view","utm_source":"instagram","utm_campaign":"spring"}`, pageStatsBrowserUA)
	if st != http.StatusNoContent {
		t.Fatalf("page_view: %d %s", st, body)
	}
	// Same pair again, a bot, and a hit with a rejected utm value.
	f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view","utm_source":"instagram","utm_campaign":"spring"}`, pageStatsBrowserUA)
	f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view","utm_source":"instagram","utm_campaign":"spring"}`, pageStatsBotUA)
	f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view","utm_source":"bad value!","utm_campaign":"<x>"}`, pageStatsBrowserUA)
	f.track(t, f.orgSlugA, strings.ToUpper(f.eventSlug), `{"kind":"video_play"}`, pageStatsBrowserUA)
	f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"partner_click"}`, "")

	hits, suspect, n := f.rows(t)
	if hits != 4 || suspect != 2 {
		t.Fatalf("hits=%d suspect=%d, want 4 and 2", hits, suspect)
	}
	if n != 4 {
		t.Fatalf("counter rows = %d, want 4 (page_view instagram/spring, page_view no utm, video_play, partner_click)", n)
	}

	// The day is the calendar day in the venue's zone.
	var day time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT max(day) FROM event_page_stats WHERE event_id = $1`, f.eventA).Scan(&day); err != nil {
		t.Fatal(err)
	}
	want := pagestats.Day(time.Now(), f.tz)
	if !day.UTC().Equal(want) {
		// A beacon sent just before midnight there could differ by a day; allow
		// the next day only.
		if !day.UTC().Equal(want.AddDate(0, 0, -1)) {
			t.Fatalf("day = %v, want %v", day, want)
		}
	}

	// No column holds a visitor identifier.
	rows, err := f.pool.Query(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_name = 'event_page_stats'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	allowed := map[string]bool{"event_id": true, "org_id": true, "day": true, "kind": true, "utm_source": true, "utm_campaign": true, "hits": true, "suspect_hits": true}
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		if !allowed[c] {
			t.Errorf("event_page_stats has an unexpected column %q", c)
		}
	}

	// The organizer's read.
	key := f.key(t, f.orgA, "event.read")
	base := "/v1/organizations/" + f.orgA.String() + "/events/" + f.eventA.String()
	code, raw, _ := f.get(t, base+"/page-stats", key)
	if code != http.StatusOK {
		t.Fatalf("page-stats: %d %s", code, raw)
	}
	var got struct {
		Timezone *string                     `json:"timezone"`
		Totals   map[string]map[string]int64 `json:"totals"`
		Days     []map[string]any            `json:"days"`
		Sources  []struct {
			UTMSource   string `json:"utm_source"`
			UTMCampaign string `json:"utm_campaign"`
			Hits        int64  `json:"hits"`
			SuspectHits int64  `json:"suspect_hits"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if got.Totals["page_view"]["hits"] != 3 || got.Totals["page_view"]["suspect_hits"] != 1 ||
		got.Totals["video_play"]["hits"] != 1 || got.Totals["partner_click"]["suspect_hits"] != 1 {
		t.Fatalf("totals = %v", got.Totals)
	}
	if got.Timezone == nil || *got.Timezone != f.tz {
		t.Errorf("timezone = %v", got.Timezone)
	}
	if len(got.Days) < 1 || len(got.Days) > 2 {
		t.Errorf("days = %d", len(got.Days))
	}
	if len(got.Sources) != 2 || got.Sources[0].UTMSource != "instagram" || got.Sources[0].UTMCampaign != "spring" || got.Sources[0].Hits != 2 || got.Sources[0].SuspectHits != 1 {
		t.Errorf("sources = %+v", got.Sources)
	}

	// CSV.
	code, csv, hdr := f.get(t, base+"/page-stats.csv?locale=ru", key)
	if code != http.StatusOK || !strings.HasPrefix(csv, "\ufeff") || !strings.Contains(csv, "instagram") || !strings.Contains(hdr.Get("Content-Disposition"), "page-stats_") {
		t.Fatalf("csv: %d %q %v", code, csv, hdr)
	}
	if !strings.Contains(csv, "UTM-источник") {
		t.Errorf("csv header is not in Russian: %q", csv)
	}

	// Bad windows.
	for _, q := range []string{"?from=2026-13-01", "?from=2026-05-10&to=2026-05-01", "?from=2020-01-01&to=2026-05-01"} {
		if code, raw, _ := f.get(t, base+"/page-stats"+q, key); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", q, code, raw)
		}
	}
	// A window with no data is an empty answer, not an error.
	code, raw, _ = f.get(t, base+"/page-stats?from=2001-01-01&to=2001-01-31", key)
	if code != http.StatusOK || !strings.Contains(raw, `"days":[]`) {
		t.Errorf("empty window: %d %s", code, raw)
	}
}

func TestPageStats_RejectsWhatItShouldNotCount(t *testing.T) {
	f := newPageStatsFixture(t)
	reg := f.srv.typedMetrics

	cases := []struct {
		name, org, event, body string
		want                   int
	}{
		{"unknown kind", f.orgSlugA, f.eventSlug, `{"kind":"purchase"}`, http.StatusBadRequest},
		{"no kind", f.orgSlugA, f.eventSlug, `{}`, http.StatusBadRequest},
		{"not json", f.orgSlugA, f.eventSlug, `page_view`, http.StatusBadRequest},
		{"data after the object", f.orgSlugA, f.eventSlug, `{"kind":"page_view"}{"kind":"page_view"}`, http.StatusBadRequest},
		{"body over 1 KiB", f.orgSlugA, f.eventSlug, `{"kind":"page_view","utm_source":"` + strings.Repeat("a", 2000) + `"}`, http.StatusBadRequest},
		{"unknown org", "no-such-org-" + uuid.NewString()[:6], f.eventSlug, `{"kind":"page_view"}`, http.StatusNotFound},
		{"unknown event", f.orgSlugA, "no-such-event", `{"kind":"page_view"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		st, body, _ := f.track(t, c.org, c.event, c.body, pageStatsBrowserUA)
		if st != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, st, body, c.want)
		}
	}
	if _, _, n := f.rows(t); n != 0 {
		t.Fatalf("rejected requests wrote %d rows", n)
	}

	// Taking the page off the hosted channel makes the beacon answer like the page: 404.
	if _, err := f.pool.Exec(context.Background(), `UPDATE events SET status = 'draft' WHERE id = $1`, f.eventA); err != nil {
		t.Fatal(err)
	}
	if st, body, _ := f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view"}`, pageStatsBrowserUA); st != http.StatusNotFound {
		t.Errorf("draft event: %d %s, want 404", st, body)
	}
	if got := testutil.ToFloat64(reg.PageTrackTotal.WithLabelValues("other", observability.PageTrackRejected)); got < 1 {
		t.Errorf("rejected counter for an unknown kind = %v", got)
	}
}

func TestPageStats_OrganizerReadStaysInsideTheOrganization(t *testing.T) {
	f := newPageStatsFixture(t)
	f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view"}`, pageStatsBrowserUA)

	orgB, err := f.q.InsertOrganization(context.Background(), "PageStats B "+uuid.NewString()[:6], "pagestats-b-"+strings.ToLower(uuid.NewString()[:8]), "ES", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM api_keys WHERE org_id = $1`, orgB.ID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgB.ID)
	})
	keyB := f.key(t, orgB.ID, "event.read")
	keyNoRead := f.key(t, f.orgA, "order.read")
	base := "/v1/organizations/%s/events/" + f.eventA.String()

	// Another organization's key, asking in its own path: the event is not its.
	for _, suffix := range []string{"/page-stats", "/page-stats.csv"} {
		code, raw, _ := f.get(t, fmt.Sprintf(base, orgB.ID)+suffix, keyB)
		if code != http.StatusNotFound || !strings.Contains(raw, "event.not_found") {
			t.Errorf("foreign event %s: %d %s, want 404 event.not_found", suffix, code, raw)
		}
		// The same key in organization A's path is refused by the gate.
		code, raw, _ = f.get(t, fmt.Sprintf(base, f.orgA)+suffix, keyB)
		if code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("foreign key in A's path %s: %d %s", suffix, code, raw)
		}
		// A key without event.read.
		if code, raw, _ = f.get(t, fmt.Sprintf(base, f.orgA)+suffix, keyNoRead); code != http.StatusForbidden {
			t.Errorf("no event.read %s: %d %s, want 403", suffix, code, raw)
		}
		// No credentials at all.
		if code, _, _ = f.get(t, fmt.Sprintf(base, f.orgA)+suffix, ""); code != http.StatusUnauthorized {
			t.Errorf("anonymous %s: %d, want 401", suffix, code)
		}
	}
}

func TestPageStats_UTMPairsPerDayAreCapped(t *testing.T) {
	f := newPageStatsFixture(t)
	ctx := context.Background()
	day := pagestats.Day(time.Now(), f.tz)

	// The day already holds the maximum number of different pairs.
	if _, err := f.pool.Exec(ctx, `INSERT INTO event_page_stats (event_id, org_id, day, kind, utm_source, utm_campaign, hits)
		SELECT $1, $2, $3, 'page_view', 'src' || g, 'camp', 1 FROM generate_series(1, $4) g`,
		f.eventA, f.orgA, day, pagestats.MaxUTMPairsPerDay); err != nil {
		t.Fatal(err)
	}
	// A new pair is counted without utm; an existing pair still counts under its own name.
	f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view","utm_source":"brandnew","utm_campaign":"x"}`, pageStatsBrowserUA)
	f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view","utm_source":"src7","utm_campaign":"camp"}`, pageStatsBrowserUA)

	var fresh, empty, known int64
	row := func(src, camp string) int64 {
		var h int64
		err := f.pool.QueryRow(ctx, `SELECT coalesce(sum(hits),0) FROM event_page_stats WHERE event_id=$1 AND utm_source=$2 AND utm_campaign=$3 AND day=$4`,
			f.eventA, src, camp, day).Scan(&h)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	// The beacon's day may be yesterday near a midnight race; accept a miss only for both.
	fresh, empty, known = row("brandnew", "x"), row("", ""), row("src7", "camp")
	if fresh != 0 {
		t.Errorf("the 201st pair was stored under its name (%d hits)", fresh)
	}
	if empty != 1 {
		t.Errorf("the 201st pair was not counted without utm (%d hits)", empty)
	}
	if known != 2 {
		t.Errorf("an existing pair did not keep counting (%d hits, want 2)", known)
	}
}

func TestPageStats_FloodIsAnsweredTooManyRequestsAndNotCounted(t *testing.T) {
	f := newPageStatsFixture(t)
	f.srv.publicFeedRL.pageTrackIPLimit = 5
	f.srv.publicFeedRL.pageTrackEventLimit = 0

	allowed := 0
	var limited int
	for i := 0; i < 20; i++ {
		st, body, retry := f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view"}`, pageStatsBrowserUA)
		switch st {
		case http.StatusNoContent:
			allowed++
		case http.StatusTooManyRequests:
			limited++
			if !strings.Contains(body, "track.rate_limited") || retry == "" {
				t.Fatalf("429 without the code or Retry-After: %s retry=%q", body, retry)
			}
		default:
			t.Fatalf("unexpected %d %s", st, body)
		}
	}
	if allowed != 5 || limited != 15 {
		t.Fatalf("allowed=%d limited=%d, want 5 and 15", allowed, limited)
	}
	if hits, _, _ := f.rows(t); hits != 5 {
		t.Fatalf("hits stored = %d, want 5: a refused request must not count", hits)
	}
	m := f.srv.typedMetrics.PageTrackTotal
	if got := testutil.ToFloat64(m.WithLabelValues("other", observability.PageTrackRateLimited)); got != 15 {
		t.Errorf("rate_limited metric = %v, want 15", got)
	}
	if got := testutil.ToFloat64(m.WithLabelValues("page_view", observability.PageTrackCounted)); got != 5 {
		t.Errorf("counted metric = %v, want 5", got)
	}

	// The page's own bucket stops a flood that comes from many addresses.
	f.srv.publicFeedRL.pageTrackIPLimit = 0
	f.srv.publicFeedRL.pageTrackEventLimit = 3
	f.srv.publicFeedRL.pageTrackEvents = map[string]*rateLimiterWindow{}
	blocked := 0
	for i := 0; i < 6; i++ {
		if st, _, _ := f.track(t, f.orgSlugA, f.eventSlug, `{"kind":"page_view"}`, pageStatsBrowserUA); st == http.StatusTooManyRequests {
			blocked++
		}
	}
	if blocked != 3 {
		t.Errorf("page bucket blocked %d of 6, want 3", blocked)
	}
}
