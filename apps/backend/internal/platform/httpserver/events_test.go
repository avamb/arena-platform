// events_test.go — unit tests for feature #125 (Event model + CRUD),
// reshaped in Wave 4 (AB-36/AB-37): the event no longer carries venue_id or
// start_at/end_at — the venue and the dates live on sessions, and the event
// exposes the trigger-maintained first_session_at/last_session_at cache plus
// the venue_names projection.
//
// Test coverage:
//
//	Step 1: Migration files — 0014_events.sql plus the Wave 4 reshape
//	        migrations 0079/0080/0081
//	Step 2: CRUD endpoints — route mounting, auth-gating, request validation
//	Step 3: Status transition guards — allowed and forbidden transitions
//	Step 4: i18n name/description — query file + gen file structure
//	Step 5: Legacy date/venue create fields are ignored (unknown JSON keys)
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

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
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
		// MembershipQueries backed by orgMemberAdmitFromCtxDBTX so that
		// authenticated requests through the router pass the fail-closed
		// org-membership guard (PR2-26 feature #382).
		MembershipQueries: gen.New(&orgMemberAdmitFromCtxDBTX{}),
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

// Wave 4 (AB-36): the venue binding moved from events to sessions.
// Migration 0079 drops events.venue_id.
func TestEvent125_Wave4_VenueMovedToSessions(t *testing.T) {
	content := findFileByName(t, "0079_session_owns_venue.sql")
	if content == "" {
		t.Fatal("0079_session_owns_venue.sql is empty or not found")
	}
	if !strings.Contains(content, "DROP COLUMN venue_id") {
		t.Error("0079 migration should drop events.venue_id")
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

// Wave 4 (AB-37): events.start_at/end_at (and the events_date_order CHECK)
// are gone; migration 0080 replaces them with the trigger-maintained
// first_session_at/last_session_at cache.
func TestEvent125_Wave4_DatesMovedToSessions(t *testing.T) {
	content := findFileByName(t, "0080_event_dates_from_sessions.sql")
	if content == "" {
		t.Fatal("0080_event_dates_from_sessions.sql is empty or not found")
	}
	for _, want := range []string{
		"ADD COLUMN first_session_at",
		"last_session_at",
		"DROP CONSTRAINT IF EXISTS events_date_order",
		"DROP COLUMN start_at",
		"DROP COLUMN end_at",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("0080 migration missing %q", want)
		}
	}
}

// Wave 4 (AB-38): currency is derived from the venue geography and stored on
// sessions; migration 0081 introduces it.
func TestEvent125_Wave4_CurrencyMigrationExists(t *testing.T) {
	content := findFileByName(t, "0081_currency_from_geography.sql")
	if content == "" {
		t.Fatal("0081_currency_from_geography.sql is empty or not found")
	}
	if !strings.Contains(content, "currency_source") {
		t.Error("0081 migration missing currency_source column")
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

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Wave 4 — legacy date/venue fields are ignored on create
//
// start_at / end_at / venue_id are no longer part of the create contract
// (dates and venue live on sessions, AB-36/AB-37). Unknown JSON keys are
// silently ignored, so a create carrying only a name passes validation —
// even when the legacy fields are malformed. Validation success is observed
// as "not 400": the request then proceeds to the DB layer, which fails in
// these DB-less unit tests.
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_CreateEvent_NameOnlyPassesValidation(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Just A Name"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusBadRequest {
		t.Errorf("name-only create rejected: got 400 with code %q", eventErrorCode(t, w))
	}
}

func TestEvent125_CreateEvent_LegacyDateFieldsIgnored(t *testing.T) {
	cases := []struct {
		label string
		body  string
	}{
		{"missing start_at", `{"name":"Test Event","end_at":"2026-07-01T12:00:00Z"}`},
		{"missing end_at", `{"name":"Test Event","start_at":"2026-07-01T10:00:00Z"}`},
		{"end before start", `{"name":"Bad Dates","start_at":"2026-07-01T12:00:00Z","end_at":"2026-07-01T10:00:00Z"}`},
		{"end equals start", `{"name":"Same Time","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T10:00:00Z"}`},
		{"malformed start_at", `{"name":"Bad Format","start_at":"not-a-date","end_at":"2026-07-01T12:00:00Z"}`},
		{"malformed end_at", `{"name":"Bad Format","start_at":"2026-07-01T10:00:00Z","end_at":"bad-date"}`},
		{"valid legacy dates", `{"name":"Good Dates","start_at":"2026-07-01T10:00:00Z","end_at":"2026-07-01T12:00:00Z"}`},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			s := buildEventServer(t)
			tok := mintEventToken(t, s)
			orgID := "00000000-0000-0000-0000-000000000001"

			req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
				strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)
			if w.Code == http.StatusBadRequest {
				t.Errorf("%s: legacy date fields must be ignored, got 400 with code %q",
					tc.label, eventErrorCode(t, w))
			}
		})
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

	body := `{"name":"Bad Vis","visibility":"secret"}`
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

	body := `{"name":"Bad Status","status":"pending"}`
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

// Wave 4: venue_id is no longer an event create field — the session owns the
// venue (AB-36). An unknown venue_id key is silently ignored, even when it is
// not a valid UUID.
func TestEvent125_CreateEvent_LegacyVenueIDIgnored(t *testing.T) {
	s := buildEventServer(t)
	tok := mintEventToken(t, s)
	orgID := "00000000-0000-0000-0000-000000000001"

	body := `{"name":"Legacy Venue","venue_id":"not-a-uuid"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusBadRequest {
		t.Errorf("legacy venue_id must be ignored: got 400 with code %q", eventErrorCode(t, w))
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

func TestEvent125_QueryFileHasListEventVenueNames(t *testing.T) {
	content := findFileByName(t, "events.sql")
	if !strings.Contains(content, "ListEventVenueNames") {
		t.Error("events.sql missing ListEventVenueNames query (Wave 4 venue_names projection)")
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
		"ID", "DisplayNumber", "OrgID", "Name", "Description",
		"Status", "FirstSessionAt", "LastSessionAt", "Visibility", "ImageURL",
		"CreatedAt", "UpdatedAt", "DeletedAt",
	} {
		if !strings.Contains(content, field) {
			t.Errorf("events.sql.go EventRow missing field %q", field)
		}
	}
}

func TestEvent125_GenFileEventRowNullableFields(t *testing.T) {
	content := findFileByName(t, "events.sql.go")
	// FirstSessionAt/LastSessionAt/DeletedAt are nullable (*time.Time)
	if !strings.Contains(content, "*time.Time") {
		t.Error("events.sql.go EventRow FirstSessionAt/LastSessionAt should be *time.Time (nullable)")
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
		"ListEventVenueNames",
	} {
		if !strings.Contains(content, "func (q *Queries) "+method) {
			t.Errorf("events.sql.go missing method %q", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time guard: *gen.Queries must satisfy Querier
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_QuerierInterfaceSatisfied(_ *testing.T) {
	// This is a compile-time check embedded in a test function.
	// If gen.Queries does not satisfy gen.Querier, the file won't compile.
	var _ gen.Querier = (*gen.Queries)(nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Response shape tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEvent125_EventFromRowProducesCorrectShape(t *testing.T) {
	now := time.Now().UTC()
	first := now.Add(24 * time.Hour)
	last := now.Add(26 * time.Hour)
	desc := "A wonderful event"
	imgURL := "https://example.com/image.jpg"

	row := gen.EventRow{
		ID:             mustParseUUID(t, "00000000-0000-0000-0000-000000000010"),
		DisplayNumber:  42,
		OrgID:          mustParseUUID(t, "00000000-0000-0000-0000-000000000020"),
		Name:           "My Event",
		Description:    &desc,
		Status:         "draft",
		FirstSessionAt: &first,
		LastSessionAt:  &last,
		Visibility:     "public",
		ImageURL:       &imgURL,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	resp := eventFromRow(row)

	if resp.ID != "00000000-0000-0000-0000-000000000010" {
		t.Errorf("ID: got %q", resp.ID)
	}
	if resp.DisplayNumber != 42 {
		t.Errorf("DisplayNumber: got %d, want 42", resp.DisplayNumber)
	}
	if resp.OrgID != "00000000-0000-0000-0000-000000000020" {
		t.Errorf("OrgID: got %q", resp.OrgID)
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
	if resp.FirstSessionAt == nil || *resp.FirstSessionAt != first.Format(time.RFC3339) {
		t.Errorf("FirstSessionAt: got %v, want %q", resp.FirstSessionAt, first.Format(time.RFC3339))
	}
	if resp.LastSessionAt == nil || *resp.LastSessionAt != last.Format(time.RFC3339) {
		t.Errorf("LastSessionAt: got %v, want %q", resp.LastSessionAt, last.Format(time.RFC3339))
	}
	if resp.VenueNames == nil || len(resp.VenueNames) != 0 {
		t.Errorf("VenueNames: got %v, want empty non-nil slice", resp.VenueNames)
	}
}

// An event with no sessions carries nil first/last session timestamps and an
// empty (but non-nil, so it serializes as []) venue_names list.
func TestEvent125_EventFromRowNoSessions(t *testing.T) {
	now := time.Now().UTC()

	row := gen.EventRow{
		ID:         mustParseUUID(t, "00000000-0000-0000-0000-000000000010"),
		OrgID:      mustParseUUID(t, "00000000-0000-0000-0000-000000000020"),
		Name:       "Sessionless Event",
		Status:     "published",
		Visibility: "public",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	resp := eventFromRow(row)
	if resp.FirstSessionAt != nil {
		t.Errorf("FirstSessionAt: got %v, want nil", *resp.FirstSessionAt)
	}
	if resp.LastSessionAt != nil {
		t.Errorf("LastSessionAt: got %v, want nil", *resp.LastSessionAt)
	}
	if resp.VenueNames == nil {
		t.Error("VenueNames must be non-nil so it serializes as []")
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, gone := range []string{"venue_id", "start_at", "end_at"} {
		if _, present := m[gone]; present {
			t.Errorf("event JSON must not carry legacy field %q", gone)
		}
	}
	for _, want := range []string{"first_session_at", "last_session_at", "venue_names", "display_number"} {
		if _, present := m[want]; !present {
			t.Errorf("event JSON missing field %q", want)
		}
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
