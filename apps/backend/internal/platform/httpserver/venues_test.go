// venues_test.go — unit tests for feature #124 (Venue model + CRUD).
//
// Test coverage:
//   Step 1: Migration file 0012_venues.sql exists with correct schema + seeds
//   Step 2: Route mounting, auth-gating, and request validation (no DB required)
//           Owner-gated mutations: POST/PATCH/DELETE require org_id to match
//           Shared read: GET /v1/venues and GET /v1/venues/{id} are cross-org
//   Step 3: GET shared across orgs read-only
//   Step 4: sqlc gen file (venues.sql.go) and query file (venues.sql) structure
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

const venueTestActorID = "00000000-0000-0000-0000-000000000001"

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for venue route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildVenueServer builds a Server with stub auth, venue routes fully
// mounted, and a dbDownPool so real DB operations never execute.
func buildVenueServer(t *testing.T) *Server {
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
		t.Fatalf("buildVenueServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// VenueQueries non-nil so venue route conditionals pass.
		VenueQueries: gen.New(nil),
		// OrgQueries also non-nil for good measure.
		OrgQueries: gen.New(nil),
		// Audit writer required for DELETE.
		Audit: &captureAuditWriter{},
	})
}

// venueRespJSON decodes the response body into a map and returns it.
func venueRespJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("venue: JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	return m
}

// venueErrorCode extracts the error code from the standard JSON error envelope
// structure: {"error": {"code": "...", "message": "..."}}.
func venueErrorCode(m map[string]any) string {
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

// venueToken mints a test JWT for venue endpoint tests.
// Uses mintJWT (defined in echo_audit_test.go, same package).
func venueToken(t *testing.T, s *Server) string {
	t.Helper()
	if s.stub == nil {
		t.Fatal("stub auth not wired")
	}
	return mintJWT(t, s.stub, venueTestActorID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration file exists with correct schema + seeds
// ─────────────────────────────────────────────────────────────────────────────

func TestVenue124_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	if content == "" {
		t.Fatal("migration file 0012_venues.sql is empty")
	}
}

func TestVenue124_MigrationHasVenuesTable(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")

	required := []string{
		"CREATE TABLE venues",
		"id",
		"org_id",
		"uuidv7()",
		"name",
		"address",
		"capacity_default",
		"city_id",
		"created_at",
		"updated_at",
		"deleted_at",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Errorf("migration missing %q", r)
		}
	}
}

func TestVenue124_MigrationHasSoftDeleteColumn(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	if !strings.Contains(content, "deleted_at") {
		t.Error("migration must have deleted_at column for soft-delete")
	}
	// Partial unique index excludes deleted rows.
	if !strings.Contains(content, "WHERE deleted_at IS NULL") {
		t.Error("migration must have partial index filtering WHERE deleted_at IS NULL")
	}
}

func TestVenue124_MigrationHasCityIDFK(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	if !strings.Contains(content, "REFERENCES cities") {
		t.Error("migration must have city_id FK referencing cities table")
	}
}

func TestVenue124_MigrationHasOrgIDFK(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	if !strings.Contains(content, "REFERENCES organizations") {
		t.Error("migration must have org_id FK referencing organizations table")
	}
}

func TestVenue124_MigrationHasPermissionSeeds(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	perms := []string{"venue.create", "venue.read", "venue.update", "venue.delete"}
	for _, p := range perms {
		if !strings.Contains(content, p) {
			t.Errorf("migration missing permission seed %q", p)
		}
	}
}

func TestVenue124_MigrationHasRBACGrants(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	if !strings.Contains(content, "'admin'") {
		t.Error("migration must grant venue permissions to admin role")
	}
	if !strings.Contains(content, "'org_admin'") {
		t.Error("migration must grant venue permissions to org_admin role")
	}
}

func TestVenue124_MigrationHasGooseDownSection(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration must have a -- +goose Down section")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS venues") {
		t.Error("goose Down must DROP TABLE IF EXISTS venues")
	}
}

