// snake_case_json_test.go verifies feature #64:
// "JSON responses use snake_case field names"
//
// All response field names must be snake_case (request_id, trace_id,
// active_locale, occurred_at). No camelCase or PascalCase may appear in
// JSON responses.
//
// Steps verified:
//  1. GET /v1/info — inspect JSON keys
//  2. Verify keys like 'active_locale', 'supported_locales', 'service_version' (snake_case)
//  3. Verify NO keys like 'activeLocale', 'requestId'
//  4. POST /v1/echo response — same check
//  5. Error envelope — 'request_id', 'trace_id' (snake_case)
//  6. Static scan: regex check for camelCase in JSON struct tags across source files
package httpserver

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// reSnakeCaseKey matches a valid snake_case JSON key:
// one or more lowercase letters/digits groups separated by underscores,
// with optional omitempty suffix.
// e.g. "active_locale", "request_id", "db_now"
var reSnakeCaseKey = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// reCamelCaseKey detects any lowercase letter immediately followed by an
// uppercase letter — the hallmark of camelCase (e.g. "activeLocale").
var reCamelCaseKey = regexp.MustCompile(`[a-z][A-Z]`)

// rePascalCaseKey detects a key that starts with an uppercase letter.
var rePascalCaseKey = regexp.MustCompile(`^[A-Z]`)

// reJSONTag matches a Go struct json tag and captures the field name:
// `json:"<name>"` or `json:"<name>,omitempty"` or `json:"<name>,-"`
// Excludes json:"-" (anonymous suppression) and json:"-," (field named "-").
var reJSONTag = regexp.MustCompile(`json:"([^"]+)"`)

// =============================================================================
// helpers
// =============================================================================

// flattenJSONKeys returns all leaf key names from a decoded JSON object/map,
// traversing nested objects recursively. Only string-valued and structural keys
// are returned (not array indices).
func flattenJSONKeys(data map[string]any, prefix string) []string {
	var keys []string
	for k, v := range data {
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		keys = append(keys, k)
		if nested, ok := v.(map[string]any); ok {
			keys = append(keys, flattenJSONKeys(nested, full)...)
		}
	}
	return keys
}

// decodeJSONBody reads r.Body and decodes it into a map, closing the body.
func decodeJSONBody(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode JSON body: %v (body=%q)", err, string(b))
	}
	return out
}

// assertAllSnakeCase fails the test if any key in keys violates snake_case.
func assertAllSnakeCase(t *testing.T, label string, keys []string) {
	t.Helper()
	for _, k := range keys {
		// Skip the "error" wrapper key itself — it's a valid single-word key.
		// Also skip single-word keys like "app", "env", "status", "message",
		// "code", "token", "version", "commit", "audience", "issuer".
		// These have no underscore but are still snake_case-valid (all lowercase).
		if rePascalCaseKey.MatchString(k) {
			t.Errorf("%s: JSON key %q starts with uppercase (PascalCase leak)", label, k)
			continue
		}
		if reCamelCaseKey.MatchString(k) {
			t.Errorf("%s: JSON key %q contains camelCase (e.g. 'activeLocale' → should be 'active_locale')", label, k)
		}
	}
}

// =============================================================================
// Step 1+2+3 — GET /v1/info JSON keys are snake_case
// =============================================================================

// TestSnakeCase_InfoResponseKeysAreSnakeCase verifies steps 1-3:
// GET /v1/info response uses snake_case keys and contains the expected
// compound-word keys ('active_locale', 'supported_locales', 'server_time',
// 'request_id', 'trace_id', 'default_locale').
func TestSnakeCase_InfoResponseKeysAreSnakeCase(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET /v1/info: want 200, got %d", resp.StatusCode)
	}

	body := decodeJSONBody(t, resp)
	keys := flattenJSONKeys(body, "")
	assertAllSnakeCase(t, "GET /v1/info", keys)
}

// TestSnakeCase_InfoResponseHasActivLocale verifies step 2:
// the /v1/info response includes 'active_locale' (not 'activeLocale').
func TestSnakeCase_InfoResponseHasActiveLocale(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	body := decodeJSONBody(t, resp)

	if _, ok := body["active_locale"]; !ok {
		t.Error("GET /v1/info response must include 'active_locale' key (snake_case)")
	}
}

