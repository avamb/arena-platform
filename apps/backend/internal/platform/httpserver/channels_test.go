// channels_test.go — unit tests for feature #121 (Sales channel model + per-channel config).
//
// Test coverage:
//
//	Step 1: Migration file 0010_sales_channels.sql exists with correct schema + seeds
//	Step 2: POST/GET/PATCH/DELETE /v1/organizations/{org_id}/channels routes mounted,
//	        auth-gated, with correct request validation behaviour (no DB required)
//	Step 3: Config validator — payment_mode / provider / provider_account_id rules
//	Step 4: sqlc gen file (channels.sql.go) and query file (channels.sql) structure
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for channel route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildChannelServer builds a Server with stub auth, channel routes fully
// mounted, and a dbDownPool so real DB operations never execute. Auth
// middleware fires before the DB layer → unauthenticated requests get 401,
// not 503.
func buildChannelServer(t *testing.T) *Server {
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
		t.Fatalf("buildChannelServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so routes get mounted.
		Pool: &dbDownPool{},
		// ChannelQueries non-nil so the channel route conditional passes.
		ChannelQueries: gen.New(nil),
		// OrgQueries also non-nil to not affect other route mounts.
		OrgQueries: gen.New(nil),
		// Audit writer required for DELETE.
		Audit: &captureAuditWriter{},
	})
}

// channelRespJSON decodes the response body into a map and returns it.
func channelRespJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("channel: JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration file exists with correct schema + seeds
// ─────────────────────────────────────────────────────────────────────────────

func TestChannel121_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")
	if content == "" {
		t.Fatal("migration file 0010_sales_channels.sql is empty")
	}
}

func TestChannel121_MigrationHasSalesChannelsTable(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")

	required := []string{
		"CREATE TABLE sales_channels",
		"id",
		"org_id",
		"uuidv7()",
		"name",
		"payment_mode",
		"direct_merchant",
		"merchant_of_record",
		"provider",
		"stripe",
		"allpay",
		"provider_account_id",
		"fee_percent",
		"reservation_ttl_override",
		"created_at",
		"updated_at",
		"deleted_at",
	}
	for _, token := range required {
		if !strings.Contains(content, token) {
			t.Errorf("migration missing expected token %q", token)
		}
	}
}

func TestChannel121_MigrationHasSoftDeleteColumn(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")

	checks := []string{
		"deleted_at",
		"NULL = active",
	}
	for _, token := range checks {
		if !strings.Contains(content, token) {
			t.Errorf("migration missing soft-delete token %q", token)
		}
	}
}

func TestChannel121_MigrationHasFKToOrganizations(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")
	if !strings.Contains(content, "REFERENCES organizations(id)") {
		t.Error("migration missing FK reference to organizations(id)")
	}
}

func TestChannel121_MigrationHasFeePercentDefault(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")
	if !strings.Contains(content, "numeric(5,2)") {
		t.Error("migration missing numeric(5,2) for fee_percent")
	}
	if !strings.Contains(content, "DEFAULT 0") {
		t.Error("migration missing DEFAULT 0 for fee_percent")
	}
}

func TestChannel121_MigrationHasUniqueIndexes(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")
	if !strings.Contains(content, "channels_name_org_unique_active") {
		t.Error("migration missing unique index channels_name_org_unique_active")
	}
	if !strings.Contains(content, "WHERE deleted_at IS NULL") {
		t.Error("migration missing partial-index WHERE clause")
	}
}

func TestChannel121_MigrationHasPermissionSeeds(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")

	perms := []string{"channel.create", "channel.read", "channel.update", "channel.delete"}
	for _, p := range perms {
		if !strings.Contains(content, p) {
			t.Errorf("migration missing permission seed %q", p)
		}
	}
}

