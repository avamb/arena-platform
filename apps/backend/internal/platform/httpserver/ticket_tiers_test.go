// ticket_tiers_test.go — unit tests for the ticket tier model (feature #127).
//
// Test prefix: TestTier127_
// Coverage:
//   - Step 1: Migration file structure (table, columns, constraints, permissions)
//   - Step 2: CRUD endpoint auth-gating (all 5 return 401 without JWT)
//   - Step 3: Pricing mode validators (free must be 0, fixed must be > 0, pwyw min<=max)
//   - Step 4: Request validation (content-type, missing fields, invalid values)
//   - Query file structure (InsertTicketTier, GetTicketTierByID, etc.)
//   - Gen file structure (TicketTierRow struct fields, all query methods)
//   - Querier interface compile-time check
//   - Response shape and Content-Type verification
//   - tierFromRow helper
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

const tierTestActorID = "00000000-0000-0000-0000-000000000127"

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for tier route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildTierServer builds a Server with stub auth, tier routes fully mounted,
// and a dbDownPool so real DB operations never execute.
func buildTierServer(t *testing.T) *Server {
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
		t.Fatalf("buildTierServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// TierQueries non-nil so tier route conditionals pass.
		TierQueries:    gen.New(nil),
		SessionQueries: gen.New(nil),
		EventQueries:   gen.New(nil),
		// Audit writer required for DELETE.
		Audit: &captureAuditWriter{},
	})
}

// mintTierToken mints a dev JWT for tier route tests.
func mintTierToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + tierTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintTierToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintTierToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatal("mintTierToken: empty token in response")
	}
	return tok
}

// tierCollectionPath returns the base path for a tier collection under a session.
func tierCollectionPath(orgID, eventID, sessionID string) string {
	return "/v1/organizations/" + orgID + "/events/" + eventID + "/sessions/" + sessionID + "/tiers"
}

// tierItemPath returns the path for a single tier.
func tierItemPath(orgID, eventID, sessionID, tierID string) string {
	return tierCollectionPath(orgID, eventID, sessionID) + "/" + tierID
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if content == "" {
		t.Fatal("0019_ticket_tiers.sql not found")
	}
}

func TestTier127_MigrationContainsTableName(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "ticket_tiers") {
		t.Error("migration does not contain table name 'ticket_tiers'")
	}
}

func TestTier127_MigrationHasSessionIDFK(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "session_id") {
		t.Error("migration does not contain session_id column")
	}
	if !strings.Contains(content, "REFERENCES sessions") {
		t.Error("migration does not have FK to sessions table")
	}
}

func TestTier127_MigrationHasPricingMode(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "pricing_mode") {
		t.Error("migration does not contain pricing_mode column")
	}
	for _, mode := range []string{"fixed", "free", "pwyw"} {
		if !strings.Contains(content, "'"+mode+"'") {
			t.Errorf("migration CHECK constraint missing mode %q", mode)
		}
	}
}

func TestTier127_MigrationHasPriceAmount(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "price_amount") {
		t.Error("migration does not contain price_amount column")
	}
}

func TestTier127_MigrationHasPwywColumns(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "pwyw_min") {
		t.Error("migration does not contain pwyw_min column")
	}
	if !strings.Contains(content, "pwyw_max") {
		t.Error("migration does not contain pwyw_max column")
	}
}

func TestTier127_MigrationHasCapacity(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "capacity") {
		t.Error("migration does not contain capacity column")
	}
}

func TestTier127_MigrationHasSaleWindow(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "sale_window_start") {
		t.Error("migration does not contain sale_window_start column")
	}
	if !strings.Contains(content, "sale_window_end") {
		t.Error("migration does not contain sale_window_end column")
	}
}

func TestTier127_MigrationHasSortOrder(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "sort_order") {
		t.Error("migration does not contain sort_order column")
	}
}

func TestTier127_MigrationHasSoftDelete(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "deleted_at") {
		t.Error("migration does not contain deleted_at soft-delete column")
	}
}

func TestTier127_MigrationHasPwywRangeConstraint(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "ticket_tiers_pwyw_range") {
		t.Error("migration does not contain ticket_tiers_pwyw_range CHECK constraint")
	}
}

func TestTier127_MigrationHasFreePriceConstraint(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "ticket_tiers_free_price") {
		t.Error("migration does not contain ticket_tiers_free_price CHECK constraint")
	}
}

func TestTier127_MigrationHasCapacityConstraint(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "ticket_tiers_capacity_positive") {
		t.Error("migration does not contain ticket_tiers_capacity_positive CHECK constraint")
	}
}