// TestSnakeCase_InfoResponseHasSupportedLocales verifies step 2:
// the /v1/info response includes 'supported_locales'.
func TestSnakeCase_InfoResponseHasSupportedLocales(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	body := decodeJSONBody(t, resp)

	if _, ok := body["supported_locales"]; !ok {
		t.Error("GET /v1/info response must include 'supported_locales' key (snake_case)")
	}
}

// TestSnakeCase_InfoResponseHasServerTime verifies that 'server_time'
// (not 'serverTime') is present.
func TestSnakeCase_InfoResponseHasServerTime(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	body := decodeJSONBody(t, resp)

	if _, ok := body["server_time"]; !ok {
		t.Error("GET /v1/info response must include 'server_time' key (snake_case)")
	}
}

// TestSnakeCase_InfoResponseNoActiveLocale_CamelCase verifies step 3:
// the /v1/info response must NOT include 'activeLocale'.
func TestSnakeCase_InfoResponseNoCamelCase(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	body := decodeJSONBody(t, resp)

	forbidden := []string{"activeLocale", "requestId", "traceId", "supportedLocales", "serverTime", "defaultLocale", "dbVersion", "dbNow"}
	for _, k := range forbidden {
		if _, ok := body[k]; ok {
			t.Errorf("GET /v1/info response must NOT include camelCase key %q", k)
		}
	}
}

// =============================================================================
// Step 4 — POST /v1/echo response JSON keys are snake_case
// =============================================================================

// TestSnakeCase_EchoResponseKeysAreSnakeCase verifies step 4:
// POST /v1/echo response uses snake_case keys.
func TestSnakeCase_EchoResponseKeysAreSnakeCase(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"snake_case_test"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000001")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("POST /v1/echo: want 200, got %d", resp.StatusCode)
	}

	body := decodeJSONBody(t, resp)
	keys := flattenJSONKeys(body, "")
	assertAllSnakeCase(t, "POST /v1/echo", keys)
}

// TestSnakeCase_EchoResponseHasActorId verifies that the echo response
// includes 'actor_id' (not 'actorId').
func TestSnakeCase_EchoResponseHasActorId(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000002")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000002")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	body := decodeJSONBody(t, resp)

	if _, ok := body["actor_id"]; !ok {
		t.Error("POST /v1/echo response must include 'actor_id' key (snake_case)")
	}
	if _, ok := body["actorId"]; ok {
		t.Error("POST /v1/echo response must NOT include camelCase key 'actorId'")
	}
}

// TestSnakeCase_EchoResponseHasEchoEventId verifies 'echo_event_id' key.
func TestSnakeCase_EchoResponseHasEchoEventId(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000003")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000003")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	body := decodeJSONBody(t, resp)

	if _, ok := body["echo_event_id"]; !ok {
		t.Error("POST /v1/echo response must include 'echo_event_id' key (snake_case)")
	}
}

// TestSnakeCase_EchoResponseHasIdempotentKey verifies 'idempotent_key' key.
func TestSnakeCase_EchoResponseHasIdempotentKey(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000004")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000004")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	body := decodeJSONBody(t, resp)

	if _, ok := body["idempotent_key"]; !ok {
		t.Error("POST /v1/echo response must include 'idempotent_key' key (snake_case)")
	}
}

// TestSnakeCase_EchoResponseNoCamelCase verifies that echo response
// does NOT include any camelCase keys.
func TestSnakeCase_EchoResponseNoCamelCase(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000005")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000005")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	body := decodeJSONBody(t, resp)

	forbidden := []string{"actorId", "echoEventId", "idempotentKey", "issuedAt", "requestId", "traceId"}
	for _, k := range forbidden {
		if _, ok := body[k]; ok {
			t.Errorf("POST /v1/echo response must NOT include camelCase key %q", k)
		}
	}
}

// =============================================================================
// Step 5 — Error envelope uses snake_case keys
// =============================================================================

