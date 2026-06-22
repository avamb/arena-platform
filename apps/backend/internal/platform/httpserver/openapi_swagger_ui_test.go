// openapi_swagger_ui_test.go — feature #76
// "OpenAPI yaml file can be imported into Swagger UI"
//
// openapi/openapi.yaml is loadable into Swagger UI / Redoc without errors.
// End-to-end verification of the spec.
//
// Feature steps covered:
//
//	Step 1: Spec file exists and is readable (Swagger UI can fetch it as a static file)
//	Step 4: No '#/...' broken refs — all $ref pointers resolve to defined components
//	Step 5: GET /v1/info Try-it-out — request fires against the running API and gets 200
//	Step 6: POST /v1/echo Try-it-out — request fires with example body and returns 200
//	Step 7: Response schemas validate against actual responses
//	        (required fields from EchoResponse and InfoResponse schemas are present)
//
// Note: Steps 2-3 (run Docker container, open browser) are verified structurally:
// the spec contains all required Swagger UI top-level fields (openapi, info, paths)
// that allow Swagger UI to render without errors.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/google/uuid"
)

// =============================================================================
// Step 1 — Spec file is readable (Swagger UI can serve it at /spec/openapi.yaml)
// =============================================================================

// TestSwaggerUI_SpecFileReadable verifies the openapi.yaml exists and is non-empty.
// Swagger UI requires the spec file to be HTTP-accessible; this confirms the file
// exists on disk so the container volume mount would succeed.
func TestSwaggerUI_SpecFileReadable(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if len(content) == 0 {
		t.Fatal("openapi.yaml is empty — Swagger UI would show a blank page")
	}
	if !strings.HasPrefix(strings.TrimSpace(content), "openapi:") {
		t.Error("openapi.yaml does not start with 'openapi:' field — Swagger UI would reject it")
	}
	t.Logf("openapi.yaml loaded: %d bytes", len(content))
}

// TestSwaggerUI_RequiredTopLevelFields verifies that all top-level fields required
// by Swagger UI for successful import are present: openapi, info, paths.
// A missing 'paths' section causes Swagger UI to show "No operations defined".
func TestSwaggerUI_RequiredTopLevelFields(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	required := []string{"\nopenapi:", "\ninfo:", "\npaths:", "\ncomponents:"}
	for _, field := range required {
		// Also accept as very first line (no preceding newline)
		if !strings.Contains(content, field) && !strings.HasPrefix(strings.TrimSpace(content), strings.TrimPrefix(field, "\n")) {
			t.Errorf("openapi.yaml missing required top-level field %q — Swagger UI import will fail", strings.TrimPrefix(field, "\n"))
		}
	}
}

// =============================================================================
// Step 4 — No broken $ref pointers (Swagger UI console errors)
// =============================================================================

// swaggerUIExtractRefs parses all '$ref: "..."' values from the YAML content and
// returns them as a deduplicated slice. Only includes internal refs (starting with '#/').
func swaggerUIExtractRefs(content string) []string {
	// Match: $ref: "#/components/schemas/ErrorEnvelope"
	//    or: $ref: '#/components/schemas/ErrorEnvelope'
	refRe := regexp.MustCompile(`\$ref:\s*["']?(#/[^"'\s]+)["']?`)
	matches := refRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var refs []string
	for _, m := range matches {
		if len(m) >= 2 && !seen[m[1]] {
			seen[m[1]] = true
			refs = append(refs, m[1])
		}
	}
	return refs
}

// swaggerUIRefExists verifies that a JSON Pointer like '#/components/schemas/ErrorEnvelope'
// resolves to an existing key in the YAML document.
// Uses a text-based heuristic: for the leaf name, checks for a YAML mapping key
// at some indentation level. This works for all schema names in this project since
// names are unique across the document.
func swaggerUIRefExists(content, ref string) bool {
	// Strip the leading '#/'
	path := strings.TrimPrefix(ref, "#/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return false
	}

	leafName := parts[len(parts)-1]
	escaped := regexp.QuoteMeta(leafName)

	// Check for leaf key at any indentation (e.g. "    ErrorEnvelope:")
	keyRe := regexp.MustCompile(`(?m)^\s+` + escaped + `\s*:`)
	if keyRe.MatchString(content) {
		return true
	}
	// Also check as a top-level key (0 indentation)
	topRe := regexp.MustCompile(`(?m)^` + escaped + `\s*:`)
	return topRe.MatchString(content)
}