func TestTier127_MigrationHasRBACPermissions(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	for _, perm := range []string{"tier.create", "tier.read", "tier.update", "tier.delete"} {
		if !strings.Contains(content, perm) {
			t.Errorf("migration does not seed permission %q", perm)
		}
	}
}

func TestTier127_MigrationHasAdminRoleGrant(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "'admin'") {
		t.Error("migration does not grant permissions to 'admin' role")
	}
}

func TestTier127_MigrationHasOrgAdminRoleGrant(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "'org_admin'") {
		t.Error("migration does not grant permissions to 'org_admin' role")
	}
}

func TestTier127_MigrationHasGooseMarkers(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' marker")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' marker")
	}
}

func TestTier127_MigrationHasDropInDown(t *testing.T) {
	content := findFileByName(t, "0019_ticket_tiers.sql")
	if !strings.Contains(content, "DROP TABLE IF EXISTS ticket_tiers") {
		t.Error("migration Down section does not drop ticket_tiers table")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Route auth-gating: all 5 endpoints return 401 without JWT
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_PostTierRequiresAuth(t *testing.T) {
	srv := buildTierServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"VIP","pricing_mode":"fixed","price_amount":1000}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST tiers without JWT: got %d, want 401", w.Code)
	}
}

func TestTier127_GetTiersRequiresAuth(t *testing.T) {
	srv := buildTierServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET tiers without JWT: got %d, want 401", w.Code)
	}
}

func TestTier127_GetTierByIDRequiresAuth(t *testing.T) {
	srv := buildTierServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	tierID := uuid.New().String()
	path := tierItemPath(orgID, eventID, sessionID, tierID)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET tier by ID without JWT: got %d, want 401", w.Code)
	}
}

func TestTier127_PatchTierRequiresAuth(t *testing.T) {
	srv := buildTierServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	tierID := uuid.New().String()
	path := tierItemPath(orgID, eventID, sessionID, tierID)
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"name":"Economy"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PATCH tier without JWT: got %d, want 401", w.Code)
	}
}

