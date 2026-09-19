//go:build integration

// public_page_integration_test.go — live-PostgreSQL coverage for
// GET /v1/public/pages/{org_slug}/{event_slug} (the hosted sales page
// resolver behind tickets.arenasoldout.com/{org_slug}/{event_slug}).
//
// Drives the REAL handler (HandlePublicPage) against a live database, per
// AGENTS.md: "Integration tests must use real handlers + real dispatcher."
// Proves the full resolution chain end to end: active org by slug -> active
// published event by slug -> a hosted_page-enabled, active channel of the
// SAME org -> its newest active feed token; plus the three ways that chain
// can legitimately miss (flag off, revoked token, unknown slug), all of
// which must answer the SAME indistinguishable 404.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/hfeed/ \
//	    -run TestPublicPageIntegration
package hfeed

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func publicPageIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// publicPageFixture seeds an org + published event + sales channel with the
// hosted_page settings flag + feed token + event publication, and tears
// everything down (FK-safe ordering) on cleanup. The org/event slugs are
// randomized per run (AGENTS.md: global-unique-index literals in integration
// tests must be randomized).
type publicPageFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	q         *gen.Queries
	orgID     uuid.UUID
	orgSlug   string
	eventID   uuid.UUID
	eventSlug string
	chID      uuid.UUID
	tokenID   uuid.UUID
	token     string
}

func newPublicPageFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, hostedPageEnabled bool) *publicPageFixture {
	t.Helper()
	q := gen.New(pool)
	run := uuid.NewString()

	org, err := q.InsertOrganization(ctx, "Public Page Test Org "+run, "public-page-org-"+run, "DE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}

	desc := "integration test event"
	event, err := q.InsertEvent(ctx, org.ID, "Public Page Test Event "+run, &desc, "published", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	eventSlug := "public-page-event-" + run
	if _, err := q.UpdateEventMetadata(ctx, event.ID, org.ID, &eventSlug, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateEventMetadata: %v", err)
	}

	settings := []byte(`{"hosted_page":{"enabled":false}}`)
	if hostedPageEnabled {
		settings = []byte(`{"hosted_page":{"enabled":true}}`)
	}
	ch, err := q.InsertSalesChannel(ctx, org.ID, "Public Page Test Channel "+run, "merchant_of_record", "stripe", nil, "5.00", nil, json.RawMessage(settings))
	if err != nil {
		t.Fatalf("InsertSalesChannel: %v", err)
	}

	ft, err := q.InsertFeedToken(ctx, "public-page-token-"+run, ch.ID, "public page integration test")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}

	if _, err := q.PublishEvent(ctx, event.ID, ft.ID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}

	f := &publicPageFixture{
		t: t, pool: pool, q: q,
		orgID: org.ID, orgSlug: org.Slug,
		eventID: event.ID, eventSlug: eventSlug,
		chID: ch.ID, tokenID: ft.ID, token: ft.Token,
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM event_publications WHERE event_id = $1`, event.ID); err != nil {
			t.Logf("cleanup: delete event_publications: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, ch.ID); err != nil {
			t.Logf("cleanup: delete agent_feed_tokens: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM sales_channels WHERE id = $1`, ch.ID); err != nil {
			t.Logf("cleanup: delete sales_channels: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM events WHERE id = $1`, event.ID); err != nil {
			t.Logf("cleanup: delete events: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id = $1`, org.ID); err != nil {
			t.Logf("cleanup: delete organizations: %v", err)
		}
	})
	return f
}

func publicPageHandler(pool *pgxpool.Pool) *Handler {
	q := gen.New(pool)
	return &Handler{
		publicFeedQueries: q,
		logger:            slog.Default(),
		rl:                allowAllRateLimiter{},
	}
}

// TestPublicPageIntegration_Resolves200WithToken is the happy path: an org
// with a published event, published to a channel flagged
// settings.hosted_page.enabled=true, resolves to 200 with that channel's
// feed token.
func TestPublicPageIntegration_Resolves200WithToken(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)

	w := httptest.NewRecorder()
	publicPageHandler(pool).HandlePublicPage(w, pageRequest(f.orgSlug, f.eventSlug))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var body struct {
		Org struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"org"`
		Event struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"event"`
		FeedToken string `json:"feed_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	if body.Org.Slug != f.orgSlug {
		t.Errorf("org.slug = %q, want %q", body.Org.Slug, f.orgSlug)
	}
	if body.Event.ID != f.eventID.String() {
		t.Errorf("event.id = %q, want %q", body.Event.ID, f.eventID.String())
	}
	if body.Event.Slug != f.eventSlug {
		t.Errorf("event.slug = %q, want %q", body.Event.Slug, f.eventSlug)
	}
	if body.FeedToken != f.token {
		t.Errorf("feed_token = %q, want %q", body.FeedToken, f.token)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "" {
		t.Errorf("Cache-Control header missing")
	}
}

// TestPublicPageIntegration_HostedPageDisabled_404 verifies that a channel
// without settings.hosted_page.enabled=true never resolves, even though the
// event is published to it and otherwise eligible.
func TestPublicPageIntegration_HostedPageDisabled_404(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, false)

	w := httptest.NewRecorder()
	publicPageHandler(pool).HandlePublicPage(w, pageRequest(f.orgSlug, f.eventSlug))
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// TestPublicPageIntegration_RevokedToken_404 verifies that revoking the
// channel's feed token (is_active=false) turns the same resolvable page into
// a 404 — the resolution requires an ACTIVE token, not merely an existing one.
func TestPublicPageIntegration_RevokedToken_404(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)

	if _, err := f.q.RevokeFeedToken(ctx, f.tokenID, f.chID); err != nil {
		t.Fatalf("RevokeFeedToken: %v", err)
	}

	w := httptest.NewRecorder()
	publicPageHandler(pool).HandlePublicPage(w, pageRequest(f.orgSlug, f.eventSlug))
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// TestPublicPageIntegration_UnknownSlug_404 verifies an org/event slug pair
// that matches nothing answers the same 404 as every other miss.
func TestPublicPageIntegration_UnknownSlug_404(t *testing.T) {
	pool := publicPageIntegrationPool(t)

	w := httptest.NewRecorder()
	publicPageHandler(pool).HandlePublicPage(w, pageRequest("no-such-org-"+uuid.NewString(), "no-such-event"))
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// TestPublicPageIntegration_CaseInsensitiveSlugs_200 verifies that BOTH
// org_slug and event_slug resolve case-insensitively — an organizer printing
// "MasterClassTeatro" (or a legacy mixed-case event slug written before
// write-time lowercasing existed on org slugs, which event slugs still never
// get) must still resolve.
func TestPublicPageIntegration_CaseInsensitiveSlugs_200(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)

	upperOrg := strings.ToUpper(f.orgSlug)
	upperEvent := strings.ToUpper(f.eventSlug)

	w := httptest.NewRecorder()
	publicPageHandler(pool).HandlePublicPage(w, pageRequest(upperOrg, upperEvent))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 for uppercased slugs %q/%q (body: %s)", w.Code, upperOrg, upperEvent, w.Body.String())
	}
	var body struct {
		Event struct {
			ID string `json:"id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	if body.Event.ID != f.eventID.String() {
		t.Errorf("event.id = %q, want %q", body.Event.ID, f.eventID.String())
	}
}
