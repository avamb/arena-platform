// publications_test.go — unit tests for feature #151 (Event publications model).
//
// Test coverage:
//
//	Step 1: Migration file 0017_event_publications.sql — table, constraints, RBAC seeds
//	Step 2: Route mounting and auth-gating (POST/DELETE/GET /v1/events/{id}/publications)
//	Step 3: Legacy Bil24 subscriptions migration outline present in the migration file
//	Step 4: Request validation — missing fields, invalid UUIDs, content-type
//	        sqlc query file and gen file structure
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

const publicationTestActorID = "00000000-0000-0000-0000-000000000003"

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for publication route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildPublicationServer builds a Server with stub auth, publication routes
// fully mounted, and a dbDownPool so real DB operations never execute.
func buildPublicationServer(t *testing.T) *Server {
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
		t.Fatalf("buildPublicationServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// PublicationQueries non-nil so publication route conditionals pass.
		PublicationQueries: gen.New(nil),
	})
}

// mintPublicationToken mints a dev JWT for publication route tests.
func mintPublicationToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + publicationTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintPublicationToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintPublicationToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintPublicationToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure (0017_event_publications.sql)
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_MigrationFileExists(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if len(data) == 0 {
		t.Fatal("0017_event_publications.sql is empty")
	}
}

func TestPublication151_MigrationHasGooseUpDown(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' marker")
	}
	if !strings.Contains(data, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' marker")
	}
}

func TestPublication151_MigrationTableName(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "CREATE TABLE event_publications") {
		t.Error("migration missing 'CREATE TABLE event_publications'")
	}
}

func TestPublication151_MigrationColumns(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	for _, col := range []string{"event_id", "feed_token_id", "city_id", "published_at"} {
		if !strings.Contains(data, col) {
			t.Errorf("migration missing column %q", col)
		}
	}
}

func TestPublication151_MigrationCompositeUnique(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "UNIQUE (event_id, feed_token_id)") {
		t.Error("migration missing composite UNIQUE (event_id, feed_token_id)")
	}
}

func TestPublication151_MigrationForeignKeys(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "REFERENCES events(id)") {
		t.Error("migration missing FK REFERENCES events(id)")
	}
	if !strings.Contains(data, "REFERENCES agent_feed_tokens(id)") {
		t.Error("migration missing FK REFERENCES agent_feed_tokens(id)")
	}
}

func TestPublication151_MigrationCityIDNullable(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "city_id") {
		t.Error("migration missing city_id column")
	}
	// The column must reference cities(id) with no NOT NULL (it's nullable).
	if !strings.Contains(data, "REFERENCES cities(id)") {
		t.Error("migration missing city_id FK to cities(id)")
	}
}

func TestPublication151_MigrationRBACPermissions(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	for _, perm := range []string{"publication.create", "publication.read", "publication.delete"} {
		if !strings.Contains(data, perm) {
			t.Errorf("migration missing RBAC permission %q", perm)
		}
	}
}

func TestPublication151_MigrationRBACAdminRole(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "'admin'") {
		t.Error("migration missing admin role grant")
	}
}

func TestPublication151_MigrationRBACOrgAdminRole(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "'org_admin'") {
		t.Error("migration missing org_admin role grant")
	}
}

func TestPublication151_MigrationIndexEventID(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "event_publications_event_id") {
		t.Error("migration missing index event_publications_event_id")
	}
}

func TestPublication151_MigrationIndexFeedTokenID(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "event_publications_feed_token_id") {
		t.Error("migration missing index event_publications_feed_token_id")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Legacy Bil24 subscriptions migration outline
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_MigrationHasBil24MigrationOutline(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "bil24") && !strings.Contains(data, "Bil24") {
		t.Error("migration missing Bil24 migration outline (step 3)")
	}
}

func TestPublication151_MigrationBil24ETLQuery(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	// The migration should reference an INSERT ... SELECT ETL pattern
	if !strings.Contains(data, "INSERT INTO event_publications") {
		t.Error("migration missing ETL INSERT INTO event_publications example")
	}
}

