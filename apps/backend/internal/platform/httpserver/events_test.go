// events_test.go — unit tests for feature #125 (Event model + CRUD).
//
// Test coverage:
//   Step 1: Migration file 0014_events.sql — schema, status enum, date CHECK, RBAC seeds
//   Step 2: CRUD endpoints — route mounting, auth-gating, request validation
//   Step 3: Status transition guards — allowed and forbidden transitions
//   Step 4: i18n name/description — query file + gen file structure
//   Step 5: Integration: date invariant validation (end_at <= start_at → 400)
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/google/uuid"
)

const eventTestActorID = "00000000-0000-0000-0000-000000000002"

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for event route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildEventServer builds a Server with stub auth, event routes fully
// mounted, and a dbDownPool so real DB operations never execute.
func buildEventServer(t *testing.T) *Server {
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
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildEventServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// EventQueries non-nil so event route conditionals pass.
		EventQueries: gen.New(nil),
	})
}

// mintEventToken mints a dev JWT for event route tests.
func mintEventToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + eventTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintEventToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintEventToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatal("mintEventToken: empty token in response")
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if content == "" {
		t.Fatal("0014_events.sql is empty or not found")
	}
}

func TestEvent125_MigrationHasGooseDirectives(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' directive")
	}
}

func TestEvent125_MigrationCreateEventsTable(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "CREATE TABLE events") {
		t.Error("migration missing CREATE TABLE events")
	}
}

func TestEvent125_MigrationHasOrgIDColumn(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "org_id") {
		t.Error("migration missing org_id column")
	}
}

func TestEvent125_MigrationHasVenueIDColumn(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "venue_id") {
		t.Error("migration missing venue_id column")
	}
}

func TestEvent125_MigrationHasStatusEnum(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	for _, status := range []string{"draft", "published", "cancelled", "archived"} {
		if !strings.Contains(content, "'"+status+"'") {
			t.Errorf("migration missing status value %q in CHECK constraint", status)
		}
	}
}

func TestEvent125_MigrationHasVisibilityEnum(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	for _, vis := range []string{"public", "private", "unlisted"} {
		if !strings.Contains(content, "'"+vis+"'") {
			t.Errorf("migration missing visibility value %q in CHECK constraint", vis)
		}
	}
}

func TestEvent125_MigrationHasDateOrderCheck(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "events_date_order") {
		t.Error("migration missing events_date_order CHECK constraint")
	}
	if !strings.Contains(content, "end_at > start_at") {
		t.Error("migration missing end_at > start_at date order check")
	}
}

func TestEvent125_MigrationHasSoftDelete(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "deleted_at") {
		t.Error("migration missing deleted_at soft-delete column")
	}
}

func TestEvent125_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	for _, perm := range []string{"event.create", "event.read", "event.update", "event.delete", "event.publish"} {
		if !strings.Contains(content, "'"+perm+"'") {
			t.Errorf("migration missing RBAC permission %q", perm)
		}
	}
}

func TestEvent125_MigrationHasIndexes(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "events_org_id_active") {
		t.Error("migration missing events_org_id_active index")
	}
}

func TestEvent125_MigrationDropsTableInDown(t *testing.T) {
	content := findFileByName(t, "0014_events.sql")
	if !strings.Contains(content, "DROP TABLE IF EXISTS events") {
		t.Error("migration Down section missing DROP TABLE IF EXISTS events")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Route auth gating — all endpoints return 401 without a JWT
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_ListEventsRequiresAuth(t *testing.T) {
	s := buildEventServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/events without auth: got %d, want 401", w.Code)
	}
}

func TestEvent125_GetEventRequiresAuth(t *testing.T) {
	s := buildEventServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/events/00000000-0000-0000-0000-000000000001", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/events/{id} without auth: got %d, want 401", w.Code)
	}
}

