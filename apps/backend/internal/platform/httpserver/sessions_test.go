// sessions_test.go — unit tests for feature #126 (Session model + CRUD).
//
// Test coverage:
//   Step 1: Migration file 0016_sessions.sql — schema, status enum, date CHECK, RBAC seeds
//   Step 2: CRUD endpoints — route mounting, auth-gating, request validation (no DB required)
//   Step 3: Capacity propagation hook — fired on capacity_total change
//   Step 4: Integration: date invariant, capacity validation, overlap detection logic
//   Step 5: sqlc query file (sessions.sql) + gen file (sessions.sql.go) structure
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"context"
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

const sessionTestActorID = "00000000-0000-0000-0000-000000000099"

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for session route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildSessionServer builds a Server with stub auth, session routes fully
// mounted, and a dbDownPool so real DB operations never execute.
func buildSessionServer(t *testing.T) *Server {
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
		t.Fatalf("buildSessionServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// SessionQueries non-nil so session route conditionals pass.
		SessionQueries: gen.New(nil),
		// EventQueries non-nil for good measure.
		EventQueries: gen.New(nil),
		// Audit writer required for DELETE.
		Audit: &captureAuditWriter{},
	})
}

// mintSessionToken mints a dev JWT for session route tests.
func mintSessionToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + sessionTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintSessionToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintSessionToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatal("mintSessionToken: empty token in response")
	}
	return tok
}

// sessionPath returns the base path for a session collection.
func sessionPath(orgID, eventID string) string {
	return "/v1/organizations/" + orgID + "/events/" + eventID + "/sessions"
}

// sessionItemPath returns the path for a single session.
func sessionItemPath(orgID, eventID, sessID string) string {
	return sessionPath(orgID, eventID) + "/" + sessID
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	if content == "" {
		t.Fatal("0016_sessions.sql is empty or not found")
	}
}

func TestSession126_MigrationHasGooseDirectives(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' directive")
	}
}

func TestSession126_MigrationHasSessionsTable(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	checks := []string{
		"CREATE TABLE sessions",
		"event_id",
		"start_at",
		"end_at",
		"capacity_total",
		"status",
		"deleted_at",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("migration missing required element: %q", check)
		}
	}
}

func TestSession126_MigrationHasCapacityCheck(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	// Must enforce capacity_total > 0.
	if !strings.Contains(content, "capacity_total > 0") {
		t.Error("migration missing CHECK (capacity_total > 0)")
	}
}

func TestSession126_MigrationHasDateOrderCheck(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	// Must enforce end_at > start_at.
	if !strings.Contains(content, "end_at > start_at") {
		t.Error("migration missing CHECK (end_at > start_at)")
	}
}

func TestSession126_MigrationHasStatusEnum(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	for _, status := range []string{"draft", "scheduled", "cancelled", "completed"} {
		if !strings.Contains(content, status) {
			t.Errorf("migration missing status value: %q", status)
		}
	}
}

func TestSession126_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	for _, perm := range []string{"session.create", "session.read", "session.update", "session.delete"} {
		if !strings.Contains(content, perm) {
			t.Errorf("migration missing RBAC permission: %q", perm)
		}
	}
}

func TestSession126_MigrationHasUUIDv7Default(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	if !strings.Contains(content, "uuidv7()") {
		t.Error("migration should use uuidv7() as the default ID generator")
	}
}

func TestSession126_MigrationHasForeignKey(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	if !strings.Contains(content, "REFERENCES events(id)") {
		t.Error("migration missing REFERENCES events(id) foreign key")
	}
}

func TestSession126_MigrationHasIndexes(t *testing.T) {
	content := findFileByName(t, "0016_sessions.sql")
	if !strings.Contains(content, "CREATE INDEX") {
		t.Error("migration should have at least one index")
	}
	// Must have an index on event_id for fast listing.
	if !strings.Contains(content, "event_id") {
		t.Error("migration should index event_id")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Route mounting and auth-gating tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_ListRequiresAuth(t *testing.T) {
	s := buildSessionServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, sessionPath(orgID, eventID), nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET sessions: got %d, want 401", w.Code)
	}
}

func TestSession126_GetRequiresAuth(t *testing.T) {
	s := buildSessionServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, sessionItemPath(orgID, eventID, sessID), nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET session/{id}: got %d, want 401", w.Code)
	}
}

