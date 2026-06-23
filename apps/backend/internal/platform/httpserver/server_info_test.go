// server_info_test.go verifies feature #104:
// "Example read endpoint GET /v1/server-info"
//
// All 5 feature steps are exercised entirely with in-process httptest helpers.
// No external PostgreSQL connection is required — the siQueries field is left
// nil so the handler falls back to the injected clock for server_time.
//
// Steps verified:
//  1. GET /v1/server-info described in openapi.yaml (static inspection of spec file)
//  2. Handler implemented via *Server method (compile-time: method exists + is mounted)
//  3. platform/clock drives server_time; readBuildSHA reads runtime/debug
//  4. welcome_message is locale-aware (Accept-Language: ru → Russian message)
//  5. GET returns 200, all required fields present; Accept-Language: ru returns
//     Russian welcome_message (i18n integration via Bundle)
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
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// =============================================================================
// Helpers
// =============================================================================

// serverInfoTestConfig builds the minimum *config.Config for server-info tests.
func serverInfoTestConfig() *config.Config {
	return &config.Config{
		AppEnv:         config.EnvDevelopment,
		AppName:        "arena-api-test",
		AppVersion:     "1.2.3-test",
		AppCommit:      "abc123",
		HTTPListenAddr: "127.0.0.1:0",
		BodyLimitBytes: 1 << 20,
		RequestTimeout: 5 * time.Second,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
		LogLevel:       "info",
		LogFormat:      "json",
	}
}

// buildServerInfoServer constructs a minimal Server with the i18n Bundle wired
// so that locale-resolution works for welcome_message tests.
func buildServerInfoServer(t *testing.T, fakeClock clock.Clock) *Server {
	t.Helper()
	cfg := serverInfoTestConfig()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	opts := Options{
		Config: cfg,
		Bundle: bundle,
		Clock:  fakeClock,
	}
	return New(opts)
}

// doServerInfoRequest issues GET /v1/server-info with the given Accept-Language
// header and returns the recorded response.
func doServerInfoRequest(srv *Server, acceptLang string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/server-info", nil)
	if acceptLang != "" {
		req.Header.Set("Accept-Language", acceptLang)
	}
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	return rr
}

// decodeServerInfoBody JSON-decodes the response body into a map so tests can
// assert individual fields without depending on the exact struct type.
func decodeServerInfoBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode server-info response: %v (body=%q)", err, rr.Body.String())
	}
	return body
}

// =============================================================================
// Step 1: openapi.yaml describes GET /v1/server-info
// =============================================================================

// TestServerInfo104_OpenAPIHasPath verifies that openapi.yaml contains the
// /v1/server-info path described in feature step 1.
func TestServerInfo104_OpenAPIHasPath(t *testing.T) {
	data := readOpenAPISpec(t)
	if !strings.Contains(data, "/v1/server-info") {
		t.Error("openapi.yaml: missing /v1/server-info path")
	}
}

// TestServerInfo104_OpenAPIHasServerInfoResponse verifies the ServerInfoResponse
// schema is present in openapi.yaml components/schemas.
func TestServerInfo104_OpenAPIHasServerInfoResponse(t *testing.T) {
	data := readOpenAPISpec(t)
	if !strings.Contains(data, "ServerInfoResponse") {
		t.Error("openapi.yaml: missing ServerInfoResponse schema")
	}
}

// TestServerInfo104_OpenAPIRequiredFields verifies that the schema declares the
// expected required fields: version, build_sha, server_time, environment,
// locales, welcome_message.
func TestServerInfo104_OpenAPIRequiredFields(t *testing.T) {
	data := readOpenAPISpec(t)
	for _, field := range []string{"version", "build_sha", "server_time", "environment", "locales", "welcome_message"} {
		if !strings.Contains(data, field) {
			t.Errorf("openapi.yaml: missing required field %q in ServerInfoResponse schema", field)
		}
	}
}

// readOpenAPISpec loads the openapi.yaml file by walking upward from the test
// file's directory until the repo root is found (identified by go.mod), then
// returning the contents as a string.
func readOpenAPISpec(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	// Walk up to repo root (contains go.mod)
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, "apps", "backend", "openapi", "openapi.yaml")
		if _, err := os.Stat(candidate); err == nil {
			b, err := os.ReadFile(candidate)
			if err != nil {
				t.Fatalf("read openapi.yaml: %v", err)
			}
			return string(b)
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("openapi.yaml not found — searched 10 levels up from test file")
	return ""
}