func TestEvent125_ListEventsByOrgRequiresAuth(t *testing.T) {
	s := buildEventServer(t)
	orgID := "00000000-0000-0000-0000-000000000001"
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID+"/events", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/organizations/{org_id}/events without auth: got %d, want 401", w.Code)
	}
}

func TestEvent125_CreateEventRequiresAuth(t *testing.T) {
	s := buildEventServer(t)
	orgID := "00000000-0000-0000-0000-000000000001"
	body := `{"name":"Test Event","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/organizations/{org_id}/events without auth: got %d, want 401", w.Code)
	}
}

func TestEvent125_UpdateEventRequiresAuth(t *testing.T) {
	s := buildEventServer(t)
	orgID := "00000000-0000-0000-0000-000000000001"
	eventID := "00000000-0000-0000-0000-000000000002"
	body := `{"name":"Updated"}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/organizations/"+orgID+"/events/"+eventID,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PATCH /v1/organizations/{org_id}/events/{id} without auth: got %d, want 401", w.Code)
	}
}

func TestEvent125_DeleteEventRequiresAuth(t *testing.T) {
	s := buildEventServer(t)
	orgID := "00000000-0000-0000-0000-000000000001"
	eventID := "00000000-0000-0000-0000-000000000002"
	req := httptest.NewRequest(http.MethodDelete, "/v1/organizations/"+orgID+"/events/"+eventID, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/organizations/{org_id}/events/{id} without auth: got %d, want 401", w.Code)
	}
}

func TestEvent125_UpdateEventStatusRequiresAuth(t *testing.T) {
	s := buildEventServer(t)
	orgID := "00000000-0000-0000-0000-000000000001"
	eventID := "00000000-0000-0000-0000-000000000002"
	body := `{"status":"published"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events/"+eventID+"/status",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/organizations/{org_id}/events/{id}/status without auth: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Request validation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_CreateEvent_EmptyBodyReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body: got %d, want 400", w.Code)
	}
}

func TestEvent125_CreateEvent_InvalidJSONReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(`{not valid json}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON: got %d, want 400", w.Code)
	}
}

func TestEvent125_CreateEvent_MissingNameReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing name: got %d, want 400", w.Code)
	}
	if code := eventErrorCode(t, w); code != "event.invalid_name" {
		t.Errorf("missing name: got code %q, want event.invalid_name", code)
	}
}

func TestEvent125_CreateEvent_MissingStartAtReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Test Event","end_at":"2026-07-01T12:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing start_at: got %d, want 400", w.Code)
	}
}

func TestEvent125_CreateEvent_MissingEndAtReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Test Event","start_at":"2026-07-01T10:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing end_at: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Date invariant — end_at must be strictly after start_at
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_DateInvariant_EndAtBeforeStartAtReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	// end_at is BEFORE start_at — must be rejected.
	body := `{"name":"Bad Dates","start_at":"2026-07-01T12:00:00Z","end_at":"2026-07-01T10:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("end_at before start_at: got %d, want 400", w.Code)
	}
	if code := eventErrorCode(t, w); code != "event.invalid_date_range" {
		t.Errorf("end_at before start_at: got code %q, want event.invalid_date_range", code)
	}
}

func TestEvent125_DateInvariant_EndAtEqualStartAtReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	// end_at EQUALS start_at — must be rejected (strict >).
	body := `{"name":"Same Time","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T10:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("end_at == start_at: got %d, want 400", w.Code)
	}
}

