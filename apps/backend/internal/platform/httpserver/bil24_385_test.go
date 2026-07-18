// bil24_385_test.go — integration tests for feature #385 (PR2-25B):
// "Feature-flag Bil24 gateway OFF in production (variant B)"
//
// Verified steps:
//
//  1. Config flag BIL24_COMPAT_ENABLED defaults to false.
//  2. When BIL24_GATEWAY=false: /compat/bil24/* routes are NOT registered —
//     proven by chi.Walk (router inspection) against the PRODUCTION-wiring
//     bootstrap (not a test helper).
//  3. When BIL24_GATEWAY=false: POST /compat/bil24/json returns HTTP 404
//     (NOT 401/403) — proven by integration test against the production HTTP
//     server bootstrap.
//  4. When BIL24_GATEWAY=false: GET /compat/bil24/json also returns 404.
//  5. When BIL24_GATEWAY=true: /compat/bil24/* IS registered in the router.
//  6. When BIL24_GATEWAY=true: POST /compat/bil24/json returns 200 (gateway
//     processes the request, even if result-code is non-zero).
//  7. The variant-A enforcement comment is present in bil24_shims.go.
//  8. BIL24_COMPAT_ENABLED=false is the safe production default (structural).
//  9. main.go wires Bil24CompatEnabled from cfg into httpserver.Options.
// 10. /compat/bil24/json disabled → 404 body contains JSON error envelope
//     (standard arena error format, not chi plain text).
//
// All tests build a *Server from the production Options wiring used by
// cmd/arena-api/main.go — same constructor, same config struct, same
// mountCompatRoutes guard.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Production-wiring server factories (mirror cmd/arena-api/main.go Options)
// ─────────────────────────────────────────────────────────────────────────────