// =============================================================================
// Step 2: handler implemented via server interface (compile-time + route mount)
// =============================================================================

// TestServerInfo104_RouteIsMounted verifies that GET /v1/server-info is
// mounted and returns 200 (not 404).
func TestServerInfo104_RouteIsMounted(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	if rr.Code != http.StatusOK {
		t.Errorf("GET /v1/server-info: want 200, got %d (body=%q)", rr.Code, rr.Body.String())
	}
}

// TestServerInfo104_ContentTypeJSON verifies the response has the standard
// Content-Type: application/json; charset=utf-8 header.
func TestServerInfo104_ContentTypeJSON(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("want Content-Type application/json; got %q", ct)
	}
}

// TestServerInfo104_NoAuthRequired verifies that GET /v1/server-info succeeds
// without any Authorization header (public endpoint).
func TestServerInfo104_NoAuthRequired(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	req := httptest.NewRequest(http.MethodGet, "/v1/server-info", nil)
	// Explicitly ensure no Authorization header is set.
	req.Header.Del("Authorization")
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("anonymous GET /v1/server-info: want 200, got %d", rr.Code)
	}
}

// =============================================================================
// Step 3: platform/clock for server_time; runtime/debug.ReadBuildInfo for build_sha
// =============================================================================

// TestServerInfo104_ServerTimeFromClock verifies that when siQueries is nil the
// server_time in the response reflects the injected FakeClock.
func TestServerInfo104_ServerTimeFromClock(t *testing.T) {
	fixedTime := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	srv := buildServerInfoServer(t, clock.NewFake(fixedTime))

	rr := doServerInfoRequest(srv, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := decodeServerInfoBody(t, rr)

	serverTimeRaw, ok := body["server_time"].(string)
	if !ok || serverTimeRaw == "" {
		t.Fatalf("server_time missing or not a string: %v", body["server_time"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, serverTimeRaw)
	if err != nil {
		t.Fatalf("server_time not RFC3339Nano: %q, err=%v", serverTimeRaw, err)
	}
	if !parsed.Equal(fixedTime) {
		t.Errorf("server_time: want %v, got %v", fixedTime, parsed)
	}
}

// TestServerInfo104_ServerTimeIsUTC verifies that server_time is always UTC.
func TestServerInfo104_ServerTimeIsUTC(t *testing.T) {
	// Supply a time in a non-UTC zone; handler must normalise to UTC.
	loc, _ := time.LoadLocation("America/New_York")
	localTime := time.Date(2026, 6, 22, 8, 0, 0, 0, loc)
	srv := buildServerInfoServer(t, clock.NewFake(localTime))

	rr := doServerInfoRequest(srv, "")
	body := decodeServerInfoBody(t, rr)

	serverTimeRaw, _ := body["server_time"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, serverTimeRaw)
	if err != nil {
		t.Fatalf("parse server_time: %v", err)
	}
	if parsed.Location() != time.UTC {
		t.Errorf("server_time must be UTC, got zone=%v", parsed.Location())
	}
}

// TestServerInfo104_BuildShaField verifies that build_sha is present in the
// response and is a non-empty string. The actual value is "dev" in unit tests
// (no real VCS binary embedded) or a hex SHA in a real build.
func TestServerInfo104_BuildShaField(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	body := decodeServerInfoBody(t, rr)

	sha, ok := body["build_sha"].(string)
	if !ok || sha == "" {
		t.Errorf("build_sha missing or empty: %v", body["build_sha"])
	}
	// In unit tests there is no embedded VCS info, so "dev" is the expected fallback.
	t.Logf("build_sha = %q", sha)
}

// TestServerInfo104_ReadBuildSHA_ReturnsNonEmpty verifies that readBuildSHA
// always returns a non-empty string (either a real SHA or the "dev" sentinel).
func TestServerInfo104_ReadBuildSHA_ReturnsNonEmpty(t *testing.T) {
	sha := readBuildSHA()
	if sha == "" {
		t.Error("readBuildSHA returned empty string; want 'dev' or a git SHA")
	}
	t.Logf("readBuildSHA = %q", sha)
}

// =============================================================================
// Step 4: locale-dependent welcome_message via i18n
// =============================================================================

// TestServerInfo104_WelcomeMessageEnglish verifies that the default locale
// (no Accept-Language header) returns the English welcome_message.
func TestServerInfo104_WelcomeMessageEnglish(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "") // no Accept-Language → default "en"
	body := decodeServerInfoBody(t, rr)

	msg, ok := body["welcome_message"].(string)
	if !ok || msg == "" {
		t.Fatalf("welcome_message missing or empty: %v", body["welcome_message"])
	}
	// English catalog has "Welcome to Arena Platform!"
	if !strings.Contains(strings.ToLower(msg), "welcome") {
		t.Errorf("English welcome_message should contain 'welcome', got %q", msg)
	}
	t.Logf("English welcome_message = %q", msg)
}

// TestServerInfo104_WelcomeMessageRussian verifies that Accept-Language: ru
// returns the Russian welcome_message (feature step 4 + step 5).
func TestServerInfo104_WelcomeMessageRussian(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "ru")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rr.Code, rr.Body.String())
	}
	body := decodeServerInfoBody(t, rr)

	msg, ok := body["welcome_message"].(string)
	if !ok || msg == "" {
		t.Fatalf("welcome_message missing or empty: %v", body["welcome_message"])
	}
	// Russian catalog has "Добро пожаловать на платформу Arena!"
	// Check for a distinctive Cyrillic character or the known phrase.
	if !strings.Contains(msg, "Добро пожаловать") {
		t.Errorf("Russian welcome_message should contain 'Добро пожаловать', got %q", msg)
	}
	t.Logf("Russian welcome_message = %q", msg)
}

