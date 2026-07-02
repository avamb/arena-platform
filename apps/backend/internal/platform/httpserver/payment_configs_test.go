// payment_configs_test.go — unit tests for feature #237.
//
// Coverage:
//   - Migration file 0046_payment_provider_configs.sql exists with the
//     expected schema, permission seeds, and goose Down block.
//   - Route auth-gating (every endpoint returns 401 without a JWT).
//   - Request validation: empty body / invalid JSON / unknown provider /
//     invalid mode / invalid public_config.
//   - Status-derivation helper correctly flags configured vs. missing
//     required fields for each supported provider.
//   - Secret merge helper: replaces, deletes (empty-string), keeps
//     untouched keys.
//   - Response serialization never leaks secret values; secret_fields_set
//     and missing_required_fields shape are stable.
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const paymentConfigTestActorID = "00000000-0000-0000-0000-000000000237"

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory
// ─────────────────────────────────────────────────────────────────────────────

func buildPaymentConfigServer(t *testing.T) *Server {
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
		t.Fatalf("buildPaymentConfigServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:               cfg,
		Auth:                 stub,
		Pool:                 &dbDownPool{},
		PaymentConfigQueries: gen.New(nil),
		OrgQueries:           gen.New(nil),
		Audit:                &captureAuditWriter{},
	})
}

func paymentConfigToken(t *testing.T, s *Server) string {
	t.Helper()
	if s.stub == nil {
		t.Fatal("stub auth not wired")
	}
	return mintJWT(t, s.stub, paymentConfigTestActorID)
}

func paymentConfigRespJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("payment_config: JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	return m
}

func paymentConfigErrorCode(m map[string]any) string {
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration sanity
// ─────────────────────────────────────────────────────────────────────────────

func TestPaymentConfig237_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0046_payment_provider_configs.sql")
	if content == "" {
		t.Fatal("migration file 0046_payment_provider_configs.sql is empty or missing")
	}
}

func TestPaymentConfig237_MigrationHasTable(t *testing.T) {
	content := findFileByName(t, "0046_payment_provider_configs.sql")
	required := []string{
		"CREATE TABLE payment_provider_configs",
		"id",
		"org_id",
		"provider",
		"mode",
		"provider_account_id",
		"public_config",
		"secrets",
		"status",
		"is_active",
		"created_at",
		"updated_at",
		"deleted_at",
		"REFERENCES organizations(id)",
		"uuidv7()",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("migration missing %q", want)
		}
	}
}

func TestPaymentConfig237_MigrationHasUniqueIndex(t *testing.T) {
	content := findFileByName(t, "0046_payment_provider_configs.sql")
	if !strings.Contains(content, "payment_provider_configs_unique_active") {
		t.Error("migration must have partial unique index payment_provider_configs_unique_active")
	}
	if !strings.Contains(content, "WHERE deleted_at IS NULL") {
		t.Error("migration must have partial-index filter WHERE deleted_at IS NULL")
	}
}

func TestPaymentConfig237_MigrationHasPermissionSeeds(t *testing.T) {
	content := findFileByName(t, "0046_payment_provider_configs.sql")
	for _, p := range []string{"payment_config.read", "payment_config.write"} {
		if !strings.Contains(content, p) {
			t.Errorf("migration missing permission seed %q", p)
		}
	}
	for _, role := range []string{"'admin'", "'org_admin'", "'platform_superadmin'"} {
		if !strings.Contains(content, role) {
			t.Errorf("migration missing RBAC bind for role %s", role)
		}
	}
}

func TestPaymentConfig237_MigrationHasGooseDown(t *testing.T) {
	content := findFileByName(t, "0046_payment_provider_configs.sql")
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration must have a -- +goose Down section")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS payment_provider_configs") {
		t.Error("goose Down must DROP TABLE IF EXISTS payment_provider_configs")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Auth gating
// ─────────────────────────────────────────────────────────────────────────────

func TestPaymentConfig237_AllRoutesRequireAuth(t *testing.T) {
	s := buildPaymentConfigServer(t)
	orgID := uuid.New().String()
	cfgID := uuid.New().String()

	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/organizations/" + orgID + "/payment-configs", ""},
		{http.MethodGet, "/v1/organizations/" + orgID + "/payment-configs/" + cfgID, ""},
		{http.MethodPost, "/v1/organizations/" + orgID + "/payment-configs", `{"provider":"stripe"}`},
		{http.MethodPatch, "/v1/organizations/" + orgID + "/payment-configs/" + cfgID, `{}`},
		{http.MethodDelete, "/v1/organizations/" + orgID + "/payment-configs/" + cfgID, ""},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest(c.method, c.path, nil)
		} else {
			r = httptest.NewRequest(c.method, c.path, body)
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without auth: want 401, got %d", c.method, c.path, w.Code)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Request-validation surface (with auth)
// ─────────────────────────────────────────────────────────────────────────────

func TestPaymentConfig237_Create_EmptyBody(t *testing.T) {
	s := buildPaymentConfigServer(t)
	tok := paymentConfigToken(t, s)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/payment-configs",
		strings.NewReader(""))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body: want 400, got %d", w.Code)
	}
	if code := paymentConfigErrorCode(paymentConfigRespJSON(t, w)); code != "payment_config.empty_body" {
		t.Errorf("want code payment_config.empty_body, got %q", code)
	}
}

