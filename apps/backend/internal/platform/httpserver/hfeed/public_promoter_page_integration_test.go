//go:build integration

// public_promoter_page_integration_test.go — live-PostgreSQL coverage for
// GET /v1/public/pages/{org_slug} (the promoter landing page resolver behind
// tickets.arenasoldout.com/{org_slug}).
//
// Drives the REAL handler (HandlePublicPromoterPage) against a live
// database, per AGENTS.md: "Integration tests must use real handlers + real
// dispatcher." Proves: only currently-visible events are listed (future
// published, not past or unpublished), org slugs resolve case-insensitively,
// an org with a hosted channel but zero visible events still answers 200
// with an empty list, and a channel without settings.hosted_page.enabled
// answers the same 404 as an unknown org.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/hfeed/ \
//	    -run TestPublicPromoterPageIntegration
package hfeed

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// promoterPageFixture seeds an org + hosted-page-enabled sales channel with
// an active feed token, and tears everything down (FK-safe ordering) on
// cleanup. The org slug is randomized per run (AGENTS.md: global-unique-index
// literals in integration tests must be randomized).
type promoterPageFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	q        *gen.Queries
	orgID    uuid.UUID
	orgSlug  string
	chID     uuid.UUID
	tokenID  uuid.UUID
	token    string
	venueID  uuid.UUID
	eventIDs []uuid.UUID
}

func newPromoterPageFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, hostedPageEnabled bool) *promoterPageFixture {
	t.Helper()
	q := gen.New(pool)
	run := uuid.NewString()

	org, err := q.InsertOrganization(ctx, "Promoter Page Test Org "+run, "promoter-page-org-"+run, "DE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}

	settings := []byte(`{"hosted_page":{"enabled":false}}`)
	if hostedPageEnabled {
		settings = []byte(`{"hosted_page":{"enabled":true}}`)
	}
	ch, err := q.InsertSalesChannel(ctx, org.ID, "Promoter Page Test Channel "+run, "merchant_of_record", "stripe", nil, "5.00", nil, json.RawMessage(settings))
	if err != nil {
		t.Fatalf("InsertSalesChannel: %v", err)
	}

	ft, err := q.InsertFeedToken(ctx, "promoter-page-token-"+run, ch.ID, "promoter page integration test")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}

	venue, err := q.InsertVenue(ctx, org.ID, nil, "Promoter Page Test Venue "+run, nil, nil,
		nil, nil, nil, nil, nil, nil, strPtr("Europe/Prague"), nil, nil, nil, "active")
	if err != nil {
		t.Fatalf("InsertVenue: %v", err)
	}

	f := &promoterPageFixture{
		t: t, pool: pool, q: q,
		orgID: org.ID, orgSlug: org.Slug,
		chID: ch.ID, tokenID: ft.ID, token: ft.Token, venueID: venue.ID,
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if len(f.eventIDs) > 0 {
			if _, err := pool.Exec(cleanupCtx, `DELETE FROM sessions WHERE event_id = ANY($1::uuid[])`, f.eventIDs); err != nil {
				t.Logf("cleanup: delete sessions: %v", err)
			}
			if _, err := pool.Exec(cleanupCtx, `DELETE FROM event_publications WHERE event_id = ANY($1::uuid[])`, f.eventIDs); err != nil {
				t.Logf("cleanup: delete event_publications: %v", err)
			}
			if _, err := pool.Exec(cleanupCtx, `DELETE FROM events WHERE id = ANY($1::uuid[])`, f.eventIDs); err != nil {
				t.Logf("cleanup: delete events: %v", err)
			}
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, ch.ID); err != nil {
			t.Logf("cleanup: delete agent_feed_tokens: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM sales_channels WHERE id = $1`, ch.ID); err != nil {
			t.Logf("cleanup: delete sales_channels: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM venues WHERE id = $1`, venue.ID); err != nil {
			t.Logf("cleanup: delete venues: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id = $1`, org.ID); err != nil {
			t.Logf("cleanup: delete organizations: %v", err)
		}
	})
	return f
}

func strPtr(s string) *string { return &s }

// addEvent seeds one published-or-not event with the given status, an
// optional session at the given start offset from now (nil skips the
// session), and publishes it through the fixture's feed token so it becomes
// eligible for the resolution chain. Returns the created event's slug.
func (f *promoterPageFixture) addEvent(ctx context.Context, t *testing.T, name, status string, sessionStartOffset *time.Duration) (uuid.UUID, string) {
	t.Helper()
	desc := "integration test event"
	event, err := f.q.InsertEvent(ctx, f.orgID, name, &desc, status, "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	f.eventIDs = append(f.eventIDs, event.ID)

	slug := "promoter-page-event-" + uuid.NewString()
	if _, err := f.q.UpdateEventMetadata(ctx, event.ID, f.orgID, &slug, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateEventMetadata: %v", err)
	}

	if sessionStartOffset != nil {
		start := time.Now().UTC().Add(*sessionStartOffset)
		end := start.Add(90 * time.Minute)
		if _, err := f.q.InsertSession(ctx, event.ID, f.venueID, start, end, 10, nil, "scheduled", nil, "EUR", "override"); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
	}

	if _, err := f.q.PublishEvent(ctx, event.ID, f.tokenID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}

	return event.ID, slug
}

func promoterPageHandler(pool *pgxpool.Pool) *Handler {
	q := gen.New(pool)
	return &Handler{
		publicFeedQueries: q,
		logger:            slog.Default(),
		rl:                allowAllRateLimiter{},
	}
}

type promoterPageBody struct {
	Org struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"org"`
	DefaultLocale string `json:"default_locale"`
	Events        []struct {
		ID                   string  `json:"id"`
		Slug                 string  `json:"slug"`
		Title                string  `json:"title"`
		FirstSessionAt       *string `json:"first_session_at"`
		FirstSessionTimezone *string `json:"first_session_timezone"`
		FeedToken            string  `json:"feed_token"`
	} `json:"events"`
}

// TestPublicPromoterPageIntegration_ListsOnlyFutureVisibleEvents seeds three
// events — a future published one, a past published one, and an unpublished
// (draft) one with a future session — and verifies only the future published
// event appears in the promoter page's list.
func TestPublicPromoterPageIntegration_ListsOnlyFutureVisibleEvents(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPromoterPageFixture(t, ctx, pool, true)

	future := 48 * time.Hour
	past := -48 * time.Hour
	_, futureSlug := f.addEvent(ctx, t, "Future Master Class", "published", &future)
	f.addEvent(ctx, t, "Past Master Class", "published", &past)
	f.addEvent(ctx, t, "Draft Master Class", "draft", &future)

	w := httptest.NewRecorder()
	promoterPageHandler(pool).HandlePublicPromoterPage(w, promoterPageRequest(f.orgSlug))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	var body promoterPageBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	if body.Org.Slug != f.orgSlug {
		t.Errorf("org.slug = %q, want %q", body.Org.Slug, f.orgSlug)
	}
	if len(body.Events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (body: %s)", len(body.Events), w.Body.String())
	}
	if body.Events[0].Slug != futureSlug {
		t.Errorf("events[0].slug = %q, want %q", body.Events[0].Slug, futureSlug)
	}
	if body.Events[0].FirstSessionTimezone == nil || *body.Events[0].FirstSessionTimezone != "Europe/Prague" {
		t.Errorf("events[0].first_session_timezone = %v, want Europe/Prague", body.Events[0].FirstSessionTimezone)
	}
	// The page mounts a ticket picker under every date, and a picker cannot
	// be mounted without the token the event is published through. Without
	// it here the page has to resolve each date's own page first, which is
	// one extra request per date on every visit.
	if body.Events[0].FeedToken != f.token {
		t.Errorf("events[0].feed_token = %q, want %q", body.Events[0].FeedToken, f.token)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "" {
		t.Errorf("Cache-Control header missing")
	}
}

// TestPublicPromoterPageIntegration_CaseInsensitiveOrgSlug verifies an
// upper/mixed-case org_slug path segment resolves the same org as the
// stored (lowercase) slug — an organizer printing "MasterClassTeatro" must
// still work.
func TestPublicPromoterPageIntegration_CaseInsensitiveOrgSlug(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPromoterPageFixture(t, ctx, pool, true)

	mixedCase := strings.ToUpper(f.orgSlug[:1]) + f.orgSlug[1:]
	if mixedCase == f.orgSlug {
		t.Fatalf("fixture slug %q has no lowercase-first-char to uppercase — test setup invalid", f.orgSlug)
	}

	w := httptest.NewRecorder()
	promoterPageHandler(pool).HandlePublicPromoterPage(w, promoterPageRequest(mixedCase))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 for mixed-case slug %q (body: %s)", w.Code, mixedCase, w.Body.String())
	}
	var body promoterPageBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	if body.Org.Slug != f.orgSlug {
		t.Errorf("org.slug = %q, want stored slug %q", body.Org.Slug, f.orgSlug)
	}
}

// TestPublicPromoterPageIntegration_HostedPageDisabled_404 verifies that an
// org whose only channel does NOT have settings.hosted_page.enabled=true
// answers the same 404 as an unknown org.
func TestPublicPromoterPageIntegration_HostedPageDisabled_404(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPromoterPageFixture(t, ctx, pool, false)

	w := httptest.NewRecorder()
	promoterPageHandler(pool).HandlePublicPromoterPage(w, promoterPageRequest(f.orgSlug))
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// TestPublicPromoterPageIntegration_EmptyEvents_200 verifies an org with a
// properly hosted-page-enabled channel but zero visible events answers 200
// with an empty events list, not a 404.
func TestPublicPromoterPageIntegration_EmptyEvents_200(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPromoterPageFixture(t, ctx, pool, true)

	w := httptest.NewRecorder()
	promoterPageHandler(pool).HandlePublicPromoterPage(w, promoterPageRequest(f.orgSlug))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var body promoterPageBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	if body.Events == nil {
		t.Errorf("events = nil, want an empty (non-null) array")
	}
	if len(body.Events) != 0 {
		t.Errorf("len(events) = %d, want 0", len(body.Events))
	}
}

// TestPublicPromoterPageIntegration_UnknownOrg_404 verifies an org_slug that
// matches nothing answers the same 404 as every other miss.
func TestPublicPromoterPageIntegration_UnknownOrg_404(t *testing.T) {
	pool := publicPageIntegrationPool(t)

	w := httptest.NewRecorder()
	promoterPageHandler(pool).HandlePublicPromoterPage(w, promoterPageRequest("no-such-org-"+uuid.NewString()))
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}