func TestEvent125_DateInvariant_ValidDatesPassValidation(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	// Valid dates (end_at > start_at). Will hit DB → 503 (dbDownPool), not 400.
	body := `{"name":"Good Dates","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	// Validation passed (date check OK); DB error expected from dbDownPool.
	if w.Code == http.StatusBadRequest {
		t.Errorf("valid dates rejected: got 400 with code %q", eventErrorCode(t, w))
	}
}

func TestEvent125_DateInvariant_InvalidStartAtFormatReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Bad Format","start_at":"not-a-date","end_at":"2026-07-01T12:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid start_at format: got %d, want 400", w.Code)
	}
	if code := eventErrorCode(t, w); code != "event.invalid_start_at" {
		t.Errorf("invalid start_at: got code %q, want event.invalid_start_at", code)
	}
}

func TestEvent125_DateInvariant_InvalidEndAtFormatReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Bad Format","start_at":"2026-07-01T10:00:00Z","end_at":"bad-date"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid end_at format: got %d, want 400", w.Code)
	}
	if code := eventErrorCode(t, w); code != "event.invalid_end_at" {
		t.Errorf("invalid end_at: got code %q, want event.invalid_end_at", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Status transition guards (unit tests — logic only)
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_StatusTransition_AllowedTransitions(t *testing.T) {
	allowed := []struct {
		from, to string
	}{
		{"draft", "published"},
		{"draft", "cancelled"},
		{"published", "cancelled"},
		{"published", "archived"},
		{"cancelled", "archived"},
	}
	for _, tc := range allowed {
		if !isValidEventTransition(tc.from, tc.to) {
			t.Errorf("transition %q → %q: expected ALLOWED, got FORBIDDEN", tc.from, tc.to)
		}
	}
}

func TestEvent125_StatusTransition_ForbiddenTransitions(t *testing.T) {
	forbidden := []struct {
		from, to string
	}{
		{"draft", "archived"},
		{"published", "draft"},
		{"cancelled", "draft"},
		{"cancelled", "published"},
		{"archived", "draft"},
		{"archived", "published"},
		{"archived", "cancelled"},
	}
	for _, tc := range forbidden {
		if isValidEventTransition(tc.from, tc.to) {
			t.Errorf("transition %q → %q: expected FORBIDDEN, got ALLOWED", tc.from, tc.to)
		}
	}
}

func TestEvent125_StatusTransition_UnknownFromStatusForbidden(t *testing.T) {
	if isValidEventTransition("unknown", "published") {
		t.Error("transition from unknown status: expected FORBIDDEN, got ALLOWED")
	}
}

func TestEvent125_StatusTransition_NoopSameStatus(t *testing.T) {
	// Same-status "transition" is handled as a no-op in the handler (not via
	// isValidEventTransition), so the function correctly returns false for it.
	if isValidEventTransition("draft", "draft") {
		t.Error("same-status 'transition' should not be in the transition table")
	}
}

func TestEvent125_StatusTransitionEndpoint_EmptyBodyReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"
	eventID := "00000000-0000-0000-0000-000000000002"

	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events/"+eventID+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status endpoint empty body: got %d, want 400", w.Code)
	}
}

func TestEvent125_StatusTransitionEndpoint_InvalidStatusValueReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"
	eventID := "00000000-0000-0000-0000-000000000002"

	body := `{"status":"unknown_value"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events/"+eventID+"/status",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid status value: got %d, want 400", w.Code)
	}
	if code := eventErrorCode(t, w); code != "event.invalid_status" {
		t.Errorf("invalid status: got code %q, want event.invalid_status", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Visibility validation
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_CreateEvent_InvalidVisibilityReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Bad Vis","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z","visibility":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid visibility: got %d, want 400", w.Code)
	}
	if code := eventErrorCode(t, w); code != "event.invalid_visibility" {
		t.Errorf("invalid visibility: got code %q, want event.invalid_visibility", code)
	}
}

func TestEvent125_CreateEvent_InvalidStatusValueReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Bad Status","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z","status":"pending"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid status: got %d, want 400", w.Code)
	}
}

func TestEvent125_CreateEvent_InvalidVenueIDReturns400(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Bad Venue","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z","venue_id":"not-a-uuid"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid venue_id: got %d, want 400", w.Code)
	}
	if code := eventErrorCode(t, w); code != "event.invalid_venue_id" {
		t.Errorf("invalid venue_id: got code %q, want event.invalid_venue_id", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: sqlc query file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if content == "" {
		t.Fatal("events.sql query file is empty or not found")
	}
}

func TestEvent125_QueryFileHasInsertEvent(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "InsertEvent") {
		t.Error("events.sql missing InsertEvent query")
	}
}

