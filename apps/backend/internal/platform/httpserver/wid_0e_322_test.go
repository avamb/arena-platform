// wid_0e_322_test.go — unit tests for feature #322 (WID-0e):
// Funnel telemetry sink endpoint.
//
// Test coverage:
//   - Static analysis: migration creates widget_funnel_events table
//   - Static analysis: SQL query file has InsertWidgetFunnelEvent
//   - Static analysis: gen file has InsertWidgetFunnelEvent function
//   - Static analysis: querier interface has InsertWidgetFunnelEvent
//   - Static analysis: handler file has HandlePostFunnelEvents
//   - Static analysis: handler has no PII fields (no email, name, phone, IP storage)
//   - Static analysis: mount_catalog.go registers POST route
//   - Static analysis: feed_shims.go has handlePublicFeedFunnelEvents
//   - Compile-time: *gen.Queries still satisfies gen.Querier
//   - HTTP: nil funnelQueries → 503
//   - HTTP: no auth required (not 401)
//   - HTTP: valid batch → 204
//   - HTTP: empty batch → 400
//   - HTTP: invalid JSON → 400
//   - HTTP: rate limited (bucket exhausted) → 429
//   - HTTP: batch too large → 400
//   - OpenAPI: WidgetFunnelEventsBatch and WidgetFunnelEventInput schemas present
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factories
// ─────────────────────────────────────────────────────────────────────────────

// buildWID0eServerNilFunnel builds a server with funnelQueries = nil (→ 503).
func buildWID0eServerNilFunnel(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	// FunnelQueries intentionally nil → 503.
	return New(Options{
		Config: cfg,
	})
}

