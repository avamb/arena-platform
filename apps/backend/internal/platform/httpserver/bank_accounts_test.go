// bank_accounts_test.go — unit tests for feature #255 (organization
// bank-accounts CRUD).
//
// Coverage:
//   - Migration files 0048_organization_bank_accounts.sql (table, partial
//     unique index, goose Down) and 0056_organization_bank_accounts_country.sql
//     (country column required by the API contract) exist with the expected
//     DDL.
//   - Route auth-gating (every endpoint returns 401 without a JWT).
//   - Request validation: empty body / invalid JSON / unknown field /
//     missing holder_name / invalid currency / invalid country / missing
//     identifier (bank_account.identifier_required) / wrong field types.
//   - Validation runs BEFORE any transaction: a valid body against a downed
//     pool yields 503 dependency.database_unavailable, not a panic.
//   - Response serialization: identifiers (iban / account_number) are
//     returned VERBATIM (all endpoints require org.update, whose holders are
//     entitled to raw values per the spec), timestamps are RFC3339, the JSON
//     key set matches BankAccountItem exactly, and nullable fields are null.
//   - Route mount is gated on the queries handle (chi returns 404 when
//     BankAccountQueries is absent).
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const bankAccountTestActorID = "00000000-0000-0000-0000-000000000255"

// validBankAccountBody is a CreateBankAccountRequest that passes every
// app-side validation rule, so requests carrying it reach the transaction
// boundary (where the dbDownPool then fails with 503).
const validBankAccountBody = `{
	"holder_name": "ACME Tickets GmbH",
	"currency": "EUR",
	"country": "DE",
	"iban": "DE89370400440532013000",
	"bic": "COBADEFFXXX",
	"is_primary": true
}`

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory
// ─────────────────────────────────────────────────────────────────────────────

func buildBankAccountServer(t *testing.T) *Server {
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
		t.Fatalf("buildBankAccountServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:             cfg,
		Auth:               stub,
		Pool:               &dbDownPool{},
		BankAccountQueries: gen.New(nil),
		OrgQueries:         gen.New(nil),
		Audit:              &captureAuditWriter{},
	})
}

func bankAccountToken(t *testing.T, s *Server) string {
	t.Helper()
	if s.stub == nil {
		t.Fatal("stub auth not wired")
	}
	return mintJWT(t, s.stub, bankAccountTestActorID)
}

func bankAccountRespJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("bank_account: JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	return m
}

func bankAccountErrorCode(m map[string]any) string {
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

// doBankAccountRequest fires an authenticated JSON request at the server.
func doBankAccountRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+bankAccountToken(t, s))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration sanity
// ─────────────────────────────────────────────────────────────────────────────

func TestBankAccount255_MigrationHasTable(t *testing.T) {
	content := findFileByName(t, "0048_organization_bank_accounts.sql")
	required := []string{
		"CREATE TABLE organization_bank_accounts",
		"org_id",
		"holder_name",
		"iban",
		"bic_swift",
		"account_number",
		"routing_number",
		"currency",
		"is_default",
		"created_at",
		"updated_at",
		"deleted_at",
		"REFERENCES organizations(id)",
		"uuidv7()",
		"organization_bank_accounts_one_default_per_org",
		"WHERE is_default AND deleted_at IS NULL",
		"-- +goose Down",
		"DROP TABLE IF EXISTS organization_bank_accounts",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("migration 0048 missing %q", want)
		}
	}
}

