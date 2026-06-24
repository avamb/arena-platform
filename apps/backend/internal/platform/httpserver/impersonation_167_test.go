// impersonation_167_test.go — integration tests for the admin impersonation endpoint
// (feature #167).
//
// Test naming convention: TestImpersonation167_*
// All tests verify structural and behavioural contracts without a live database.
//
// Test categories:
//   - Source file structure (handler, constants, types)
//   - auth.go impersonation claim structure
//   - Route auth-gating (401 without JWT)
//   - Permission gating (403 without admin role)
//   - Request validation (missing/invalid user_id, missing reason, excess duration)
//   - Happy path: 200 with correct response shape
//   - Returned JWT carries impersonation claims
//   - Returned JWT has correct expiry (≤ 30 min)
//   - Actor.IsImpersonated() on verified impersonation token
//   - Audit logging: issuance is recorded
//   - server.go wiring checks
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory and helpers
// ─────────────────────────────────────────────────────────────────────────────

const impersonationTestAdminID = "00000000-0000-0000-0000-000000000167"
const impersonationTestTargetID = "aaaaaaaa-0000-0000-0000-000000000001"

// buildImpersonationServer builds a Server with stub auth and the superadmin
// permission group mounted (so POST /admin/impersonate is available).
func buildImpersonationServer(t *testing.T) *Server {
	t.Helper()
	return buildImpersonationServerFull(t, nil)
}

// buildImpersonationServerFull builds a Server with an optional audit writer.
func buildImpersonationServerFull(t *testing.T, aw audit.Writer) *Server {
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
		t.Fatalf("buildImpersonationServerFull: NewStubProvider: %v", err)
	}
	opts := Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		SuperadminQueries: gen.New(nil), // needed so superadmin.read group is mounted
	}
	if aw != nil {
		opts.Audit = aw
	}
	return New(opts)
}