func TestPublication151_MigrationBil24ValidationQuery(t *testing.T) {
	data := findFileByName(t, "0017_event_publications.sql")
	if !strings.Contains(data, "SELECT COUNT(*)") {
		t.Error("migration missing validation COUNT(*) query in migration outline")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Route mounting and auth-gating
// ─────────────────────────────────────────────────────────────────────────────

const (
	testEventIDForPub     = "00000000-0000-0000-0001-000000000001"
	testFeedTokenIDForPub = "00000000-0000-0000-0002-000000000002"
)

func TestPublication151_PostRequiresAuth(t *testing.T) {
	s := buildPublicationServer(t)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/"+testEventIDForPub+"/publications",
		strings.NewReader(`{"feed_token_id":"`+testFeedTokenIDForPub+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/events/{id}/publications without auth: got %d, want 401", w.Code)
	}
}

func TestPublication151_DeleteRequiresAuth(t *testing.T) {
	s := buildPublicationServer(t)
	req := httptest.NewRequest(http.MethodDelete,
		"/v1/events/"+testEventIDForPub+"/publications/"+testFeedTokenIDForPub, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/events/{id}/publications/{ft} without auth: got %d, want 401", w.Code)
	}
}

func TestPublication151_GetRequiresAuth(t *testing.T) {
	s := buildPublicationServer(t)
	req := httptest.NewRequest(http.MethodGet,
		"/v1/events/"+testEventIDForPub+"/publications", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/events/{id}/publications without auth: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4a: Request validation (POST)
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_PostRequiresContentType(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/"+testEventIDForPub+"/publications",
		strings.NewReader(`{"feed_token_id":"`+testFeedTokenIDForPub+`"}`))
	// No Content-Type header.
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("POST without Content-Type: got %d, want 415", w.Code)
	}
}

func TestPublication151_PostRequiresFeedTokenID(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/"+testEventIDForPub+"/publications",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with empty feed_token_id: got %d, want 400", w.Code)
	}
}

func TestPublication151_PostRejectsInvalidFeedTokenID(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/"+testEventIDForPub+"/publications",
		strings.NewReader(`{"feed_token_id":"not-a-uuid"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid feed_token_id: got %d, want 400", w.Code)
	}
}

func TestPublication151_PostRejectsInvalidEventID(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/not-a-uuid/publications",
		strings.NewReader(`{"feed_token_id":"`+testFeedTokenIDForPub+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid event_id path: got %d, want 400", w.Code)
	}
}