// TestServerInfo104_WelcomeMessageRussianWithQuality verifies that a full
// Accept-Language header with quality factors (e.g. "ru-RU,ru;q=0.9,en;q=0.8")
// is correctly negotiated to Russian.
func TestServerInfo104_WelcomeMessageRussianWithQuality(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "ru-RU,ru;q=0.9,en;q=0.8")
	body := decodeServerInfoBody(t, rr)

	msg, _ := body["welcome_message"].(string)
	if !strings.Contains(msg, "Добро пожаловать") {
		t.Errorf("quality-weighted ru Accept-Language: want Russian message, got %q", msg)
	}
}

// TestServerInfo104_LangQueryParamOverridesAcceptLanguage verifies that
// ?lang=ru takes priority over Accept-Language: en (per i18n spec).
func TestServerInfo104_LangQueryParamOverridesAcceptLanguage(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	req := httptest.NewRequest(http.MethodGet, "/v1/server-info?lang=ru", nil)
	req.Header.Set("Accept-Language", "en") // should be overridden by ?lang=ru
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	body := decodeServerInfoBody(t, rr)

	msg, _ := body["welcome_message"].(string)
	if !strings.Contains(msg, "Добро пожаловать") {
		t.Errorf("?lang=ru should produce Russian message, got %q", msg)
	}
}

// =============================================================================
// Step 5: GET returns 200, all fields present; Accept-Language: ru → Russian
// =============================================================================

// TestServerInfo104_AllRequiredFieldsPresent verifies that the 200 response
// body contains all six required fields from the spec.
func TestServerInfo104_AllRequiredFieldsPresent(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := decodeServerInfoBody(t, rr)

	required := []string{"version", "build_sha", "server_time", "environment", "locales", "welcome_message"}
	for _, field := range required {
		if _, ok := body[field]; !ok {
			t.Errorf("required field %q missing from response body", field)
		}
	}
}

// TestServerInfo104_VersionField verifies the version field reflects cfg.AppVersion.
func TestServerInfo104_VersionField(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	body := decodeServerInfoBody(t, rr)

	version, ok := body["version"].(string)
	if !ok || version == "" {
		t.Fatalf("version missing or empty: %v", body["version"])
	}
	// serverInfoTestConfig sets AppVersion = "1.2.3-test"
	if version != "1.2.3-test" {
		t.Errorf("version: want %q, got %q", "1.2.3-test", version)
	}
}

// TestServerInfo104_EnvironmentField verifies the environment field reflects cfg.AppEnv.
func TestServerInfo104_EnvironmentField(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	body := decodeServerInfoBody(t, rr)

	env, ok := body["environment"].(string)
	if !ok || env == "" {
		t.Fatalf("environment missing or empty: %v", body["environment"])
	}
	// serverInfoTestConfig sets AppEnv = config.EnvDevelopment = "development"
	if env != "development" {
		t.Errorf("environment: want %q, got %q", "development", env)
	}
}