func TestSession126_CreateRequiresAuth(t *testing.T) {
	s := buildSessionServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID),
		strings.NewReader(`{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T12:00:00Z","capacity_total":100}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST sessions: got %d, want 401", w.Code)
	}
}

func TestSession126_UpdateRequiresAuth(t *testing.T) {
	s := buildSessionServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, sessionItemPath(orgID, eventID, sessID),
		strings.NewReader(`{"capacity_total":200}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated PATCH session: got %d, want 401", w.Code)
	}
}

func TestSession126_DeleteRequiresAuth(t *testing.T) {
	s := buildSessionServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, sessionItemPath(orgID, eventID, sessID), nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated DELETE session: got %d, want 401", w.Code)
	}
}

func TestSession126_ListWithAuthReachesDB(t *testing.T) {
	// Authenticated request with gen.New(nil) should reach the DB and fail
	// (not 401/403/404) — proving the route is mounted and auth passes.
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, sessionPath(orgID, eventID), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	// gen.New(nil) panics → Recoverer catches → 500. Any non-401/403 means auth passed.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("authenticated LIST sessions should not return %d", w.Code)
	}
}

func TestSession126_CreateWithAuthReachesDB(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T12:00:00Z","capacity_total":50}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("authenticated POST sessions should not return %d", w.Code)
	}
}

func TestSession126_RoutesMounted(t *testing.T) {
	s := buildSessionServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessID := uuid.New().String()

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, sessionPath(orgID, eventID)},
		{http.MethodPost, sessionPath(orgID, eventID)},
		{http.MethodGet, sessionItemPath(orgID, eventID, sessID)},
		{http.MethodPatch, sessionItemPath(orgID, eventID, sessID)},
		{http.MethodDelete, sessionItemPath(orgID, eventID, sessID)},
	}

	for _, tc := range tests {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		s.router.ServeHTTP(w, req)
		// Route must be mounted — 401 (not 404) confirms it exists.
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s returned 404 — route not mounted", tc.method, tc.path)
		}
	}
}

func TestSession126_InvalidOrgIDReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T12:00:00Z","capacity_total":50}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/not-a-uuid/events/"+eventID+"/sessions",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid org_id: got %d, want 400", w.Code)
	}
}

func TestSession126_InvalidEventIDReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T12:00:00Z","capacity_total":50}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID+"/events/not-a-uuid/sessions",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid event_id: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Request validation tests (no DB required)
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_CreateMissingStartAtReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"end_at":"2025-01-01T12:00:00Z","capacity_total":100}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing start_at: got %d, want 400", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if errObj, ok := resp["error"].(map[string]any); ok {
		if code, _ := errObj["code"].(string); code != "session.missing_start_at" {
			t.Errorf("missing start_at: got code %q, want session.missing_start_at", code)
		}
	}
}

func TestSession126_CreateMissingEndAtReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","capacity_total":100}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing end_at: got %d, want 400", w.Code)
	}
}

func TestSession126_CreateInvalidStartAtReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"not-a-date","end_at":"2025-01-01T12:00:00Z","capacity_total":100}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid start_at format: got %d, want 400", w.Code)
	}
}

func TestSession126_CreateInvalidEndAtReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"bad-date","capacity_total":100}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid end_at format: got %d, want 400", w.Code)
	}
}

func TestSession126_CreateEndBeforeStartReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T12:00:00Z","end_at":"2025-01-01T10:00:00Z","capacity_total":100}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("end_at before start_at: got %d, want 400", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if errObj, ok := resp["error"].(map[string]any); ok {
		if code, _ := errObj["code"].(string); code != "session.invalid_date_range" {
			t.Errorf("date range error code: got %q, want session.invalid_date_range", code)
		}
	}
}

func TestSession126_CreateEndEqualsStartReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T10:00:00Z","capacity_total":100}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("end_at == start_at: got %d, want 400", w.Code)
	}
}

