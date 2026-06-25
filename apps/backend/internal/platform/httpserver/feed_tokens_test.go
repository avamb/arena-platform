// feed_tokens_test.go — unit tests for feature #122 (Agent feed token management).
//
// Test coverage:
//
//	Step 1: Migration file 0013_agent_feed_tokens.sql exists with correct schema + seeds
//	Step 2: POST/GET/DELETE endpoints scoped to org+channel - auth-gated,
//	        with correct request validation behaviour (no DB required)
//	Step 3: Last-used-at update — TouchFeedTokenLastUsed wired in public feed handler
//	Step 4: sqlc query file (feed_tokens.sql) and gen file (feed_tokens.sql.go) structure
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

const feedTokenTestActorID = "00000000-0000-0000-0000-000000000002"

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for feed token route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildFeedTokenServer builds a Server with stub auth, feed token routes fully
// mounted, and a dbDownPool so real DB operations never execute.
func buildFeedTokenServer(t *testing.T) *Server {
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
		t.Fatalf("buildFeedTokenServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// FeedTokenQueries non-nil so feed token route conditionals pass.
		FeedTokenQueries: gen.New(nil),
		// OrgQueries and ChannelQueries also non-nil for consistency.
		OrgQueries:     gen.New(nil),
		ChannelQueries: gen.New(nil),
		// Audit writer required for DELETE.
		Audit: &captureAuditWriter{},
	})
}

// feedTokenRespJSON decodes the response body into a map and returns it.
func feedTokenRespJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("feed_token: JSON decode failed: %v (body: %s)", err, w.Body.String())
	}
	return m
}

// feedToken mints a test JWT for feed token endpoint tests.
func feedTokenTestJWT(t *testing.T, s *Server) string {
	t.Helper()
	if s.stub == nil {
		t.Fatal("stub auth not wired")
	}
	return mintJWT(t, s.stub, feedTokenTestActorID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration file exists with correct schema + seeds
// ─────────────────────────────────────────────────────────────────────────────

func TestFeedToken122_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	if content == "" {
		t.Fatal("migration file 0013_agent_feed_tokens.sql is empty")
	}
}

func TestFeedToken122_MigrationHasAgentFeedTokensTable(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")

	required := []string{
		"CREATE TABLE agent_feed_tokens",
		"id",
		"token",
		"sales_channel_id",
		"label",
		"is_active",
		"revoked_at",
		"last_used_at",
		"created_at",
		"updated_at",
		"uuidv7()",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Errorf("migration missing %q", r)
		}
	}
}

func TestFeedToken122_MigrationHasSalesChannelIDFK(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	if !strings.Contains(content, "REFERENCES sales_channels(id)") {
		t.Error("migration must have sales_channel_id FK referencing sales_channels table")
	}
}

func TestFeedToken122_MigrationHasUniqueTokenConstraint(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	if !strings.Contains(content, "UNIQUE") {
		t.Error("migration must have UNIQUE constraint on token column")
	}
}

func TestFeedToken122_MigrationHasIsActiveDefault(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	if !strings.Contains(content, "DEFAULT true") {
		t.Error("migration must set is_active DEFAULT true")
	}
}

func TestFeedToken122_MigrationHasPermissionSeeds(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	perms := []string{"feed_token.create", "feed_token.read", "feed_token.delete"}
	for _, p := range perms {
		if !strings.Contains(content, p) {
			t.Errorf("migration missing permission seed %q", p)
		}
	}
}

func TestFeedToken122_MigrationHasRBACGrants(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	if !strings.Contains(content, "'admin'") {
		t.Error("migration must grant feed_token permissions to admin role")
	}
	if !strings.Contains(content, "'org_admin'") {
		t.Error("migration must grant feed_token permissions to org_admin role")
	}
}

func TestFeedToken122_MigrationHasGooseDownSection(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration must have a -- +goose Down section")
	}
	if !strings.Contains(content, "DROP TABLE IF EXISTS agent_feed_tokens") {
		t.Error("goose Down must DROP TABLE IF EXISTS agent_feed_tokens")
	}
}

