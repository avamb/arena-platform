// promo_128_test.go — unit tests for feature #128 (Promo code model + validation).
//
// Test coverage:
//
//	Step 1: Migration file 0022_promo_codes.sql — table, constraints, RBAC seeds
//	Step 2: SQL query file and gen file structure
//	Step 3: Discount math — pure function tests for computeDiscount
//	Step 4: HTTP routes — auth-gating, server wiring
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
)

// ─────────────────────────────────────────────────────────────────────────────
// Test actor ID
// ─────────────────────────────────────────────────────────────────────────────

const promoTestActorID = "00000000-0000-0000-0000-000000000128"

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for promo route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildPromoServer builds a Server with stub auth, promo routes fully mounted,
// and a dbDownPool so real DB operations never execute.
func buildPromoServer(t *testing.T) *Server {
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
		t.Fatalf("buildPromoServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// PromoQueries non-nil so promo route conditionals pass.
		PromoQueries: gen.New(nil),
	})
}

// mintPromoToken mints a dev JWT for promo route tests.
func mintPromoToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + promoTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintPromoToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintPromoToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintPromoToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo128_Step1_MigrationExists(t *testing.T) {
	content := findFileByName(t, "0022_promo_codes.sql")

	t.Run("goose_up_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Up") {
			t.Error("0022_promo_codes.sql: missing '-- +goose Up' marker")
		}
	})

	t.Run("goose_down_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Down") {
			t.Error("0022_promo_codes.sql: missing '-- +goose Down' marker")
		}
	})

	t.Run("creates_promo_codes_table", func(t *testing.T) {
		if !strings.Contains(content, "CREATE TABLE promo_codes") {
			t.Error("0022_promo_codes.sql: missing 'CREATE TABLE promo_codes'")
		}
	})

	t.Run("creates_promo_code_redemptions_table", func(t *testing.T) {
		if !strings.Contains(content, "CREATE TABLE promo_code_redemptions") {
			t.Error("0022_promo_codes.sql: missing 'CREATE TABLE promo_code_redemptions'")
		}
	})

	t.Run("discount_type_check_constraint", func(t *testing.T) {
		if !strings.Contains(content, "discount_type IN ('percent', 'fixed_amount')") {
			t.Error("0022_promo_codes.sql: missing discount_type CHECK constraint")
		}
	})

	t.Run("status_check_constraint", func(t *testing.T) {
		if !strings.Contains(content, "status IN ('active', 'inactive', 'exhausted', 'expired')") {
			t.Error("0022_promo_codes.sql: missing status CHECK constraint")
		}
	})

	t.Run("unique_per_org_constraint", func(t *testing.T) {
		if !strings.Contains(content, "UNIQUE (org_id, code)") {
			t.Error("0022_promo_codes.sql: missing UNIQUE (org_id, code) constraint")
		}
	})

	t.Run("promo_create_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "promo.create") {
			t.Error("0022_promo_codes.sql: missing 'promo.create' permission seed")
		}
	})

	t.Run("promo_validate_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "promo.validate") {
			t.Error("0022_promo_codes.sql: missing 'promo.validate' permission seed")
		}
	})

	t.Run("drop_tables_in_down_section", func(t *testing.T) {
		if !strings.Contains(content, "DROP TABLE IF EXISTS promo_codes") {
			t.Error("0022_promo_codes.sql: Down section missing DROP TABLE promo_codes")
		}
		if !strings.Contains(content, "DROP TABLE IF EXISTS promo_code_redemptions") {
			t.Error("0022_promo_codes.sql: Down section missing DROP TABLE promo_code_redemptions")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file and gen file
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo128_Step2_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "promo_codes.sql")

	t.Run("insert_promo_code_query", func(t *testing.T) {
		if !strings.Contains(content, "InsertPromoCode") {
			t.Error("promo_codes.sql: missing 'InsertPromoCode' query")
		}
	})

	t.Run("get_promo_code_by_code_query", func(t *testing.T) {
		if !strings.Contains(content, "GetPromoCodeByCode") {
			t.Error("promo_codes.sql: missing 'GetPromoCodeByCode' query")
		}
	})

	t.Run("count_promo_code_redemptions_query", func(t *testing.T) {
		if !strings.Contains(content, "CountPromoCodeRedemptions") {
			t.Error("promo_codes.sql: missing 'CountPromoCodeRedemptions' query")
		}
	})

	t.Run("insert_promo_code_redemption_query", func(t *testing.T) {
		if !strings.Contains(content, "InsertPromoCodeRedemption") {
			t.Error("promo_codes.sql: missing 'InsertPromoCodeRedemption' query")
		}
	})
}

func TestPromo128_Step2_GenFileExists(t *testing.T) {
	content := findFileByName(t, "promo_codes.sql.go")

	t.Run("promo_code_row_struct", func(t *testing.T) {
		if !strings.Contains(content, "PromoCodeRow") {
			t.Error("promo_codes.sql.go: missing 'PromoCodeRow' struct")
		}
	})

	t.Run("promo_code_redemption_row_struct", func(t *testing.T) {
		if !strings.Contains(content, "PromoCodeRedemptionRow") {
			t.Error("promo_codes.sql.go: missing 'PromoCodeRedemptionRow' struct")
		}
	})

	t.Run("insert_promo_code_method", func(t *testing.T) {
		if !strings.Contains(content, "InsertPromoCode") {
			t.Error("promo_codes.sql.go: missing 'InsertPromoCode' method")
		}
	})

	t.Run("get_promo_code_by_code_method", func(t *testing.T) {
		if !strings.Contains(content, "GetPromoCodeByCode") {
			t.Error("promo_codes.sql.go: missing 'GetPromoCodeByCode' method")
		}
	})

	t.Run("count_promo_code_redemptions_method", func(t *testing.T) {
		if !strings.Contains(content, "CountPromoCodeRedemptions") {
			t.Error("promo_codes.sql.go: missing 'CountPromoCodeRedemptions' method")
		}
	})

	t.Run("soft_delete_promo_code_method", func(t *testing.T) {
		if !strings.Contains(content, "SoftDeletePromoCode") {
			t.Error("promo_codes.sql.go: missing 'SoftDeletePromoCode' method")
		}
	})
}

func TestPromo128_Step2_QuerierInterface(t *testing.T) {
	content := findFileByName(t, "querier.go")

	t.Run("insert_promo_code_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "InsertPromoCode") {
			t.Error("querier.go: missing 'InsertPromoCode' in Querier interface")
		}
	})

	t.Run("get_promo_code_by_code_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "GetPromoCodeByCode") {
			t.Error("querier.go: missing 'GetPromoCodeByCode' in Querier interface")
		}
	})

	t.Run("count_promo_code_redemptions_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "CountPromoCodeRedemptions") {
			t.Error("querier.go: missing 'CountPromoCodeRedemptions' in Querier interface")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Discount math — pure function tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo128_Step3_DiscountMath(t *testing.T) {
	t.Run("percent_10_of_5000", func(t *testing.T) {
		d := computeDiscount("percent", 10, 5000)
		if d != 500 {
			t.Errorf("got %d, want 500", d)
		}
	})

	t.Run("percent_100_caps_at_order", func(t *testing.T) {
		d := computeDiscount("percent", 100, 5000)
		if d != 5000 {
			t.Errorf("got %d, want 5000", d)
		}
	})

	t.Run("percent_0_order_is_zero", func(t *testing.T) {
		d := computeDiscount("percent", 50, 0)
		if d != 0 {
			t.Errorf("got %d, want 0", d)
		}
	})

	t.Run("fixed_amount_less_than_order", func(t *testing.T) {
		d := computeDiscount("fixed_amount", 1000, 5000)
		if d != 1000 {
			t.Errorf("got %d, want 1000", d)
		}
	})

	t.Run("fixed_amount_more_than_order_caps", func(t *testing.T) {
		d := computeDiscount("fixed_amount", 9000, 5000)
		if d != 5000 {
			t.Errorf("got %d, want 5000 (capped)", d)
		}
	})

	t.Run("unknown_type_returns_zero", func(t *testing.T) {
		d := computeDiscount("bogus", 10, 5000)
		if d != 0 {
			t.Errorf("got %d, want 0", d)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Validation logic tests
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo128_Step3_ValidatePromoCode(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	t.Run("inactive_status_rejected", func(t *testing.T) {
		pc := gen.PromoCodeRow{
			Status:        "inactive",
			DiscountType:  "percent",
			DiscountValue: 10,
		}
		_, errCode := validatePromoCode(pc, 5000, now)
		if errCode != "promo.not_active" {
			t.Errorf("got errCode=%q, want 'promo.not_active'", errCode)
		}
	})

	t.Run("expired_rejected", func(t *testing.T) {
		past := now.Add(-24 * time.Hour)
		pc := gen.PromoCodeRow{
			Status:        "active",
			DiscountType:  "percent",
			DiscountValue: 10,
			ValidUntil:    &past,
		}
		_, errCode := validatePromoCode(pc, 5000, now)
		if errCode != "promo.expired" {
			t.Errorf("got errCode=%q, want 'promo.expired'", errCode)
		}
	})

	t.Run("not_yet_valid_rejected", func(t *testing.T) {
		future := now.Add(24 * time.Hour)
		pc := gen.PromoCodeRow{
			Status:        "active",
			DiscountType:  "percent",
			DiscountValue: 10,
			ValidFrom:     &future,
		}
		_, errCode := validatePromoCode(pc, 5000, now)
		if errCode != "promo.not_yet_valid" {
			t.Errorf("got errCode=%q, want 'promo.not_yet_valid'", errCode)
		}
	})

	t.Run("below_min_order_rejected", func(t *testing.T) {
		pc := gen.PromoCodeRow{
			Status:         "active",
			DiscountType:   "percent",
			DiscountValue:  10,
			MinOrderAmount: 10000,
		}
		_, errCode := validatePromoCode(pc, 5000, now)
		if errCode != "promo.invalid_order_amount" {
			t.Errorf("got errCode=%q, want 'promo.invalid_order_amount'", errCode)
		}
	})

	t.Run("valid_returns_discount", func(t *testing.T) {
		pc := gen.PromoCodeRow{
			Status:         "active",
			DiscountType:   "percent",
			DiscountValue:  10,
			MinOrderAmount: 0,
		}
		discount, errCode := validatePromoCode(pc, 5000, now)
		if errCode != "" {
			t.Errorf("unexpected error code: %q", errCode)
		}
		if discount != 500 {
			t.Errorf("got discount=%d, want 500", discount)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: HTTP routes — auth-gating
// ─────────────────────────────────────────────────────────────────────────────

const promoBasePath = "/v1/organizations/00000000-0000-0000-0000-000000000001/promo-codes"
const promoValidatePath = "/v1/checkout/promo-validate"

func TestPromo128_Step4_GetListRequiresAuth(t *testing.T) {
	s := buildPromoServer(t)
	req := httptest.NewRequest(http.MethodGet, promoBasePath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET promo-codes without auth: got %d, want 401", w.Code)
	}
}

func TestPromo128_Step4_GetSingleRequiresAuth(t *testing.T) {
	s := buildPromoServer(t)
	req := httptest.NewRequest(http.MethodGet, promoBasePath+"/00000000-0000-0000-0000-000000000099", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET promo-codes/{id} without auth: got %d, want 401", w.Code)
	}
}

func TestPromo128_Step4_PostCreateRequiresAuth(t *testing.T) {
	s := buildPromoServer(t)
	req := httptest.NewRequest(http.MethodPost, promoBasePath,
		strings.NewReader(`{"code":"SAVE10","discount_type":"percent","discount_value":10}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST promo-codes without auth: got %d, want 401", w.Code)
	}
}

func TestPromo128_Step4_PatchUpdateRequiresAuth(t *testing.T) {
	s := buildPromoServer(t)
	req := httptest.NewRequest(http.MethodPatch, promoBasePath+"/00000000-0000-0000-0000-000000000099",
		strings.NewReader(`{"status":"inactive"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PATCH promo-codes/{id} without auth: got %d, want 401", w.Code)
	}
}

func TestPromo128_Step4_DeleteRequiresAuth(t *testing.T) {
	s := buildPromoServer(t)
	req := httptest.NewRequest(http.MethodDelete, promoBasePath+"/00000000-0000-0000-0000-000000000099", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE promo-codes/{id} without auth: got %d, want 401", w.Code)
	}
}

func TestPromo128_Step4_ValidateRequiresAuth(t *testing.T) {
	s := buildPromoServer(t)
	req := httptest.NewRequest(http.MethodPost, promoValidatePath,
		strings.NewReader(`{"org_id":"00000000-0000-0000-0000-000000000001","code":"SAVE10","order_amount":5000}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST checkout/promo-validate without auth: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Routes are mounted (not 404)
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo128_Step4_RoutesAreMounted(t *testing.T) {
	s := buildPromoServer(t)

	t.Run("GET_list_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, promoBasePath, nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("GET promo-codes: route not mounted (404)")
		}
	})

	t.Run("GET_single_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, promoBasePath+"/00000000-0000-0000-0000-000000000099", nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("GET promo-codes/{id}: route not mounted (404)")
		}
	})

	t.Run("POST_create_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, promoBasePath,
			strings.NewReader(`{"code":"SAVE10"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("POST promo-codes: route not mounted (404)")
		}
	})

	t.Run("PATCH_update_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, promoBasePath+"/00000000-0000-0000-0000-000000000099",
			strings.NewReader(`{"status":"inactive"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("PATCH promo-codes/{id}: route not mounted (404)")
		}
	})

	t.Run("DELETE_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, promoBasePath+"/00000000-0000-0000-0000-000000000099", nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("DELETE promo-codes/{id}: route not mounted (404)")
		}
	})

	t.Run("POST_validate_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, promoValidatePath,
			strings.NewReader(`{"org_id":"00000000-0000-0000-0000-000000000001","code":"X"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("POST checkout/promo-validate: route not mounted (404)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Server wiring — fields exist on Server and Options
// ─────────────────────────────────────────────────────────────────────────────

func TestPromo128_Step4_ServerWiring(t *testing.T) {
	content := findFileByName(t, "server.go")

	t.Run("server_struct_has_promoQueries", func(t *testing.T) {
		if !strings.Contains(content, "promoQueries") {
			t.Error("server.go: Server struct missing 'promoQueries' field")
		}
	})

	t.Run("options_struct_has_PromoQueries", func(t *testing.T) {
		if !strings.Contains(content, "PromoQueries") {
			t.Error("server.go: Options struct missing 'PromoQueries' field")
		}
	})
}
