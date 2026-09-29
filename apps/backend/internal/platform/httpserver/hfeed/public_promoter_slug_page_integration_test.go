//go:build integration

// public_promoter_slug_page_integration_test.go — a promoter's OWN pages
// (migration 0117): tickets.arenasoldout.com/{promoter_slug} lists only the
// events linked to that promoter, /{promoter_slug}/{event_slug} resolves one
// of them, and the organization's own pages keep working unchanged.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/hfeed/ \
//	    -run TestPublicPromoterSlugPage
package hfeed

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPublicPromoterSlugPage_ListsOnlyThePromotersEvents(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPromoterPageFixture(t, ctx, pool, true)

	// Two promoters of the organization, each with a page; one event each,
	// plus one event of the organization itself (no promoter).
	run := uuid.NewString()[:8]
	slugA := "kolybel-" + run
	slugB := "partner-" + run
	promA, err := f.q.InsertOrgPromoter(ctx, f.orgID, "Семейный Театр Колыбель "+run, nil, nil, nil, &slugA)
	if err != nil {
		t.Fatalf("InsertOrgPromoter A: %v", err)
	}
	promB, err := f.q.InsertOrgPromoter(ctx, f.orgID, "Partner "+run, nil, nil, nil, &slugB)
	if err != nil {
		t.Fatalf("InsertOrgPromoter B: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM event_promoters WHERE promoter_id = ANY($1::uuid[])`, []uuid.UUID{promA.ID, promB.ID})
		_, _ = pool.Exec(context.Background(), `DELETE FROM org_promoters WHERE id = ANY($1::uuid[])`, []uuid.UUID{promA.ID, promB.ID})
	})
	future := 48 * time.Hour
	eventA, eventASlug := f.addEvent(ctx, t, "Серебряное копытце", "published", &future)
	eventB, _ := f.addEvent(ctx, t, "Partner show", "published", &future)
	_, orgEventSlug := f.addEvent(ctx, t, "Org's own show", "published", &future)
	if err := f.q.SetEventPromoter(ctx, eventA, f.orgID, promA.ID); err != nil {
		t.Fatalf("SetEventPromoter A: %v", err)
	}
	if err := f.q.SetEventPromoter(ctx, eventB, f.orgID, promB.ID); err != nil {
		t.Fatalf("SetEventPromoter B: %v", err)
	}
	h := promoterPageHandler(pool)

	// 1. The promoter's landing page: its slug and name, its one event.
	w := httptest.NewRecorder()
	h.HandlePublicPromoterPage(w, promoterPageRequest(strings.ToUpper(slugA)))
	if w.Code != 200 {
		t.Fatalf("promoter page: status = %d (body: %s)", w.Code, w.Body.String())
	}
	var page promoterPageBody
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Org.Slug != slugA || page.Org.Name != promA.Name {
		t.Errorf("org = %+v, want the promoter's slug %q and name %q", page.Org, slugA, promA.Name)
	}
	if len(page.Events) != 1 || page.Events[0].Slug != eventASlug {
		t.Fatalf("promoter page should list exactly the promoter's event: %s", w.Body.String())
	}
	if page.Events[0].FeedToken != f.token {
		t.Errorf("events[0].feed_token = %q, want the organization's channel token", page.Events[0].FeedToken)
	}

	// 2. The promoter's event page resolves; the other promoter's event and
	//    the organization's own event do NOT resolve under this promoter.
	w = httptest.NewRecorder()
	h.HandlePublicPage(w, pageRequest(slugA, eventASlug))
	if w.Code != 200 {
		t.Fatalf("promoter event page: status = %d (body: %s)", w.Code, w.Body.String())
	}
	var ev struct {
		Org struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"org"`
		Event struct {
			Slug string `json:"slug"`
		} `json:"event"`
		FeedToken string `json:"feed_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Org.Slug != slugA || ev.Org.Name != promA.Name || ev.Event.Slug != eventASlug || ev.FeedToken != f.token {
		t.Errorf("promoter event page = %+v", ev)
	}
	for _, other := range []string{orgEventSlug} {
		w = httptest.NewRecorder()
		h.HandlePublicPage(w, pageRequest(slugA, other))
		if w.Code != 404 {
			t.Errorf("%s under promoter %s: status = %d, want 404", other, slugA, w.Code)
		}
	}

	// 3. The organization's own pages are unchanged: its landing page lists
	//    all three events, its event page still resolves the promoter's
	//    event too (an old link keeps working).
	w = httptest.NewRecorder()
	h.HandlePublicPromoterPage(w, promoterPageRequest(f.orgSlug))
	if w.Code != 200 {
		t.Fatalf("org page: status = %d", w.Code)
	}
	page = promoterPageBody{}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Org.Slug != f.orgSlug || len(page.Events) != 3 {
		t.Errorf("org page should list every event of the organization: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	h.HandlePublicPage(w, pageRequest(f.orgSlug, eventASlug))
	if w.Code != 200 {
		t.Errorf("org event page for the promoter's event: status = %d, want 200", w.Code)
	}

	// 4. An archived promoter has no page; an unknown slug is the same 404.
	if _, err := f.q.UpdateOrgPromoter(ctx, promB.ID, f.orgID, promB.Name, nil, nil, nil, true, &slugB); err != nil {
		t.Fatalf("archive B: %v", err)
	}
	for _, slug := range []string{slugB, "no-such-promoter-" + run} {
		w = httptest.NewRecorder()
		h.HandlePublicPromoterPage(w, promoterPageRequest(slug))
		if w.Code != 404 {
			t.Errorf("%s: status = %d, want 404", slug, w.Code)
		}
	}

	// 5. The slug namespace is shared with organizations.
	taken, err := f.q.PromoterSlugTaken(ctx, strings.ToUpper(f.orgSlug))
	if err != nil || !taken {
		t.Errorf("an organization's slug must count as taken: %v %v", taken, err)
	}
	taken, err = f.q.PromoterSlugTaken(ctx, slugA)
	if err != nil || !taken {
		t.Errorf("a promoter's slug must count as taken: %v %v", taken, err)
	}
	taken, err = f.q.PromoterSlugTaken(ctx, "free-"+run)
	if err != nil || taken {
		t.Errorf("a fresh slug must be free: %v %v", taken, err)
	}
}