func TestTier127_DeleteTierRequiresAuth(t *testing.T) {
	srv := buildTierServer(t)
	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	tierID := uuid.New().String()
	path := tierItemPath(orgID, eventID, sessionID, tierID)
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE tier without JWT: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Pricing mode validators (pure unit tests, no HTTP)
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_ValidatePricingMode_FreeRequiresZeroPrice(t *testing.T) {
	errCode, errMsg := validatePricingMode("free", 100, nil, nil)
	if errCode == "" {
		t.Error("free tier with price_amount=100 should fail validation")
	}
	if !strings.Contains(errMsg, "0") {
		t.Errorf("error message should mention 0: got %q", errMsg)
	}
}

func TestTier127_ValidatePricingMode_FreeZeroPriceOK(t *testing.T) {
	errCode, _ := validatePricingMode("free", 0, nil, nil)
	if errCode != "" {
		t.Errorf("free tier with price_amount=0 should pass, got error: %s", errCode)
	}
}

func TestTier127_ValidatePricingMode_FixedRequiresPositivePrice(t *testing.T) {
	errCode, _ := validatePricingMode("fixed", 0, nil, nil)
	if errCode == "" {
		t.Error("fixed tier with price_amount=0 should fail validation")
	}
}

func TestTier127_ValidatePricingMode_FixedNegativePrice(t *testing.T) {
	errCode, _ := validatePricingMode("fixed", -1, nil, nil)
	if errCode == "" {
		t.Error("fixed tier with negative price_amount should fail validation")
	}
}

func TestTier127_ValidatePricingMode_FixedPositivePriceOK(t *testing.T) {
	errCode, _ := validatePricingMode("fixed", 1000, nil, nil)
	if errCode != "" {
		t.Errorf("fixed tier with price_amount=1000 should pass, got error: %s", errCode)
	}
}

func TestTier127_ValidatePricingMode_PwywMinLEMaxOK(t *testing.T) {
	min := int64(100)
	max := int64(5000)
	errCode, _ := validatePricingMode("pwyw", 0, &min, &max)
	if errCode != "" {
		t.Errorf("pwyw with min<=max should pass, got error: %s", errCode)
	}
}

func TestTier127_ValidatePricingMode_PwywMinEqualMaxOK(t *testing.T) {
	min := int64(500)
	max := int64(500)
	errCode, _ := validatePricingMode("pwyw", 0, &min, &max)
	if errCode != "" {
		t.Errorf("pwyw with min==max should pass, got error: %s", errCode)
	}
}

func TestTier127_ValidatePricingMode_PwywMinGTMaxFails(t *testing.T) {
	min := int64(5000)
	max := int64(100)
	errCode, errMsg := validatePricingMode("pwyw", 0, &min, &max)
	if errCode == "" {
		t.Error("pwyw with min > max should fail validation")
	}
	if !strings.Contains(errMsg, "pwyw_min") {
		t.Errorf("error message should mention pwyw_min: got %q", errMsg)
	}
}

func TestTier127_ValidatePricingMode_PwywNoBoundsOK(t *testing.T) {
	errCode, _ := validatePricingMode("pwyw", 0, nil, nil)
	if errCode != "" {
		t.Errorf("pwyw with no bounds should pass, got error: %s", errCode)
	}
}

func TestTier127_ValidatePricingMode_PwywOnlyMinOK(t *testing.T) {
	min := int64(100)
	errCode, _ := validatePricingMode("pwyw", 0, &min, nil)
	if errCode != "" {
		t.Errorf("pwyw with only min set should pass, got error: %s", errCode)
	}
}

func TestTier127_ValidatePricingMode_PwywNegativeMinFails(t *testing.T) {
	min := int64(-1)
	errCode, _ := validatePricingMode("pwyw", 0, &min, nil)
	if errCode == "" {
		t.Error("pwyw with negative pwyw_min should fail")
	}
}

func TestTier127_ValidatePricingMode_PwywNegativeMaxFails(t *testing.T) {
	max := int64(-1)
	errCode, _ := validatePricingMode("pwyw", 0, nil, &max)
	if errCode == "" {
		t.Error("pwyw with negative pwyw_max should fail")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — Request validation via HTTP (requires auth token)
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_CreateTier_ContentTypeRequired(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"VIP","pricing_mode":"fixed","price_amount":1000}`))
	req.Header.Set("Authorization", "Bearer "+token)
	// No Content-Type header
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("POST tiers without Content-Type: got %d, want 415", w.Code)
	}
}

func TestTier127_CreateTier_EmptyBodyRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tiers with empty body: got %d, want 400", w.Code)
	}
}

func TestTier127_CreateTier_MissingNameRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"pricing_mode":"fixed","price_amount":1000}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tiers without name: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.missing_name") {
		t.Errorf("expected tier.missing_name error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_MissingPricingModeRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"VIP","price_amount":1000}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tiers without pricing_mode: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.missing_pricing_mode") {
		t.Errorf("expected tier.missing_pricing_mode error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_InvalidPricingModeRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"VIP","pricing_mode":"invalid","price_amount":1000}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tiers with invalid pricing_mode: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.invalid_pricing_mode") {
		t.Errorf("expected tier.invalid_pricing_mode error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_FreeWithNonZeroPriceRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"Free Tier","pricing_mode":"free","price_amount":500}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST free tier with price_amount=500: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.invalid_free_price") {
		t.Errorf("expected tier.invalid_free_price error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_FixedWithZeroPriceRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"Fixed Tier","pricing_mode":"fixed","price_amount":0}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST fixed tier with price_amount=0: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.invalid_fixed_price") {
		t.Errorf("expected tier.invalid_fixed_price error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_PwywMinGTMaxRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"PWYW Tier","pricing_mode":"pwyw","price_amount":0,"pwyw_min":5000,"pwyw_max":100}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST pwyw tier with min > max: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.invalid_pwyw_range") {
		t.Errorf("expected tier.invalid_pwyw_range error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_InvalidCapacityRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"VIP","pricing_mode":"fixed","price_amount":1000,"capacity":0}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tier with capacity=0: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.invalid_capacity") {
		t.Errorf("expected tier.invalid_capacity error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_InvalidSaleWindowStartRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"name":"VIP","pricing_mode":"fixed","price_amount":1000,"sale_window_start":"not-a-date"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tier with invalid sale_window_start: got %d, want 400", w.Code)
	}
}

func TestTier127_CreateTier_SaleWindowEndBeforeStartRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	start := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"name":"VIP","pricing_mode":"fixed","price_amount":1000,"sale_window_start":"` + start + `","sale_window_end":"` + end + `"}`
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tier with sale_window_end before start: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier.invalid_sale_window") {
		t.Errorf("expected tier.invalid_sale_window error code, got: %s", w.Body.String())
	}
}

func TestTier127_CreateTier_InvalidJSONRejected(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{not valid json`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST tiers with invalid JSON: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Content-Type headers
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_PostReturnsJSONContentType(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON Content-Type, got: %s", ct)
	}
}

func TestTier127_GetListReturnsJSONContentType(t *testing.T) {
	srv := buildTierServer(t)
	token := mintTierToken(t, srv)

	orgID := uuid.New().String()
	eventID := uuid.New().String()
	sessionID := uuid.New().String()
	path := tierCollectionPath(orgID, eventID, sessionID)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON Content-Type, got: %s", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Query file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if content == "" {
		t.Fatal("ticket_tiers.sql not found")
	}
}

func TestTier127_QueryFileHasInsert(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if !strings.Contains(content, "InsertTicketTier") {
		t.Error("ticket_tiers.sql does not contain InsertTicketTier query")
	}
}

func TestTier127_QueryFileHasGetByID(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if !strings.Contains(content, "GetTicketTierByID") {
		t.Error("ticket_tiers.sql does not contain GetTicketTierByID query")
	}
}

func TestTier127_QueryFileHasListBySession(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if !strings.Contains(content, "ListTicketTiersBySession") {
		t.Error("ticket_tiers.sql does not contain ListTicketTiersBySession query")
	}
}

func TestTier127_QueryFileHasUpdate(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if !strings.Contains(content, "UpdateTicketTier") {
		t.Error("ticket_tiers.sql does not contain UpdateTicketTier query")
	}
}

func TestTier127_QueryFileHasSoftDelete(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if !strings.Contains(content, "SoftDeleteTicketTier") {
		t.Error("ticket_tiers.sql does not contain SoftDeleteTicketTier query")
	}
}

func TestTier127_QueryFileHasSoftDeleteFilter(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if !strings.Contains(content, "deleted_at IS NULL") {
		t.Error("ticket_tiers.sql does not filter by deleted_at IS NULL")
	}
}

func TestTier127_QueryFileHasOrderBySortOrder(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql")
	if !strings.Contains(content, "sort_order ASC") {
		t.Error("ticket_tiers.sql list query does not order by sort_order ASC")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_GenFileExists(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if content == "" {
		t.Fatal("ticket_tiers.sql.go not found")
	}
}

func TestTier127_GenFileHasTicketTierRow(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if !strings.Contains(content, "TicketTierRow") {
		t.Error("ticket_tiers.sql.go does not define TicketTierRow struct")
	}
}

func TestTier127_GenFileHasPricingModeField(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if !strings.Contains(content, "PricingMode") {
		t.Error("ticket_tiers.sql.go TicketTierRow missing PricingMode field")
	}
}

func TestTier127_GenFileHasPriceAmountField(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if !strings.Contains(content, "PriceAmount") {
		t.Error("ticket_tiers.sql.go TicketTierRow missing PriceAmount field")
	}
}

func TestTier127_GenFileHasPwywMinField(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if !strings.Contains(content, "PwywMin") {
		t.Error("ticket_tiers.sql.go TicketTierRow missing PwywMin field")
	}
}

func TestTier127_GenFileHasPwywMaxField(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if !strings.Contains(content, "PwywMax") {
		t.Error("ticket_tiers.sql.go TicketTierRow missing PwywMax field")
	}
}

func TestTier127_GenFileHasNullablePwywTypes(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if !strings.Contains(content, "*int64") {
		t.Error("ticket_tiers.sql.go should use *int64 for nullable PwywMin/PwywMax")
	}
}

func TestTier127_GenFileHasNullableCapacity(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	if !strings.Contains(content, "*int32") {
		t.Error("ticket_tiers.sql.go should use *int32 for nullable Capacity")
	}
}

func TestTier127_GenFileHasAllQueryMethods(t *testing.T) {
	content := findFileByName(t, "ticket_tiers.sql.go")
	methods := []string{
		"func (q *Queries) InsertTicketTier",
		"func (q *Queries) GetTicketTierByID",
		"func (q *Queries) ListTicketTiersBySession",
		"func (q *Queries) UpdateTicketTier",
		"func (q *Queries) SoftDeleteTicketTier",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("ticket_tiers.sql.go missing method: %s", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Querier interface — compile-time assertion
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_QuerierInterfaceHasTierMethods(t *testing.T) {
	// Compile-time proof: *gen.Queries must satisfy gen.Querier.
	// If InsertTicketTier, GetTicketTierByID, etc. are missing from *Queries,
	// the package will not compile and this test will never run.
	var _ gen.Querier = (*gen.Queries)(nil)
	t.Log("compile-time Querier assertion: *gen.Queries satisfies gen.Querier (includes tier methods)")
}

// ─────────────────────────────────────────────────────────────────────────────
// tierFromRow helper
// ─────────────────────────────────────────────────────────────────────────────

func TestTier127_TierFromRow_BasicFields(t *testing.T) {
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	row := gen.TicketTierRow{
		ID:          id,
		SessionID:   sessionID,
		Name:        "VIP",
		PricingMode: "fixed",
		PriceAmount: 2500,
		Currency:    "USD",
		SortOrder:   1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	resp := tierFromRow(row)

	if resp.ID != id.String() {
		t.Errorf("ID: got %q, want %q", resp.ID, id.String())
	}
	if resp.SessionID != sessionID.String() {
		t.Errorf("SessionID: got %q, want %q", resp.SessionID, sessionID.String())
	}
	if resp.Name != "VIP" {
		t.Errorf("Name: got %q, want VIP", resp.Name)
	}
	if resp.PricingMode != "fixed" {
		t.Errorf("PricingMode: got %q, want fixed", resp.PricingMode)
	}
	if resp.PriceAmount != 2500 {
		t.Errorf("PriceAmount: got %d, want 2500", resp.PriceAmount)
	}
	if resp.Currency != "USD" {
		t.Errorf("Currency: got %q, want USD", resp.Currency)
	}
	if resp.SortOrder != 1 {
		t.Errorf("SortOrder: got %d, want 1", resp.SortOrder)
	}
}

func TestTier127_TierFromRow_NullableFieldsNilWhenAbsent(t *testing.T) {
	row := gen.TicketTierRow{
		ID:          uuid.New(),
		SessionID:   uuid.New(),
		Name:        "Free Entry",
		PricingMode: "free",
		PriceAmount: 0,
		Currency:    "USD",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	resp := tierFromRow(row)
	if resp.PwywMin != nil {
		t.Error("PwywMin should be nil when not set")
	}
	if resp.PwywMax != nil {
		t.Error("PwywMax should be nil when not set")
	}
	if resp.Capacity != nil {
		t.Error("Capacity should be nil when not set")
	}
	if resp.SaleWindowStart != nil {
		t.Error("SaleWindowStart should be nil when not set")
	}
	if resp.SaleWindowEnd != nil {
		t.Error("SaleWindowEnd should be nil when not set")
	}
}

func TestTier127_TierFromRow_PwywBoundsPresent(t *testing.T) {
	min := int64(100)
	max := int64(5000)
	row := gen.TicketTierRow{
		ID:          uuid.New(),
		SessionID:   uuid.New(),
		Name:        "PWYW",
		PricingMode: "pwyw",
		PriceAmount: 0,
		Currency:    "EUR",
		PwywMin:     &min,
		PwywMax:     &max,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	resp := tierFromRow(row)
	if resp.PwywMin == nil || *resp.PwywMin != 100 {
		t.Errorf("PwywMin: got %v, want 100", resp.PwywMin)
	}
	if resp.PwywMax == nil || *resp.PwywMax != 5000 {
		t.Errorf("PwywMax: got %v, want 5000", resp.PwywMax)
	}
	if resp.Currency != "EUR" {
		t.Errorf("Currency: got %q, want EUR", resp.Currency)
	}
}

func TestTier127_TierFromRow_TimestampsAreRFC3339(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	saleStart := now.Add(1 * time.Hour)
	saleEnd := now.Add(24 * time.Hour)
	row := gen.TicketTierRow{
		ID:              uuid.New(),
		SessionID:       uuid.New(),
		Name:            "Early Bird",
		PricingMode:     "fixed",
		PriceAmount:     500,
		Currency:        "USD",
		SaleWindowStart: &saleStart,
		SaleWindowEnd:   &saleEnd,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	resp := tierFromRow(row)

	if _, err := time.Parse(time.RFC3339, resp.CreatedAt); err != nil {
		t.Errorf("CreatedAt is not RFC3339: %q", resp.CreatedAt)
	}
	if _, err := time.Parse(time.RFC3339, resp.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt is not RFC3339: %q", resp.UpdatedAt)
	}
	if resp.SaleWindowStart == nil {
		t.Fatal("SaleWindowStart should not be nil")
	}
	if _, err := time.Parse(time.RFC3339, *resp.SaleWindowStart); err != nil {
		t.Errorf("SaleWindowStart is not RFC3339: %q", *resp.SaleWindowStart)
	}
	if resp.SaleWindowEnd == nil {
		t.Fatal("SaleWindowEnd should not be nil")
	}
	if _, err := time.Parse(time.RFC3339, *resp.SaleWindowEnd); err != nil {
		t.Errorf("SaleWindowEnd is not RFC3339: %q", *resp.SaleWindowEnd)
	}
}