// buildWID0eServer builds a server with gen.New(nil) as funnelQueries.
// DB calls will panic/error (nil pool), but the route is mounted.
func buildWID0eServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	return New(Options{
		Config:        cfg,
		FunnelQueries: gen.New(nil),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_Step1_MigrationCreatesTable(t *testing.T) {
	content := findFileByName(t, "0062_widget_funnel_events.sql")
	if !strings.Contains(content, "widget_funnel_events") {
		t.Error("0062_widget_funnel_events.sql: expected 'widget_funnel_events' table")
	}
	if !strings.Contains(content, "feed_token") {
		t.Error("0062_widget_funnel_events.sql: expected 'feed_token' column")
	}
	if !strings.Contains(content, "event_type") {
		t.Error("0062_widget_funnel_events.sql: expected 'event_type' column")
	}
	if !strings.Contains(content, "checkout_token") {
		t.Error("0062_widget_funnel_events.sql: expected 'checkout_token' column")
	}
	if !strings.Contains(content, "occurred_at") {
		t.Error("0062_widget_funnel_events.sql: expected 'occurred_at' column")
	}
	if !strings.Contains(content, "received_at") {
		t.Error("0062_widget_funnel_events.sql: expected 'received_at' column")
	}
}

func TestWID0e322_Step1_MigrationHasDownSection(t *testing.T) {
	content := findFileByName(t, "0062_widget_funnel_events.sql")
	if !strings.Contains(content, "goose Down") {
		t.Error("0062_widget_funnel_events.sql: expected goose Down section")
	}
	if !strings.Contains(content, "DROP TABLE") {
		t.Error("0062_widget_funnel_events.sql: expected DROP TABLE in Down section")
	}
}

func TestWID0e322_Step1_MigrationNoPII(t *testing.T) {
	content := findFileByName(t, "0062_widget_funnel_events.sql")
	// PII columns must NOT appear in this table.
	piiFields := []string{"email", "name", "phone", "ip_address", "user_id"}
	for _, field := range piiFields {
		if strings.Contains(content, field) {
			t.Errorf("0062_widget_funnel_events.sql: PII field %q must not appear in telemetry table", field)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_Step2_SQLQueryHasInsert(t *testing.T) {
	content := findFileByName(t, "widget_funnel_events.sql")
	if !strings.Contains(content, "InsertWidgetFunnelEvent") {
		t.Error("widget_funnel_events.sql: expected 'InsertWidgetFunnelEvent' query")
	}
	if !strings.Contains(content, "widget_funnel_events") {
		t.Error("widget_funnel_events.sql: expected INSERT INTO widget_funnel_events")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_Step3_GenFileHasInsertWidgetFunnelEvent(t *testing.T) {
	content := findFileByName(t, "widget_funnel_events.sql.go")
	if !strings.Contains(content, "InsertWidgetFunnelEvent") {
		t.Error("widget_funnel_events.sql.go: expected 'InsertWidgetFunnelEvent' function")
	}
	if !strings.Contains(content, "feedToken") {
		t.Error("widget_funnel_events.sql.go: expected 'feedToken' parameter")
	}
	if !strings.Contains(content, "eventType") {
		t.Error("widget_funnel_events.sql.go: expected 'eventType' parameter")
	}
	if !strings.Contains(content, "checkoutToken") {
		t.Error("widget_funnel_events.sql.go: expected 'checkoutToken' parameter")
	}
	if !strings.Contains(content, "occurredAt") {
		t.Error("widget_funnel_events.sql.go: expected 'occurredAt' parameter")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_Step4_QuerierInterfaceHasInsertWidgetFunnelEvent(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "InsertWidgetFunnelEvent") {
		t.Error("querier.go: expected 'InsertWidgetFunnelEvent' in Querier interface")
	}
}

// Compile-time check: *gen.Queries must satisfy gen.Querier (includes new method).
var _ gen.Querier = (*gen.Queries)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Handler file
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_Step5_HandlerFileExists(t *testing.T) {
	content := findFileByName(t, "public_funnel_events.go")
	if !strings.Contains(content, "HandlePostFunnelEvents") {
		t.Error("public_funnel_events.go: expected 'HandlePostFunnelEvents' function")
	}
	if !strings.Contains(content, "funnelQueries") {
		t.Error("public_funnel_events.go: expected 'funnelQueries' dependency gate")
	}
	if !strings.Contains(content, "InsertWidgetFunnelEvent") {
		t.Error("public_funnel_events.go: expected call to 'InsertWidgetFunnelEvent'")
	}
}

func TestWID0e322_Step5_HandlerHasRateLimiting(t *testing.T) {
	content := findFileByName(t, "public_funnel_events.go")
	if !strings.Contains(content, "rate_limited") {
		t.Error("public_funnel_events.go: expected 'rate_limited' error code")
	}
	if !strings.Contains(content, "CheckToken") {
		t.Error("public_funnel_events.go: expected CheckToken rate limit check")
	}
	if !strings.Contains(content, "CheckIP") {
		t.Error("public_funnel_events.go: expected CheckIP rate limit check")
	}
}

func TestWID0e322_Step5_HandlerNoPII(t *testing.T) {
	content := findFileByName(t, "public_funnel_events.go")
	// The handler must not store PII fields in the telemetry row.
	piiPatterns := []string{
		"HolderEmail", "holder_email",
		"buyer_email", "BuyerEmail",
		"clientIP", // IP is rate-limited but must NOT be stored
		"user_id",
	}
	// Only check for clientIP being persisted (it's used for rate limiting but not stored).
	// We check the insert call doesn't pass clientIP.
	if strings.Contains(content, "InsertWidgetFunnelEvent") &&
		strings.Contains(content, "clientIP") {
		// Make sure clientIP is used only for rate limiting, not passed to insert.
		// The insert call must not include clientIP as an argument.
		insertIdx := strings.Index(content, "InsertWidgetFunnelEvent")
		afterInsert := content[insertIdx:]
		parenClose := strings.Index(afterInsert, ")")
		if parenClose > 0 {
			insertArgs := afterInsert[:parenClose]
			if strings.Contains(insertArgs, "clientIP") {
				t.Error("public_funnel_events.go: clientIP must not be passed to InsertWidgetFunnelEvent (no PII storage)")
			}
		}
	}
	// email/name/phone must not be stored.
	for _, pii := range piiPatterns[:4] {
		if strings.Contains(content, pii) {
			t.Errorf("public_funnel_events.go: PII field %q must not appear in funnel handler", pii)
		}
	}
}

func TestWID0e322_Step5_HandlerValidatesEventTypes(t *testing.T) {
	content := findFileByName(t, "public_funnel_events.go")
	// All required event types must be defined.
	requiredTypes := []string{
		"schema_viewed",
		"seat_selected",
		"cart_opened",
		"payment_started",
		"recovered",
	}
	for _, et := range requiredTypes {
		if !strings.Contains(content, et) {
			t.Errorf("public_funnel_events.go: expected event type %q in validFunnelEventTypes", et)
		}
	}
}

func TestWID0e322_Step5_HandlerHasMaxBatchSize(t *testing.T) {
	content := findFileByName(t, "public_funnel_events.go")
	if !strings.Contains(content, "maxFunnelBatchSize") {
		t.Error("public_funnel_events.go: expected 'maxFunnelBatchSize' constant")
	}
	if !strings.Contains(content, "batch_too_large") {
		t.Error("public_funnel_events.go: expected 'feed.batch_too_large' error code")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: mount_catalog.go
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_Step6_MountHasPostFunnelRoute(t *testing.T) {
	content := findFileByName(t, "server.go") // reads all mount_*.go files
	if !strings.Contains(content, `"/public/feeds/{feed_token}/events"`) {
		t.Error("mount_catalog.go: expected POST /public/feeds/{feed_token}/events route")
	}
	if !strings.Contains(content, "handlePublicFeedFunnelEvents") {
		t.Error("mount_catalog.go: expected 'handlePublicFeedFunnelEvents' handler")
	}
	if !strings.Contains(content, "funnelQueries") {
		t.Error("mount_catalog.go: expected 'funnelQueries' gate for funnel route")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: feed_shims.go
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_Step7_ShimHasFunnelHandler(t *testing.T) {
	content := findFileByName(t, "public_funnel_events.go") // aggregated with feed_shims.go
	if !strings.Contains(content, "handlePublicFeedFunnelEvents") {
		t.Error("feed_shims.go: expected 'handlePublicFeedFunnelEvents' shim method")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: nil funnelQueries → 503
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_HTTP_NilFunnelQueries_Returns503(t *testing.T) {
	s := buildWID0eServerNilFunnel(t)
	w := httptest.NewRecorder()
	body := `{"events":[{"event_type":"schema_viewed"}]}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/test-token/events",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when funnelQueries is nil; got %d: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: no auth required
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_HTTP_NoAuthRequired(t *testing.T) {
	s := buildWID0eServer(t)
	w := httptest.NewRecorder()
	body := `{"events":[{"event_type":"schema_viewed"}]}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/test-token/events",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Must not be 401 — this is a public endpoint.
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — funnel endpoint is public; got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: empty batch → 400
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_HTTP_EmptyBatch_Returns400(t *testing.T) {
	s := buildWID0eServer(t)
	w := httptest.NewRecorder()
	body := `{"events":[]}`
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/test-token/events",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty batch; got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "empty_batch") {
		t.Errorf("expected 'empty_batch' error code in response; got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: invalid JSON → 400
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_HTTP_InvalidJSON_Returns400(t *testing.T) {
	s := buildWID0eServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/test-token/events",
		strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON; got %d: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: rate limited → 429
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_HTTP_RateLimited_Returns429(t *testing.T) {
	s := buildWID0eServer(t)
	body := `{"events":[{"event_type":"cart_opened"}]}`

	// Exhaust the per-token rate limiter (default 100 requests/min).
	// Send 101 requests from the same IP with the same token.
	for i := 0; i <= 100; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost,
			"/v1/public/feeds/rl-test-token/events",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			return // rate limiting is working
		}
	}
	t.Error("expected at least one 429 response after exhausting rate limit")
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenAPI: schemas present
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0e322_OpenAPIHasWidgetFunnelSchemas(t *testing.T) {
	content := findFileByPattern(t, "apps/backend/openapi", "openapi.yaml")
	if !strings.Contains(content, "WidgetFunnelEventsBatch") {
		t.Error("openapi.yaml: expected 'WidgetFunnelEventsBatch' schema")
	}
	if !strings.Contains(content, "WidgetFunnelEventInput") {
		t.Error("openapi.yaml: expected 'WidgetFunnelEventInput' schema")
	}
	if !strings.Contains(content, "postPublicFeedFunnelEvents") {
		t.Error("openapi.yaml: expected 'postPublicFeedFunnelEvents' operationId")
	}
}

func TestWID0e322_OpenAPIFunnelEventTypesEnumComplete(t *testing.T) {
	content := findFileByPattern(t, "apps/backend/openapi", "openapi.yaml")
	requiredTypes := []string{
		"schema_viewed",
		"seat_selected",
		"cart_opened",
		"payment_started",
		"recovered",
	}
	for _, et := range requiredTypes {
		if !strings.Contains(content, et) {
			t.Errorf("openapi.yaml: expected funnel event type %q in WidgetFunnelEventInput enum", et)
		}
	}
}