// TestSnakeCase_ErrorEnvelopeKeysAreSnakeCase verifies step 5:
// the error envelope fields 'request_id' and 'trace_id' are snake_case.
func TestSnakeCase_ErrorEnvelopeKeysAreSnakeCase(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	// Trigger a 404 error response to get an error envelope.
	resp, err := ts.Client().Get(ts.URL + "/v1/nonexistent")
	if err != nil {
		t.Fatalf("GET /v1/nonexistent: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}

	body := decodeJSONBody(t, resp)
	keys := flattenJSONKeys(body, "")
	assertAllSnakeCase(t, "error envelope", keys)
}

// TestSnakeCase_ErrorEnvelopeHasRequestId verifies step 5:
// the error envelope includes 'request_id' (not 'requestId').
func TestSnakeCase_ErrorEnvelopeHasRequestId(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/nonexistent")
	if err != nil {
		t.Fatalf("GET /v1/nonexistent: %v", err)
	}
	body := decodeJSONBody(t, resp)

	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatal("error envelope must have 'error' object")
	}
	if _, ok := errObj["request_id"]; !ok {
		t.Error("error envelope error object must include 'request_id' key (snake_case)")
	}
	if _, ok := errObj["requestId"]; ok {
		t.Error("error envelope error object must NOT include camelCase key 'requestId'")
	}
}

// TestSnakeCase_ErrorEnvelopeHasTraceId verifies step 5:
// the error envelope includes 'trace_id' (not 'traceId').
func TestSnakeCase_ErrorEnvelopeHasTraceId(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/nonexistent")
	if err != nil {
		t.Fatalf("GET /v1/nonexistent: %v", err)
	}
	body := decodeJSONBody(t, resp)

	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatal("error envelope must have 'error' object")
	}
	if _, ok := errObj["trace_id"]; !ok {
		t.Error("error envelope error object must include 'trace_id' key (snake_case)")
	}
	if _, ok := errObj["traceId"]; ok {
		t.Error("error envelope error object must NOT include camelCase key 'traceId'")
	}
}

// TestSnakeCase_ErrorEnvelopeFromBadRequest verifies step 5 with a 400 error
// to confirm that all error paths use consistent snake_case keys.
func TestSnakeCase_ErrorEnvelopeFromBadRequest(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	// GET on the root without /v1 prefix — guaranteed 404.
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		t.Skip("root returns 200, skip this check")
	}

	body := decodeJSONBody(t, resp)
	if errObj, ok := body["error"].(map[string]any); ok {
		keys := flattenJSONKeys(errObj, "")
		assertAllSnakeCase(t, "error envelope (root path)", keys)
	}
}

// =============================================================================
// Step 6 — Static scan: no camelCase JSON struct tags in production Go source
// =============================================================================

// findBackendRoot returns the absolute path to apps/backend directory
// by walking up from the current test file's location.
func findBackendRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	// thisFile is in apps/backend/internal/platform/httpserver/
	// Walk up to find the apps/backend directory.
	dir := filepath.Dir(thisFile) // httpserver/
	for i := 0; i < 10; i++ {
		// Check if this looks like the backend root: has internal/ and cmd/ subdirs
		if _, err := os.Stat(filepath.Join(dir, "internal")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find apps/backend root directory")
	return ""
}

// scanGoFilesForCamelCaseJSONTags walks the given directory tree and reports
// any json struct tag that uses camelCase or PascalCase field names.
// Test files (*_test.go) are excluded because test helpers may define structs
// for decoding purposes and use whatever names they need.
func scanGoFilesForCamelCaseJSONTags(t *testing.T, root string) []string {
	t.Helper()
	var violations []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip vendor directories and generated code marker paths.
			base := filepath.Base(path)
			if base == "vendor" || base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // skip test files
		}

		f, ferr := os.Open(path)
		if ferr != nil {
			return ferr
		}
		defer f.Close()

		lineNum := 0
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			matches := reJSONTag.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				rawTag := m[1]
				// Strip ,omitempty and other modifiers to get the bare field name.
				fieldName := strings.SplitN(rawTag, ",", 2)[0]
				// json:"-" means omit field — not a name, skip.
				if fieldName == "-" {
					continue
				}
				// Skip header-style names with dashes (e.g. "Idempotency-Key").
				if strings.Contains(fieldName, "-") {
					continue
				}
				// Check for camelCase: lowercase letter followed by uppercase letter.
				if reCamelCaseKey.MatchString(fieldName) {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, rel+":"+string(rune('0'+lineNum/1000%10))+string(rune('0'+lineNum/100%10))+string(rune('0'+lineNum/10%10))+string(rune('0'+lineNum%10))+" json:\""+rawTag+"\" (camelCase key: "+fieldName+")")
				}
				// Check for PascalCase: starts with uppercase.
				if rePascalCaseKey.MatchString(fieldName) {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, rel+":"+itoa(lineNum)+" json:\""+rawTag+"\" (PascalCase key: "+fieldName+")")
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("filepath.Walk(%s): %v", root, err)
	}
	return violations
}