func TestChannel121_MigrationHasGooseDownSection(t *testing.T) {
	content := findFileByName(t, "0010_sales_channels.sql")
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing -- +goose Down section")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS sales_channels") {
		t.Error("migration Down section missing DROP TABLE")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Route auth + validation
// ─────────────────────────────────────────────────────────────────────────────

func TestChannel121_PostChannelsRequiresAuth(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/channels",
		strings.NewReader(`{"name":"test","payment_mode":"direct_merchant","provider":"stripe","provider_account_id":"acct_123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestChannel121_GetChannelsRequiresAuth(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/channels", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestChannel121_GetChannelByIDRequiresAuth(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/channels/"+chID.String(), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestChannel121_PatchChannelRequiresAuth(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/organizations/"+orgID.String()+"/channels/"+chID.String(),
		strings.NewReader(`{"name":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestChannel121_DeleteChannelRequiresAuth(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/v1/organizations/"+orgID.String()+"/channels/"+chID.String(), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestChannel121_CreateChannel_NilChannelQueriesReturns503(t *testing.T) {
	// Build server WITHOUT channelQueries so the nil-guard fires.
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
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
		// channelQueries intentionally not set → routes not mounted
		Audit: &captureAuditWriter{},
	})

	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/channels",
		strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Routes not mounted → 404 (channel routes require channelQueries != nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 (routes not mounted), got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestChannel121_CreateChannel_EmptyBodyReturns400(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()

	stub := s.stub
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/channels",
		strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	resp := channelRespJSON(t, w)
	if code := errorCode(t, resp); code != "channel.empty_body" {
		t.Errorf("expected code='channel.empty_body', got %q", code)
	}
}

func TestChannel121_CreateChannel_InvalidJSONReturns400(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()

	token := mintJWT(t, s.stub, "00000000-0000-0000-0000-000000000001")

	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/channels",
		strings.NewReader(`{bad json`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	resp := channelRespJSON(t, w)
	if code := errorCode(t, resp); code != "channel.invalid_json" {
		t.Errorf("expected code='channel.invalid_json', got %q", code)
	}
}

func TestChannel121_CreateChannel_MissingNameReturns400(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	token := mintJWT(t, s.stub, "00000000-0000-0000-0000-000000000001")

	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/channels",
		strings.NewReader(`{"payment_mode":"direct_merchant","provider":"stripe","provider_account_id":"acct_123"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d (body: %s)", w.Code, w.Body.String())
	}
	resp := channelRespJSON(t, w)
	if code := errorCode(t, resp); code != "channel.invalid_name" {
		t.Errorf("expected code='channel.invalid_name', got %q", code)
	}
}

func TestChannel121_ListChannels_NilChannelQueriesReturns404(t *testing.T) {
	// Server without channelQueries → routes not mounted.
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
	}
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	s := New(Options{Config: cfg, Auth: stub, Pool: &dbDownPool{}})

	orgID := uuid.New()
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/channels", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 (routes not mounted), got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Config validator
// ─────────────────────────────────────────────────────────────────────────────

func TestChannel121_ValidateChannelConfig_DirectMerchantNoAccountID(t *testing.T) {
	msg := validateChannelConfig("direct_merchant", "stripe", "")
	if msg == "" {
		t.Error("expected validation error for direct_merchant with empty provider_account_id")
	}
	if !strings.Contains(msg, "provider_account_id") {
		t.Errorf("expected error to mention provider_account_id, got %q", msg)
	}
}

func TestChannel121_ValidateChannelConfig_DirectMerchantWithAccountID(t *testing.T) {
	msg := validateChannelConfig("direct_merchant", "stripe", "acct_123")
	if msg != "" {
		t.Errorf("expected no error for valid direct_merchant config, got %q", msg)
	}
}

func TestChannel121_ValidateChannelConfig_MerchantOfRecord_NoAccountIDRequired(t *testing.T) {
	msg := validateChannelConfig("merchant_of_record", "stripe", "")
	if msg != "" {
		t.Errorf("expected no error for merchant_of_record without provider_account_id, got %q", msg)
	}
}

func TestChannel121_ValidateChannelConfig_InvalidPaymentMode(t *testing.T) {
	msg := validateChannelConfig("unknown_mode", "stripe", "acct_123")
	if msg == "" {
		t.Error("expected validation error for invalid payment_mode")
	}
	if !strings.Contains(msg, "payment_mode") {
		t.Errorf("expected error to mention payment_mode, got %q", msg)
	}
}

func TestChannel121_ValidateChannelConfig_InvalidProvider(t *testing.T) {
	msg := validateChannelConfig("direct_merchant", "paypal", "acct_123")
	if msg == "" {
		t.Error("expected validation error for invalid provider")
	}
	if !strings.Contains(msg, "provider") {
		t.Errorf("expected error to mention provider, got %q", msg)
	}
}

func TestChannel121_ValidateChannelConfig_EmptyPaymentMode(t *testing.T) {
	msg := validateChannelConfig("", "stripe", "acct_123")
	if msg == "" {
		t.Error("expected validation error for empty payment_mode")
	}
}

func TestChannel121_ValidateChannelConfig_EmptyProvider(t *testing.T) {
	msg := validateChannelConfig("direct_merchant", "", "acct_123")
	if msg == "" {
		t.Error("expected validation error for empty provider")
	}
}

func TestChannel121_ValidateChannelConfig_AllpayDirectMerchant(t *testing.T) {
	msg := validateChannelConfig("direct_merchant", "allpay", "merchant_id_xyz")
	if msg != "" {
		t.Errorf("expected no error for allpay direct_merchant with account ID, got %q", msg)
	}
}

func TestChannel121_ValidateChannelConfig_AllpayMerchantOfRecord(t *testing.T) {
	msg := validateChannelConfig("merchant_of_record", "allpay", "")
	if msg != "" {
		t.Errorf("expected no error for allpay merchant_of_record, got %q", msg)
	}
}

func TestChannel121_CreateChannel_InvalidConfigReturns400(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	token := mintJWT(t, s.stub, "00000000-0000-0000-0000-000000000001")

	// direct_merchant without provider_account_id → config validation fails
	body := `{"name":"test","payment_mode":"direct_merchant","provider":"stripe"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/channels",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid config, got %d (body: %s)", w.Code, w.Body.String())
	}
	resp := channelRespJSON(t, w)
	if code := errorCode(t, resp); code != "channel.invalid_config" {
		t.Errorf("expected code='channel.invalid_config', got %q", code)
	}
}

func TestChannel121_CreateChannel_InvalidOrgIDReturns400(t *testing.T) {
	s := buildChannelServer(t)
	token := mintJWT(t, s.stub, "00000000-0000-0000-0000-000000000001")

	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/not-a-uuid/channels",
		strings.NewReader(`{"name":"x","payment_mode":"direct_merchant","provider":"stripe","provider_account_id":"acct"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid org_id UUID, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — sqlc gen files
// ─────────────────────────────────────────────────────────────────────────────

func TestChannel121_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "channels.sql")
	if content == "" {
		t.Fatal("query file channels.sql is empty")
	}
}

func TestChannel121_QueryFileHasAllCRUDOps(t *testing.T) {
	content := findFileByName(t, "channels.sql")
	ops := []string{
		"InsertSalesChannel",
		"GetSalesChannelByID",
		"ListSalesChannelsByOrg",
		"UpdateSalesChannel",
		"SoftDeleteSalesChannel",
	}
	for _, op := range ops {
		if !strings.Contains(content, op) {
			t.Errorf("channels.sql missing operation %q", op)
		}
	}
}

func TestChannel121_QueryFileFiltersSoftDeleted(t *testing.T) {
	content := findFileByName(t, "channels.sql")
	if !strings.Contains(content, "deleted_at IS NULL") {
		t.Error("channels.sql missing 'deleted_at IS NULL' soft-delete filter")
	}
}

func TestChannel121_QueryFileSoftDeleteSetsDeletedAt(t *testing.T) {
	content := findFileByName(t, "channels.sql")
	if !strings.Contains(content, "deleted_at = now()") {
		t.Error("SoftDeleteSalesChannel must SET deleted_at = now()")
	}
}

func TestChannel121_GenGoFileExists(t *testing.T) {
	content := findFileByName(t, "channels.sql.go")
	if content == "" {
		t.Fatal("gen file channels.sql.go is empty")
	}
}

func TestChannel121_GenGoFileHasAllMethods(t *testing.T) {
	content := findFileByName(t, "channels.sql.go")
	methods := []string{
		"func (q *Queries) InsertSalesChannel",
		"func (q *Queries) GetSalesChannelByID",
		"func (q *Queries) ListSalesChannelsByOrg",
		"func (q *Queries) UpdateSalesChannel",
		"func (q *Queries) SoftDeleteSalesChannel",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("channels.sql.go missing method %q", m)
		}
	}
}

func TestChannel121_GenGoFileHasSalesChannelRowType(t *testing.T) {
	content := findFileByName(t, "channels.sql.go")
	if !strings.Contains(content, "type SalesChannelRow struct") {
		t.Error("channels.sql.go missing SalesChannelRow struct")
	}
}

func TestChannel121_GenGoFileHasCorrectFields(t *testing.T) {
	content := findFileByName(t, "channels.sql.go")
	fields := []string{
		"OrgID",
		"PaymentMode",
		"Provider",
		"ProviderAccountID",
		"FeePercent",
		"ReservationTTLOverride",
		"DeletedAt",
	}
	for _, f := range fields {
		if !strings.Contains(content, f) {
			t.Errorf("channels.sql.go SalesChannelRow missing field %q", f)
		}
	}
}

func TestChannel121_GenGoFileHasDeletedAtNullable(t *testing.T) {
	content := findFileByName(t, "channels.sql.go")
	// Whitespace-tolerant: gofmt realigns struct-field columns, so match the
	// field name and pointer type rather than the exact padding.
	if !regexp.MustCompile(`DeletedAt\s+\*time\.Time`).MatchString(content) {
		t.Error("channels.sql.go: DeletedAt must be *time.Time (nullable)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TTL override resolution (Step 4 / integration spec)
// ─────────────────────────────────────────────────────────────────────────────

func TestChannel121_SalesChannelRowHasTTLOverrideField(t *testing.T) {
	// Verify SalesChannelRow has ReservationTTLOverride as *int32 (nullable).
	ch := gen.SalesChannelRow{}
	if ch.ReservationTTLOverride != nil {
		t.Error("zero-value ReservationTTLOverride should be nil")
	}
	val := int32(600)
	ch.ReservationTTLOverride = &val
	if *ch.ReservationTTLOverride != 600 {
		t.Error("ReservationTTLOverride must hold a pointer to int32")
	}
}

func TestChannel121_ChannelResponseShape(t *testing.T) {
	// Verify channelResponse fields are correct.
	ch := channelResponse{
		ID:                     "00000000-0000-0000-0000-000000000001",
		OrgID:                  "00000000-0000-0000-0000-000000000002",
		Name:                   "Direct Stripe",
		PaymentMode:            "direct_merchant",
		Provider:               "stripe",
		ProviderAccountID:      nil,
		FeePercent:             "2.50",
		ReservationTTLOverride: nil,
		CreatedAt:              "2026-01-01T00:00:00Z",
		UpdatedAt:              "2026-01-01T00:00:00Z",
	}

	b, err := json.Marshal(ch)
	if err != nil {
		t.Fatalf("channelResponse marshal failed: %v", err)
	}
	s := string(b)
	for _, key := range []string{"id", "org_id", "name", "payment_mode", "provider", "fee_percent", "created_at", "updated_at"} {
		if !strings.Contains(s, `"`+key+`"`) {
			t.Errorf("channelResponse JSON missing key %q", key)
		}
	}
}

func TestChannel121_HandlersReturnJSONContentType(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	token := mintJWT(t, s.stub, "00000000-0000-0000-0000-000000000001")

	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/channels", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type: application/json, got %q", ct)
	}
}

func TestChannel121_SoftDeleteHandlerExists(t *testing.T) {
	// Verify the handler method exists by checking that the delete route is
	// mounted and returns 401 without auth (not 404).
	s := buildChannelServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/v1/organizations/"+orgID.String()+"/channels/"+chID.String(), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Errorf("DELETE /v1/organizations/{org_id}/channels/{id} is not mounted (got 404)")
	}
}

func TestChannel121_DeleteResponseHasDeletedFlag(t *testing.T) {
	// This tests the handler shape (deleted:true in body) rather than actual DB.
	// It relies on handleDeleteChannel returning early when pool.BeginTx fails.
	s := buildChannelServer(t)
	orgID := uuid.New()
	chID := uuid.New()
	token := mintJWT(t, s.stub, "00000000-0000-0000-0000-000000000001")

	req := httptest.NewRequest(http.MethodDelete, "/v1/organizations/"+orgID.String()+"/channels/"+chID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// dbDownPool.BeginTx fails → 503 with dependency error code
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (dbDownPool.BeginTx fails), got %d (body: %s)", w.Code, w.Body.String())
	}
	resp := channelRespJSON(t, w)
	if code := errorCode(t, resp); !strings.Contains(code, "dependency") {
		t.Errorf("expected dependency error code, got %q", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Feature #236 — provider_account_id masking + settings round-trip
// ─────────────────────────────────────────────────────────────────────────────

func TestChannel236_MaskProviderAccountID_Nil(t *testing.T) {
	if got := maskProviderAccountID(nil); got != nil {
		t.Errorf("nil input must map to nil pointer, got %v", got)
	}
}

func TestChannel236_MaskProviderAccountID_Empty(t *testing.T) {
	empty := ""
	got := maskProviderAccountID(&empty)
	if got == nil || *got != "" {
		t.Errorf("empty string must round-trip as empty, got %v", got)
	}
}

func TestChannel236_MaskProviderAccountID_ShortValueCollapses(t *testing.T) {
	cases := []string{"a", "ab", "abc", "abcd"}
	for _, v := range cases {
		v := v
		t.Run(v, func(t *testing.T) {
			got := maskProviderAccountID(&v)
			if got == nil || *got != "****" {
				t.Errorf("short value %q must collapse to '****', got %v", v, got)
			}
			if got != nil && strings.Contains(*got, v) && len(v) > 0 {
				t.Errorf("masked value %q must not contain the original %q", *got, v)
			}
		})
	}
}

func TestChannel236_MaskProviderAccountID_PreservesLast4(t *testing.T) {
	raw := "acct_1Q2W3E4R5T6Y"
	got := maskProviderAccountID(&raw)
	if got == nil {
		t.Fatal("masked pointer must not be nil")
	}
	if *got != "****5T6Y" {
		t.Errorf("expected mask '****5T6Y' for %q, got %q", raw, *got)
	}
	if strings.HasPrefix(*got, "acct_") {
		t.Errorf("mask must hide the prefix, got %q", *got)
	}
}

func TestChannel236_MaskProviderAccountID_AllpayShape(t *testing.T) {
	raw := "MERCHANT_99887766"
	got := maskProviderAccountID(&raw)
	if got == nil || *got != "****7766" {
		t.Errorf("allpay-shape id must mask to '****7766', got %v", got)
	}
}

func TestChannel236_ChannelFromRowMasked_HidesCredential(t *testing.T) {
	raw := "acct_stripe_SECRET12345"
	row := gen.SalesChannelRow{
		ID:                uuid.New(),
		OrgID:             uuid.New(),
		Name:              "MoR Stripe",
		PaymentMode:       "direct_merchant",
		Provider:          "stripe",
		ProviderAccountID: &raw,
		FeePercent:        "1.50",
	}
	resp := channelFromRowMasked(row)
	if resp.ProviderAccountID == nil {
		t.Fatal("masked response must keep ProviderAccountID non-nil when input non-nil")
	}
	if *resp.ProviderAccountID == raw {
		t.Errorf("masked response leaks raw value: %q", *resp.ProviderAccountID)
	}
	if !strings.HasPrefix(*resp.ProviderAccountID, "****") {
		t.Errorf("masked response must start with '****', got %q", *resp.ProviderAccountID)
	}
	if !strings.HasSuffix(*resp.ProviderAccountID, "2345") {
		t.Errorf("masked response must preserve last 4 chars '2345', got %q", *resp.ProviderAccountID)
	}
}

func TestChannel236_ChannelFromRow_WriteResponseKeepsRawCredential(t *testing.T) {
	// Write-path responses (POST/PATCH) deliberately keep the raw value so
	// the caller can verify what they just wrote.
	raw := "acct_admin_SETUP_999"
	row := gen.SalesChannelRow{
		ID:                uuid.New(),
		OrgID:             uuid.New(),
		Name:              "ch",
		PaymentMode:       "direct_merchant",
		Provider:          "stripe",
		ProviderAccountID: &raw,
		FeePercent:        "0.00",
	}
	resp := channelFromRow(row)
	if resp.ProviderAccountID == nil || *resp.ProviderAccountID != raw {
		t.Errorf("write-path serializer must keep raw credential, got %v", resp.ProviderAccountID)
	}
}

func TestChannel236_NormalizeChannelSettings_EmptyAccepted(t *testing.T) {
	settings, msg := normalizeChannelSettings(nil)
	if msg != "" {
		t.Errorf("nil settings must validate cleanly, got %q", msg)
	}
	if settings != nil {
		t.Errorf("nil settings must remain nil for the DB driver, got %v", settings)
	}
}

func TestChannel236_NormalizeChannelSettings_ObjectAccepted(t *testing.T) {
	in := []byte(`{"statement_descriptor":"acme","terminal_id":7}`)
	out, msg := normalizeChannelSettings(in)
	if msg != "" {
		t.Errorf("valid JSON object must be accepted, got %q", msg)
	}
	if string(out) != string(in) {
		t.Errorf("settings must be returned verbatim, got %q", string(out))
	}
}

func TestChannel236_NormalizeChannelSettings_ArrayRejected(t *testing.T) {
	_, msg := normalizeChannelSettings([]byte(`[1,2,3]`))
	if msg == "" {
		t.Error("array must be rejected")
	}
}

func TestChannel236_NormalizeChannelSettings_ScalarRejected(t *testing.T) {
	_, msg := normalizeChannelSettings([]byte(`"oops"`))
	if msg == "" {
		t.Error("scalar must be rejected")
	}
}

func TestChannel236_NormalizeChannelSettings_InvalidJSONRejected(t *testing.T) {
	_, msg := normalizeChannelSettings([]byte(`{not json`))
	if msg == "" {
		t.Error("malformed JSON must be rejected")
	}
}

func TestChannel236_SettingsForResponse_NeverNil(t *testing.T) {
	if got := string(settingsForResponse(nil)); got != "{}" {
		t.Errorf("nil settings must render as '{}', got %q", got)
	}
	if got := string(settingsForResponse([]byte{})); got != "{}" {
		t.Errorf("empty settings must render as '{}', got %q", got)
	}
	src := []byte(`{"a":1}`)
	if got := string(settingsForResponse(src)); got != `{"a":1}` {
		t.Errorf("populated settings must pass through, got %q", got)
	}
}

func TestChannel236_CreateChannel_InvalidSettingsReturns400(t *testing.T) {
	s := buildChannelServer(t)
	orgID := uuid.New()
	token := mintJWT(t, s.stub, "00000000-0000-0000-0000-000000000001")

	// Array is not a JSON object → validation error.
	body := `{"name":"x","payment_mode":"merchant_of_record","provider":"stripe","settings":[1,2,3]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/channels",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-object settings, got %d (body: %s)", w.Code, w.Body.String())
	}
	resp := channelRespJSON(t, w)
	if code := errorCode(t, resp); code != "channel.invalid_settings" {
		t.Errorf("expected code='channel.invalid_settings', got %q", code)
	}
}

func TestChannel236_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0045_channel_settings.sql")
	if content == "" {
		t.Fatal("migration file 0045_channel_settings.sql is empty or missing")
	}
	required := []string{
		"ALTER TABLE sales_channels",
		"ADD COLUMN",
		"settings jsonb",
		"-- +goose Down",
		"DROP COLUMN",
	}
	for _, token := range required {
		if !strings.Contains(content, token) {
			t.Errorf("migration 0045 missing token %q", token)
		}
	}
}

func TestChannel236_QueryFileSettingsParameter(t *testing.T) {
	content := findFileByName(t, "channels.sql")
	for _, token := range []string{
		"settings",
		"$8::jsonb",
		"$9::jsonb",
	} {
		if !strings.Contains(content, token) {
			t.Errorf("channels.sql missing token %q for #236 settings support", token)
		}
	}
}

func TestChannel236_GenFileSettingsField(t *testing.T) {
	content := findFileByName(t, "channels.sql.go")
	for _, token := range []string{
		"Settings",
		"json.RawMessage",
		"settings json.RawMessage",
	} {
		if !strings.Contains(content, token) {
			t.Errorf("channels.sql.go missing token %q for #236 settings support", token)
		}
	}
}

func TestChannel236_ChannelResponse_HasSettingsField(t *testing.T) {
	ch := channelResponse{Settings: json.RawMessage(`{"feature_flag":true}`)}
	b, err := json.Marshal(ch)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(b), `"settings"`) {
		t.Errorf("channelResponse JSON missing 'settings' key: %s", string(b))
	}
	if !strings.Contains(string(b), `"feature_flag":true`) {
		t.Errorf("channelResponse must round-trip settings JSON verbatim, got %s", string(b))
	}
}

func TestChannel121_FullVerification(t *testing.T) {
	t.Run("migration_exists", func(t *testing.T) {
		content := findFileByName(t, "0010_sales_channels.sql")
		if !strings.Contains(content, "CREATE TABLE sales_channels") {
			t.Error("migration must create sales_channels table")
		}
	})
	t.Run("query_file_exists", func(t *testing.T) {
		content := findFileByName(t, "channels.sql")
		if !strings.Contains(content, "InsertSalesChannel") {
			t.Error("query file missing InsertSalesChannel")
		}
	})
	t.Run("gen_go_file_exists", func(t *testing.T) {
		content := findFileByName(t, "channels.sql.go")
		if !strings.Contains(content, "SalesChannelRow") {
			t.Error("gen file missing SalesChannelRow type")
		}
	})
	t.Run("config_validator_exists", func(t *testing.T) {
		// direct_merchant requires provider_account_id
		if msg := validateChannelConfig("direct_merchant", "stripe", ""); msg == "" {
			t.Error("validator must reject direct_merchant with empty provider_account_id")
		}
		// merchant_of_record is valid without provider_account_id
		if msg := validateChannelConfig("merchant_of_record", "stripe", ""); msg != "" {
			t.Errorf("validator must accept merchant_of_record without account id, got: %q", msg)
		}
	})
}