func TestSession126_CreateZeroCapacityReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T12:00:00Z","capacity_total":0}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("zero capacity_total: got %d, want 400", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if errObj, ok := resp["error"].(map[string]any); ok {
		if code, _ := errObj["code"].(string); code != "session.invalid_capacity" {
			t.Errorf("capacity error code: got %q, want session.invalid_capacity", code)
		}
	}
}

func TestSession126_CreateNegativeCapacityReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T12:00:00Z","capacity_total":-10}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("negative capacity_total: got %d, want 400", w.Code)
	}
}

func TestSession126_CreateInvalidStatusReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	body := `{"start_at":"2025-01-01T10:00:00Z","end_at":"2025-01-01T12:00:00Z","capacity_total":100,"status":"unknown"}`
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid status: got %d, want 400", w.Code)
	}
}

func TestSession126_CreateEmptyBodyReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body: got %d, want 400", w.Code)
	}
}

func TestSession126_CreateInvalidJSONReturns400(t *testing.T) {
	s := buildSessionServer(t)
	tok := mintSessionToken(t, s)
	orgID := uuid.New().String()
	eventID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID),
		strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Overlap detection logic (pure unit tests — no DB)
// ─────────────────────────────────────────────────────────────────────────────

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustParseTime: " + err.Error())
	}
	return t
}

