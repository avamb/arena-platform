//go:build integration

// public_series_integration_test.go: live-PostgreSQL coverage of the public
// side of tours (event_series): the feed's /series routes, series_id on feed
// events and the hosted tour page.
//
//	DATABASE_URL=postgres://... go test -tags integration -p 1 \
//	    ./apps/backend/internal/platform/httpserver/hfeed/ -run TestPublicSeries
package hfeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type seriesFixture struct {
	pool        *pgxpool.Pool
	q           *gen.Queries
	orgID       uuid.UUID
	orgSlug     string
	tokenA      string // hosted-page channel
	tokenB      string // channel without a hosted page
	series      gen.EventSeriesRow
	evInA       uuid.UUID // in series, published to token A, two cities, two currencies
	evInB       uuid.UUID // in series, published to token B only
	archived    gen.EventSeriesRow
	evArchived  uuid.UUID
	onlyB       gen.EventSeriesRow // tour whose only event is in feed B
	emptySeries gen.EventSeriesRow
	start1      time.Time
	start2      time.Time
}

func newSeriesFixture(t *testing.T) *seriesFixture {
	t.Helper()
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	q := gen.New(pool)
	run := uuid.NewString()
	f := &seriesFixture{pool: pool, q: q}

	org, err := q.InsertOrganization(ctx, "Series Test Org "+run, "series-org-"+run, "DE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	f.orgID, f.orgSlug = org.ID, org.Slug

	chA, err := q.InsertSalesChannel(ctx, org.ID, "Series Channel A "+run, "merchant_of_record", "stripe", nil, "5.00", nil, json.RawMessage(`{"hosted_page":{"enabled":true}}`))
	if err != nil {
		t.Fatalf("InsertSalesChannel A: %v", err)
	}
	chB, err := q.InsertSalesChannel(ctx, org.ID, "Series Channel B "+run, "merchant_of_record", "stripe", nil, "5.00", nil, json.RawMessage(`{"hosted_page":{"enabled":false}}`))
	if err != nil {
		t.Fatalf("InsertSalesChannel B: %v", err)
	}
	ftA, err := q.InsertFeedToken(ctx, "series-token-a-"+run, chA.ID, "series test A")
	if err != nil {
		t.Fatalf("InsertFeedToken A: %v", err)
	}
	ftB, err := q.InsertFeedToken(ctx, "series-token-b-"+run, chB.ID, "series test B")
	if err != nil {
		t.Fatalf("InsertFeedToken B: %v", err)
	}
	f.tokenA, f.tokenB = ftA.Token, ftB.Token

	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("no country in the database: %v", err)
	}
	city1, err := q.InsertCity(ctx, countryID, "series-city-1-"+run)
	if err != nil {
		t.Fatalf("InsertCity 1: %v", err)
	}
	city2, err := q.InsertCity(ctx, countryID, "series-city-2-"+run)
	if err != nil {
		t.Fatalf("InsertCity 2: %v", err)
	}
	venue := func(name string, city uuid.UUID) uuid.UUID {
		v, err := q.InsertVenue(ctx, org.ID, &city, name+run, nil, nil,
			nil, nil, nil, nil, nil, nil, strPtr("Europe/Prague"), nil, nil, nil, "active")
		if err != nil {
			t.Fatalf("InsertVenue: %v", err)
		}
		return v.ID
	}
	v1, v2 := venue("Series Venue 1 ", city1.ID), venue("Series Venue 2 ", city2.ID)

	mkSeries := func(name string) gen.EventSeriesRow {
		s, err := q.CreateEventSeries(ctx, org.ID, gen.EventSeriesParams{
			Slug: "tour-" + uuid.NewString(), Name: name + " " + run,
		})
		if err != nil {
			t.Fatalf("CreateEventSeries: %v", err)
		}
		return s
	}
	f.series = mkSeries("Tour")
	f.archived = mkSeries("Archived tour")
	f.onlyB = mkSeries("Tour in feed B")
	f.emptySeries = mkSeries("Empty tour")

	var eventIDs, sessionIDs []uuid.UUID
	mkEvent := func(name string, series uuid.UUID, tokenID uuid.UUID) uuid.UUID {
		desc := "series test event"
		e, err := q.InsertEvent(ctx, org.ID, name+" "+run, &desc, "published", "public", nil, "text")
		if err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
		eventIDs = append(eventIDs, e.ID)
		slug := "series-event-" + uuid.NewString()
		if _, err := q.UpdateEventMetadata(ctx, e.ID, org.ID, &slug, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
			t.Fatalf("UpdateEventMetadata: %v", err)
		}
		if _, err := q.SetEventSeries(ctx, e.ID, org.ID, &series); err != nil {
			t.Fatalf("SetEventSeries: %v", err)
		}
		if _, err := q.PublishEvent(ctx, e.ID, tokenID, nil); err != nil {
			t.Fatalf("PublishEvent: %v", err)
		}
		return e.ID
	}
	mkSession := func(eventID, venueID uuid.UUID, start time.Time, currency string) uuid.UUID {
		s, err := q.InsertSession(ctx, eventID, venueID, start, start.Add(90*time.Minute), 10, nil, "scheduled", nil, currency, "override")
		if err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		sessionIDs = append(sessionIDs, s.ID)
		return s.ID
	}
	mkTier := func(sessionID uuid.UUID, price int64, currency string) {
		if _, err := q.InsertTicketTier(ctx, sessionID, "Tier", "fixed", price, currency, nil, nil, nil, nil, nil, 0); err != nil {
			t.Fatalf("InsertTicketTier: %v", err)
		}
	}

	f.start1 = time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	f.start2 = time.Now().UTC().Add(96 * time.Hour).Truncate(time.Second)
	f.evInA = mkEvent("Series event A", f.series.ID, ftA.ID)
	s1 := mkSession(f.evInA, v1, f.start1, "EUR")
	mkTier(s1, 2500, "EUR")
	mkTier(s1, 1800, "EUR")
	s2 := mkSession(f.evInA, v2, f.start2, "USD")
	mkTier(s2, 3000, "USD")

	f.evInB = mkEvent("Series event B", f.series.ID, ftB.ID)
	s3 := mkSession(f.evInB, v2, f.start2.Add(24*time.Hour), "EUR")
	mkTier(s3, 100, "EUR")

	oB := mkEvent("Only B event", f.onlyB.ID, ftB.ID)
	mkTier(mkSession(oB, v1, f.start1, "EUR"), 500, "EUR")

	f.evArchived = mkEvent("Archived tour event", f.archived.ID, ftA.ID)
	mkTier(mkSession(f.evArchived, v1, f.start1, "EUR"), 700, "EUR")
	if _, err := q.ArchiveEventSeries(ctx, f.archived.ID, org.ID); err != nil {
		t.Fatalf("ArchiveEventSeries: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		for _, stmt := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM ticket_tiers WHERE session_id = ANY($1::uuid[])`, sessionIDs},
			{`DELETE FROM sessions WHERE event_id = ANY($1::uuid[])`, eventIDs},
			{`DELETE FROM event_publications WHERE event_id = ANY($1::uuid[])`, eventIDs},
			{`DELETE FROM events WHERE id = ANY($1::uuid[])`, eventIDs},
			{`DELETE FROM event_series WHERE org_id = $1`, org.ID},
			{`DELETE FROM agent_feed_tokens WHERE sales_channel_id = ANY($1::uuid[])`, []uuid.UUID{chA.ID, chB.ID}},
			{`DELETE FROM sales_channels WHERE org_id = $1`, org.ID},
			{`DELETE FROM venues WHERE org_id = $1`, org.ID},
			{`DELETE FROM cities WHERE id = ANY($1::uuid[])`, []uuid.UUID{city1.ID, city2.ID}},
			{`DELETE FROM organizations WHERE id = $1`, org.ID},
		} {
			if _, err := pool.Exec(c, stmt.sql, stmt.arg); err != nil {
				t.Logf("cleanup %q: %v", stmt.sql, err)
			}
		}
	})
	return f
}