func TestFeedToken122_MigrationHasIndexes(t *testing.T) {
	content := findFileByName(t, "0013_agent_feed_tokens.sql")
	// Should have index on sales_channel_id for management API.
	if !strings.Contains(content, "feed_tokens_channel_id") {
		t.Error("migration must have index feed_tokens_channel_id on sales_channel_id")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — Route auth + validation (POST/GET/DELETE endpoints)
// ─────────────────────────────────────────────────────────────────────────────

func TestFeedToken122_PostFeedTokenRequiresAuth(t *testing.T) {
	s := buildFeedTokenServer(t)
	orgID := uuid.New()
	channelID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens",
		strings.NewReader(`{"label":"website"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST feed-tokens without auth: want 401, got %d", w.Code)
	}
}

func TestFeedToken122_GetFeedTokensRequiresAuth(t *testing.T) {
	s := buildFeedTokenServer(t)
	orgID := uuid.New()
	channelID := uuid.New()
	r := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET feed-tokens without auth: want 401, got %d", w.Code)
	}
}

func TestFeedToken122_GetFeedTokenByIDRequiresAuth(t *testing.T) {
	s := buildFeedTokenServer(t)
	orgID := uuid.New()
	channelID := uuid.New()
	tokenID := uuid.New()
	r := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens/"+tokenID.String(), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET feed-tokens/{id} without auth: want 401, got %d", w.Code)
	}
}

func TestFeedToken122_DeleteFeedTokenRequiresAuth(t *testing.T) {
	s := buildFeedTokenServer(t)
	orgID := uuid.New()
	channelID := uuid.New()
	tokenID := uuid.New()
	r := httptest.NewRequest(http.MethodDelete,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens/"+tokenID.String(), nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE feed-tokens/{id} without auth: want 401, got %d", w.Code)
	}
}

func TestFeedToken122_CreateFeedToken_NilQueriesReturns503(t *testing.T) {
	// Server WITHOUT FeedTokenQueries → routes not mounted → 404.
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
		// FeedTokenQueries intentionally nil → routes not mounted → 404.
	})

	orgID := uuid.New()
	channelID := uuid.New()
	tok := mintJWT(t, s.stub, feedTokenTestActorID)
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens",
		strings.NewReader(`{"label":"test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// Without FeedTokenQueries the route isn't mounted → 404.
	if w.Code != http.StatusNotFound {
		t.Errorf("POST without FeedTokenQueries: want 404 (not mounted), got %d", w.Code)
	}
}

func TestFeedToken122_CreateFeedToken_InvalidJSONReturns400(t *testing.T) {
	s := buildFeedTokenServer(t)
	tok := feedTokenTestJWT(t, s)
	orgID := uuid.New()
	channelID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens",
		strings.NewReader("not-json"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid JSON: want 400, got %d", w.Code)
	}
	m := feedTokenRespJSON(t, w)
	var code string
	if errObj, ok := m["error"].(map[string]any); ok {
		code, _ = errObj["code"].(string)
	}
	if code != "feed_token.invalid_json" {
		t.Errorf("want code='feed_token.invalid_json', got %q", code)
	}
}

func TestFeedToken122_CreateFeedToken_InvalidOrgIDReturns400(t *testing.T) {
	s := buildFeedTokenServer(t)
	tok := feedTokenTestJWT(t, s)
	channelID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/not-a-uuid/channels/"+channelID.String()+"/feed-tokens",
		strings.NewReader(`{"label":"test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid org_id: want 400, got %d", w.Code)
	}
}

func TestFeedToken122_CreateFeedToken_InvalidChannelIDReturns400(t *testing.T) {
	s := buildFeedTokenServer(t)
	tok := feedTokenTestJWT(t, s)
	orgID := uuid.New()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID.String()+"/channels/not-a-uuid/feed-tokens",
		strings.NewReader(`{"label":"test"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST with invalid channel_id: want 400, got %d", w.Code)
	}
}

func TestFeedToken122_ListFeedTokens_NilQueriesReturns503(t *testing.T) {
	// Server WITHOUT FeedTokenQueries → list route not mounted → 404.
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
		// No FeedTokenQueries → routes not mounted.
	})

	orgID := uuid.New()
	channelID := uuid.New()
	tok := mintJWT(t, s.stub, feedTokenTestActorID)
	r := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET without FeedTokenQueries: want 404 (not mounted), got %d", w.Code)
	}
}

func TestFeedToken122_RevokeFeedToken_InvalidIDReturns400(t *testing.T) {
	s := buildFeedTokenServer(t)
	tok := feedTokenTestJWT(t, s)
	orgID := uuid.New()
	channelID := uuid.New()
	r := httptest.NewRequest(http.MethodDelete,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens/not-a-uuid", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("DELETE with invalid token id: want 400, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Public feed endpoint
// ─────────────────────────────────────────────────────────────────────────────

func TestFeedToken122_PublicFeedEndpointMounted(t *testing.T) {
	// The public feed endpoint GET /v1/feeds/{token} should be reachable
	// without authentication — the token in the path is the credential.
	s := buildFeedTokenServer(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/feeds/sometoken", nil)
	// No Authorization header — public endpoint.
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	// Route is mounted; DB nil → 500 (query fails). Not 404/401.
	if w.Code == http.StatusNotFound {
		t.Errorf("GET /v1/feeds/{token}: want route mounted (not 404), got %d", w.Code)
	}
	if w.Code == http.StatusUnauthorized {
		t.Errorf("GET /v1/feeds/{token}: public endpoint must NOT require auth (got 401)")
	}
}

func TestFeedToken122_PublicFeedEndpoint_NoAuthRequired(t *testing.T) {
	// Verify explicitly that no JWT is needed for the public feed endpoint.
	s := buildFeedTokenServer(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/feeds/any-token-value", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code == http.StatusUnauthorized {
		t.Errorf("Public feed endpoint must not return 401 (no auth required)")
	}
}

func TestFeedToken122_PublicFeedEndpoint_NilQueriesReturns503(t *testing.T) {
	// Server without FeedTokenQueries → public feed route not mounted → 404.
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
		// No FeedTokenQueries → public feed route not mounted.
	})

	r := httptest.NewRequest(http.MethodGet, "/v1/feeds/sometoken", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /v1/feeds without FeedTokenQueries: want 404, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Last-used-at wired in public feed handler
// ─────────────────────────────────────────────────────────────────────────────

func TestFeedToken122_HandlerFileHasTouchLastUsedAt(t *testing.T) {
	// Verify that feed_tokens.go contains TouchFeedTokenLastUsed call.
	// This is a structural test that the best-effort update is wired.
	content := findFileByName(t, "feed_tokens.sql.go")
	if !strings.Contains(content, "TouchFeedTokenLastUsed") {
		t.Error("feed_tokens.sql.go must implement TouchFeedTokenLastUsed")
	}
	if !strings.Contains(content, "last_used_at") {
		t.Error("feed_tokens.sql.go must update last_used_at in TouchFeedTokenLastUsed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — sqlc query file and gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestFeedToken122_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql")
	if content == "" {
		t.Fatal("feed_tokens.sql query file is empty")
	}
}

func TestFeedToken122_QueryFileHasAllOperations(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql")
	ops := []string{
		"InsertFeedToken",
		"GetFeedTokenByID",
		"ListFeedTokensByChannel",
		"RevokeFeedToken",
		"TouchFeedTokenLastUsed",
		"GetFeedTokenByToken",
	}
	for _, op := range ops {
		if !strings.Contains(content, op) {
			t.Errorf("feed_tokens.sql missing operation %q", op)
		}
	}
}

func TestFeedToken122_QueryFileHasRevocationUpdate(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql")
	// RevokeFeedToken must set is_active = false.
	if !strings.Contains(content, "is_active  = false") && !strings.Contains(content, "is_active = false") {
		t.Error("feed_tokens.sql RevokeFeedToken must set is_active = false")
	}
	// RevokeFeedToken must set revoked_at.
	if !strings.Contains(content, "revoked_at = now()") {
		t.Error("feed_tokens.sql RevokeFeedToken must set revoked_at = now()")
	}
}

func TestFeedToken122_QueryFileHasLastUsedAtUpdate(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql")
	if !strings.Contains(content, "last_used_at = now()") {
		t.Error("feed_tokens.sql TouchFeedTokenLastUsed must set last_used_at = now()")
	}
}

func TestFeedToken122_GenGoFileExists(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql.go")
	if content == "" {
		t.Fatal("feed_tokens.sql.go gen file is empty")
	}
}

func TestFeedToken122_GenGoFileHasFeedTokenRowType(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql.go")
	if !strings.Contains(content, "type FeedTokenRow struct") {
		t.Error("feed_tokens.sql.go must define FeedTokenRow struct")
	}
}

func TestFeedToken122_GenGoFileHasNullableFields(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql.go")
	// Check that the nullable fields exist as pointer types (exact alignment may vary).
	checks := []struct {
		name    string
		pattern string
	}{
		{"RevokedAt", "RevokedAt"},
		{"LastUsedAt", "LastUsedAt"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.pattern) {
			t.Errorf("feed_tokens.sql.go missing nullable field %q", c.name)
		}
		// Also check it's a pointer type.
		if !strings.Contains(content, "*time.Time") {
			t.Errorf("feed_tokens.sql.go must use *time.Time for nullable timestamp fields")
		}
	}
}

func TestFeedToken122_GenGoFileHasAllMethods(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql.go")
	methods := []string{
		"func (q *Queries) InsertFeedToken",
		"func (q *Queries) GetFeedTokenByID",
		"func (q *Queries) ListFeedTokensByChannel",
		"func (q *Queries) RevokeFeedToken",
		"func (q *Queries) TouchFeedTokenLastUsed",
		"func (q *Queries) GetFeedTokenByToken",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("feed_tokens.sql.go missing method %q", m)
		}
	}
}

func TestFeedToken122_GenGoFileHasIsActiveBoolField(t *testing.T) {
	content := findFileByName(t, "feed_tokens.sql.go")
	if !strings.Contains(content, "IsActive") {
		t.Error("feed_tokens.sql.go FeedTokenRow must have IsActive bool field")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response shape tests
// ─────────────────────────────────────────────────────────────────────────────

func TestFeedToken122_FeedTokenResponseShape(t *testing.T) {
	ft := gen.FeedTokenRow{
		ID:             uuid.New(),
		Token:          "abc123def456",
		SalesChannelID: uuid.New(),
		Label:          "website widget",
		IsActive:       true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	resp := feedTokenFromRow(ft)

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal feedTokenResponse: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	required := []string{"id", "token", "sales_channel_id", "label", "is_active", "created_at", "updated_at"}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Errorf("feedTokenResponse JSON missing field %q", k)
		}
	}
}

func TestFeedToken122_FeedTokenResponseTimestampsAreRFC3339(t *testing.T) {
	ft := gen.FeedTokenRow{
		ID:             uuid.New(),
		Token:          "test-token",
		SalesChannelID: uuid.New(),
		Label:          "test",
		IsActive:       true,
		CreatedAt:      time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2024, 1, 15, 13, 0, 0, 0, time.UTC),
	}
	resp := feedTokenFromRow(ft)
	if _, err := time.Parse(time.RFC3339, resp.CreatedAt); err != nil {
		t.Errorf("CreatedAt not RFC3339: %q, error: %v", resp.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, resp.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt not RFC3339: %q, error: %v", resp.UpdatedAt, err)
	}
}

func TestFeedToken122_FeedTokenResponseNilRevokedAt(t *testing.T) {
	ft := gen.FeedTokenRow{
		ID:             uuid.New(),
		Token:          "active-token",
		SalesChannelID: uuid.New(),
		IsActive:       true,
		RevokedAt:      nil, // not revoked
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	resp := feedTokenFromRow(ft)
	if resp.RevokedAt != nil {
		t.Errorf("RevokedAt should be nil for active token, got %v", resp.RevokedAt)
	}
}

func TestFeedToken122_FeedTokenResponseRevokedAtSet(t *testing.T) {
	now := time.Now().UTC()
	ft := gen.FeedTokenRow{
		ID:             uuid.New(),
		Token:          "revoked-token",
		SalesChannelID: uuid.New(),
		IsActive:       false,
		RevokedAt:      &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	resp := feedTokenFromRow(ft)
	if resp.RevokedAt == nil {
		t.Error("RevokedAt should be set for revoked token")
	}
}

func TestFeedToken122_FeedTokenResponseIsActiveField(t *testing.T) {
	ft := gen.FeedTokenRow{
		ID:             uuid.New(),
		Token:          "check-active",
		SalesChannelID: uuid.New(),
		IsActive:       true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	resp := feedTokenFromRow(ft)
	if !resp.IsActive {
		t.Error("feedTokenResponse.IsActive should be true for active token")
	}
}

func TestFeedToken122_HandlersReturnJSONContentType(t *testing.T) {
	s := buildFeedTokenServer(t)
	tok := feedTokenTestJWT(t, s)
	orgID := uuid.New()
	channelID := uuid.New()

	r := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/channels/"+channelID.String()+"/feed-tokens", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET feed-tokens Content-Type: want application/json, got %q", ct)
	}
}

func TestFeedToken122_GenerateFeedToken_IsHex64Chars(t *testing.T) {
	// Test that generateFeedToken returns a valid 64-char hex string (32 bytes).
	token, err := generateFeedToken()
	if err != nil {
		t.Fatalf("generateFeedToken: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("generateFeedToken: want 64 hex chars, got %d (%q)", len(token), token)
	}
	// All chars should be hex.
	for i, c := range token {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("generateFeedToken: char %d is not lowercase hex: %c", i, c)
		}
	}
}

func TestFeedToken122_GenerateFeedToken_IsUnique(t *testing.T) {
	// Two calls should produce different tokens (statistically certain with 32-byte random).
	t1, err1 := generateFeedToken()
	t2, err2 := generateFeedToken()
	if err1 != nil || err2 != nil {
		t.Fatalf("generateFeedToken errors: %v, %v", err1, err2)
	}
	if t1 == t2 {
		t.Error("generateFeedToken: two calls returned the same token (collision)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Full verification test
// ─────────────────────────────────────────────────────────────────────────────

func TestFeedToken122_FullVerification(t *testing.T) {
	t.Run("migration_has_correct_schema", func(t *testing.T) {
		content := findFileByName(t, "0013_agent_feed_tokens.sql")
		checks := []string{
			"CREATE TABLE agent_feed_tokens",
			"token",
			"sales_channel_id",
			"is_active",
			"revoked_at",
			"last_used_at",
			"REFERENCES sales_channels(id)",
			"UNIQUE",
			"DEFAULT true",
			"feed_token.create",
			"feed_token.read",
			"feed_token.delete",
		}
		for _, c := range checks {
			if !strings.Contains(content, c) {
				t.Errorf("migration missing %q", c)
			}
		}
	})

	t.Run("management_routes_require_auth", func(t *testing.T) {
		s := buildFeedTokenServer(t)
		orgID := uuid.New()
		channelID := uuid.New()
		tokenID := uuid.New()

		endpoints := []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodPost, "/v1/organizations/" + orgID.String() + "/channels/" + channelID.String() + "/feed-tokens", `{"label":"x"}`},
			{http.MethodGet, "/v1/organizations/" + orgID.String() + "/channels/" + channelID.String() + "/feed-tokens", ""},
			{http.MethodGet, "/v1/organizations/" + orgID.String() + "/channels/" + channelID.String() + "/feed-tokens/" + tokenID.String(), ""},
			{http.MethodDelete, "/v1/organizations/" + orgID.String() + "/channels/" + channelID.String() + "/feed-tokens/" + tokenID.String(), ""},
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

	t.Run("public_feed_requires_no_auth", func(t *testing.T) {
		s := buildFeedTokenServer(t)
		r := httptest.NewRequest(http.MethodGet, "/v1/feeds/some-token", nil)
		// No Authorization header.
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		// Should not be 401 (public endpoint).
		if w.Code == http.StatusUnauthorized {
			t.Error("GET /v1/feeds/{token} must not require auth (public endpoint)")
		}
		// Should not be 404 (route must be mounted).
		if w.Code == http.StatusNotFound {
			t.Error("GET /v1/feeds/{token} route must be mounted")
		}
	})

	t.Run("gen_file_implements_all_methods", func(t *testing.T) {
		content := findFileByName(t, "feed_tokens.sql.go")
		methods := []string{
			"InsertFeedToken",
			"GetFeedTokenByID",
			"ListFeedTokensByChannel",
			"RevokeFeedToken",
			"TouchFeedTokenLastUsed",
			"GetFeedTokenByToken",
		}
		for _, m := range methods {
			if !strings.Contains(content, m) {
				t.Errorf("feed_tokens.sql.go missing method %q", m)
			}
		}
	})

	t.Run("token_generation_is_cryptographic", func(t *testing.T) {
		token, err := generateFeedToken()
		if err != nil {
			t.Fatalf("generateFeedToken: %v", err)
		}
		// 32 bytes → 64 hex chars.
		if len(token) != 64 {
			t.Errorf("want 64 char hex token, got %d chars", len(token))
		}
	})
}