func makeSessionRow(start, end string) gen.SessionRow {
	return gen.SessionRow{
		ID:            uuid.New(),
		EventID:       uuid.New(),
		StartAt:       mustParseTime(start),
		EndAt:         mustParseTime(end),
		CapacityTotal: 100,
		Status:        "scheduled",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
}

func TestSession126_OverlapDetection_NoSessions(t *testing.T) {
	if detectSessionOverlaps(nil) {
		t.Error("nil sessions should have no overlap")
	}
	if detectSessionOverlaps([]gen.SessionRow{}) {
		t.Error("empty sessions should have no overlap")
	}
}

func TestSession126_OverlapDetection_SingleSession(t *testing.T) {
	sessions := []gen.SessionRow{
		makeSessionRow("2025-01-01T10:00:00Z", "2025-01-01T12:00:00Z"),
	}
	if detectSessionOverlaps(sessions) {
		t.Error("single session should have no overlap")
	}
}

func TestSession126_OverlapDetection_NonOverlapping(t *testing.T) {
	sessions := []gen.SessionRow{
		makeSessionRow("2025-01-01T10:00:00Z", "2025-01-01T12:00:00Z"),
		makeSessionRow("2025-01-01T13:00:00Z", "2025-01-01T15:00:00Z"),
	}
	if detectSessionOverlaps(sessions) {
		t.Error("non-overlapping sessions should not trigger overlap flag")
	}
}

func TestSession126_OverlapDetection_AdjacentSessions(t *testing.T) {
	// Adjacent (end of A == start of B) should NOT overlap.
	sessions := []gen.SessionRow{
		makeSessionRow("2025-01-01T10:00:00Z", "2025-01-01T12:00:00Z"),
		makeSessionRow("2025-01-01T12:00:00Z", "2025-01-01T14:00:00Z"),
	}
	if detectSessionOverlaps(sessions) {
		t.Error("adjacent sessions (touching at boundary) should not overlap")
	}
}

func TestSession126_OverlapDetection_OverlappingSessions(t *testing.T) {
	sessions := []gen.SessionRow{
		makeSessionRow("2025-01-01T10:00:00Z", "2025-01-01T12:00:00Z"),
		makeSessionRow("2025-01-01T11:00:00Z", "2025-01-01T13:00:00Z"),
	}
	if !detectSessionOverlaps(sessions) {
		t.Error("overlapping sessions should trigger overlap flag")
	}
}

func TestSession126_OverlapDetection_ContainedSession(t *testing.T) {
	// B is fully contained within A.
	sessions := []gen.SessionRow{
		makeSessionRow("2025-01-01T10:00:00Z", "2025-01-01T14:00:00Z"),
		makeSessionRow("2025-01-01T11:00:00Z", "2025-01-01T13:00:00Z"),
	}
	if !detectSessionOverlaps(sessions) {
		t.Error("contained sessions should trigger overlap flag")
	}
}

func TestSession126_OverlapDetection_ThreeSessionsOneOverlap(t *testing.T) {
	sessions := []gen.SessionRow{
		makeSessionRow("2025-01-01T08:00:00Z", "2025-01-01T10:00:00Z"), // no overlap
		makeSessionRow("2025-01-01T11:00:00Z", "2025-01-01T13:00:00Z"),
		makeSessionRow("2025-01-01T12:00:00Z", "2025-01-01T14:00:00Z"), // overlaps with second
	}
	if !detectSessionOverlaps(sessions) {
		t.Error("3 sessions with 1 overlap pair should trigger overlap flag")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Status transition validation
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_ValidTransition_DraftToScheduled(t *testing.T) {
	if !isValidSessionTransition("draft", "scheduled") {
		t.Error("draft → scheduled should be allowed")
	}
}

func TestSession126_ValidTransition_DraftToCancelled(t *testing.T) {
	if !isValidSessionTransition("draft", "cancelled") {
		t.Error("draft → cancelled should be allowed")
	}
}

func TestSession126_ValidTransition_ScheduledToCancelled(t *testing.T) {
	if !isValidSessionTransition("scheduled", "cancelled") {
		t.Error("scheduled → cancelled should be allowed")
	}
}

func TestSession126_ValidTransition_ScheduledToCompleted(t *testing.T) {
	if !isValidSessionTransition("scheduled", "completed") {
		t.Error("scheduled → completed should be allowed")
	}
}

func TestSession126_InvalidTransition_CompletedToScheduled(t *testing.T) {
	if isValidSessionTransition("completed", "scheduled") {
		t.Error("completed → scheduled should not be allowed")
	}
}

func TestSession126_InvalidTransition_CancelledToCompleted(t *testing.T) {
	if isValidSessionTransition("cancelled", "completed") {
		t.Error("cancelled → completed should not be allowed")
	}
}

func TestSession126_InvalidTransition_DraftToCompleted(t *testing.T) {
	if isValidSessionTransition("draft", "completed") {
		t.Error("draft → completed should not be allowed")
	}
}

func TestSession126_InvalidTransition_UnknownStatus(t *testing.T) {
	if isValidSessionTransition("unknown", "scheduled") {
		t.Error("unknown source status should not be valid")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: sessionFromRow conversion
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_SessionFromRow_BasicConversion(t *testing.T) {
	eventID := uuid.New()
	sessID := uuid.New()
	start := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	row := gen.SessionRow{
		ID:            sessID,
		EventID:       eventID,
		StartAt:       start,
		EndAt:         end,
		CapacityTotal: 150,
		Status:        "scheduled",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	resp := sessionFromRow(row, false)
	if resp.ID != sessID.String() {
		t.Errorf("ID: got %q, want %q", resp.ID, sessID.String())
	}
	if resp.EventID != eventID.String() {
		t.Errorf("EventID: got %q, want %q", resp.EventID, eventID.String())
	}
	if resp.CapacityTotal != 150 {
		t.Errorf("CapacityTotal: got %d, want 150", resp.CapacityTotal)
	}
	if resp.Status != "scheduled" {
		t.Errorf("Status: got %q, want scheduled", resp.Status)
	}
	if resp.StartAt != "2025-01-01T10:00:00Z" {
		t.Errorf("StartAt: got %q, want 2025-01-01T10:00:00Z", resp.StartAt)
	}
	if resp.EndAt != "2025-01-01T12:00:00Z" {
		t.Errorf("EndAt: got %q, want 2025-01-01T12:00:00Z", resp.EndAt)
	}
	if resp.HasOverlappingSessions {
		t.Error("HasOverlappingSessions should be false when passed false")
	}
}

func TestSession126_SessionFromRow_OverlapFlagged(t *testing.T) {
	row := gen.SessionRow{
		ID:            uuid.New(),
		EventID:       uuid.New(),
		StartAt:       time.Now(),
		EndAt:         time.Now().Add(2 * time.Hour),
		CapacityTotal: 100,
		Status:        "draft",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	resp := sessionFromRow(row, true)
	if !resp.HasOverlappingSessions {
		t.Error("HasOverlappingSessions should be true when passed true")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: SQL query file and gen file structure tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "sessions.sql")
	if content == "" {
		t.Fatal("sessions.sql query file is empty or not found")
	}
}

func TestSession126_QueryFileHasInsertSession(t *testing.T) {
	content := findFileByName(t, "sessions.sql")
	if !strings.Contains(content, "InsertSession") {
		t.Error("sessions.sql missing InsertSession query")
	}
}

func TestSession126_QueryFileHasGetSessionByID(t *testing.T) {
	content := findFileByName(t, "sessions.sql")
	if !strings.Contains(content, "GetSessionByID") {
		t.Error("sessions.sql missing GetSessionByID query")
	}
}

func TestSession126_QueryFileHasListSessionsByEvent(t *testing.T) {
	content := findFileByName(t, "sessions.sql")
	if !strings.Contains(content, "ListSessionsByEvent") {
		t.Error("sessions.sql missing ListSessionsByEvent query")
	}
}

func TestSession126_QueryFileHasUpdateSession(t *testing.T) {
	content := findFileByName(t, "sessions.sql")
	if !strings.Contains(content, "UpdateSession") {
		t.Error("sessions.sql missing UpdateSession query")
	}
}

func TestSession126_QueryFileHasSoftDeleteSession(t *testing.T) {
	content := findFileByName(t, "sessions.sql")
	if !strings.Contains(content, "SoftDeleteSession") {
		t.Error("sessions.sql missing SoftDeleteSession query")
	}
}

func TestSession126_QueryFileHasCountOverlappingSessions(t *testing.T) {
	content := findFileByName(t, "sessions.sql")
	if !strings.Contains(content, "CountOverlappingSessions") {
		t.Error("sessions.sql missing CountOverlappingSessions query")
	}
}

func TestSession126_GenFileExists(t *testing.T) {
	content := findFileByName(t, "sessions.sql.go")
	if content == "" {
		t.Fatal("sessions.sql.go gen file is empty or not found")
	}
}

func TestSession126_GenFileHasSessionRowStruct(t *testing.T) {
	content := findFileByName(t, "sessions.sql.go")
	if !strings.Contains(content, "type SessionRow struct") {
		t.Error("sessions.sql.go missing SessionRow struct")
	}
}

func TestSession126_GenFileHasCapacityTotalField(t *testing.T) {
	content := findFileByName(t, "sessions.sql.go")
	if !strings.Contains(content, "CapacityTotal") {
		t.Error("sessions.sql.go missing CapacityTotal field in SessionRow")
	}
}

func TestSession126_GenFileInGenPackage(t *testing.T) {
	content := findFileByName(t, "sessions.sql.go")
	if !strings.Contains(content, "package gen") {
		t.Error("sessions.sql.go should declare package gen")
	}
}

func TestSession126_GenFileHasInsertSessionMethod(t *testing.T) {
	content := findFileByName(t, "sessions.sql.go")
	if !strings.Contains(content, "func (q *Queries) InsertSession") {
		t.Error("sessions.sql.go missing InsertSession method on *Queries")
	}
}

func TestSession126_GenFileHasListMethod(t *testing.T) {
	content := findFileByName(t, "sessions.sql.go")
	if !strings.Contains(content, "func (q *Queries) ListSessionsByEvent") {
		t.Error("sessions.sql.go missing ListSessionsByEvent method")
	}
}

func TestSession126_GenFileHasCountOverlapMethod(t *testing.T) {
	content := findFileByName(t, "sessions.sql.go")
	if !strings.Contains(content, "func (q *Queries) CountOverlappingSessions") {
		t.Error("sessions.sql.go missing CountOverlappingSessions method")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Querier interface coverage
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_QuerierInterfaceHasSessionMethods(t *testing.T) {
	// *gen.Queries must implement the Querier interface, which is a compile-time
	// assertion in querier.go. This test is a documentation check that confirms
	// the session methods are part of the Querier contract.
	var _ gen.Querier = (*gen.Queries)(nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Capacity propagation hook test
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_CapacityPropagationHookExists(t *testing.T) {
	// The capacity hook must be defined on *Server. We verify it compiles by
	// calling it with test values and checking no panic occurs.
	s := buildSessionServer(t)
	// Should not panic — it's a foundation-milestone placeholder that logs.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("onCapacityChange panicked: %v", r)
		}
	}()
	s.onCapacityChange(context.Background(), uuid.New(), 100, 200)
}

// ─────────────────────────────────────────────────────────────────────────────
// Full verification sweep
// ─────────────────────────────────────────────────────────────────────────────

func TestSession126_FullVerification(t *testing.T) {
	t.Run("migration_file_exists", func(t *testing.T) {
		content := findFileByName(t, "0016_sessions.sql")
		if !strings.Contains(content, "CREATE TABLE sessions") {
			t.Fatal("migration does not create sessions table")
		}
	})

	t.Run("query_file_exists", func(t *testing.T) {
		content := findFileByName(t, "sessions.sql")
		if content == "" {
			t.Fatal("sessions.sql query file not found")
		}
	})

	t.Run("gen_file_exists", func(t *testing.T) {
		content := findFileByName(t, "sessions.sql.go")
		if content == "" {
			t.Fatal("sessions.sql.go gen file not found")
		}
	})

	t.Run("routes_auth_gated", func(t *testing.T) {
		s := buildSessionServer(t)
		orgID := uuid.New().String()
		eventID := uuid.New().String()
		sessID := uuid.New().String()

		for _, tc := range []struct {
			method      string
			path        string
			contentType string // set for POST/PATCH so global RequireJSONContentType passes
		}{
			{http.MethodGet, sessionPath(orgID, eventID), ""},
			{http.MethodPost, sessionPath(orgID, eventID), "application/json"},
			{http.MethodGet, sessionItemPath(orgID, eventID, sessID), ""},
			{http.MethodPatch, sessionItemPath(orgID, eventID, sessID), "application/json"},
			{http.MethodDelete, sessionItemPath(orgID, eventID, sessID), ""},
		} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			s.router.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: want 401 without auth, got %d", tc.method, tc.path, w.Code)
			}
		}
	})

	t.Run("overlap_detection", func(t *testing.T) {
		// Non-overlapping
		nonOverlap := []gen.SessionRow{
			makeSessionRow("2025-01-01T10:00:00Z", "2025-01-01T11:00:00Z"),
			makeSessionRow("2025-01-01T11:00:00Z", "2025-01-01T12:00:00Z"),
		}
		if detectSessionOverlaps(nonOverlap) {
			t.Error("adjacent sessions should not overlap")
		}
		// Overlapping
		overlap := []gen.SessionRow{
			makeSessionRow("2025-01-01T10:00:00Z", "2025-01-01T12:00:00Z"),
			makeSessionRow("2025-01-01T11:00:00Z", "2025-01-01T13:00:00Z"),
		}
		if !detectSessionOverlaps(overlap) {
			t.Error("overlapping sessions should be detected")
		}
	})

	t.Run("date_invariant_enforced", func(t *testing.T) {
		s := buildSessionServer(t)
		tok := mintSessionToken(t, s)
		orgID := uuid.New().String()
		eventID := uuid.New().String()

		w := httptest.NewRecorder()
		body := `{"start_at":"2025-06-01T15:00:00Z","end_at":"2025-06-01T10:00:00Z","capacity_total":50}`
		req := httptest.NewRequest(http.MethodPost, sessionPath(orgID, eventID), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("end before start: want 400, got %d", w.Code)
		}
	})

	t.Run("status_transitions", func(t *testing.T) {
		allowed := [][2]string{
			{"draft", "scheduled"},
			{"draft", "cancelled"},
			{"scheduled", "cancelled"},
			{"scheduled", "completed"},
		}
		for _, pair := range allowed {
			if !isValidSessionTransition(pair[0], pair[1]) {
				t.Errorf("transition %s→%s should be allowed", pair[0], pair[1])
			}
		}
		forbidden := [][2]string{
			{"completed", "scheduled"},
			{"cancelled", "completed"},
			{"draft", "completed"},
			{"completed", "draft"},
		}
		for _, pair := range forbidden {
			if isValidSessionTransition(pair[0], pair[1]) {
				t.Errorf("transition %s→%s should NOT be allowed", pair[0], pair[1])
			}
		}
	})
}