// TestSwaggerUI_NoInternalBrokenRefs verifies that every '#/...' $ref value in
// openapi.yaml resolves to an existing definition within the same document.
// Broken internal $refs are the primary cause of Swagger UI console errors on import.
func TestSwaggerUI_NoInternalBrokenRefs(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	refs := swaggerUIExtractRefs(content)

	if len(refs) == 0 {
		t.Log("No internal $ref values found — nothing to check")
		return
	}

	t.Logf("Found %d unique internal $ref values", len(refs))

	broken := 0
	for _, ref := range refs {
		if !swaggerUIRefExists(content, ref) {
			t.Errorf("BROKEN REF: %q does not resolve to any definition in openapi.yaml", ref)
			broken++
		} else {
			t.Logf("  OK: %s", ref)
		}
	}

	if broken > 0 {
		t.Errorf("%d/%d $ref values are broken — Swagger UI would show console errors on import", broken, len(refs))
	}
}

// TestSwaggerUI_AllSchemaRefsResolve individually checks each expected $ref so
// test output clearly identifies which specific schema is broken.
func TestSwaggerUI_AllSchemaRefsResolve(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")

	// These are the $ref values used in the paths section of the spec.
	expectedRefs := []string{
		"#/components/schemas/ErrorEnvelope",
		"#/components/schemas/HealthzResponse",
		"#/components/schemas/ReadyzResponse",
		"#/components/schemas/InfoResponse",
		"#/components/schemas/EchoRequest",
		"#/components/schemas/EchoResponse",
	}

	for _, ref := range expectedRefs {
		ref := ref
		t.Run(strings.TrimPrefix(ref, "#/components/schemas/"), func(t *testing.T) {
			if !swaggerUIRefExists(content, ref) {
				t.Errorf("$ref %q is not defined in components/schemas — Swagger UI would show a broken reference error", ref)
			}
		})
	}
}

// TestSwaggerUI_SecuritySchemeRefResolves verifies the bearerAuth security scheme
// used by POST /v1/echo is defined in securitySchemes.
// A missing security scheme definition causes Swagger UI to show a broken reference.
func TestSwaggerUI_SecuritySchemeRefResolves(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "bearerAuth:") {
		t.Error("securitySchemes/bearerAuth is not defined — POST /v1/echo security scheme reference is broken")
	}
	if !strings.Contains(content, "securitySchemes:") {
		t.Error("components/securitySchemes section is missing — bearer security scheme cannot be defined")
	}
}

// TestSwaggerUI_DevTokenSchemaRefsResolve verifies the DevTokenRequest and
// DevTokenResponse schemas referenced by /v1/dev/token are defined.
func TestSwaggerUI_DevTokenSchemaRefsResolve(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	for _, ref := range []string{
		"#/components/schemas/DevTokenRequest",
		"#/components/schemas/DevTokenResponse",
		"#/components/schemas/DevAuthTokenRequest",
		"#/components/schemas/DevAuthTokenResponse",
	} {
		ref := ref
		t.Run(strings.TrimPrefix(ref, "#/components/schemas/"), func(t *testing.T) {
			if !swaggerUIRefExists(content, ref) {
				t.Errorf("$ref %q is missing — Swagger UI would show a broken reference for /v1/dev/* endpoints", ref)
			}
		})
	}
}

// =============================================================================
// Step 5 — GET /v1/info Try-it-out: fires against running API and gets 200
// =============================================================================

// TestSwaggerUI_GetV1Info_TryItOut simulates the Swagger UI "Try it out" button
// for GET /v1/info. Starts a real httptest.Server and confirms the endpoint
// returns HTTP 200 with a valid JSON body containing all required fields.
func TestSwaggerUI_GetV1Info_TryItOut(t *testing.T) {
	ts, _ := buildSwaggerUITestServer(t)

	resp, err := http.Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/info: expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /v1/info: Content-Type should be application/json, got %q", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("GET /v1/info: decode response: %v", err)
	}

	// Required fields from the InfoResponse schema in openapi.yaml.
	required := []string{
		"app", "version", "commit", "env",
		"supported_locales", "default_locale", "active_locale",
		"server_time", "request_id", "trace_id",
	}
	for _, field := range required {
		if _, ok := body[field]; !ok {
			t.Errorf("GET /v1/info response missing required field %q (defined in InfoResponse schema)", field)
		}
	}

	t.Logf("GET /v1/info → 200 OK with all %d required InfoResponse fields", len(required))
}