func seriesFeedReq(path string, params map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func (f *seriesFixture) get(t *testing.T, h http.HandlerFunc, path string, params map[string]string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, seriesFeedReq(path, params))
	return w.Code, w.Body.Bytes()
}

func TestPublicSeries_FeedListShowsOnlyToursWithAnEventOfThisFeed(t *testing.T) {
	f := newSeriesFixture(t)
	h := promoterPageHandler(f.pool)

	code, body := f.get(t, h.HandlePublicFeedSeries, "/x", map[string]string{"feed_token": f.tokenA})
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Items []struct {
			ID           string         `json:"id"`
			EventIDs     []string       `json:"event_ids"`
			Translations map[string]any `json:"translations"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("feed A shows %d tours, want 1 (archived and foreign tours hidden): %s", len(out.Items), body)
	}
	it := out.Items[0]
	if it.ID != f.series.ID.String() {
		t.Errorf("tour id = %s, want %s", it.ID, f.series.ID)
	}
	if len(it.EventIDs) != 1 || it.EventIDs[0] != f.evInA.String() {
		t.Errorf("event_ids = %v, want only %s (the event of feed B must not leak)", it.EventIDs, f.evInA)
	}
	if it.Translations == nil || len(it.Translations) != 0 {
		t.Errorf("translations = %v, want {}", it.Translations)
	}

	// Feed B sees the tour too (through its own event) and the tour of its own.
	code, body = f.get(t, h.HandlePublicFeedSeries, "/x", map[string]string{"feed_token": f.tokenB})
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var outB struct {
		Items []struct {
			ID       string   `json:"id"`
			EventIDs []string `json:"event_ids"`
		} `json:"items"`
	}
	_ = json.Unmarshal(body, &outB)
	if len(outB.Items) != 2 {
		t.Fatalf("feed B shows %d tours, want 2: %s", len(outB.Items), body)
	}
}

func TestPublicSeries_FeedDetailSummary(t *testing.T) {
	f := newSeriesFixture(t)
	h := promoterPageHandler(f.pool)

	code, body := f.get(t, h.HandlePublicFeedSeriesItem, "/x", map[string]string{
		"feed_token": f.tokenA, "series_id": f.series.ID.String()})
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var s struct {
		Slug    string `json:"slug"`
		Summary struct {
			DateFrom          *string `json:"date_from"`
			DateTo            *string `json:"date_to"`
			CitiesCount       int     `json:"cities_count"`
			CitiesOnSaleCount int     `json:"cities_on_sale_count"`
			NextSession       *struct {
				EventID  string `json:"event_id"`
				StartsAt string `json:"starts_at"`
			} `json:"next_session"`
			PriceFrom []struct {
				Currency string  `json:"currency"`
				Amount   float64 `json:"amount"`
			} `json:"price_from"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	if s.Slug != f.series.Slug {
		t.Errorf("slug = %q", s.Slug)
	}
	sum := s.Summary
	if sum.DateFrom == nil || *sum.DateFrom != f.start1.Format(time.RFC3339) {
		t.Errorf("date_from = %v, want %s", sum.DateFrom, f.start1.Format(time.RFC3339))
	}
	if sum.DateTo == nil || *sum.DateTo != f.start2.Format(time.RFC3339) {
		t.Errorf("date_to = %v, want %s", sum.DateTo, f.start2.Format(time.RFC3339))
	}
	if sum.CitiesCount != 2 || sum.CitiesOnSaleCount != 2 {
		t.Errorf("cities = %d / on sale %d, want 2 / 2", sum.CitiesCount, sum.CitiesOnSaleCount)
	}
	if sum.NextSession == nil || sum.NextSession.EventID != f.evInA.String() || sum.NextSession.StartsAt != f.start1.Format(time.RFC3339) {
		t.Errorf("next_session = %+v", sum.NextSession)
	}
	if len(sum.PriceFrom) != 2 ||
		sum.PriceFrom[0].Currency != "EUR" || sum.PriceFrom[0].Amount != 18.0 ||
		sum.PriceFrom[1].Currency != "USD" || sum.PriceFrom[1].Amount != 30.0 {
		t.Errorf("price_from = %+v, want EUR 18 and USD 30 in major units", sum.PriceFrom)
	}
}

func TestPublicSeries_FeedDetail404(t *testing.T) {
	f := newSeriesFixture(t)
	h := promoterPageHandler(f.pool)
	for name, tc := range map[string]struct{ token, id string }{
		"archived":            {f.tokenA, f.archived.ID.String()},
		"no event in feed":    {f.tokenA, f.onlyB.ID.String()},
		"tour without events": {f.tokenA, f.emptySeries.ID.String()},
		"not a uuid":          {f.tokenA, "nope"},
	} {
		code, body := f.get(t, h.HandlePublicFeedSeriesItem, "/x", map[string]string{"feed_token": tc.token, "series_id": tc.id})
		if code != 404 {
			t.Errorf("%s: status %d, want 404: %s", name, code, body)
		}
	}
}

func TestPublicSeries_SeriesIDOnFeedEvents(t *testing.T) {
	f := newSeriesFixture(t)
	h := promoterPageHandler(f.pool)

	code, body := f.get(t, h.HandlePublicFeedEvents, "/x", map[string]string{"feed_token": f.tokenA})
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var list struct {
		Events []struct {
			ID       string  `json:"id"`
			SeriesID *string `json:"series_id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range list.Events {
		if e.ID == f.evInA.String() {
			found = true
			if e.SeriesID == nil || *e.SeriesID != f.series.ID.String() {
				t.Errorf("series_id = %v, want %s", e.SeriesID, f.series.ID)
			}
		}
	}
	if !found {
		t.Fatalf("event %s not in the feed list: %s", f.evInA, body)
	}
}

func TestPublicSeries_PageRoute(t *testing.T) {
	f := newSeriesFixture(t)
	h := promoterPageHandler(f.pool)
	page := func(slug string) (int, []byte) {
		return f.get(t, h.HandlePublicSeriesPage, "/x", map[string]string{"org_slug": f.orgSlug, "series_slug": slug})
	}

	code, body := page(f.series.Slug)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Org           struct{ Slug string } `json:"org"`
		DefaultLocale string                `json:"default_locale"`
		Series        struct {
			ID       string   `json:"id"`
			EventIDs []string `json:"event_ids"`
		} `json:"series"`
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Org.Slug != f.orgSlug || out.DefaultLocale == "" {
		t.Errorf("org/default_locale = %q / %q", out.Org.Slug, out.DefaultLocale)
	}
	if out.Series.ID != f.series.ID.String() {
		t.Errorf("series.id = %s", out.Series.ID)
	}
	if len(out.Events) != 1 || out.Events[0].ID != f.evInA.String() {
		t.Errorf("events = %+v, want only the event of the hosted-page channel", out.Events)
	}
	if len(out.Series.EventIDs) != 1 {
		t.Errorf("series.event_ids = %v", out.Series.EventIDs)
	}

	for name, slug := range map[string]string{
		"archived":        f.archived.Slug,
		"no hosted event": f.onlyB.Slug,
		"empty":           f.emptySeries.Slug,
		"unknown":         "no-such-tour-" + uuid.NewString(),
	} {
		if code, body := page(slug); code != 404 {
			t.Errorf("%s: status %d, want 404: %s", name, code, body)
		}
	}
	if code, _ := f.get(t, h.HandlePublicSeriesPage, "/x", map[string]string{"org_slug": "no-such-org-" + uuid.NewString(), "series_slug": f.series.Slug}); code != 404 {
		t.Errorf("unknown org: status %d, want 404", code)
	}
}