func TestEvent125_QueryFileHasGetEventByID(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "GetEventByID") {
		t.Error("events.sql missing GetEventByID query")
	}
}

func TestEvent125_QueryFileHasListEvents(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "ListEvents") {
		t.Error("events.sql missing ListEvents query")
	}
}

func TestEvent125_QueryFileHasListEventsByOrg(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "ListEventsByOrg") {
		t.Error("events.sql missing ListEventsByOrg query")
	}
}

func TestEvent125_QueryFileHasUpdateEvent(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "UpdateEvent") {
		t.Error("events.sql missing UpdateEvent query")
	}
}

func TestEvent125_QueryFileHasUpdateEventStatus(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "UpdateEventStatus") {
		t.Error("events.sql missing UpdateEventStatus query")
	}
}

func TestEvent125_QueryFileHasSoftDeleteEvent(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "SoftDeleteEvent") {
		t.Error("events.sql missing SoftDeleteEvent query")
	}
}

func TestEvent125_QueryFileHasI18nQueries(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "UpsertEventI18nName") {
		t.Error("events.sql missing UpsertEventI18nName query")
	}
	if !strings.Contains(content, "UpsertEventI18nDescription") {
		t.Error("events.sql missing UpsertEventI18nDescription query")
	}
}

func TestEvent125_QueryFileHasI18nJoins(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "i18n_text") {
		t.Error("events.sql missing i18n_text joins for localized name/description")
	}
	if !strings.Contains(content, "event.name") {
		t.Error("events.sql missing 'event.name' i18n_text namespace")
	}
	if !strings.Contains(content, "event.description") {
		t.Error("events.sql missing 'event.description' i18n_text namespace")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Generated Go file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_GenFileExists(t *testing.T) {
	content := findFileByName(t, "events.sql.go")
	if content == "" {
		t.Fatal("events.sql.go gen file is empty or not found")
	}
}

func TestEvent125_GenFileHasEventRowStruct(t *testing.T) {
	content := findFileByName(t, "events.sql.go")
	if !strings.Contains(content, "type EventRow struct") {
		t.Error("events.sql.go missing EventRow struct")
	}
}

func TestEvent125_GenFileEventRowHasRequiredFields(t *testing.T) {
	content := findFileByName(t, "events.sql.go")
	for _, field := range []string{
		"ID", "OrgID", "VenueID", "Name", "Description",
		"Status", "StartAt", "EndAt", "Visibility", "ImageURL",
		"CreatedAt", "UpdatedAt", "DeletedAt",
	} {
		if !strings.Contains(content, field) {
			t.Errorf("events.sql.go EventRow missing field %q", field)
		}
	}
}

func TestEvent125_GenFileEventRowNullableFields(t *testing.T) {
	content := findFileByName(t, "events.sql.go")
	// VenueID is nullable (*uuid.UUID)
	if !strings.Contains(content, "*uuid.UUID") {
		t.Error("events.sql.go EventRow VenueID should be *uuid.UUID (nullable)")
	}
	// Description is nullable (*string)
	if !strings.Contains(content, "*string") {
		t.Error("events.sql.go EventRow Description/ImageURL should be *string (nullable)")
	}
}

func TestEvent125_GenFileHasAllMethods(t *testing.T) {
	content := findFileByName(t, "events.sql.go")
	for _, method := range []string{
		"InsertEvent", "GetEventByID", "GetEventRaw", "ListEvents", "ListEventsByOrg",
		"UpdateEvent", "UpdateEventStatus", "SoftDeleteEvent",
		"UpsertEventI18nName", "UpsertEventI18nDescription",
	} {
		if !strings.Contains(content, "func (q *Queries) "+method) {
			t.Errorf("events.sql.go missing method %q", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time guard: *gen.Queries must satisfy Querier
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_QuerierInterfaceSatisfied(t *testing.T) {
	// This is a compile-time check embedded in a test function.
	// If gen.Queries does not satisfy gen.Querier, the file won't compile.
	var _ gen.Querier = (*gen.Queries)(nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Response shape tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_EventFromRowProducesCorrectShape(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(24 * time.Hour)
	end := now.Add(26 * time.Hour)
	desc := "A wonderful event"
	imgURL := "https://example.com/image.jpg"

	row := gen.EventRow{
		ID:          mustParseUUID(t, "00000000-0000-0000-0000-000000000010"),
		OrgID:       mustParseUUID(t, "00000000-0000-0000-0000-000000000020"),
		Name:        "My Event",
		Description: &desc,
		Status:      "draft",
		StartAt:     start,
		EndAt:       end,
		Visibility:  "public",
		ImageURL:    &imgURL,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	resp := eventFromRow(row)

	if resp.ID != "00000000-0000-0000-0000-000000000010" {
		t.Errorf("ID: got %q", resp.ID)
	}
	if resp.OrgID != "00000000-0000-0000-0000-000000000020" {
		t.Errorf("OrgID: got %q", resp.OrgID)
	}
	if resp.VenueID != nil {
		t.Error("VenueID should be nil when EventRow.VenueID is nil")
	}
	if resp.Name != "My Event" {
		t.Errorf("Name: got %q", resp.Name)
	}
	if resp.Description == nil || *resp.Description != "A wonderful event" {
		t.Errorf("Description: got %v", resp.Description)
	}
	if resp.Status != "draft" {
		t.Errorf("Status: got %q", resp.Status)
	}
	if resp.Visibility != "public" {
		t.Errorf("Visibility: got %q", resp.Visibility)
	}
	if resp.ImageURL == nil || *resp.ImageURL != "https://example.com/image.jpg" {
		t.Errorf("ImageURL: got %v", resp.ImageURL)
	}
	if resp.StartAt != start.Format(time.RFC3339) {
		t.Errorf("StartAt: got %q, want %q", resp.StartAt, start.Format(time.RFC3339))
	}
	if resp.EndAt != end.Format(time.RFC3339) {
		t.Errorf("EndAt: got %q, want %q", resp.EndAt, end.Format(time.RFC3339))
	}
}

func TestEvent125_EventFromRowWithVenueID(t *testing.T) {
	now := time.Now().UTC()
	venueID := mustParseUUID(t, "00000000-0000-0000-0000-000000000030")

	row := gen.EventRow{
		ID:        mustParseUUID(t, "00000000-0000-0000-0000-000000000010"),
		OrgID:     mustParseUUID(t, "00000000-0000-0000-0000-000000000020"),
		VenueID:   &venueID,
		Name:      "Venue Event",
		Status:    "published",
		StartAt:   now.Add(time.Hour),
		EndAt:     now.Add(2 * time.Hour),
		Visibility: "public",
		CreatedAt: now,
		UpdatedAt: now,
	}

	resp := eventFromRow(row)
	if resp.VenueID == nil {
		t.Fatal("VenueID should not be nil")
	}
	if *resp.VenueID != "00000000-0000-0000-0000-000000000030" {
		t.Errorf("VenueID: got %q", *resp.VenueID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response Content-Type tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_ListEvents_ReturnsJSONContentType(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
}

func TestEvent125_CreateEvent_ContentTypeRequired(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	// Valid body but the DB is down → should fail at DB not at content-type check
	body := `{"name":"Event","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("response Content-Type: got %q, want application/json", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// eventErrorCode extracts the error code from the standard JSON error envelope.
// Structure: {"error": {"code": "...", "message": "..."}}
func eventErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("eventErrorCode: JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("eventErrorCode: no 'error' object in response (body: %v)", m)
	}
	code, _ := errObj["code"].(string)
	return code
}

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("mustParseUUID(%q): %v", s, err)
	}
	return id
}