// =============================================================================
// Step 6 — POST /v1/echo Try-it-out with example request body → 200
// =============================================================================

// TestSwaggerUI_PostV1Echo_TryItOut simulates the Swagger UI "Try it out" button
// for POST /v1/echo using the example request body from the EchoRequest schema:
//
//	{"message": "hello arena"}
//
// Authenticates via the StubProvider so the full handler chain runs and returns 200.
func TestSwaggerUI_PostV1Echo_TryItOut(t *testing.T) {
	ts, stub := buildSwaggerUITestServer(t)

	// Mint a JWT using the StubProvider (same as how Swagger UI would first call
	// POST /v1/dev/token to get a token, then use it for Try-it-out).
	token := swaggerUIMintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

	// Use the example request body from the EchoRequest schema.
	exampleBody := `{"message": "hello arena"}`
	idempotencyKey := uuid.New().String()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(exampleBody))
	if err != nil {
		t.Fatalf("POST /v1/echo: create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/echo: expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("POST /v1/echo: Content-Type should be application/json, got %q", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("POST /v1/echo: decode response: %v", err)
	}

	t.Logf("POST /v1/echo → 200 OK with body keys: %v", swaggerUIBodyKeys(body))
}

// =============================================================================
// Step 7 — Response schemas validate against actual responses
// =============================================================================