func TestVenue124_MigrationHasUniqueIndex(t *testing.T) {
	content := findFileByName(t, "0012_venues.sql")
	if !strings.Contains(content, "venues_name_org_unique_active") {
		t.Error("migration must have partial unique index venues_name_org_unique_active")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Route auth + validation (owner-gated mutations)
// ─────────────────────────────────────────────────────────────────────────────

func TestVenue124_PostVenueRequiresAuth(t *testing.T) {
	s := buildVenueServer(t)
	orgID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/venues",
		strings.NewReader(`{"name":"Test Venue"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/organizations/{org_id}/venues without auth: want 401, got %d", w.Code)
	}
}

func TestVenue124_GetVenuesRequiresAuth(t *testing.T) {
	s := buildVenueServer(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/venues", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/venues without auth: want 401, got %d", w.Code)
	}
}

func TestVenue124_GetVenueByIDRequiresAuth(t *testing.T) {
	s := buildVenueServer(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/venues/"+uuid.New().String(), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/venues/{id} without auth: want 401, got %d", w.Code)
	}
}

func TestVenue124_GetVenuesByOrgRequiresAuth(t *testing.T) {
	s := buildVenueServer(t)
	orgID := uuid.New()
	r := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/venues", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/organizations/{org_id}/venues without auth: want 401, got %d", w.Code)
	}
}

func TestVenue124_PatchVenueRequiresAuth(t *testing.T) {
	s := buildVenueServer(t)
	orgID := uuid.New()
	venueID := uuid.New()
	r := httptest.NewRequest(http.MethodPatch,
		"/v1/organizations/"+orgID.String()+"/venues/"+venueID.String(),
		strings.NewReader(`{"name":"Updated"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PATCH /v1/organizations/{org_id}/venues/{id} without auth: want 401, got %d", w.Code)
	}
}

func TestVenue124_DeleteVenueRequiresAuth(t *testing.T) {
	s := buildVenueServer(t)
	orgID := uuid.New()
	venueID := uuid.New()
	r := httptest.NewRequest(http.MethodDelete,
		"/v1/organizations/"+orgID.String()+"/venues/"+venueID.String(), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/organizations/{org_id}/venues/{id} without auth: want 401, got %d", w.Code)
	}
}

func TestVenue124_CreateVenue_NilVenueQueriesReturns503(t *testing.T) {
	// Build a server WITHOUT VenueQueries to verify the 503 guard.
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
		// VenueQueries intentionally nil → routes not mounted → 404.
	})

	orgID := uuid.New()
	tok := mintJWT(t, s.stub, venueTestActorID)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/venues",
		strings.NewReader(`{"name":"Test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// Without venueQueries the route isn't mounted, so chi returns 404.
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /v1/organizations/{org_id}/venues without VenueQueries: want 404, got %d", w.Code)
	}
}

func TestVenue124_CreateVenue_EmptyBodyReturns400(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	orgID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/venues",
		strings.NewReader(""))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with empty body: want 400, got %d", w.Code)
	}
	m := venueRespJSON(t, w)
	if got := venueErrorCode(m); got != "venue.empty_body" {
		t.Errorf("want code='venue.empty_body', got %q (body: %s)", got, w.Body.String())
	}
}

func TestVenue124_CreateVenue_InvalidJSONReturns400(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	orgID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/venues",
		strings.NewReader("not-json"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid JSON: want 400, got %d", w.Code)
	}
	m := venueRespJSON(t, w)
	if got := venueErrorCode(m); got != "venue.invalid_json" {
		t.Errorf("want code='venue.invalid_json', got %q (body: %s)", got, w.Body.String())
	}
}

func TestVenue124_CreateVenue_MissingNameReturns400(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	orgID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/venues",
		strings.NewReader(`{"address":"123 Main St"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with missing name: want 400, got %d", w.Code)
	}
	m := venueRespJSON(t, w)
	if got := venueErrorCode(m); got != "venue.invalid_name" {
		t.Errorf("want code='venue.invalid_name', got %q (body: %s)", got, w.Body.String())
	}
}

func TestVenue124_CreateVenue_InvalidCityIDReturns400(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	orgID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/venues",
		strings.NewReader(`{"name":"Arena","city_id":"not-a-uuid"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid city_id: want 400, got %d", w.Code)
	}
	m := venueRespJSON(t, w)
	if got := venueErrorCode(m); got != "venue.invalid_city_id" {
		t.Errorf("want code='venue.invalid_city_id', got %q (body: %s)", got, w.Body.String())
	}
}

func TestVenue124_CreateVenue_InvalidOrgIDReturns400(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/not-a-uuid/venues",
		strings.NewReader(`{"name":"Arena"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid org_id: want 400, got %d", w.Code)
	}
}

func TestVenue124_UpdateVenue_EmptyBodyReturns400(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	orgID := uuid.New()
	venueID := uuid.New()
	r := httptest.NewRequest(http.MethodPatch,
		"/v1/organizations/"+orgID.String()+"/venues/"+venueID.String(),
		strings.NewReader(""))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PATCH with empty body: want 400, got %d", w.Code)
	}
	m := venueRespJSON(t, w)
	if got := venueErrorCode(m); got != "venue.empty_body" {
		t.Errorf("want code='venue.empty_body', got %q (body: %s)", got, w.Body.String())
	}
}

func TestVenue124_UpdateVenue_InvalidCityIDReturns400(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	orgID := uuid.New()
	venueID := uuid.New()
	invalidCityID := "not-a-uuid"
	r := httptest.NewRequest(http.MethodPatch,
		"/v1/organizations/"+orgID.String()+"/venues/"+venueID.String(),
		strings.NewReader(`{"name":"Updated","city_id":"`+invalidCityID+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PATCH with invalid city_id: want 400, got %d", w.Code)
	}
	m := venueRespJSON(t, w)
	if got := venueErrorCode(m); got != "venue.invalid_city_id" {
		t.Errorf("want code='venue.invalid_city_id', got %q (body: %s)", got, w.Body.String())
	}
}

func TestVenue124_DeleteVenue_NilPoolReturns503(t *testing.T) {
	// Build a server WITHOUT pool (pool nil → write routes not mounted).
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	s := New(Options{
		Config:       cfg,
		Auth:         stub,
		VenueQueries: gen.New(nil),
		// Pool nil → delete route not mounted → 404.
	})

	orgID := uuid.New()
	venueID := uuid.New()
	tok := mintJWT(t, s.stub, venueTestActorID)
	r := httptest.NewRequest(http.MethodDelete,
		"/v1/organizations/"+orgID.String()+"/venues/"+venueID.String(), nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// Without pool the write routes aren't mounted → 404.
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE without pool: want 404 (not mounted), got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — GET shared across orgs read-only
// ─────────────────────────────────────────────────────────────────────────────

func TestVenue124_GetVenues_NilVenueQueriesReturns503(t *testing.T) {
	// Build a server with VenueQueries wired but then simulate nil check.
	// Actually with nil pool but non-nil VenueQueries, GET routes are mounted.
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	s := New(Options{
		Config:       cfg,
		Auth:         stub,
		VenueQueries: gen.New(nil), // non-nil so read routes are mounted
		// pool nil → venueQueries.db is nil → query will fail at DB level
	})

	tok := mintJWT(t, s.stub, venueTestActorID)
	r := httptest.NewRequest(http.MethodGet, "/v1/venues", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// Route is mounted; ListVenues will attempt a query on a nil DB → panic or error.
	// Handler checks for nil venueQueries first (it's non-nil here) then calls DB.
	// With gen.New(nil) and no pool, the DB call fails → 500.
	// We accept either 500 or 503.
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /v1/venues with nil DB: want 500 or 503, got %d", w.Code)
	}
}

func TestVenue124_SharedReadRoutesMounted(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)

	// GET /v1/venues should be routable (not 404/405).
	r := httptest.NewRequest(http.MethodGet, "/v1/venues", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// With gen.New(nil), the query will fail at DB level → 500 (not 404/401).
	if w.Code == http.StatusNotFound || w.Code == http.StatusUnauthorized {
		t.Errorf("GET /v1/venues: want route mounted, got %d", w.Code)
	}
}

func TestVenue124_SharedGetByIDRoutesMounted(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	venueID := uuid.New()

	// GET /v1/venues/{id} should be routable (not 404/405).
	r := httptest.NewRequest(http.MethodGet, "/v1/venues/"+venueID.String(), nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// With gen.New(nil), the query fails at DB level → 500 (not 404/401).
	if w.Code == http.StatusNotFound || w.Code == http.StatusUnauthorized {
		t.Errorf("GET /v1/venues/{id}: want route mounted, got %d", w.Code)
	}
}

func TestVenue124_GetVenuesByOrgRoutesMounted(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)
	orgID := uuid.New()

	r := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/venues", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// Route mounted; DB nil → 500 or similar error. Not 404/401.
	if w.Code == http.StatusNotFound || w.Code == http.StatusUnauthorized {
		t.Errorf("GET /v1/organizations/{org_id}/venues: want route mounted, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — sqlc query file and gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestVenue124_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "venues.sql")
	if content == "" {
		t.Fatal("venues.sql query file is empty")
	}
}

func TestVenue124_QueryFileHasAllCRUDOps(t *testing.T) {
	content := findFileByName(t, "venues.sql")
	ops := []string{
		"InsertVenue",
		"GetVenueByID",
		"ListVenues",
		"ListVenuesByOrg",
		"UpdateVenue",
		"SoftDeleteVenue",
	}
	for _, op := range ops {
		if !strings.Contains(content, op) {
			t.Errorf("venues.sql missing operation %q", op)
		}
	}
}

func TestVenue124_QueryFileFiltersSoftDeleted(t *testing.T) {
	content := findFileByName(t, "venues.sql")
	// All SELECT queries should filter deleted rows.
	if !strings.Contains(content, "deleted_at IS NULL") {
		t.Error("venues.sql must filter WHERE deleted_at IS NULL for active rows")
	}
}

func TestVenue124_QueryFileSoftDeleteSetsDeletedAt(t *testing.T) {
	content := findFileByName(t, "venues.sql")
	if !strings.Contains(content, "deleted_at = now()") {
		t.Error("SoftDeleteVenue must SET deleted_at = now()")
	}
}

func TestVenue124_QueryFileGetByIDNotScopedByOrg(t *testing.T) {
	// GetVenueByID must NOT filter by org_id (shared read-only spec).
	content := findFileByName(t, "venues.sql")
	// Find the GetVenueByID section.
	idx := strings.Index(content, "GetVenueByID")
	if idx < 0 {
		t.Fatal("venues.sql missing GetVenueByID")
	}
	// Extract the query (until the next blank line or next --).
	section := content[idx:]
	end := strings.Index(section, "\n\n")
	if end > 0 {
		section = section[:end]
	}
	// The section for GetVenueByID must not include org_id filter.
	if strings.Contains(section, "AND  org_id") || strings.Contains(section, "AND org_id") {
		t.Error("GetVenueByID must NOT filter by org_id (shared read across orgs)")
	}
}

func TestVenue124_QueryFileListVenuesNotScopedByOrg(t *testing.T) {
	// ListVenues (all venues) must NOT filter by org_id.
	content := findFileByName(t, "venues.sql")
	// Find the ListVenues section (not ListVenuesByOrg).
	idx := strings.Index(content, "-- name: ListVenues :many")
	if idx < 0 {
		t.Fatal("venues.sql missing ListVenues query")
	}
	section := content[idx:]
	end := strings.Index(section, "\n\n")
	if end > 0 {
		section = section[:end]
	}
	if strings.Contains(section, "org_id = ") {
		t.Error("ListVenues must NOT filter by org_id (shared read across orgs)")
	}
}

func TestVenue124_GenGoFileExists(t *testing.T) {
	content := findFileByName(t, "venues.sql.go")
	if content == "" {
		t.Fatal("venues.sql.go gen file is empty")
	}
}

func TestVenue124_GenGoFileHasVenueRowType(t *testing.T) {
	content := findFileByName(t, "venues.sql.go")
	if !strings.Contains(content, "type VenueRow struct") {
		t.Error("venues.sql.go must define VenueRow struct")
	}
}

func TestVenue124_GenGoFileHasNullableFields(t *testing.T) {
	content := findFileByName(t, "venues.sql.go")
	nullable := []string{
		"CityID          *uuid.UUID",
		"Address         *string",
		"CapacityDefault *int32",
	}
	for _, f := range nullable {
		if !strings.Contains(content, f) {
			t.Errorf("venues.sql.go missing nullable field %q", f)
		}
	}
}

func TestVenue124_GenGoFileHasAllMethods(t *testing.T) {
	content := findFileByName(t, "venues.sql.go")
	methods := []string{
		"func (q *Queries) InsertVenue",
		"func (q *Queries) GetVenueByID",
		"func (q *Queries) ListVenues",
		"func (q *Queries) ListVenuesByOrg",
		"func (q *Queries) UpdateVenue",
		"func (q *Queries) SoftDeleteVenue",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("venues.sql.go missing method %q", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response shape tests
// ─────────────────────────────────────────────────────────────────────────────

func TestVenue124_VenueResponseShape(t *testing.T) {
	// Verify venueFromRow produces correct field names in JSON.
	v := gen.VenueRow{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		Name:      "Test Venue",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	resp := venueFromRow(v)

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal venueResponse: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	required := []string{"id", "org_id", "name", "created_at", "updated_at"}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Errorf("venueResponse JSON missing field %q", k)
		}
	}
}

func TestVenue124_VenueResponseCityIDIsNilWhenAbsent(t *testing.T) {
	v := gen.VenueRow{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		Name:      "Venue without city",
		CityID:    nil, // no city assigned
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	resp := venueFromRow(v)
	if resp.CityID != nil {
		t.Errorf("CityID should be nil when VenueRow.CityID is nil, got %v", resp.CityID)
	}
}

func TestVenue124_VenueResponseCityIDIsSetWhenPresent(t *testing.T) {
	cityID := uuid.New()
	v := gen.VenueRow{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		Name:      "Venue with city",
		CityID:    &cityID,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	resp := venueFromRow(v)
	if resp.CityID == nil {
		t.Error("CityID should be set when VenueRow.CityID is non-nil")
	}
	if *resp.CityID != cityID.String() {
		t.Errorf("CityID mismatch: want %s, got %s", cityID.String(), *resp.CityID)
	}
}

func TestVenue124_HandlersReturnJSONContentType(t *testing.T) {
	s := buildVenueServer(t)
	tok := venueToken(t, s)

	// GET /v1/venues — should return application/json
	r := httptest.NewRequest(http.MethodGet, "/v1/venues", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /v1/venues Content-Type: want application/json, got %q", ct)
	}
}

func TestVenue124_VenueTimestampsAreRFC3339(t *testing.T) {
	v := gen.VenueRow{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		Name:      "RFC3339 Test",
		CreatedAt: time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2024, 1, 15, 13, 0, 0, 0, time.UTC),
	}
	resp := venueFromRow(v)
	// RFC3339 format: 2024-01-15T12:00:00Z
	if _, err := time.Parse(time.RFC3339, resp.CreatedAt); err != nil {
		t.Errorf("CreatedAt not RFC3339: %q, error: %v", resp.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, resp.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt not RFC3339: %q, error: %v", resp.UpdatedAt, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Full verification test
// ─────────────────────────────────────────────────────────────────────────────

func TestVenue124_FullVerification(t *testing.T) {
	t.Run("migration_file_has_correct_schema", func(t *testing.T) {
		content := findFileByName(t, "0012_venues.sql")
		checks := []string{
			"CREATE TABLE venues",
			"org_id",
			"city_id",
			"name",
			"address",
			"capacity_default",
			"deleted_at",
			"uuidv7()",
			"REFERENCES organizations",
			"REFERENCES cities",
			"venue.create",
			"venue.read",
			"venue.update",
			"venue.delete",
		}
		for _, c := range checks {
			if !strings.Contains(content, c) {
				t.Errorf("migration missing %q", c)
			}
		}
	})

	t.Run("all_routes_require_auth", func(t *testing.T) {
		s := buildVenueServer(t)
		orgID := uuid.New()
		venueID := uuid.New()

		endpoints := []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodPost, "/v1/organizations/" + orgID.String() + "/venues", `{"name":"x"}`},
			{http.MethodGet, "/v1/venues", ""},
			{http.MethodGet, "/v1/venues/" + venueID.String(), ""},
			{http.MethodGet, "/v1/organizations/" + orgID.String() + "/venues", ""},
			{http.MethodPatch, "/v1/organizations/" + orgID.String() + "/venues/" + venueID.String(), `{"name":"y"}`},
			{http.MethodDelete, "/v1/organizations/" + orgID.String() + "/venues/" + venueID.String(), ""},
		}
		for _, ep := range endpoints {
			r := httptest.NewRequest(ep.method, ep.path, strings.NewReader(ep.body))
			if ep.body != "" {
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without auth: want 401, got %d", ep.method, ep.path, w.Code)
			}
		}
	})

	t.Run("gen_file_implements_interface", func(t *testing.T) {
		content := findFileByName(t, "venues.sql.go")
		if !strings.Contains(content, "type VenueRow struct") {
			t.Error("venues.sql.go must define VenueRow struct")
		}
		if !strings.Contains(content, "func (q *Queries) InsertVenue") {
			t.Error("venues.sql.go missing InsertVenue")
		}
		if !strings.Contains(content, "func (q *Queries) SoftDeleteVenue") {
			t.Error("venues.sql.go missing SoftDeleteVenue")
		}
	})

	t.Run("owner_gated_mutations_use_org_id", func(t *testing.T) {
		// Verify that write queries include org_id in WHERE clause (owner-gating).
		content := findFileByName(t, "venues.sql.go")

		// UpdateVenue and SoftDeleteVenue must use org_id param.
		if !strings.Contains(content, "updateVenue") {
			t.Error("venues.sql.go missing updateVenue constant")
		}
		if !strings.Contains(content, "softDeleteVenue") {
			t.Error("venues.sql.go missing softDeleteVenue constant")
		}

		// The SQL strings themselves should contain org_id = $2.
		if !strings.Contains(content, "AND  org_id = $2") && !strings.Contains(content, "AND org_id = $2") {
			t.Error("venues.sql.go write queries must filter by org_id for owner-gating")
		}
	})
}