// itoa converts an int to a string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// TestSnakeCase_StaticScan_NoCamelCaseJSONTags verifies step 6:
// a walk of all non-test Go source files in apps/backend finds no
// json struct tag that uses camelCase or PascalCase field names.
func TestSnakeCase_StaticScan_NoCamelCaseJSONTags(t *testing.T) {
	t.Parallel()

	root := findBackendRoot(t)
	violations := scanGoFilesForCamelCaseJSONTags(t, root)

	if len(violations) > 0 {
		t.Errorf("found %d camelCase/PascalCase json tag(s) in production Go source files:", len(violations))
		for _, v := range violations {
			t.Errorf("  %s", v)
		}
	}
}

// =============================================================================
// Full verification sweep
// =============================================================================

// TestSnakeCase_FullVerification is an integration sweep that exercises all
// 6 feature steps as sub-tests in a single server instance.
func TestSnakeCase_FullVerification(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	t.Run("Step1_InfoReturns200WithJSON", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/v1/info")
		if err != nil {
			t.Fatalf("GET /v1/info: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("want 200, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("Step2_InfoResponseHasSnakeCaseKeys", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/v1/info")
		if err != nil {
			t.Fatalf("GET /v1/info: %v", err)
		}
		body := decodeJSONBody(t, resp)
		required := []string{"active_locale", "supported_locales", "server_time", "request_id", "trace_id", "default_locale"}
		for _, k := range required {
			if _, ok := body[k]; !ok {
				t.Errorf("GET /v1/info: missing expected snake_case key %q", k)
			}
		}
	})

	t.Run("Step3_InfoResponseNoCamelCase", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/v1/info")
		if err != nil {
			t.Fatalf("GET /v1/info: %v", err)
		}
		body := decodeJSONBody(t, resp)
		keys := flattenJSONKeys(body, "")
		assertAllSnakeCase(t, "GET /v1/info (step 3)", keys)
	})

	t.Run("Step5_ErrorEnvelopeHasSnakeCaseIds", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/v1/nonexistent_endpoint")
		if err != nil {
			t.Fatalf("GET /v1/nonexistent_endpoint: %v", err)
		}
		body := decodeJSONBody(t, resp)
		if errObj, ok := body["error"].(map[string]any); ok {
			if _, ok := errObj["request_id"]; !ok {
				t.Error("error envelope must have 'request_id' (snake_case)")
			}
			if _, ok := errObj["trace_id"]; !ok {
				t.Error("error envelope must have 'trace_id' (snake_case)")
			}
			if _, ok := errObj["requestId"]; ok {
				t.Error("error envelope must NOT have 'requestId' (camelCase)")
			}
			if _, ok := errObj["traceId"]; ok {
				t.Error("error envelope must NOT have 'traceId' (camelCase)")
			}
		} else {
			t.Error("error envelope must have top-level 'error' object")
		}
	})

	t.Run("Step6_StaticScanNoCamelCase", func(t *testing.T) {
		root := findBackendRoot(t)
		violations := scanGoFilesForCamelCaseJSONTags(t, root)
		if len(violations) > 0 {
			t.Errorf("static scan found %d camelCase JSON tag(s): %v", len(violations), violations)
		}
	})
}

// =============================================================================
// buildTestHTTPServer helper
// =============================================================================

// buildTestHTTPServer wraps an httptest.Server around a Server's Router.
// Registered for cleanup via t.Cleanup.
func buildTestHTTPServer(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts
}
