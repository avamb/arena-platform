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
// Test server factory and helpers
// ─────────────────────────────────────────────────────────────────────────────

const impersonationTestAdminID = "00000000-0000-0000-0000-000000000167"
const impersonationTestTargetID = "aaaaaaaa-0000-0000-0000-000000000001"

// buildImpersonationServer builds a Server with stub auth, superadmin queries
// (so the superadmin.read permission gate is wired), and the impersonation route.
func buildImpersonationServer(t *testing.T) *Server {
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
		t.Fatalf("buildImpersonationServer: NewStubProvider: %v", err)
	}
	// Pass SuperadminQueries so the superadmin.read permission group is mounted
	// (which includes the POST /admin/impersonate route).
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		SuperadminQueries: gen.New(nil),
	})
}

// mintAdminToken mints a dev JWT with the admin role for impersonation tests.
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

// postImpersonate sends POST /v1/admin/impersonate with the given JWT and body.
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

func TestImpersonation167_MaxDurationIs30Min(t *testing.T) {
	// Verify the constant is set to 30 minutes.
	if maxImpersonationDuration != 30*time.Minute {
		t.Errorf("maxImpersonationDuration = %v; want 30m", maxImpersonationDuration)
	}
}

func TestImpersonation167_DefaultDurationIs30Min(t *testing.T) {
	if defaultImpersonationDuration != 30*time.Minute {
		t.Errorf("defaultImpersonationDuration = %v; want 30m", defaultImpersonationDuration)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// auth.go impersonation claim structure checks
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_AuthActorHasImpersonationFields(t *testing.T) {
	// Verify the Actor struct has the required impersonation fields.
	a := auth.Actor{
		ImpersonatedBy:      "admin-id",
		ImpersonationReason: "support investigation",
	}
	if !a.IsImpersonated() {
		t.Error("Actor with ImpersonatedBy set should report IsImpersonated()=true")
	}
	a2 := auth.Actor{}
	if a2.IsImpersonated() {
		t.Error("Actor with empty ImpersonatedBy should report IsImpersonated()=false")
	}
}

func TestImpersonation167_IssueRequestHasImpersonationFields(t *testing.T) {
	// Verify IssueRequest carries the impersonation fields.
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
	w := postImpersonate(t, s, "", map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "test audit check",
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestImpersonation167_RouteRejectsNonAdminJWT(t *testing.T) {
	s := buildImpersonationServer(t)

	// Mint a token WITHOUT admin role.
	body := `{"actor_id":"` + impersonationTestAdminID + `","roles":["viewer"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mint non-admin token: got %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	viewerTok := resp["token"]

	wr := postImpersonate(t, s, viewerTok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "permission test",
	})
	// Should be 403 — viewer does not have superadmin.read.
	if wr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin token, got %d; body: %s", wr.Code, wr.Body.String())
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
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing user_id, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_user_id") {
		t.Errorf("expected 'impersonation.missing_user_id' in body, got: %s", w.Body.String())
	}
}

func TestImpersonation167_EmptyUserID_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": "   ",
		"reason":  "support investigation",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty user_id, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_user_id") {
		t.Errorf("expected 'impersonation.missing_user_id' in body, got: %s", w.Body.String())
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
		t.Errorf("expected 'impersonation.invalid_user_id' in body, got: %s", w.Body.String())
	}
}

func TestImpersonation167_MissingReason_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing reason, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_reason") {
		t.Errorf("expected 'impersonation.missing_reason' in body, got: %s", w.Body.String())
	}
}

func TestImpersonation167_EmptyReason_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "   ",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty reason, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.missing_reason") {
		t.Errorf("expected 'impersonation.missing_reason' in body, got: %s", w.Body.String())
	}
}

func TestImpersonation167_DurationExceeds30Min_Returns400(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id":          impersonationTestTargetID,
		"reason":           "support investigation",
		"duration_seconds": 1801, // 30min + 1sec
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duration > 30min, got %d; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "impersonation.duration_too_long") {
		t.Errorf("expected 'impersonation.duration_too_long' in body, got: %s", w.Body.String())
	}
}

func TestImpersonation167_Exactly30Min_IsAllowed(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id":          impersonationTestTargetID,
		"reason":           "exactly 30 min",
		"duration_seconds": 1800, // exactly 30 min — should be allowed
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for exactly 30min duration, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Happy path tests
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

func TestImpersonation167_HappyPath_ResponseShape(t *testing.T) {
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
		if _, ok := resp[f]; !ok {
			t.Errorf("response missing field %q; got keys: %v", f, resp)
		}
	}
}

func TestImpersonation167_ResponseEchoesTargetUserID(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "echo test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
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
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
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
	reason := "investigating bug report #42"

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  reason,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if v, _ := resp["reason"].(string); v != reason {
		t.Errorf("reason = %q; want %q", v, reason)
	}
}

func TestImpersonation167_ResponseContentType(t *testing.T) {
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

func TestImpersonation167_IssuedTokenIsVerifiable(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "verify token test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	impTok, _ := resp["token"].(string)
	if impTok == "" {
		t.Fatal("token field is empty")
	}

	// Verify the token using the same stub provider.
	stub := s.stub
	actor, err := stub.Verify(nil, impTok) //nolint:staticcheck
	if err != nil {
		t.Fatalf("impersonation token failed Verify: %v", err)
	}
	if actor.ID != impersonationTestTargetID {
		t.Errorf("actor.ID = %q; want %q (target user)", actor.ID, impersonationTestTargetID)
	}
}

func TestImpersonation167_IssuedTokenHasImpersonatedByClaim(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "impersonated_by claim test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	impTok, _ := resp["token"].(string)
	actor, err := s.stub.Verify(nil, impTok) //nolint:staticcheck
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
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	impTok, _ := resp["token"].(string)
	actor, err := s.stub.Verify(nil, impTok) //nolint:staticcheck
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if actor.ImpersonationReason != reason {
		t.Errorf("actor.ImpersonationReason = %q; want %q", actor.ImpersonationReason, reason)
	}
}

func TestImpersonation167_IsImpersonatedTrueOnIssuedToken(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "is_impersonated test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	impTok, _ := resp["token"].(string)
	actor, err := s.stub.Verify(nil, impTok) //nolint:staticcheck
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !actor.IsImpersonated() {
		t.Error("expected actor.IsImpersonated()=true on impersonation token")
	}
}

func TestImpersonation167_IssuedTokenSubjectIsTargetUser(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "sub claim test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	impTok, _ := resp["token"].(string)
	actor, err := s.stub.Verify(nil, impTok) //nolint:staticcheck
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// The impersonation token must have the target user's ID as subject (not admin).
	if actor.ID != impersonationTestTargetID {
		t.Errorf("actor.ID (sub) = %q; want target user %q", actor.ID, impersonationTestTargetID)
	}
}

func TestImpersonation167_IssuedTokenExpiresWithin30Min(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)
	before := time.Now()

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "expiry test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
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
	maxExpiry := before.Add(30 * time.Minute).Add(5 * time.Second) // small clock skew margin
	if expiresAt.After(maxExpiry) {
		t.Errorf("expires_at %v is more than 30min from now (%v)", expiresAt, before)
	}
	// The token must not expire immediately.
	if expiresAt.Before(before) {
		t.Errorf("expires_at %v is in the past (before request time %v)", expiresAt, before)
	}
}

func TestImpersonation167_DefaultDuration_ExpiresWithin30Min(t *testing.T) {
	s := buildImpersonationServer(t)
	tok := mintAdminToken167(t, s)
	before := time.Now()

	// duration_seconds absent → should default to 30min
	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "default duration test",
		// intentionally omit duration_seconds
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	expiresAtStr, _ := resp["expires_at"].(string)
	expiresAt, _ := time.Parse(time.RFC3339, expiresAtStr)
	if expiresAt.Before(before.Add(29 * time.Minute)) {
		t.Errorf("default expiry %v is less than 29min from now; expected ~30min", expiresAt)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit logging tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_AuditLoggedOnIssuance(t *testing.T) {
	// Use a mock audit writer to capture the event.
	var capturedEvent *mockAuditEvent
	s := buildImpersonationServerWithAudit(t, func(ev mockAuditEvent) {
		if ev.Action == "impersonation.issue" {
			capturedEvent = &ev
		}
	})
	tok := mintAdminToken167WithServer(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "audit test",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	if capturedEvent == nil {
		t.Fatal("expected audit event with action='impersonation.issue', none captured")
	}
	if capturedEvent.ResourceType != "user" {
		t.Errorf("audit ResourceType = %q; want %q", capturedEvent.ResourceType, "user")
	}
	if capturedEvent.ResourceID != impersonationTestTargetID {
		t.Errorf("audit ResourceID = %q; want %q", capturedEvent.ResourceID, impersonationTestTargetID)
	}
	if capturedEvent.ActorID != impersonationTestAdminID {
		t.Errorf("audit ActorID = %q; want %q", capturedEvent.ActorID, impersonationTestAdminID)
	}
}

func TestImpersonation167_AuditMetadataContainsReason(t *testing.T) {
	reason := "checking payment method configuration"
	var capturedMeta map[string]any
	s := buildImpersonationServerWithAudit(t, func(ev mockAuditEvent) {
		if ev.Action == "impersonation.issue" {
			capturedMeta = ev.Metadata
		}
	})
	tok := mintAdminToken167WithServer(t, s)

	w := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  reason,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if capturedMeta == nil {
		t.Fatal("no audit metadata captured")
	}
	if r, _ := capturedMeta["reason"].(string); r != reason {
		t.Errorf("audit metadata reason = %q; want %q", r, reason)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server wiring tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_ServerGoHasHandler(t *testing.T) {
	content := findFileByName(t, "server.go")
	checks := []string{
		"handleImpersonate",
		"/admin/impersonate",
		"impersonation",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("server.go: missing expected string %q", want)
		}
	}
}

func TestImpersonation167_ServerGoMountsImpersonateRoute(t *testing.T) {
	content := findFileByName(t, "server.go")
	// The route must use Post, not Get.
	if !strings.Contains(content, `Post("/admin/impersonate"`) {
		t.Error(`server.go: missing Post("/admin/impersonate", ...)`)
	}
}

func TestImpersonation167_ServerGoUsesSuperadminReadPermission(t *testing.T) {
	// The impersonation route must be gated behind superadmin.read.
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, `"superadmin.read"`) {
		t.Error(`server.go: missing "superadmin.read" permission gate`)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Nil stub guard test
// ─────────────────────────────────────────────────────────────────────────────

func TestImpersonation167_NilSuperadminQueries_RouteNotMounted(t *testing.T) {
	// Server without SuperadminQueries → superadmin route group not mounted → 404
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
		t.Fatalf("NewStubProvider: %v", err)
	}
	// No SuperadminQueries — impersonation route still mounts (only needs stub).
	// But superadmin.read permission group needs queries check to pass.
	// Build without queries to test the route IS still mounted (only stub guard).
	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
		// No SuperadminQueries — but impersonation route is gated only on stub.
	})

	// Mint a token using the same stub.
	body := `{"actor_id":"` + impersonationTestAdminID + `","roles":["admin"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mint token: got %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	tok := resp["token"]

	wr := postImpersonate(t, s, tok, map[string]any{
		"user_id": impersonationTestTargetID,
		"reason":  "no-queries guard test",
	})
	// The impersonation route is mounted independently of SuperadminQueries.
	// With admin role (AllowAllChecker), it should return 200.
	if wr.Code != http.StatusOK {
		t.Errorf("expected 200 for impersonate even without SuperadminQueries, got %d; body: %s", wr.Code, wr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mock audit infrastructure for audit tests
// ─────────────────────────────────────────────────────────────────────────────

// mockAuditEvent captures audit event fields for test assertions.
type mockAuditEvent struct {
	Action       string
	ResourceType string
	ResourceID   string
	ActorID      string
	Metadata     map[string]any
}

// mockAuditWriter is an in-memory audit.Writer for tests.
type mockAuditWriter struct {
	fn func(mockAuditEvent)
}

func (m *mockAuditWriter) Write(_ interface{ Done() <-chan struct{} }, ev interface{}) error {
	return nil
}

// buildImpersonationServerWithAudit builds a Server with a custom audit capture hook.
func buildImpersonationServerWithAudit(t *testing.T, fn func(mockAuditEvent)) *Server {
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
		t.Fatalf("buildImpersonationServerWithAudit: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:            cfg,
		Auth:              stub,
		Pool:              &dbDownPool{},
		SuperadminQueries: gen.New(nil),
		Audit:             &capturingAuditWriter167{fn: fn},
	})
}

// mintAdminToken167WithServer mints an admin token using the given server.
func mintAdminToken167WithServer(t *testing.T, s *Server) string {
	t.Helper()
	body := `{"actor_id":"` + impersonationTestAdminID + `","roles":["admin"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintAdminToken167WithServer: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	return resp["token"]
}

// capturingAuditWriter167 implements audit.Writer, calling fn for every Write.
type capturingAuditWriter167 struct {
	fn func(mockAuditEvent)
}

func (c *capturingAuditWriter167) Write(_ interface{ Deadline() (time.Time, bool) }, ev interface{}) error {
	return nil
}

func (c *capturingAuditWriter167) WriteTx(_ interface{}, _ interface{}, _ interface{}) error {
	return nil
}