func TestPaymentConfig237_Create_InvalidJSON(t *testing.T) {
	s := buildPaymentConfigServer(t)
	tok := paymentConfigToken(t, s)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/payment-configs",
		strings.NewReader("not-json"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid json: want 400, got %d", w.Code)
	}
	if code := paymentConfigErrorCode(paymentConfigRespJSON(t, w)); code != "payment_config.invalid_json" {
		t.Errorf("want code payment_config.invalid_json, got %q", code)
	}
}

func TestPaymentConfig237_Create_MissingProvider(t *testing.T) {
	s := buildPaymentConfigServer(t)
	tok := paymentConfigToken(t, s)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/payment-configs",
		strings.NewReader(`{"mode":"live"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing provider: want 400, got %d", w.Code)
	}
	if code := paymentConfigErrorCode(paymentConfigRespJSON(t, w)); code != "payment_config.invalid_provider" {
		t.Errorf("want code payment_config.invalid_provider, got %q", code)
	}
}

func TestPaymentConfig237_Create_UnsupportedProvider(t *testing.T) {
	s := buildPaymentConfigServer(t)
	tok := paymentConfigToken(t, s)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/payment-configs",
		strings.NewReader(`{"provider":"made-up","mode":"test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unsupported provider: want 400, got %d", w.Code)
	}
	if code := paymentConfigErrorCode(paymentConfigRespJSON(t, w)); code != "payment_config.unsupported_provider" {
		t.Errorf("want code payment_config.unsupported_provider, got %q", code)
	}
}