// TestServerInfo104_LocalesField verifies the locales field is a non-empty array
// that includes "en" and "ru".
func TestServerInfo104_LocalesField(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	body := decodeServerInfoBody(t, rr)

	localesRaw, ok := body["locales"].([]interface{})
	if !ok || len(localesRaw) == 0 {
		t.Fatalf("locales missing or not an array: %v", body["locales"])
	}
	var locales []string
	for _, v := range localesRaw {
		if s, ok := v.(string); ok {
			locales = append(locales, s)
		}
	}
	hasEN, hasRU := false, false
	for _, l := range locales {
		if l == "en" {
			hasEN = true
		}
		if l == "ru" {
			hasRU = true
		}
	}
	if !hasEN {
		t.Error("locales array missing 'en'")
	}
	if !hasRU {
		t.Error("locales array missing 'ru'")
	}
}

// TestServerInfo104_ServerTimeRFC3339Nano verifies that server_time is a valid
// RFC3339Nano string.
func TestServerInfo104_ServerTimeRFC3339Nano(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	rr := doServerInfoRequest(srv, "")
	body := decodeServerInfoBody(t, rr)

	serverTimeRaw, ok := body["server_time"].(string)
	if !ok || serverTimeRaw == "" {
		t.Fatalf("server_time missing: %v", body["server_time"])
	}
	if _, err := time.Parse(time.RFC3339Nano, serverTimeRaw); err != nil {
		// Also try RFC3339 (seconds precision) since Go may omit trailing zeros.
		if _, err2 := time.Parse(time.RFC3339, serverTimeRaw); err2 != nil {
			t.Errorf("server_time is not RFC3339/RFC3339Nano: %q", serverTimeRaw)
		}
	}
}

// TestServerInfo104_FullVerification is a composite test that mirrors the five
// feature steps in a single end-to-end scenario.
func TestServerInfo104_FullVerification(t *testing.T) {
	fixedTime := time.Date(2026, 6, 23, 9, 30, 0, 0, time.UTC)
	srv := buildServerInfoServer(t, clock.NewFake(fixedTime))

	t.Run("Step1_OpenAPIPath", func(t *testing.T) {
		data := readOpenAPISpec(t)
		if !strings.Contains(data, "/v1/server-info") {
			t.Error("openapi.yaml missing /v1/server-info path")
		}
		if !strings.Contains(data, "ServerInfoResponse") {
			t.Error("openapi.yaml missing ServerInfoResponse schema")
		}
	})

	t.Run("Step2_HandlerMounted_Returns200", func(t *testing.T) {
		rr := doServerInfoRequest(srv, "")
		if rr.Code != http.StatusOK {
			t.Errorf("want 200, got %d", rr.Code)
		}
	})

	t.Run("Step3_ClockDrivesServerTime", func(t *testing.T) {
		rr := doServerInfoRequest(srv, "")
		body := decodeServerInfoBody(t, rr)
		serverTimeRaw, _ := body["server_time"].(string)
		parsed, err := time.Parse(time.RFC3339Nano, serverTimeRaw)
		if err != nil {
			t.Fatalf("server_time parse: %v", err)
		}
		if !parsed.Equal(fixedTime) {
			t.Errorf("server_time: want %v, got %v", fixedTime, parsed)
		}
		sha, _ := body["build_sha"].(string)
		if sha == "" {
			t.Error("build_sha must not be empty")
		}
	})

	t.Run("Step4_WelcomeMessageRussian", func(t *testing.T) {
		rr := doServerInfoRequest(srv, "ru")
		body := decodeServerInfoBody(t, rr)
		msg, _ := body["welcome_message"].(string)
		if !strings.Contains(msg, "Добро пожаловать") {
			t.Errorf("Accept-Language: ru → want Russian message, got %q", msg)
		}
	})

	t.Run("Step5_AllFieldsAndRuReturn", func(t *testing.T) {
		rr := doServerInfoRequest(srv, "ru")
		if rr.Code != http.StatusOK {
			t.Errorf("want 200, got %d", rr.Code)
		}
		body := decodeServerInfoBody(t, rr)
		for _, field := range []string{"version", "build_sha", "server_time", "environment", "locales", "welcome_message"} {
			if _, ok := body[field]; !ok {
				t.Errorf("required field %q missing", field)
			}
		}
		msg, _ := body["welcome_message"].(string)
		if !strings.Contains(msg, "Добро пожаловать") {
			t.Errorf("ru welcome_message: want Russian, got %q", msg)
		}
	})
}
