package hgo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

const (
	browserUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1"
	testBase  = "https://tickets.example"
	fallback  = "https://home.example"
)

type bump struct {
	id      uuid.UUID
	suspect bool
}

type hit struct {
	id      uuid.UUID
	day     time.Time
	country string
	suspect bool
}

type fakeQueries struct {
	mu        sync.Mutex
	links     map[string]gen.SalesLinkRow
	lookupErr error
	facts     gen.SalesLinkTargetFactsRow
	factsErr  error
	bumpErr   error
	bumps     []bump
	hits      []hit
	lookups   int
}

func (f *fakeQueries) GetSalesLinkByCode(_ context.Context, code string) (gen.SalesLinkRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.lookupErr != nil {
		return gen.SalesLinkRow{}, f.lookupErr
	}
	row, ok := f.links[strings.ToLower(code)]
	if !ok {
		return gen.SalesLinkRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeQueries) GetSalesLinkTargetFacts(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, *uuid.UUID) (gen.SalesLinkTargetFactsRow, error) {
	if f.factsErr != nil {
		return gen.SalesLinkTargetFactsRow{}, f.factsErr
	}
	return f.facts, nil
}

func (f *fakeQueries) BumpSalesLinkScan(_ context.Context, id uuid.UUID, suspect bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bumpErr != nil {
		return f.bumpErr
	}
	f.bumps = append(f.bumps, bump{id, suspect})
	return nil
}

func (f *fakeQueries) UpsertSalesLinkHit(_ context.Context, id uuid.UUID, day time.Time, country string, suspect bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = append(f.hits, hit{id, day, country, suspect})
	return nil
}

type fakeLimiter struct {
	allow bool
	retry int
	ips   []string
}

func (l *fakeLimiter) CheckIP(ip string) (bool, int) {
	l.ips = append(l.ips, ip)
	return l.allow, l.retry
}

var testNow = time.Date(2026, 10, 24, 23, 30, 0, 0, time.FixedZone("CEST", 2*3600))

func eventLink(code string) gen.SalesLinkRow {
	ev := uuid.New()
	return gen.SalesLinkRow{
		ID: uuid.New(), OrgID: uuid.New(), Code: code, TargetKind: salelinks.KindEvent, EventID: &ev,
		Status: "active",
		UTM:    json.RawMessage(`{"utm_source":"qr","utm_medium":"offline","utm_campaign":"koncert-2026","utm_content":"` + code + `"}`),
	}
}

func publishedFacts() gen.SalesLinkTargetFactsRow {
	return gen.SalesLinkTargetFactsRow{OrgSlug: "teatr", EventSlug: "koncert", EventPublished: true}
}

type rig struct {
	q        *fakeQueries
	lim      *fakeLimiter
	h        *Handler
	router   chi.Router
	outcomes []string
}

func newRig(t *testing.T, trusted int) *rig {
	t.Helper()
	rg := &rig{
		q:   &fakeQueries{links: map[string]gen.SalesLinkRow{}, facts: publishedFacts()},
		lim: &fakeLimiter{allow: true},
	}
	rg.h = New(Options{
		Queries: rg.q, Limiter: rg.lim, Base: testBase, Fallback: fallback,
		TrustedProxies: trusted, Now: func() time.Time { return testNow },
		Observe: func(o string) { rg.outcomes = append(rg.outcomes, o) },
	})
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/go/{code}", rg.h)
	r.Method(http.MethodHead, "/go/{code}", rg.h)
	rg.router = r
	return rg
}

func (rg *rig) do(method, path, ua string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.7:51234"
	req.Header.Set("User-Agent", ua)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	rg.router.ServeHTTP(rec, req)
	return rec
}

func assertRedirect(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer-when-downgrade" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("redirect body = %q, want empty", rec.Body.String())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	return u
}

func TestGo_ValidCodeRedirectsWithUTMAndRefAndCountsOnce(t *testing.T) {
	rg := newRig(t, 0)
	row := eventLink("x7k2qpm")
	rg.q.links["x7k2qpm"] = row

	u := assertRedirect(t, rg.do(http.MethodGet, "/go/X7K2QPM", browserUA, nil))
	if got := u.Scheme + "://" + u.Host + u.Path; got != testBase+"/teatr/koncert" {
		t.Fatalf("destination = %q", got)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"utm_source": "qr", "utm_medium": "offline", "utm_campaign": "koncert-2026",
		"utm_content": "x7k2qpm", "ref": "x7k2qpm",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(rg.q.bumps) != 1 || rg.q.bumps[0].suspect || rg.q.bumps[0].id != row.ID {
		t.Fatalf("bumps = %+v, want one non-suspect bump of the link", rg.q.bumps)
	}
	if len(rg.q.hits) != 1 {
		t.Fatalf("hits = %+v", rg.q.hits)
	}
	wantDay := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC) // 23:30 CEST is 21:30 UTC
	if h := rg.q.hits[0]; !h.day.Equal(wantDay) || h.day.Location() != time.UTC || h.country != "" {
		t.Errorf("hit = %+v, want day %v and no country", h, wantDay)
	}
	if rg.lim.ips[0] != "203.0.113.7" {
		t.Errorf("limiter key = %q, want the peer address", rg.lim.ips[0])
	}

	// The same client again: redirected, not counted.
	assertRedirect(t, rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil))
	if len(rg.q.bumps) != 1 || len(rg.q.hits) != 1 {
		t.Fatalf("second scan counted: bumps=%d hits=%d", len(rg.q.bumps), len(rg.q.hits))
	}
	if strings.Join(rg.outcomes, ",") != "redirect,dedup" {
		t.Errorf("outcomes = %v", rg.outcomes)
	}

	// Another client is a new scan.
	rg.do(http.MethodGet, "/go/x7k2qpm", browserUA+" other", nil)
	if len(rg.q.bumps) != 2 {
		t.Fatalf("another client not counted: %d", len(rg.q.bumps))
	}
}