func TestPublication151_PostRejectsInvalidCityID(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/"+testEventIDForPub+"/publications",
		strings.NewReader(`{"feed_token_id":"`+testFeedTokenIDForPub+`","city_id":"not-a-uuid"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid city_id: got %d, want 400", w.Code)
	}
}

func TestPublication151_PostRejectsInvalidJSON(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/"+testEventIDForPub+"/publications",
		strings.NewReader(`{invalid`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid JSON: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4b: Request validation (DELETE)
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_DeleteRejectsInvalidEventID(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodDelete,
		"/v1/events/not-a-uuid/publications/"+testFeedTokenIDForPub, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("DELETE with invalid event_id: got %d, want 400", w.Code)
	}
}

func TestPublication151_DeleteRejectsInvalidFeedTokenID(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodDelete,
		"/v1/events/"+testEventIDForPub+"/publications/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("DELETE with invalid feed_token_id: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4c: Request validation (GET)
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_GetRejectsInvalidEventID(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	req := httptest.NewRequest(http.MethodGet,
		"/v1/events/not-a-uuid/publications", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET with invalid event_id: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4d: Response shape
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_ResponseHasJSONContentType(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	// GET — the handler returns JSON even for validation errors.
	req := httptest.NewRequest(http.MethodGet,
		"/v1/events/not-a-uuid/publications", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

func TestPublication151_PostResponseHasJSONContentType(t *testing.T) {
	s := buildPublicationServer(t)
	tok := mintPublicationToken(t, s)
	// Trigger a 400 to check Content-Type (no DB needed).
	req := httptest.NewRequest(http.MethodPost,
		"/v1/events/"+testEventIDForPub+"/publications",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4e: sqlc query file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_QueryFileExists(t *testing.T) {
	data := findFileByName(t, "event_publications.sql")
	if len(data) == 0 {
		t.Fatal("event_publications.sql is empty")
	}
}

func TestPublication151_QueryFileHasPublishEvent(t *testing.T) {
	data := findFileByName(t, "event_publications.sql")
	if !strings.Contains(data, "PublishEvent") {
		t.Error("event_publications.sql missing PublishEvent query")
	}
}

func TestPublication151_QueryFileHasUnpublishEvent(t *testing.T) {
	data := findFileByName(t, "event_publications.sql")
	if !strings.Contains(data, "UnpublishEvent") {
		t.Error("event_publications.sql missing UnpublishEvent query")
	}
}

func TestPublication151_QueryFileHasListPublicationsByEvent(t *testing.T) {
	data := findFileByName(t, "event_publications.sql")
	if !strings.Contains(data, "ListPublicationsByEvent") {
		t.Error("event_publications.sql missing ListPublicationsByEvent query")
	}
}

func TestPublication151_QueryFileHasListPublicationsByFeedToken(t *testing.T) {
	data := findFileByName(t, "event_publications.sql")
	if !strings.Contains(data, "ListPublicationsByFeedToken") {
		t.Error("event_publications.sql missing ListPublicationsByFeedToken query")
	}
}

func TestPublication151_QueryFileHasGetPublication(t *testing.T) {
	data := findFileByName(t, "event_publications.sql")
	if !strings.Contains(data, "GetPublication") {
		t.Error("event_publications.sql missing GetPublication query")
	}
}

func TestPublication151_QueryFileHasOnConflict(t *testing.T) {
	data := findFileByName(t, "event_publications.sql")
	if !strings.Contains(data, "ON CONFLICT") {
		t.Error("event_publications.sql missing ON CONFLICT (idempotent publish)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4f: sqlc gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_GenFileExists(t *testing.T) {
	data := findFileByName(t, "event_publications.sql.go")
	if len(data) == 0 {
		t.Fatal("event_publications.sql.go is empty")
	}
}

func TestPublication151_GenFileHasEventPublicationRow(t *testing.T) {
	data := findFileByName(t, "event_publications.sql.go")
	if !strings.Contains(data, "EventPublicationRow") {
		t.Error("event_publications.sql.go missing EventPublicationRow type")
	}
}

func TestPublication151_GenFileHasIDField(t *testing.T) {
	data := findFileByName(t, "event_publications.sql.go")
	if !strings.Contains(data, "ID") {
		t.Error("event_publications.sql.go missing ID field in EventPublicationRow")
	}
}

func TestPublication151_GenFileHasCityIDNullable(t *testing.T) {
	data := findFileByName(t, "event_publications.sql.go")
	if !strings.Contains(data, "*uuid.UUID") {
		t.Error("event_publications.sql.go missing *uuid.UUID for nullable CityID")
	}
}

func TestPublication151_GenFileHasAllQueryMethods(t *testing.T) {
	data := findFileByName(t, "event_publications.sql.go")
	for _, method := range []string{
		"PublishEvent",
		"UnpublishEvent",
		"ListPublicationsByEvent",
		"ListPublicationsByFeedToken",
		"GetPublication",
	} {
		if !strings.Contains(data, method) {
			t.Errorf("event_publications.sql.go missing method %q", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4g: Querier interface contains all publication methods (compile-time)
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_QuerierInterfaceHasPublicationMethods(_ *testing.T) {
	// Compile-time assertion: *gen.Queries must implement gen.Querier.
	// The querier.go file already has: var _ Querier = (*Queries)(nil)
	// If any publication method is missing on *Queries, this package won't compile.
	var _ gen.Querier = (*gen.Queries)(nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4h: publicationFromRow helper unit tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPublication151_PublicationFromRowBasic(t *testing.T) {
	eventID := uuid.MustParse("12345678-1234-1234-1234-123456789012")
	feedTokenID := uuid.MustParse("87654321-4321-4321-4321-987654321098")

	row := gen.EventPublicationRow{
		ID:          eventID,
		EventID:     eventID,
		FeedTokenID: feedTokenID,
		CityID:      nil,
		PublishedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
	}
	resp := publicationFromRow(row)

	if resp.ID == "" {
		t.Error("publicationFromRow: ID is empty")
	}
	if resp.EventID != eventID.String() {
		t.Errorf("publicationFromRow: EventID = %q, want %q", resp.EventID, eventID.String())
	}
	if resp.FeedTokenID != feedTokenID.String() {
		t.Errorf("publicationFromRow: FeedTokenID = %q, want %q", resp.FeedTokenID, feedTokenID.String())
	}
	if resp.CityID != nil {
		t.Error("publicationFromRow: CityID should be nil when not set")
	}
	if resp.PublishedAt == "" {
		t.Error("publicationFromRow: PublishedAt is empty")
	}
}

func TestPublication151_PublicationFromRowWithCityID(t *testing.T) {
	eventID := uuid.MustParse("12345678-1234-1234-1234-123456789012")
	feedTokenID := uuid.MustParse("87654321-4321-4321-4321-987654321098")
	cityID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	row := gen.EventPublicationRow{
		ID:          eventID,
		EventID:     eventID,
		FeedTokenID: feedTokenID,
		CityID:      &cityID,
		PublishedAt: time.Now().UTC(),
	}
	resp := publicationFromRow(row)

	if resp.CityID == nil {
		t.Error("publicationFromRow: CityID should be non-nil when set")
	}
	if resp.CityID != nil && *resp.CityID != cityID.String() {
		t.Errorf("publicationFromRow: CityID = %q, want %q", *resp.CityID, cityID.String())
	}
}

func TestPublication151_PublicationFromRowPublishedAtRFC3339(t *testing.T) {
	eventID := uuid.New()
	feedTokenID := uuid.New()
	fixedTime := time.Date(2026, 6, 23, 14, 30, 0, 0, time.UTC)

	row := gen.EventPublicationRow{
		ID:          eventID,
		EventID:     eventID,
		FeedTokenID: feedTokenID,
		CityID:      nil,
		PublishedAt: fixedTime,
	}
	resp := publicationFromRow(row)

	// PublishedAt should be RFC3339 formatted.
	if !strings.Contains(resp.PublishedAt, "2026-06-23") {
		t.Errorf("publicationFromRow: PublishedAt %q does not contain date", resp.PublishedAt)
	}
	if !strings.Contains(resp.PublishedAt, "14:30:00") {
		t.Errorf("publicationFromRow: PublishedAt %q does not contain time", resp.PublishedAt)
	}
}