func TestBankAccount255_CountryMigrationExists(t *testing.T) {
	content := findFileByName(t, "0056_organization_bank_accounts_country.sql")
	required := []string{
		"ALTER TABLE organization_bank_accounts",
		"ADD COLUMN country char(2) NOT NULL",
		"ALTER COLUMN country DROP DEFAULT",
		"-- +goose Down",
		"DROP COLUMN IF EXISTS country",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("migration 0056 missing %q", want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Auth gating
// ─────────────────────────────────────────────────────────────────────────────

func TestBankAccount255_AllRoutesRequireAuth(t *testing.T) {
	s := buildBankAccountServer(t)
	orgID := uuid.New().String()
	accID := uuid.New().String()

	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/organizations/" + orgID + "/bank-accounts", ""},
		{http.MethodPost, "/v1/organizations/" + orgID + "/bank-accounts", validBankAccountBody},
		{http.MethodPatch, "/v1/organizations/" + orgID + "/bank-accounts/" + accID, `{}`},
		{http.MethodDelete, "/v1/organizations/" + orgID + "/bank-accounts/" + accID, ""},
	}
	for _, c := range cases {
		var r *http.Request
		if c.body == "" {
			r = httptest.NewRequest(c.method, c.path, nil)
		} else {
			r = httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
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

func TestBankAccount255_Create_EmptyBody(t *testing.T) {
	s := buildBankAccountServer(t)
	w := doBankAccountRequest(t, s, http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/bank-accounts", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body: want 400, got %d", w.Code)
	}
	if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.empty_body" {
		t.Errorf("want code bank_account.empty_body, got %q", code)
	}
}

func TestBankAccount255_Create_InvalidJSON(t *testing.T) {
	s := buildBankAccountServer(t)
	w := doBankAccountRequest(t, s, http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/bank-accounts", "not-json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid json: want 400, got %d", w.Code)
	}
	if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.invalid_json" {
		t.Errorf("want code bank_account.invalid_json, got %q", code)
	}
}

func TestBankAccount255_Create_UnknownFieldRejected(t *testing.T) {
	// Both request schemas declare additionalProperties: false.
	s := buildBankAccountServer(t)
	w := doBankAccountRequest(t, s, http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/bank-accounts",
		`{"holder_name":"A","currency":"EUR","country":"DE","iban":"DE89","swift":"nope"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: want 400, got %d", w.Code)
	}
	if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.unknown_field" {
		t.Errorf("want code bank_account.unknown_field, got %q", code)
	}
}

func TestBankAccount255_Create_MissingHolderName(t *testing.T) {
	s := buildBankAccountServer(t)
	w := doBankAccountRequest(t, s, http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/bank-accounts",
		`{"currency":"EUR","country":"DE","iban":"DE89370400440532013000"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing holder_name: want 400, got %d", w.Code)
	}
	if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.invalid_holder_name" {
		t.Errorf("want code bank_account.invalid_holder_name, got %q", code)
	}
}

func TestBankAccount255_Create_InvalidCurrency(t *testing.T) {
	s := buildBankAccountServer(t)
	for _, currency := range []string{"eur", "EURO", "E1R", ""} {
		w := doBankAccountRequest(t, s, http.MethodPost,
			"/v1/organizations/"+uuid.New().String()+"/bank-accounts",
			`{"holder_name":"A","currency":"`+currency+`","country":"DE","iban":"DE89"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("currency %q: want 400, got %d", currency, w.Code)
		}
		if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.invalid_currency" {
			t.Errorf("currency %q: want code bank_account.invalid_currency, got %q", currency, code)
		}
	}
}

func TestBankAccount255_Create_InvalidCountry(t *testing.T) {
	s := buildBankAccountServer(t)
	for _, country := range []string{"DEU", "de", "D1", ""} {
		w := doBankAccountRequest(t, s, http.MethodPost,
			"/v1/organizations/"+uuid.New().String()+"/bank-accounts",
			`{"holder_name":"A","currency":"EUR","country":"`+country+`","iban":"DE89"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("country %q: want 400, got %d", country, w.Code)
		}
		if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.invalid_country" {
			t.Errorf("country %q: want code bank_account.invalid_country, got %q", country, code)
		}
	}
}

func TestBankAccount255_Create_IdentifierRequired(t *testing.T) {
	s := buildBankAccountServer(t)
	// Neither iban nor account_number+routing_number → 400; a lone
	// account_number (without routing_number) is also insufficient.
	for _, body := range []string{
		`{"holder_name":"A","currency":"EUR","country":"DE"}`,
		`{"holder_name":"A","currency":"USD","country":"US","account_number":"000123456789"}`,
		`{"holder_name":"A","currency":"USD","country":"US","routing_number":"021000021"}`,
	} {
		w := doBankAccountRequest(t, s, http.MethodPost,
			"/v1/organizations/"+uuid.New().String()+"/bank-accounts", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: want 400, got %d", body, w.Code)
		}
		if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.identifier_required" {
			t.Errorf("body %s: want code bank_account.identifier_required, got %q", body, code)
		}
	}
}

func TestBankAccount255_Create_WrongFieldType(t *testing.T) {
	s := buildBankAccountServer(t)
	w := doBankAccountRequest(t, s, http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/bank-accounts",
		`{"holder_name":"A","currency":"EUR","country":"DE","iban":"DE89","is_primary":"yes"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("is_primary wrong type: want 400, got %d", w.Code)
	}
	if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "bank_account.invalid_field" {
		t.Errorf("want code bank_account.invalid_field, got %q", code)
	}
}

func TestBankAccount255_Create_InvalidOrgIDPathParam(t *testing.T) {
	s := buildBankAccountServer(t)
	w := doBankAccountRequest(t, s, http.MethodPost,
		"/v1/organizations/not-a-uuid/bank-accounts", validBankAccountBody)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid org_id: want 400, got %d", w.Code)
	}
	if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "http.invalid_path_param" {
		t.Errorf("want code http.invalid_path_param, got %q", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — Validation precedes the transaction (503 on downed pool, no panic)
// ─────────────────────────────────────────────────────────────────────────────

func TestBankAccount255_WritePathsReturn503WhenDBDown(t *testing.T) {
	s := buildBankAccountServer(t)
	orgID := uuid.New().String()
	accID := uuid.New().String()

	cases := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/organizations/" + orgID + "/bank-accounts", validBankAccountBody},
		{http.MethodPatch, "/v1/organizations/" + orgID + "/bank-accounts/" + accID, `{"holder_name":"New Name"}`},
		{http.MethodDelete, "/v1/organizations/" + orgID + "/bank-accounts/" + accID, ""},
	}
	for _, c := range cases {
		w := doBankAccountRequest(t, s, c.method, c.path, c.body)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with downed pool: want 503, got %d (body: %s)", c.method, c.path, w.Code, w.Body.String())
			continue
		}
		if code := bankAccountErrorCode(bankAccountRespJSON(t, w)); code != "dependency.database_unavailable" {
			t.Errorf("%s %s: want code dependency.database_unavailable, got %q", c.method, c.path, code)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5 — Response serialization matches BankAccountItem exactly
// ─────────────────────────────────────────────────────────────────────────────

func sampleBankAccountRow() gen.OrganizationBankAccountRow {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	bankName := "Commerzbank AG"
	iban := "DE89370400440532013000"
	bic := "COBADEFFXXX"
	return gen.OrganizationBankAccountRow{
		ID:         uuid.New(),
		OrgID:      uuid.New(),
		BankName:   &bankName,
		HolderName: "ACME Tickets GmbH",
		Iban:       &iban,
		Bic:        &bic,
		Currency:   "EUR",
		Country:    "DE",
		IsPrimary:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestBankAccount255_ResponseReturnsIdentifiersVerbatim(t *testing.T) {
	// Per the spec, IBAN / account_number are returned verbatim: every
	// bank-accounts endpoint requires org.update, whose holders are
	// entitled to the raw values. No masking.
	row := sampleBankAccountRow()
	raw, err := json.Marshal(bankAccountFromRow(row))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "DE89370400440532013000") {
		t.Errorf("response must contain the verbatim IBAN: %s", body)
	}
	if !strings.Contains(body, `"created_at":"2026-07-01T12:00:00Z"`) {
		t.Errorf("created_at must be RFC3339 UTC: %s", body)
	}
}

func TestBankAccount255_ResponseKeySetMatchesSpec(t *testing.T) {
	raw, err := json.Marshal(bankAccountFromRow(sampleBankAccountRow()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{
		"account_number", "bank_name", "bic", "country", "created_at",
		"currency", "holder_name", "iban", "id", "is_primary", "org_id",
		"routing_number", "updated_at",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("response keys drift from BankAccountItem:\n got:  %v\n want: %v", got, want)
	}
	// deleted_at must never leak (BankAccountItem has no such property).
	if _, present := m["deleted_at"]; present {
		t.Error("response must not include deleted_at")
	}
}

func TestBankAccount255_ResponseNullableFieldsAreNull(t *testing.T) {
	row := sampleBankAccountRow()
	row.BankName = nil
	row.Iban = nil
	row.Bic = nil
	raw, err := json.Marshal(bankAccountFromRow(row))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"bank_name", "iban", "bic", "account_number", "routing_number"} {
		v, present := m[key]
		if !present {
			t.Errorf("nullable field %q must be present (as null), not omitted", key)
			continue
		}
		if v != nil {
			t.Errorf("field %q: want null, got %v", key, v)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6 — Route mount is gated on the queries handle
// ─────────────────────────────────────────────────────────────────────────────

func TestBankAccount255_CreateWithoutQueries_404(t *testing.T) {
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
		// BankAccountQueries intentionally nil.
	})
	tok := mintJWT(t, s.stub, bankAccountTestActorID)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+uuid.New().String()+"/bank-accounts",
		strings.NewReader(validBankAccountBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("create without queries: want 404, got %d", w.Code)
	}
}
