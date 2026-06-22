// devauth_test.go covers feature #96 step 4: POST /v1/dev/auth/token
// (dev JWT issuer using jwt/v5, blocked in production).
//
// Step 6 (README PLACEHOLDER marker) is verified here via a source file scan.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// buildDevAuthTestServer creates a minimal httpserver.Server with stub auth
// enabled, suitable for testing /v1/dev/auth/token.
func buildDevAuthTestServer(t *testing.T) *Server {
	t.Helper()

	cfg := &config.Config{
		RequestTimeout: 5e9, // 5 seconds
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

	return New(Options{
		Config: cfg,
		Auth:   stub,
	})
}

// ---------------------------------------------------------------------------
// POST /v1/dev/auth/token — basic issuance (step 4)
// ---------------------------------------------------------------------------

func TestDevAuthToken_Returns200WithToken(t *testing.T) {
	srv := buildDevAuthTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/auth/token", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	tok, _ := resp["token"].(string)
	if tok == "" {
		t.Fatalf("response missing or empty 'token' field; body=%v", resp)
	}
	// Token must be a 3-segment JWT.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3-segment JWT; got %d parts in %q", len(parts), tok)
	}
}

func TestDevAuthToken_ResponseHasExpectedFields(t *testing.T) {
	srv := buildDevAuthTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/auth/token", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	for _, field := range []string{"token", "expires_at", "actor_id", "issuer", "audience"} {
		v, _ := resp[field].(string)
		if v == "" {
			t.Errorf("response missing or empty field %q; body=%v", field, resp)
		}
	}
}

func TestDevAuthToken_DefaultActorID(t *testing.T) {
	srv := buildDevAuthTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/auth/token", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	actorID, _ := resp["actor_id"].(string)
	if actorID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("default actor_id: got %q", actorID)
	}
}

func TestDevAuthToken_IssuedTokenVerifiableByValidateJWT(t *testing.T) {
	srv := buildDevAuthTestServer(t)

	// Step 1: mint a token via /v1/dev/auth/token
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/auth/token", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("mint status: got %d want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	tok, _ := resp["token"].(string)
	if tok == "" {
		t.Fatal("token is empty")
	}

	// Step 2: verify via ValidateJWT middleware with the same secret.
	verifyReq := httptest.NewRequest(http.MethodGet, "/protected", nil)
	verifyReq.Header.Set("Authorization", "Bearer "+tok)
	verifyRR := httptest.NewRecorder()

	called := false
	auth.ValidateJWT("test-secret-which-is-long-enough-for-hs256")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if _, ok := auth.FromContext(r.Context()); !ok {
				t.Error("AuthContext not present on context after ValidateJWT")
			}
			w.WriteHeader(http.StatusOK)
		}),
	).ServeHTTP(verifyRR, verifyReq)

	if !called {
		t.Fatalf("ValidateJWT must call the handler for a token issued by /v1/dev/auth/token; status=%d body=%s",
			verifyRR.Code, verifyRR.Body.String())
	}
}

func TestDevAuthToken_RouteExistsOnlyWhenStubEnabled(t *testing.T) {
	// Build a server WITHOUT stub auth — the route must return 404.
	cfg := &config.Config{
		RequestTimeout: 5e9,
		BodyLimitBytes: 1 << 20,
		EnableStubAuth: false,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	srvNoAuth := New(Options{Config: cfg})

	req := httptest.NewRequest(http.MethodPost, "/v1/dev/auth/token", nil)
	rr := httptest.NewRecorder()
	srvNoAuth.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when stub auth disabled; got %d body=%s",
			rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Step 6 — README documents auth as PLACEHOLDER
// ---------------------------------------------------------------------------

func TestDevAuth_READMEDocumentsPlaceholder(t *testing.T) {
	// Walk up from this source file to the repo root and open README.md.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate README")
	}
	// thisFile: .../apps/backend/internal/platform/httpserver/devauth_test.go
	// repo root is 6 directories up.
	repoRoot := thisFile
	for i := 0; i < 6; i++ {
		repoRoot = filepath.Dir(repoRoot)
	}
	readmePath := filepath.Join(repoRoot, "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("cannot read README.md at %s: %v", readmePath, err)
	}
	content := string(data)
	// The README must mention that the auth boundary is a PLACEHOLDER.
	if !strings.Contains(strings.ToUpper(content), "PLACEHOLDER") {
		t.Fatalf("README.md must contain the word PLACEHOLDER to document that the auth boundary is a foundation-milestone stub; path=%s", readmePath)
	}
}