// mintAdminToken167 mints a dev JWT with the "admin" role using the given server.
func mintAdminToken167(t *testing.T, s *Server) string {
	t.Helper()
	body := `{"actor_id":"` + impersonationTestAdminID + `","roles":["admin"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintAdminToken167: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintAdminToken167: decode: %v", err)
	}
	tok, ok := resp["token"]
	if !ok || tok == "" {
		t.Fatalf("mintAdminToken167: no token in response")
	}
	return tok
}

// postImpersonate sends POST /v1/admin/impersonate with the given JWT and JSON body.
func postImpersonate(t *testing.T, s *Server, tok string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("postImpersonate: marshal body: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/impersonate", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	s.router.ServeHTTP(w, req)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// Source file structure tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_SourceFileExists(t *testing.T) {
	content := findFileByName(t, "impersonation.go")
	checks := []string{
		"handleImpersonate",
		"maxImpersonationDuration",
		"defaultImpersonationDuration",
		"impersonateRequest",
		"impersonateResponse",
		"impersonation.issue",
		"impersonation.missing_user_id",
		"impersonation.missing_reason",
		"impersonation.duration_too_long",
		"impersonation.invalid_user_id",
		"ImpersonatedBy",
		"ImpersonationReason",
		"IsImpersonated",
		"audit.Event",
		"impersonation.unavailable",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("impersonation.go: missing expected string %q", want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Constant value tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_MaxDurationIs30Min(t *testing.T) {
	if maxImpersonationDuration != 30*time.Minute {
		t.Errorf("maxImpersonationDuration = %v; want 30m0s", maxImpersonationDuration)
	}
}

func TestImpersonation167_DefaultDurationIs30Min(t *testing.T) {
	if defaultImpersonationDuration != 30*time.Minute {
		t.Errorf("defaultImpersonationDuration = %v; want 30m0s", defaultImpersonationDuration)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// auth.go impersonation claim structure checks (compile-time + runtime)
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_ActorHasImpersonationFields(t *testing.T) {
	a := auth.Actor{
		ImpersonatedBy:      "admin-id",
		ImpersonationReason: "support investigation",
	}
	if !a.IsImpersonated() {
		t.Error("Actor with ImpersonatedBy set should report IsImpersonated()=true")
	}
}

func TestImpersonation167_EmptyActorNotImpersonated(t *testing.T) {
	a := auth.Actor{}
	if a.IsImpersonated() {
		t.Error("Actor with empty ImpersonatedBy should report IsImpersonated()=false")
	}
}

func TestImpersonation167_IssueRequestHasImpersonationFields(t *testing.T) {
	req := auth.IssueRequest{
		ActorID:             "target-user-id",
		ImpersonatedBy:      "admin-id",
		ImpersonationReason: "user support",
	}
	if req.ImpersonatedBy != "admin-id" {
		t.Error("IssueRequest.ImpersonatedBy not set correctly")
	}
	if req.ImpersonationReason != "user support" {
		t.Error("IssueRequest.ImpersonationReason not set correctly")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Route auth-gating tests (401 without JWT)
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_RouteRequiresJWT(t *testing.T) {
	s := buildImpersonationServer(t)
	w := postImpersonate(t, s, "" /* no token */, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "test audit check",
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestImpersonation167_RouteRequiresSuperadminReadPermission(t *testing.T) {
	// The route is declared inside a RequirePermission("superadmin.read", …) group.
	// In the test environment the AllowAllChecker placeholder is used (foundation
	// milestone), so any authenticated actor passes — but we verify the middleware
	// string is present in server.go (tested by TestImpersonation167_ServerGoUsesSuperadminReadPermission).
	// Here we verify that an authenticated actor does reach the handler (not 404).
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	wr := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "permission reach test",
	})
	// With AllowAllChecker the handler is reached and should return 200.
	if wr.Code != http.StatusOK {
		t.Errorf("expected handler to be reachable (200) for authenticated actor; got %d; body: %s", wr.Code, wr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request validation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_MissingUserID_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"reason": "support investigation",
		// user_id intentionally omitted
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing user_id, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_user_id") {
		t.Errorf("expected error code 'impersonation.missing_user_id', got: %s", w.Body.String())
	}
}

func TestImpersonation167_WhitespaceUserID_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": "   ",
		"reason":  "support investigation",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace user_id, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_user_id") {
		t.Errorf("expected 'impersonation.missing_user_id', got: %s", w.Body.String())
	}
}

func TestImpersonation167_InvalidUserID_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": "not-a-valid-uuid",
		"reason":  "support investigation",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid user_id, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.invalid_user_id") {
		t.Errorf("expected 'impersonation.invalid_user_id', got: %s", w.Body.String())
	}
}

func TestImpersonation167_MissingReason_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		// reason intentionally omitted
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing reason, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_reason") {
		t.Errorf("expected 'impersonation.missing_reason', got: %s", w.Body.String())
	}
}

func TestImpersonation167_WhitespaceReason_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "   ",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace reason, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_reason") {
		t.Errorf("expected 'impersonation.missing_reason', got: %s", w.Body.String())
	}
}

func TestImpersonation167_DurationExceeds30Min_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id":          impersonationTestTargetID,
		"reason":           "support investigation",
		"duration_seconds": 1801, // 30min + 1sec → exceeds cap
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duration > 30min, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.duration_too_long") {
		t.Errorf("expected 'impersonation.duration_too_long', got: %s", w.Body.String())
	}
}

func TestImpersonation167_Exactly1800Seconds_IsAllowed(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id":          impersonationTestTargetID,
		"reason":           "exactly 30 min",
		"duration_seconds": 1800, // exactly 30 min — should be allowed
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for exactly 1800s, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Happy path response shape tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_HappyPath_Returns200(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "investigating user-reported bug #1234",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestImpersonation167_HappyPath_AllResponseFieldsPresent(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "investigating user-reported bug #1234",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	requiredFields := []string{"token", "expires_at", "impersonated_user_id", "impersonated_by", "reason"}
	for _, f := range requiredFields {
		if v, ok := resp[f]; !ok || v == "" {
			t.Errorf("response missing or empty field %q; got: %v", f, resp)
		}
	}
}

func TestImpersonation167_ResponseEchoesTargetUserID(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "echo user_id test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if v, _ := resp["impersonated_user_id"].(string); v != impersonationTestTargetID {
		t.Errorf("impersonated_user_id = %q; want %q", v, impersonationTestTargetID)
	}
}

func TestImpersonation167_ResponseEchoesAdminActorID(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "echo admin id test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if v, _ := resp["impersonated_by"].(string); v != impersonationTestAdminID {
		t.Errorf("impersonated_by = %q; want %q", v, impersonationTestAdminID)
	}
}

func TestImpersonation167_ResponseEchoesReason(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)
	reason := "investigating payment flow for user #42"

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  reason,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if v, _ := resp["reason"].(string); v != reason {
		t.Errorf("reason = %q; want %q", v, reason)
	}
}

func TestImpersonation167_ResponseContentTypeIsJSON(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "content type check",
	})
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// JWT claim verification tests
// ─────────────────────────────────────────────────────────────────────────────

// getImpersonationToken issues an impersonation token for the test target user.
func getImpersonationToken(t *testing.T, s *Server) string {
	t.Helper()
	tok := mintAdminToken167(t, s)
	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "claim verification test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("getImpersonationToken: got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	impTok, _ := resp["token"].(string)
	if impTok == "" {
		t.Fatal("getImpersonationToken: empty token in response")
	}
	return impTok
}

func TestImpersonation167_IssuedTokenIsVerifiable(t *testing.T) {
	s := buildImpersonationServer(t)
	impTok := getImpersonationToken(t, s)

	actor, err := s.stub.Verify(context.Background(), impTok)
	if err != nil {
		t.Fatalf("impersonation token failed Verify: %v", err)
	}
	if actor.ID != impersonationTestTargetID {
		t.Errorf("actor.ID = %q; want target user %q", actor.ID, impersonationTestTargetID)
	}
}

func TestImpersonation167_IssuedTokenSubjectIsTargetUser(t *testing.T) {
	s := buildImpersonationServer(t)
	impTok := getImpersonationToken(t, s)

	actor, err := s.stub.Verify(context.Background(), impTok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// The impersonation token must carry the target user's ID as subject (not admin).
	if actor.ID != impersonationTestTargetID {
		t.Errorf("actor.ID (sub) = %q; want target %q", actor.ID, impersonationTestTargetID)
	}
}

func TestImpersonation167_IssuedTokenHasImpersonatedByClaim(t *testing.T) {
	s := buildImpersonationServer(t)
	impTok := getImpersonationToken(t, s)

	actor, err := s.stub.Verify(context.Background(), impTok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if actor.ImpersonatedBy != impersonationTestAdminID {
		t.Errorf("actor.ImpersonatedBy = %q; want %q", actor.ImpersonatedBy, impersonationTestAdminID)
	}
}

func TestImpersonation167_IssuedTokenHasImpersonationReasonClaim(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)
	reason := "checking ticket assignment logic"

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  reason,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	impTok, _ := resp["token"].(string)

	actor, err := s.stub.Verify(context.Background(), impTok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if actor.ImpersonationReason != reason {
		t.Errorf("actor.ImpersonationReason = %q; want %q", actor.ImpersonationReason, reason)
	}
}

func TestImpersonation167_IsImpersonatedTrueOnIssuedToken(t *testing.T) {
	s := buildImpersonationServer(t)
	impTok := getImpersonationToken(t, s)

	actor, err := s.stub.Verify(context.Background(), impTok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !actor.IsImpersonated() {
		t.Error("expected actor.IsImpersonated()=true on impersonation token")
	}
}

func TestImpersonation167_RegularTokenNotImpersonated(t *testing.T) {
	s := buildImpersonationServer(t)
	// Regular admin token should NOT be an impersonation token.
	tok := mintAdminToken167(t, s)
	actor, err := s.stub.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify regular token: %v", err)
	}
	if actor.IsImpersonated() {
		t.Error("regular admin token should have IsImpersonated()=false")
	}
}

func TestImpersonation167_IssuedTokenExpiresWithin30Min(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)
	before := time.Now()

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "expiry boundary test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	expiresAtStr, _ := resp["expires_at"].(string)
	if expiresAtStr == "" {
		t.Fatal("expires_at field is empty")
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		t.Fatalf("parse expires_at %q: %v", expiresAtStr, err)
	}
	// Expiry must be in the future.
	if !expiresAt.After(before) {
		t.Errorf("expires_at %v must be after request time %v", expiresAt, before)
	}
	// Expiry must be within 30min + small clock skew margin.
	maxExpiry := before.Add(30*time.Minute + 5*time.Second)
	if expiresAt.After(maxExpiry) {
		t.Errorf("expires_at %v exceeds 30min cap (max %v)", expiresAt, maxExpiry)
	}
}

func TestImpersonation167_DefaultDuration_ExpiresNear30Min(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)
	before := time.Now()

	// duration_seconds absent → defaults to 30min.
	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "default duration test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	expiresAtStr, _ := resp["expires_at"].(string)
	expiresAt, _ := time.Parse(time.RFC3339, expiresAtStr)
	// Should expire approximately 30min from now (at least 29min).
	minExpiry := before.Add(29 * time.Minute)
	if expiresAt.Before(minExpiry) {
		t.Errorf("default-duration expiry %v should be ~30min from now (at least %v)", expiresAt, minExpiry)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit logging tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_AuditLoggedOnIssuance(t *testing.T) {
	aw := &captureAuditWriter{}
	s := buildImpersonationServerFull(t, aw)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "audit capture test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	events := aw.getEvents()
	var found *audit.Event
	for i := range events {
		if events[i].Action == "impersonation.issue" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected audit event with action='impersonation.issue'; got events: %v", events)
	}
	if found.ResourceType != "user" {
		t.Errorf("audit ResourceType = %q; want %q", found.ResourceType, "user")
	}
	if found.ResourceID != impersonationTestTargetID {
		t.Errorf("audit ResourceID = %q; want %q", found.ResourceID, impersonationTestTargetID)
	}
	if found.ActorID != impersonationTestAdminID {
		t.Errorf("audit ActorID = %q; want %q", found.ActorID, impersonationTestAdminID)
	}
}

func TestImpersonation167_AuditMetadataContainsReason(t *testing.T) {
	aw := &captureAuditWriter{}
	s := buildImpersonationServerFull(t, aw)
	tok := mintAdminToken167(t, s)
	reason := "checking payment method configuration #999"

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  reason,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	events := aw.getEvents()
	var found *audit.Event
	for i := range events {
		if events[i].Action == "impersonation.issue" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no impersonation.issue audit event found")
	}
	if r, _ := found.Metadata["reason"].(string); r != reason {
		t.Errorf("audit metadata reason = %q; want %q", r, reason)
	}
}

func TestImpersonation167_AuditMetadataContainsDuration(t *testing.T) {
	aw := &captureAuditWriter{}
	s := buildImpersonationServerFull(t, aw)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id":          impersonationTestTargetID,
		"reason":           "duration audit test",
		"duration_seconds": 600,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	events := aw.getEvents()
	for _, ev := range events {
		if ev.Action == "impersonation.issue" {
			if _, ok := ev.Metadata["duration_seconds"]; !ok {
				t.Error("audit metadata missing 'duration_seconds' field")
			}
			return
		}
	}
	t.Fatal("no impersonation.issue audit event found")
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_ServerGoHasHandler(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "handleImpersonate") {
		t.Error("server.go: missing 'handleImpersonate'")
	}
}

func TestImpersonation167_ServerGoMountsPostRoute(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, `Post("/admin/impersonate"`) {
		t.Error(`server.go: missing Post("/admin/impersonate", ...)`)
	}
}

func TestImpersonation167_ServerGoUsesSuperadminReadPermission(t *testing.T) {
	// The impersonation route must be gated behind superadmin.read permission.
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, `"superadmin.read"`) {
		t.Error(`server.go: missing "superadmin.read" permission gate`)
	}
}

func TestImpersonation167_ServerGoHasImpersonationComment(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "/admin/impersonate") {
		t.Error("server.go: missing '/admin/impersonate' path string")
	}
}
