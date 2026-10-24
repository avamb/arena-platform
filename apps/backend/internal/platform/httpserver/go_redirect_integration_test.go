//go:build integration

package httpserver

// go_redirect_integration_test.go — task QL-3 (spec 43 section 4.1): the public
// short-link redirect GET|HEAD /go/{code} through the REAL router and a real
// database.
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run 'GoRedirect|ShortLink'

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

const (
	goTicketsBase = "https://tickets.example.test"
	goFallback    = "https://home.example.test"
	goBrowserUA   = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1"
)

type goRedirectFixture struct {
	srv       *Server
	q         *gen.Queries
	orgID     uuid.UUID
	orgSlug   string
	eventID   uuid.UUID
	eventSlug string
	sessionID uuid.UUID
}

func newGoRedirectFixture(t *testing.T, ipLimit int) *goRedirectFixture {
	t.Helper()
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.PublicTicketsBaseURL = goTicketsBase
		c.ShortLinkFallbackURL = goFallback
		c.PublicAPIIPRateLimit = ipLimit
	})
	ctx := context.Background()
	q := gen.New(srv.pgxPool)
	run := strings.ToLower(uuid.NewString()[:8])

	org, err := q.InsertOrganization(ctx, "GoRedirect Org "+run, "goredirect-org-"+run, "ES", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	ev, err := q.InsertEvent(ctx, org.ID, "GoRedirect Event "+run, nil, "published", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	slug := "goredirect-event-" + run
	if _, err := q.UpdateEventMetadata(ctx, ev.ID, org.ID, &slug, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateEventMetadata: %v", err)
	}
	ch, err := q.InsertSalesChannel(ctx, org.ID, "GoRedirect Channel "+run, "direct_merchant", "stripe", nil, "0.00", nil, json.RawMessage(`{"hosted_page":{"enabled":true}}`))
	if err != nil {
		t.Fatalf("InsertSalesChannel: %v", err)
	}
	ft, err := q.InsertFeedToken(ctx, "goredirect-token-"+run, ch.ID, "go redirect integration test")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}
	if _, err := q.PublishEvent(ctx, ev.ID, ft.ID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	venue, session := uuid.New(), uuid.New()
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'GoRedirect Hall', 'Europe/Madrid')`, venue, org.ID); err != nil {
		t.Fatalf("venue: %v", err)
	}
	start := time.Now().UTC().Add(72 * time.Hour)
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, sales_end_at, capacity_total, status, currency, currency_source)
		VALUES ($1, $2, $3, $4, $5, $4, 50, 'scheduled', 'EUR', 'override')`, session, ev.ID, venue, start, start.Add(2*time.Hour)); err != nil {
		t.Fatalf("session: %v", err)
	}

	t.Cleanup(func() {
		bg := context.Background()
		// sales_link_hits cascades from sales_links; sales_links has no cascade
		// from the organization or the event.
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = $1)`, org.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM sales_links WHERE org_id = $1`, org.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM sessions WHERE event_id = $1`, ev.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM venues WHERE id = $1`, venue)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM event_publications WHERE event_id = $1`, ev.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, ch.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM sales_channels WHERE id = $1`, ch.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM events WHERE id = $1`, ev.ID)
		_, _ = srv.pgxPool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	return &goRedirectFixture{srv: srv, q: q, orgID: org.ID, orgSlug: org.Slug, eventID: ev.ID, eventSlug: slug, sessionID: session}
}

// link inserts one sales link with a fresh code and returns it.
func (f *goRedirectFixture) link(t *testing.T, kind string) gen.SalesLinkRow {
	t.Helper()
	for i := 0; i < salelinks.MaxMintAttempts; i++ {
		code, err := salelinks.NewCode()
		if err != nil {
			t.Fatal(err)
		}
		p := gen.InsertSalesLinkParams{
			OrgID: f.orgID, Code: code, TargetKind: kind, Label: "GoRedirect poster",
			UTM: json.RawMessage(`{"utm_source":"qr","utm_medium":"offline","utm_campaign":"` + f.eventSlug + `","utm_content":"` + code + `"}`),
		}
		switch kind {
		case salelinks.KindEvent:
			p.EventID = &f.eventID
		case salelinks.KindSession:
			p.EventID, p.SessionID = &f.eventID, &f.sessionID
		}
		row, ok, err := f.q.InsertSalesLinkIfCodeFree(context.Background(), p)
		if err != nil {
			t.Fatalf("InsertSalesLinkIfCodeFree: %v", err)
		}
		if ok {
			return row
		}
	}
	t.Fatal("no free code")
	return gen.SalesLinkRow{}
}

func (f *goRedirectFixture) do(method, path, ua, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)
	return rec
}

func (f *goRedirectFixture) counts(t *testing.T, id uuid.UUID) (scans, suspect int64, rows int, hits, suspectHits int64) {
	t.Helper()
	ctx := context.Background()
	if err := f.srv.pgxPool.QueryRow(ctx, `SELECT scan_count, suspect_scan_count FROM sales_links WHERE id = $1`, id).Scan(&scans, &suspect); err != nil {
		t.Fatalf("sales_links: %v", err)
	}
	if err := f.srv.pgxPool.QueryRow(ctx,
		`SELECT count(*), coalesce(sum(hits),0), coalesce(sum(suspect_hits),0) FROM sales_link_hits WHERE sales_link_id = $1`, id).
		Scan(&rows, &hits, &suspectHits); err != nil {
		t.Fatalf("sales_link_hits: %v", err)
	}
	return
}

func goLocation(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer-when-downgrade" {
		t.Fatalf("headers = %v", rec.Header())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	return u
}

func TestGoRedirect_EventLinkRedirectsAndCountsEachClientOnce(t *testing.T) {
	f := newGoRedirectFixture(t, 0)
	link := f.link(t, salelinks.KindEvent)
	const client = "192.0.2.10:40000"

	u := goLocation(t, f.do(http.MethodGet, "/go/"+strings.ToUpper(link.Code), goBrowserUA, client))
	if got := u.Scheme + "://" + u.Host + u.Path; got != goTicketsBase+"/"+f.orgSlug+"/"+f.eventSlug {
		t.Fatalf("destination = %q", got)
	}
	qv := u.Query()
	for k, want := range map[string]string{
		"utm_source": "qr", "utm_medium": "offline", "utm_campaign": f.eventSlug,
		"utm_content": link.Code, "ref": link.Code,
	} {
		if qv.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, qv.Get(k), want)
		}
	}

	// The same client again: redirected, counted once.
	goLocation(t, f.do(http.MethodGet, "/go/"+link.Code, goBrowserUA, client))
	scans, suspect, rows, hits, suspectHits := f.counts(t, link.ID)
	if scans != 1 || suspect != 0 || rows != 1 || hits != 1 || suspectHits != 0 {
		t.Fatalf("after two scans of one client: scans=%d suspect=%d rows=%d hits=%d suspect_hits=%d", scans, suspect, rows, hits, suspectHits)
	}
	var country string
	var day time.Time
	if err := f.srv.pgxPool.QueryRow(context.Background(), `SELECT country, day FROM sales_link_hits WHERE sales_link_id = $1`, link.ID).Scan(&country, &day); err != nil {
		t.Fatal(err)
	}
	if country != "" {
		t.Errorf("country = %q without a trusted proxy, want empty", country)
	}
	if want := time.Now().UTC().Format("2006-01-02"); day.Format("2006-01-02") != want { // allow:timeformat: a calendar day
		t.Errorf("day = %v, want %s (UTC)", day, want)
	}

	// A bot-like client is counted in the suspect counters, every time.
	goLocation(t, f.do(http.MethodGet, "/go/"+link.Code, "curl/8.0.1", client))
	scans, suspect, rows, hits, suspectHits = f.counts(t, link.ID)
	if scans != 1 || suspect != 1 || rows != 1 || hits != 1 || suspectHits != 1 {
		t.Fatalf("after a curl scan: scans=%d suspect=%d rows=%d hits=%d suspect_hits=%d", scans, suspect, rows, hits, suspectHits)
	}

	// HEAD answers the same address and counts nothing.
	head := f.do(http.MethodHead, "/go/"+link.Code, goBrowserUA+" preview", "192.0.2.11:40000")
	if hu := goLocation(t, head); hu.String() != u.String() {
		t.Errorf("HEAD Location = %q, GET Location = %q", hu, u)
	}
	if s2, sp2, _, _, _ := f.counts(t, link.ID); s2 != 1 || sp2 != 1 {
		t.Fatalf("HEAD counted: scans=%d suspect=%d", s2, sp2)
	}
}

func TestGoRedirect_SessionLinkCarriesTheSession(t *testing.T) {
	f := newGoRedirectFixture(t, 0)
	link := f.link(t, salelinks.KindSession)
	u := goLocation(t, f.do(http.MethodGet, "/go/"+link.Code, goBrowserUA, "192.0.2.20:1"))
	if u.Path != "/"+f.orgSlug+"/"+f.eventSlug || u.Query().Get("session") != f.sessionID.String() || u.Query().Get("ref") != link.Code {
		t.Fatalf("Location = %q", u)
	}
}

func TestGoRedirect_UnknownCodeGoesToTheFallbackAndArchivedToTheOrganization(t *testing.T) {
	f := newGoRedirectFixture(t, 0)

	unknown, err := salelinks.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	if u := goLocation(t, f.do(http.MethodGet, "/go/"+unknown, goBrowserUA, "192.0.2.30:1")); u.String() != goFallback {
		t.Fatalf("unknown code: Location = %q, want %q", u, goFallback)
	}
	if u := goLocation(t, f.do(http.MethodGet, "/go/not-a-code", goBrowserUA, "192.0.2.30:1")); u.String() != goFallback {
		t.Fatalf("malformed code: Location = %q", u)
	}

	link := f.link(t, salelinks.KindEvent)
	if _, err := f.srv.pgxPool.Exec(context.Background(), `UPDATE sales_links SET status = 'archived' WHERE id = $1`, link.ID); err != nil {
		t.Fatal(err)
	}
	u := goLocation(t, f.do(http.MethodGet, "/go/"+link.Code, goBrowserUA, "192.0.2.30:1"))
	if got := u.Scheme + "://" + u.Host + u.Path; got != goTicketsBase+"/"+f.orgSlug || u.Query().Get("ref") != link.Code {
		t.Fatalf("archived link: Location = %q, want the organization page with ref", u)
	}

	// An unpublished event falls to the organization page too.
	live := f.link(t, salelinks.KindEvent)
	if _, err := f.srv.pgxPool.Exec(context.Background(), `UPDATE events SET status = 'draft' WHERE id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	if u := goLocation(t, f.do(http.MethodGet, "/go/"+live.Code, goBrowserUA, "192.0.2.30:1")); u.Path != "/"+f.orgSlug {
		t.Fatalf("unpublished event: Location = %q", u)
	}
}