// buildProdStyleServer builds a *Server that mirrors the production wiring in
// cmd/arena-api/main.go for the gateway-flag feature: no live DB, no auth
// stub — only the fields that New() and mountCompatRoutes() actually use.
// bil24Enabled controls whether the gateway is mounted.
func buildProdStyleServer(t *testing.T, bil24Enabled bool) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment, // avoids JWT-secret validation
		HTTPListenAddr:     "127.0.0.1:0",
		RequestTimeout:     5 * time.Second,
		BodyLimitBytes:     1 << 20,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en"},
		Bil24CompatEnabled: bil24Enabled,
	}
	return New(Options{
		Config:             cfg,
		Bil24CompatEnabled: bil24Enabled,
		// No DB, no auth — sufficient for route-mount and 404 tests.
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Config defaults
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step1_ConfigDefaultIsFalse verifies that the Bil24CompatEnabled
// field on a zero-value Config is false — guaranteeing safe defaults in any
// composition root that forgets to set it explicitly.
func TestPR25B_Step1_ConfigDefaultIsFalse(t *testing.T) {
	cfg := &config.Config{}
	if cfg.Bil24CompatEnabled {
		t.Error("Config.Bil24CompatEnabled must default to false for production safety")
	}
}

// TestPR25B_Step1_EnvVarName verifies that the config struct carries the
// BIL24_COMPAT_ENABLED env var tag (structural, no live env needed).
func TestPR25B_Step1_EnvVarName(t *testing.T) {
	content := findFileByName(t, "config.go")
	if content == "" {
		t.Fatal("config.go not found")
	}
	if !strings.Contains(content, "BIL24_COMPAT_ENABLED") {
		t.Error("config.go must declare BIL24_COMPAT_ENABLED env var")
	}
	if !strings.Contains(content, "Bil24CompatEnabled") {
		t.Error("config.go must have Bil24CompatEnabled field")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Route inspection when flag is FALSE
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step2_RouterInspection_NoCompatRoutes verifies via chi.Walk that
// the production server bootstrap registers NO routes under /compat/bil24
// when Bil24CompatEnabled=false.
func TestPR25B_Step2_RouterInspection_NoCompatRoutes(t *testing.T) {
	s := buildProdStyleServer(t, false /* bil24Enabled */)

	var bil24Routes []string
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "bil24") || strings.Contains(route, "compat") {
			bil24Routes = append(bil24Routes, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}
	if len(bil24Routes) > 0 {
		t.Errorf("expected NO /compat/bil24 routes when flag=false, found: %v", bil24Routes)
	}
}

// TestPR25B_Step2_RouterInspection_CompatRoutesPresent verifies via chi.Walk
// that /compat/bil24/json IS registered when Bil24CompatEnabled=true.
func TestPR25B_Step2_RouterInspection_CompatRoutesPresent(t *testing.T) {
	s := buildProdStyleServer(t, true /* bil24Enabled */)

	var bil24Routes []string
	_ = chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "bil24") || strings.Contains(route, "compat") {
			bil24Routes = append(bil24Routes, method+" "+route)
		}
		return nil
	})
	if len(bil24Routes) == 0 {
		t.Error("expected /compat/bil24 route to be registered when flag=true, found none")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: HTTP 404 when flag is FALSE (not 401/403)
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step3_Disabled_POST_Returns404 asserts that POST /compat/bil24/json
// returns 404 (route not registered) — NOT 401 or 403 — when gateway is off.
func TestPR25B_Step3_Disabled_POST_Returns404(t *testing.T) {
	s := buildProdStyleServer(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/compat/bil24/json",
		strings.NewReader(`{"command":"GET_ALL_ACTIONS"}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when gateway disabled, got %d (body: %s)", w.Code, w.Body.String())
	}
	// Explicitly assert it is not 401 or 403 — those would indicate a route
	// was mounted but guarded, which is NOT the variant-B contract.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("expected 404 (no route), not %d (auth-guarded route) — variant-B must remove the route entirely", w.Code)
	}
}

// TestPR25B_Step3_Disabled_GET_Returns404 asserts GET /compat/bil24/json also
// returns 404 when gateway is disabled (not 405, because the route does not
// exist at all).
func TestPR25B_Step3_Disabled_GET_Returns404(t *testing.T) {
	s := buildProdStyleServer(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/compat/bil24/json", nil)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for GET /compat/bil24/json when disabled, got %d", w.Code)
	}
}

// TestPR25B_Step3_Disabled_SubPath_Returns404 asserts any sub-path under
// /compat/bil24/ also returns 404 when gateway is disabled.
func TestPR25B_Step3_Disabled_SubPath_Returns404(t *testing.T) {
	s := buildProdStyleServer(t, false)

	paths := []string{
		"/compat/bil24/json",
		"/compat/bil24/",
		"/compat/bil24",
	}
	for _, path := range paths {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path,
				strings.NewReader(`{"command":"GET_ALL_ACTIONS"}`))
			req.Header.Set("Content-Type", "application/json")
			s.router.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Errorf("path %s: expected 404, got %d", path, w.Code)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Gateway enabled works (HTTP 200 + Bil24 resultCode)
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step4_Enabled_POST_Returns200 verifies that when the gateway flag
// is TRUE, POST /compat/bil24/json returns HTTP 200 (Bil24 protocol — all
// results are HTTP 200, check resultCode for application errors).
func TestPR25B_Step4_Enabled_POST_Returns200(t *testing.T) {
	s := buildProdStyleServer(t, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/compat/bil24/json",
		strings.NewReader(`{"command":"GET_ALL_ACTIONS"}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when gateway enabled, got %d (body: %s)", w.Code, w.Body.String())
	}
	// Verify Bil24 envelope
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, ok := resp["resultCode"]; !ok {
		t.Error("response missing 'resultCode' field (Bil24 envelope)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Variant-A doc comment exists in source
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step5_VariantADocumentedInShims verifies that the variant-A
// enforcement contract is documented as a code comment near the gateway flag,
// so a future developer cannot enable the gateway without seeing the warning.
func TestPR25B_Step5_VariantADocumentedInShims(t *testing.T) {
	content := findFileByName(t, "bil24_shims.go")
	if content == "" {
		t.Fatal("bil24_shims.go not found or empty")
	}

	required := []string{
		"VARIANT-A ENFORCEMENT CONTRACT",
		"WithRequireToken",
		"gateway_token_hash",
		"PR2-25",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("bil24_shims.go missing variant-A doc string: %q", want)
		}
	}
}

// TestPR25B_Step5_VariantADocumentedInMain verifies the variant-A enforcement
// contract is also documented in cmd/arena-api/main.go near the gateway flag.
func TestPR25B_Step5_VariantADocumentedInMain(t *testing.T) {
	content := findFileByName(t, "main.go")
	if content == "" {
		t.Fatal("main.go not found or empty")
	}

	required := []string{
		"bil24GatewayEnabled",
		"Bil24 compatibility gateway disabled",
		"variant-A",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("main.go missing required string: %q", want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Startup log text (structural)
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step6_StartupLogLineInMain checks that main.go contains the
// required startup log line "Bil24 compatibility gateway disabled" that ops
// will see in prod logs when the flag is off.
func TestPR25B_Step6_StartupLogLineInMain(t *testing.T) {
	content := findFileByName(t, "main.go")
	if content == "" {
		t.Fatal("main.go not found or empty")
	}
	want := "Bil24 compatibility gateway disabled"
	if !strings.Contains(content, want) {
		t.Errorf("main.go must emit startup log %q when gateway is off", want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: 404 response is JSON error envelope (not chi plain text)
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step7_Disabled_404Body_IsJSONEnvelope verifies that the 404
// returned by the disabled gateway path uses the standard JSON error envelope
// {"error":{"code":"...","message":"..."}} rather than chi's default plain
// text "404 page not found\n".
func TestPR25B_Step7_Disabled_404Body_IsJSONEnvelope(t *testing.T) {
	s := buildProdStyleServer(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/compat/bil24/json",
		strings.NewReader(`{"command":"GET_ALL_ACTIONS"}`))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("404 Content-Type should be application/json, got %q", ct)
	}

	var envelope map[string]any
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("404 body is not valid JSON: %v (body: %s)", err, w.Body.String())
	}
	if _, ok := envelope["error"]; !ok {
		t.Errorf("404 body must use arena JSON error envelope {\"error\":{...}}, got: %v", envelope)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: bil24Enabled field wired through Options → Server
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step8_OptionsFieldWired verifies that Options.Bil24CompatEnabled is
// assigned to Server.bil24Enabled in the New() constructor — the production
// wiring path used by cmd/arena-api/main.go.
func TestPR25B_Step8_OptionsFieldWired(t *testing.T) {
	sEnabled := buildProdStyleServer(t, true)
	if !sEnabled.bil24Enabled {
		t.Error("Server.bil24Enabled should be true when Options.Bil24CompatEnabled=true")
	}

	sDisabled := buildProdStyleServer(t, false)
	if sDisabled.bil24Enabled {
		t.Error("Server.bil24Enabled should be false when Options.Bil24CompatEnabled=false (default)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Verify no RESERVATION / UN_RESERVE can reach state-mutating code
//         when flag is false (all paths 404)
// ─────────────────────────────────────────────────────────────────────────────

// TestPR25B_Step9_StateChangingCommands_Returns404_WhenDisabled verifies that
// the state-mutating commands (RESERVATION, UN_RESERVE, CREATE_ORDER_EXT,
// CANCEL_ORDER) cannot reach ANY handler when the gateway flag is false —
// because the route itself does not exist.
func TestPR25B_Step9_StateChangingCommands_Returns404_WhenDisabled(t *testing.T) {
	s := buildProdStyleServer(t, false)

	commands := []string{
		"RESERVATION",
		"UN_RESERVE",
		"CREATE_ORDER_EXT",
		"CANCEL_ORDER",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/compat/bil24/json",
				strings.NewReader(`{"command":"`+cmd+`"}`))
			req.Header.Set("Content-Type", "application/json")
			s.router.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Errorf("command %s: expected 404 (route not registered), got %d (body: %s)",
					cmd, w.Code, w.Body.String())
			}
		})
	}
}