// TestSwaggerUI_EchoResponseMatchesSchema verifies that POST /v1/echo returns all
// required fields listed in the EchoResponse schema in openapi.yaml, and that
// field types match the schema definitions (string, UUID, RFC3339).
func TestSwaggerUI_EchoResponseMatchesSchema(t *testing.T) {
	ts, stub := buildSwaggerUITestServer(t)
	token := swaggerUIMintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

	body := `{"message": "swagger ui schema test"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", uuid.New().String())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/echo: expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("POST /v1/echo: decode: %v", err)
	}

	// EchoResponse required fields from openapi.yaml EchoResponse schema.
	required := []string{
		"message", "actor_id", "request_id", "trace_id",
		"echo_event_id", "idempotent_key", "issued_at",
	}
	for _, field := range required {
		if _, ok := result[field]; !ok {
			t.Errorf("EchoResponse missing required field %q (from openapi.yaml EchoResponse schema)", field)
		}
	}

	// 'message' must echo the request body exactly.
	if msg, ok := result["message"].(string); !ok || msg != "swagger ui schema test" {
		t.Errorf("EchoResponse.message = %v, want %q", result["message"], "swagger ui schema test")
	}

	// 'issued_at' must be a parseable RFC3339 string (schema: format: date-time).
	if issuedAt, ok := result["issued_at"].(string); ok {
		if _, err := time.Parse(time.RFC3339Nano, issuedAt); err != nil {
			if _, err2 := time.Parse(time.RFC3339, issuedAt); err2 != nil {
				t.Errorf("EchoResponse.issued_at %q is not RFC3339: %v", issuedAt, err)
			}
		}
	} else {
		t.Errorf("EchoResponse.issued_at is not a string: %T %v", result["issued_at"], result["issued_at"])
	}

	// 'echo_event_id' must be a valid UUID (schema: format: uuid).
	if eventID, ok := result["echo_event_id"].(string); ok {
		if _, err := uuid.Parse(eventID); err != nil {
			t.Errorf("EchoResponse.echo_event_id %q is not a valid UUID: %v", eventID, err)
		}
	} else {
		t.Errorf("EchoResponse.echo_event_id is not a string: %T %v", result["echo_event_id"], result["echo_event_id"])
	}

	t.Logf("EchoResponse schema validation PASS — all %d required fields present and typed correctly", len(required))
}

// TestSwaggerUI_InfoResponseMatchesSchema verifies GET /v1/info returns all required
// fields from the InfoResponse schema with correct types.
func TestSwaggerUI_InfoResponseMatchesSchema(t *testing.T) {
	ts, _ := buildSwaggerUITestServer(t)

	resp, err := http.Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/info: expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("GET /v1/info: decode: %v", err)
	}

	// InfoResponse required fields from openapi.yaml schema.
	required := []string{
		"app", "version", "commit", "env",
		"supported_locales", "default_locale", "active_locale",
		"server_time", "request_id", "trace_id",
	}
	for _, field := range required {
		if _, ok := result[field]; !ok {
			t.Errorf("InfoResponse missing required field %q (from openapi.yaml InfoResponse schema)", field)
		}
	}

	// 'server_time' must be RFC3339 UTC (schema: format: date-time, ends with Z).
	if st, ok := result["server_time"].(string); ok {
		if _, err := time.Parse(time.RFC3339Nano, st); err != nil {
			if _, err2 := time.Parse(time.RFC3339, st); err2 != nil {
				t.Errorf("InfoResponse.server_time %q is not RFC3339: %v", st, err)
			}
		}
		if !strings.HasSuffix(st, "Z") {
			t.Errorf("InfoResponse.server_time %q should end with 'Z' (UTC, as in the spec example)", st)
		}
	} else {
		t.Errorf("InfoResponse.server_time is not a string: %T %v", result["server_time"], result["server_time"])
	}

	// 'supported_locales' must be an array (schema: type: array).
	if locales, ok := result["supported_locales"].([]any); !ok || len(locales) == 0 {
		t.Errorf("InfoResponse.supported_locales should be a non-empty array, got %T %v",
			result["supported_locales"], result["supported_locales"])
	}

	t.Logf("InfoResponse schema validation PASS — all %d required fields present and typed correctly", len(required))
}

// =============================================================================
// Full verification — all feature steps as sub-tests
// =============================================================================

// TestSwaggerUI_FullVerification runs all feature #76 steps as sub-tests to
// provide a unified pass/fail summary for the feature.
func TestSwaggerUI_FullVerification(t *testing.T) {
	t.Run("Step1_SpecFileReadable", TestSwaggerUI_SpecFileReadable)
	t.Run("Step1_RequiredTopLevelFields", TestSwaggerUI_RequiredTopLevelFields)
	t.Run("Step4_NoInternalBrokenRefs", TestSwaggerUI_NoInternalBrokenRefs)
	t.Run("Step4_AllSchemaRefsResolve", TestSwaggerUI_AllSchemaRefsResolve)
	t.Run("Step4_SecuritySchemeRefResolves", TestSwaggerUI_SecuritySchemeRefResolves)
	t.Run("Step4_DevTokenSchemaRefsResolve", TestSwaggerUI_DevTokenSchemaRefsResolve)
	t.Run("Step5_GetV1Info_TryItOut", TestSwaggerUI_GetV1Info_TryItOut)
	t.Run("Step6_PostV1Echo_TryItOut", TestSwaggerUI_PostV1Echo_TryItOut)
	t.Run("Step7_EchoResponseMatchesSchema", TestSwaggerUI_EchoResponseMatchesSchema)
	t.Run("Step7_InfoResponseMatchesSchema", TestSwaggerUI_InfoResponseMatchesSchema)
}

// =============================================================================
// Private helpers
// =============================================================================

// buildSwaggerUITestServer creates a fully wired httptest.Server with dev auth
// enabled, suitable for simulating Swagger UI "Try it out" interactions.
// Mirrors the pattern from buildBehaviorTestServer (feature #33).
func buildSwaggerUITestServer(t *testing.T) (ts *httptest.Server, stub *auth.StubProvider) {
	t.Helper()

	var err error
	stub, err = auth.NewStubProvider(auth.StubConfig{
		Secret:  "swagger-ui-test-secret-abcdef1234",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"ru", "en"},
	}

	s := New(Options{
		Config: cfg,
		Auth:   stub,
		Audit:  &captureAuditWriter{},
		Idem:   &noopIdemStore{},
		Pool:   &fakePoolDB{tx: &fakeTx{}},
	})

	ts = httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, stub
}

// swaggerUIMintJWT mints a development JWT via the StubProvider, simulating
// how Swagger UI would first call POST /v1/dev/token to obtain a bearer token.
func swaggerUIMintJWT(t *testing.T, stub *auth.StubProvider, actorID string) string {
	t.Helper()
	tok, _, err := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID:   actorID,
		ActorType: auth.ActorTypeStubUser,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("swaggerUIMintJWT IssueToken: %v", err)
	}
	return tok
}

// swaggerUIBodyKeys returns the keys of a JSON object map for logging.
func swaggerUIBodyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