func TestGo_UnknownAndMalformedCodesGoToTheFallbackUncounted(t *testing.T) {
	rg := newRig(t, 0)
	for _, path := range []string{"/go/abcdefg", "/go/abc", "/go/x7k2qp0", "/go/%D0%B9%D0%B9%D0%B9%D0%B9"} {
		u := assertRedirect(t, rg.do(http.MethodGet, path, browserUA, nil))
		if u.String() != fallback {
			t.Errorf("%s: Location = %q, want the fallback", path, u.String())
		}
	}
	if len(rg.q.bumps)+len(rg.q.hits) != 0 {
		t.Fatalf("fallback counted something: %+v %+v", rg.q.bumps, rg.q.hits)
	}
	// Only the well-formed unknown code reached the database.
	if rg.q.lookups != 1 {
		t.Errorf("lookups = %d, want 1", rg.q.lookups)
	}
}

func TestGo_DefaultFallback(t *testing.T) {
	h := New(Options{Queries: &fakeQueries{}})
	if h.fallback != DefaultFallbackURL {
		t.Fatalf("fallback = %q", h.fallback)
	}
}

func TestGo_ArchivedLinkGoesToTheOrganizationPage(t *testing.T) {
	rg := newRig(t, 0)
	row := eventLink("archv23")
	row.Status = "archived"
	rg.q.links["archv23"] = row

	u := assertRedirect(t, rg.do(http.MethodGet, "/go/archv23", browserUA, nil))
	if u.Path != "/teatr" || u.Query().Get("ref") != "archv23" {
		t.Fatalf("Location = %q, want the organization page with ref", u.String())
	}
	if len(rg.q.bumps) != 1 {
		t.Errorf("an archived link's scan is still counted: %d", len(rg.q.bumps))
	}
}

func TestGo_UnpublishedEventFallsToTheOrganization(t *testing.T) {
	rg := newRig(t, 0)
	rg.q.links["unpub23"] = eventLink("unpub23")
	rg.q.facts.EventPublished = false
	u := assertRedirect(t, rg.do(http.MethodGet, "/go/unpub23", browserUA, nil))
	if u.Path != "/teatr" {
		t.Fatalf("Location = %q", u.String())
	}
}

func TestGo_SuspectAgentIsCountedAsSuspectEveryTime(t *testing.T) {
	rg := newRig(t, 0)
	rg.q.links["x7k2qpm"] = eventLink("x7k2qpm")
	for i := 0; i < 2; i++ {
		assertRedirect(t, rg.do(http.MethodGet, "/go/x7k2qpm", "curl/8.0.1", nil))
	}
	if len(rg.q.bumps) != 2 || !rg.q.bumps[0].suspect || !rg.q.hits[1].suspect {
		t.Fatalf("bumps=%+v hits=%+v, want two suspect scans", rg.q.bumps, rg.q.hits)
	}
	assertRedirect(t, rg.do(http.MethodGet, "/go/x7k2qpm", "", nil))
	if len(rg.q.bumps) != 3 || !rg.q.bumps[2].suspect {
		t.Fatalf("an empty agent is suspect: %+v", rg.q.bumps)
	}
}

func TestGo_HeadRedirectsWithoutCounting(t *testing.T) {
	rg := newRig(t, 0)
	rg.q.links["x7k2qpm"] = eventLink("x7k2qpm")
	u := assertRedirect(t, rg.do(http.MethodHead, "/go/x7k2qpm", browserUA, nil))
	if u.Query().Get("ref") != "x7k2qpm" {
		t.Fatalf("Location = %q", u.String())
	}
	if len(rg.q.bumps)+len(rg.q.hits) != 0 {
		t.Fatal("HEAD counted a scan")
	}
	// A HEAD does not use up the client's dedup slot either.
	rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil)
	if len(rg.q.bumps) != 1 {
		t.Fatalf("GET after HEAD not counted: %d", len(rg.q.bumps))
	}
}