func TestPaymentConfig237_Create_InvalidMode(t *testing.T) {
	s := buildPaymentConfigServer(t)
	tok := paymentConfigToken(t, s)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/payment-configs",
		strings.NewReader(`{"provider":"stripe","mode":"sandbox"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid mode: want 400, got %d", w.Code)
	}
	if code := paymentConfigErrorCode(paymentConfigRespJSON(t, w)); code != "payment_config.invalid_mode" {
		t.Errorf("want code payment_config.invalid_mode, got %q", code)
	}
}

func TestPaymentConfig237_Create_InvalidPublicConfig(t *testing.T) {
	s := buildPaymentConfigServer(t)
	tok := paymentConfigToken(t, s)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/payment-configs",
		strings.NewReader(`{"provider":"stripe","public_config":"not-an-object"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid public_config: want 400, got %d", w.Code)
	}
	if code := paymentConfigErrorCode(paymentConfigRespJSON(t, w)); code != "payment_config.invalid_public_config" {
		t.Errorf("want code payment_config.invalid_public_config, got %q", code)
	}
}

// 503 path: build a server WITHOUT PaymentConfigQueries to verify the
// route mount is gated on the queries handle being non-nil (no panic,
// no 500 — chi simply returns 404 because no handler is mounted).
func TestPaymentConfig237_CreateWithoutQueries_404(t *testing.T) {
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
		// PaymentConfigQueries intentionally nil.
	})
	tok := mintJWT(t, s.stub, paymentConfigTestActorID)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/payment-configs",
		strings.NewReader(`{"provider":"stripe"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("create without queries: want 404, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — Pure-function helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestPaymentConfig237_DeriveStatus_StripeMissingAll(t *testing.T) {
	got := deriveStatus("stripe", []byte(`{}`))
	if got != "missing_required_fields" {
		t.Errorf("stripe with no secrets: want missing_required_fields, got %q", got)
	}
}

func TestPaymentConfig237_DeriveStatus_StripeMissingOne(t *testing.T) {
	got := deriveStatus("stripe", []byte(`{"api_key":"sk_x"}`))
	if got != "missing_required_fields" {
		t.Errorf("stripe with only api_key: want missing_required_fields, got %q", got)
	}
}

func TestPaymentConfig237_DeriveStatus_StripeAllPresent(t *testing.T) {
	got := deriveStatus("stripe", []byte(`{"api_key":"sk_x","webhook_secret":"whsec_x"}`))
	if got != "configured" {
		t.Errorf("stripe fully configured: want configured, got %q", got)
	}
}

func TestPaymentConfig237_DeriveStatus_ManualHasNoRequirements(t *testing.T) {
	got := deriveStatus("manual", []byte(`{}`))
	if got != "configured" {
		t.Errorf("manual provider with empty secrets: want configured, got %q", got)
	}
}

func TestPaymentConfig237_DeriveStatus_EmptyStringTreatedAsMissing(t *testing.T) {
	got := deriveStatus("stripe", []byte(`{"api_key":"   ","webhook_secret":""}`))
	if got != "missing_required_fields" {
		t.Errorf("stripe with empty/whitespace values: want missing_required_fields, got %q", got)
	}
}

func TestPaymentConfig237_ComputeMissingRequiredFields_Stripe(t *testing.T) {
	missing := computeMissingRequiredFields("stripe", []byte(`{"api_key":"x"}`))
	want := []string{"webhook_secret"}
	if !reflect.DeepEqual(missing, want) {
		t.Errorf("stripe with only api_key: want %v missing, got %v", want, missing)
	}
}

func TestPaymentConfig237_ExtractStoredSecretKeys_IgnoresEmpty(t *testing.T) {
	got := extractStoredSecretKeys([]byte(`{"a":"value","b":"","c":"   ","d":"ok"}`))
	sort.Strings(got)
	want := []string{"a", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractStoredSecretKeys: want %v, got %v", want, got)
	}
}

func TestPaymentConfig237_MergeSecrets_ReplaceAndDelete(t *testing.T) {
	existing := json.RawMessage(`{"api_key":"sk_old","webhook_secret":"wh_old","keep":"keep_me"}`)
	patch := map[string]string{
		"api_key":        "sk_new",
		"webhook_secret": "", // delete
		// "keep" omitted, must remain
	}
	merged, changed, err := mergeSecrets(existing, patch)
	if err != nil {
		t.Fatalf("mergeSecrets: %v", err)
	}
	if !changed {
		t.Error("changed flag must be true when patch replaces or deletes")
	}
	var got map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("merged json invalid: %v", err)
	}
	if got["api_key"] != "sk_new" {
		t.Errorf("api_key: want sk_new, got %v", got["api_key"])
	}
	if _, present := got["webhook_secret"]; present {
		t.Error("webhook_secret should have been deleted by empty-string patch")
	}
	if got["keep"] != "keep_me" {
		t.Errorf("keep: want keep_me, got %v", got["keep"])
	}
}

func TestPaymentConfig237_MergeSecrets_NoOpPatchReportsUnchanged(t *testing.T) {
	existing := json.RawMessage(`{"a":"1"}`)
	_, changed, err := mergeSecrets(existing, map[string]string{"a": "1"})
	if err != nil {
		t.Fatalf("mergeSecrets: %v", err)
	}
	if changed {
		t.Error("setting an unchanged value must not flip changed=true")
	}
}

func TestPaymentConfig237_MergeSecrets_EmptyExistingOK(t *testing.T) {
	merged, changed, err := mergeSecrets(nil, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("mergeSecrets: %v", err)
	}
	if !changed {
		t.Error("inserting into empty must flip changed=true")
	}
	var got map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("merged: %v", err)
	}
	if got["k"] != "v" {
		t.Errorf("want k=v, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5 — Response serialization NEVER exposes secrets
// ─────────────────────────────────────────────────────────────────────────────

func TestPaymentConfig237_ResponseDropsSecrets(t *testing.T) {
	now := time.Now().UTC()
	row := gen.PaymentProviderConfigRow{
		ID:           uuid.New(),
		OrgID:        uuid.New(),
		Provider:     "stripe",
		Mode:         "live",
		PublicConfig: []byte(`{"statement_descriptor":"ARENA"}`),
		Secrets:      []byte(`{"api_key":"sk_live_SUPER_SECRET","webhook_secret":"whsec_SUPER"}`),
		Status:       "configured",
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	resp := paymentConfigFromRow(row)

	// Serialize the response struct and confirm no secret VALUES leak.
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "sk_live_SUPER_SECRET") {
		t.Errorf("response leaked api_key value: %s", body)
	}
	if strings.Contains(body, "whsec_SUPER") {
		t.Errorf("response leaked webhook_secret value: %s", body)
	}
	if strings.Contains(body, `"secrets"`) {
		t.Errorf("response must not include the raw `secrets` key: %s", body)
	}

	// secret_fields_set should list both keys (sorted, no values).
	wantSet := []string{"api_key", "webhook_secret"}
	if !reflect.DeepEqual(resp.SecretFieldsSet, wantSet) {
		t.Errorf("secret_fields_set: want %v, got %v", wantSet, resp.SecretFieldsSet)
	}
	if len(resp.MissingRequiredFields) != 0 {
		t.Errorf("missing_required_fields: want [], got %v", resp.MissingRequiredFields)
	}
}

func TestPaymentConfig237_ResponseEmptyArraysAreNeverNil(t *testing.T) {
	now := time.Now().UTC()
	row := gen.PaymentProviderConfigRow{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		Provider:  "manual",
		Mode:      "test",
		Status:    "configured",
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	resp := paymentConfigFromRow(row)
	if resp.SecretFieldsSet == nil {
		t.Error("secret_fields_set must never be nil (use empty slice)")
	}
	if resp.MissingRequiredFields == nil {
		t.Error("missing_required_fields must never be nil (use empty slice)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6 — Supported provider catalogue is what callers expect
// ─────────────────────────────────────────────────────────────────────────────

func TestPaymentConfig237_SupportedProvidersIncludeCoreSet(t *testing.T) {
	for _, want := range []string{"stripe", "allpay", "cloudpayments", "yookassa", "manual"} {
		if !supportedPaymentProviders[want] {
			t.Errorf("supportedPaymentProviders missing %q", want)
		}
	}
}
