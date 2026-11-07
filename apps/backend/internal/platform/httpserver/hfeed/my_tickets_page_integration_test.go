//go:build integration

// my_tickets_page_integration_test.go - the "my tickets" link request keeps
// page_url only when it lies on the feed token's OWN channel site origins. A
// page of another organizer's site (allowed by the global CORS list, but not
// by this channel) falls back to the tickets site's /my-tickets page, so a
// token of one organizer cannot mail a genuine link to another's page.
package hfeed

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsite"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
)

func latestLinkPage(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, email string) (page string, jobs int) {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id, COALESCE(page_url, '') FROM my_tickets_link_requests
	      WHERE org_id = $1 AND email_normalized = $2 ORDER BY created_at DESC LIMIT 1`, orgID, email).Scan(&id, &page); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'request_id' = $2`,
		mytickets.JobTypeLinkEmail, id.String()).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	return page, jobs
}

func TestSiteP7_MyTicketsLinkPageMustBeTheChannelsOwnSite(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	a := seedSiteOrder(t, pool, "page.a@example.com")
	b := seedSiteOrder(t, pool, "page.b@example.com")
	siteExec(t, pool, `UPDATE sales_channels SET settings = jsonb_set(settings, '{site}', '{"origins":["https://site-a.example"]}'::jsonb, true) WHERE id = $1`, a.channelID)
	siteExec(t, pool, `UPDATE sales_channels SET settings = jsonb_set(settings, '{site}', '{"origins":["https://site-b.example"]}'::jsonb, true) WHERE id = $1`, b.channelID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1`, mytickets.JobTypeLinkEmail)
	})

	h := siteHandler(pool).WithMyTicketsPageCheck(func(ctx context.Context, feedToken, pageURL string) bool {
		ch, err := hsite.ChannelByFeedToken(ctx, pool, feedToken)
		return err == nil && ch.AllowsPage(pageURL)
	})
	request := func(feedToken, body string) {
		t.Helper()
		// A fresh client address per request: the per-IP hourly limit of
		// the shared fixed test address must not swallow the request.
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", rand.IntN(250)+1)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("feed_token", feedToken)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.HandleMyTicketsRequestLink(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("link request: %d %s", w.Code, w.Body.String())
		}
	}

	// Organizer A's token naming organizer B's page: the page is dropped and
	// the letter links the tickets site instead.
	request(a.feedToken, `{"email":"page.a@example.com","page_url":"https://site-b.example/my-tickets/"}`)
	page, jobs := latestLinkPage(t, pool, a.orgID, "page.a@example.com")
	if page != "" || jobs != 1 {
		t.Fatalf("foreign page: stored page=%q jobs=%d, want empty page and one job", page, jobs)
	}
	if got := delivery.MyTicketsLinkTarget(page, "https://tickets.example"); got != "https://tickets.example/my-tickets" {
		t.Fatalf("foreign page: link target %q, want the tickets site fallback", got)
	}

	// A's own page is kept.
	request(a.feedToken, `{"email":"page.a@example.com","page_url":"https://site-a.example/my-tickets/"}`)
	if page, _ := latestLinkPage(t, pool, a.orgID, "page.a@example.com"); page != "https://site-a.example/my-tickets/" {
		t.Fatalf("own page: stored page=%q", page)
	}
}