func TestGo_RateLimitAnswers429BeforeTheDatabase(t *testing.T) {
	rg := newRig(t, 0)
	rg.q.links["x7k2qpm"] = eventLink("x7k2qpm")
	rg.lim.allow, rg.lim.retry = false, 42
	rec := rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "42" {
		t.Fatalf("status=%d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "short_link.rate_limited") {
		t.Fatalf("headers %v body %q", rec.Header(), rec.Body.String())
	}
	if rg.q.lookups != 0 || len(rg.q.bumps) != 0 {
		t.Fatal("a refused request reached the database")
	}
	rg.lim.retry = 0
	if rec := rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil); rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After floor = %q", rec.Header().Get("Retry-After"))
	}
}

func TestGo_DatabaseErrorsAnswer503(t *testing.T) {
	rg := newRig(t, 0)
	rg.q.lookupErr = errors.New("connection refused")
	rec := rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("lookup failure: %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Body.String(), "short_link.unavailable") {
		t.Fatalf("body %q", rec.Body.String())
	}

	rg = newRig(t, 0)
	rg.q.links["x7k2qpm"] = eventLink("x7k2qpm")
	rg.q.factsErr = errors.New("timeout")
	if rec := rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("facts failure: %d", rec.Code)
	}

	// A vanished organization has no page to fall to: the platform page.
	rg.q.factsErr = pgx.ErrNoRows
	if u := assertRedirect(t, rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil)); u.String() != fallback {
		t.Fatalf("org gone: %q", u.String())
	}
}

func TestGo_CountingFailureStillRedirects(t *testing.T) {
	rg := newRig(t, 0)
	rg.q.links["x7k2qpm"] = eventLink("x7k2qpm")
	rg.q.bumpErr = errors.New("deadlock")
	assertRedirect(t, rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil))
	if strings.Join(rg.outcomes, ",") != "error" {
		t.Errorf("outcomes = %v", rg.outcomes)
	}
}

func TestGo_CountryFollowsTheTrustedProxy(t *testing.T) {
	cases := []struct {
		trusted int
		header  string
		want    string
	}{
		{0, "ES", ""},
		{1, "es", "ES"},
		{1, " CZ ", "CZ"},
		{1, "xx1", ""},
		{1, "XX", ""},
		{1, "T1", ""},
		{1, "", ""},
	}
	for _, c := range cases {
		rg := newRig(t, c.trusted)
		rg.q.links["x7k2qpm"] = eventLink("x7k2qpm")
		rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, map[string]string{"CF-IPCountry": c.header})
		if len(rg.q.hits) != 1 || rg.q.hits[0].country != c.want {
			t.Errorf("trusted=%d header=%q: hits=%+v, want country %q", c.trusted, c.header, rg.q.hits, c.want)
		}
	}
}

func TestGo_URLTargetKeepsItsParametersAndRefusesOtherSchemes(t *testing.T) {
	rg := newRig(t, 0)
	row := eventLink("urtarg2")
	row.TargetKind, row.EventID = salelinks.KindURL, nil
	target := "https://shop.example/merch?utm_source=old&size=m#top"
	row.URL = &target
	rg.q.links["urtarg2"] = row
	u := assertRedirect(t, rg.do(http.MethodGet, "/go/urtarg2", browserUA, nil))
	if u.Host != "shop.example" || u.Query().Get("size") != "m" || u.Query().Get("utm_source") != "qr" || u.Fragment != "top" {
		t.Fatalf("Location = %q", u.String())
	}

	bad := "javascript:alert(1)"
	row.URL = &bad
	rg.q.links["urtarg2"] = row
	if u := assertRedirect(t, rg.do(http.MethodGet, "/go/urtarg2", browserUA, nil)); u.String() != fallback {
		t.Fatalf("unsafe url: Location = %q", u.String())
	}
}

func TestGo_BrokenUTMDocumentStillRedirectsWithRef(t *testing.T) {
	rg := newRig(t, 0)
	row := eventLink("badutm2")
	row.UTM = json.RawMessage(`{"utm_source": 5}`)
	rg.q.links["badutm2"] = row
	u := assertRedirect(t, rg.do(http.MethodGet, "/go/badutm2", browserUA, nil))
	if u.Query().Get("ref") != "badutm2" || u.Query().Get("utm_source") != "" {
		t.Fatalf("Location = %q", u.String())
	}
}

func TestGo_NoBaseAddressGoesToTheFallback(t *testing.T) {
	rg := newRig(t, 0)
	rg.h.base = ""
	rg.q.links["x7k2qpm"] = eventLink("x7k2qpm")
	if u := assertRedirect(t, rg.do(http.MethodGet, "/go/x7k2qpm", browserUA, nil)); u.String() != fallback {
		t.Fatalf("Location = %q", u.String())
	}
	if len(rg.q.bumps) != 0 {
		t.Fatal("a fallback was counted")
	}
}