// TestGoRedirect_ResponseArrivesBeforeTheCount drives a real listener: the
// client receives a complete, empty 302 and the scan is counted afterwards.
func TestGoRedirect_ResponseArrivesBeforeTheCount(t *testing.T) {
	f := newGoRedirectFixture(t, 0)
	link := f.link(t, salelinks.KindEvent)
	ts := httptest.NewServer(f.srv.Router())
	t.Cleanup(ts.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/go/"+link.Code, nil)
	req.Header.Set("User-Agent", goBrowserUA)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.ContentLength != 0 || !strings.Contains(resp.Header.Get("Location"), "ref="+link.Code) {
		t.Fatalf("status=%d length=%d Location=%q", resp.StatusCode, resp.ContentLength, resp.Header.Get("Location"))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		scans, _, _, _, _ := f.counts(t, link.ID)
		if scans == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan never counted: %d", scans)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGoRedirect_FloodIsRateLimitedWithoutFailure(t *testing.T) {
	const limit = 20
	f := newGoRedirectFixture(t, limit)
	link := f.link(t, salelinks.KindEvent)

	byStatus := map[int]int{}
	for i := 0; i < 1000; i++ {
		rec := f.do(http.MethodGet, "/go/"+link.Code, goBrowserUA, "198.51.100.77:5000")
		byStatus[rec.Code]++
		if rec.Code == http.StatusTooManyRequests && rec.Header().Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
	}
	if byStatus[http.StatusFound] != limit || byStatus[http.StatusTooManyRequests] != 1000-limit || len(byStatus) != 2 {
		t.Fatalf("statuses = %v, want %d redirects and the rest 429", byStatus, limit)
	}
	if scans, _, _, hits, _ := f.counts(t, link.ID); scans != 1 || hits != 1 {
		t.Fatalf("flood counted scans=%d hits=%d, want 1 (one client)", scans, hits)
	}
	// Another client still gets through.
	goLocation(t, f.do(http.MethodGet, "/go/"+link.Code, goBrowserUA, "198.51.100.78:5000"))
}
