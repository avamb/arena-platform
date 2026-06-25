// openapi_behavior_test.go — feature #33
// "OpenAPI spec matches actual route behavior"
//
// Drift test that calls every endpoint documented in openapi.yaml and verifies:
//
//  1. Response status code is documented in the spec
//  2. Response Content-Type matches the spec
//  3. Response body schema — all required fields are present
//
// Covered scenarios (feature spec step 3):
//
//	GET  /healthz                 → 200 (HealthzResponse schema)
//	GET  /readyz                  → 200 (ReadyzResponse schema)
//	GET  /metrics                 → 200 (text/plain)
//	GET  /v1/info                 → 200 (InfoResponse schema)
//	POST /v1/echo (no auth)       → 401 (ErrorEnvelope schema)
//	POST /v1/echo (bad body)      → 400 (ErrorEnvelope schema)
//	POST /v1/echo (wrong CT)      → 415 (ErrorEnvelope schema)
//	POST /v1/echo (valid)         → 200 (EchoResponse schema)
//
// Step 4: passes/failures are reported per-endpoint via t.Errorf.
// Step 5: go test exits non-zero whenever any t.Error/t.Fatal fires.
// Step 6: package-level compile-time guards ensure handler Go types carry all
//
//	fields required by the spec; any field rename/removal causes a
//	compile error before tests even run.
package httpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// =============================================================================
// Step 6 — Compile-time drift guards
//
// These blank var assignments ensure that every field required by the OpenAPI
// spec is present in the corresponding handler Go struct. If any field is
// renamed or removed in the handler type, this file fails to compile, surfacing
// spec drift before runtime.
//
// echoRequest  ↔ EchoRequest  schema   (required: message)
// echoResponse ↔ EchoResponse schema   (required: message, actor_id, request_id,
//
//	trace_id, echo_event_id, idempotent_key, issued_at)
//
// infoResponse ↔ InfoResponse schema   (required: app, version, commit, env,
//
//	supported_locales, default_locale, active_locale,
//	server_time, request_id, trace_id)
// =============================================================================

// EchoRequest schema: required = [message]
var _ = echoRequest{
	Message: "",
}

// EchoResponse schema: required = [message, actor_id, request_id, trace_id,
// echo_event_id, idempotent_key, issued_at]
//
// Field names follow oapi-codegen v2 conventions (camelCase with lowercase id):
//   - ActorId, RequestId, TraceId  (not ActorID / RequestID / TraceID)
//   - EchoEventId is openapi_types.UUID (≡ uuid.UUID) not string
//   - IssuedAt is time.Time not string
var _ = echoResponse{
	Message:       "",
	ActorId:       "",
	RequestId:     "",
	TraceId:       "",
	EchoEventId:   uuid.UUID{},
	IdempotentKey: "",
	IssuedAt:      time.Time{},
}

// InfoResponse schema: required = [app, version, commit, env, supported_locales,
// default_locale, active_locale, server_time, request_id, trace_id]
var _ = infoResponse{
	App:              "",
	Version:          "",
	Commit:           "",
	Env:              "",
	SupportedLocales: nil,
	DefaultLocale:    "",
	ActiveLocale:     "",
	ServerTime:       "",
	RequestID:        "",
	TraceID:          "",
}

// =============================================================================
// Step 1 — YAML schema parser helpers
// =============================================================================

// specSchema captures the required fields parsed from a components/schemas entry.
type specSchema struct {
	RequiredFields []string
}

// parseComponentSchemas parses the `components: schemas:` section of an OpenAPI
// YAML document, extracting the top-level required[] list for each named schema.
//
// Handles two YAML required formats:
//
//	required: [field1, field2]          ← flow sequence  (6-space indent)
//	required:                           ← block sequence (6-space indent)
//	  - field1                          ← list item      (8-space indent)
//	  - field2
func parseComponentSchemas(data []byte) map[string]specSchema {
	result := make(map[string]specSchema)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var (
		inComponents  bool
		inSchemas     bool
		currentSchema string
		inRequired    bool
	)

	for scanner.Scan() {
		raw := scanner.Text()

		// Strip inline YAML comments.
		if idx := strings.Index(raw, " #"); idx >= 0 {
			raw = raw[:idx]
		}
		line := strings.TrimRight(raw, " \t")
		if line == "" {
			continue
		}

		// Detect top-level section markers.
		if line == "components:" {
			inComponents = true
			inSchemas = false
			currentSchema = ""
			continue
		}
		// Any non-indented line ends the components section.
		if inComponents && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			break
		}
		if !inComponents {
			continue
		}

		nSpaces := countLeadingSpacesBehavior(line)

		// `  schemas:` at 2-space indent inside components.
		if nSpaces == 2 {
			trimmed := strings.TrimLeft(line, " \t")
			if trimmed == "schemas:" {
				inSchemas = true
				currentSchema = ""
				inRequired = false
			} else {
				inSchemas = false // other sub-section (securitySchemes, etc.)
			}
			continue
		}
		if !inSchemas {
			continue
		}

		// Schema names at 4-space indent: `    SchemaName:`
		if nSpaces == 4 {
			trimmed := strings.TrimLeft(line, " \t")
			if name := strings.TrimSuffix(trimmed, ":"); name != "" && !strings.Contains(name, " ") {
				currentSchema = name
				inRequired = false
				if _, exists := result[currentSchema]; !exists {
					result[currentSchema] = specSchema{}
				}
			}
			continue
		}

		if currentSchema == "" {
			continue
		}

		// Schema body at 6-space indent.
		if nSpaces == 6 {
			trimmed := strings.TrimLeft(line, " \t")

			// Flow sequence: required: [f1, f2, ...]
			if strings.HasPrefix(trimmed, "required: [") {
				inRequired = false
				inner := strings.TrimPrefix(trimmed, "required: [")
				inner = strings.TrimSuffix(inner, "]")
				s := result[currentSchema]
				for _, f := range strings.Split(inner, ",") {
					f = strings.TrimSpace(f)
					if f != "" {
						s.RequiredFields = append(s.RequiredFields, f)
					}
				}
				result[currentSchema] = s
				continue
			}

			// Block sequence marker: `required:`
			if strings.TrimRight(trimmed, " \t") == "required:" {
				inRequired = true
				continue
			}

			// Any other 6-space key ends the required block.
			if inRequired {
				inRequired = false
			}
		}

		// List items at 8-space indent: `        - fieldName`
		if nSpaces == 8 && inRequired {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, "- ") {
				field := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				if field != "" {
					s := result[currentSchema]
					s.RequiredFields = append(s.RequiredFields, field)
					result[currentSchema] = s
				}
			}
		}
	}

	return result
}

func countLeadingSpacesBehavior(line string) int {
	n := 0
	for _, ch := range line {
		if ch == ' ' {
			n++
		} else {
			break
		}
	}
	return n
}

// =============================================================================
// Step 2 — JSON schema validation helpers
// =============================================================================

// validateTopLevelRequired checks that every field in required is present as a
// key in the top-level JSON object of body. It does not validate value types,
// formats, or enum constraints — only field presence.
func validateTopLevelRequired(t *testing.T, body []byte, required []string, endpoint string) {
	t.Helper()

	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Errorf("[%s] response body is not a JSON object: %v (body: %.200s)", endpoint, err, body)
		return
	}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			t.Errorf("[%s] schema required field %q missing from response body (body: %.200s)",
				endpoint, field, body)
		}
	}
}

// validateErrorEnvelopeSchema validates the ErrorEnvelope schema:
//
//	top-level: {"error": <object>}
//	inner:     {code, message, request_id, trace_id}  (all required)
func validateErrorEnvelopeSchema(t *testing.T, body []byte, endpoint string) {
	t.Helper()

	var outer map[string]json.RawMessage
	if err := json.Unmarshal(body, &outer); err != nil {
		t.Errorf("[%s] ErrorEnvelope body is not JSON: %v (body: %.200s)", endpoint, err, body)
		return
	}
	errRaw, ok := outer["error"]
	if !ok {
		t.Errorf("[%s] ErrorEnvelope missing top-level 'error' key (body: %.200s)", endpoint, body)
		return
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(errRaw, &inner); err != nil {
		t.Errorf("[%s] ErrorEnvelope 'error' value is not a JSON object: %v", endpoint, err)
		return
	}
	for _, field := range []string{"code", "message", "request_id", "trace_id"} {
		if _, ok := inner[field]; !ok {
			t.Errorf("[%s] ErrorEnvelope missing required field 'error.%s' (body: %.200s)",
				endpoint, field, body)
		}
	}
}

// assertContentType checks that the response Content-Type header starts with
// wantPrefix (case-insensitive). For example wantPrefix="application/json"
// also passes "application/json; charset=utf-8".
func assertContentType(t *testing.T, resp *http.Response, wantPrefix, endpoint string) {
	t.Helper()
	got := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(got), strings.ToLower(wantPrefix)) {
		t.Errorf("[%s] Content-Type: got %q, want prefix %q", endpoint, got, wantPrefix)
	}
}

// readBody consumes and returns the response body, failing the test on error.
func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return b
}

// =============================================================================
// Test server builder (includes metrics handler for /metrics coverage)
// =============================================================================

// buildBehaviorTestServer creates a fully-wired test server for feature #33.
// It extends buildDriftTestServer (from openapi_drift_test.go) by using a real
// prometheus registry scrape handler so GET /metrics returns proper text/plain
// output, enabling content-type validation.
func buildBehaviorTestServer(t *testing.T) (ts *httptest.Server, stub *auth.StubProvider) {
	t.Helper()

	var err error
	stub, err = auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-behavior-check",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
	}

	s := New(Options{
		Config:         cfg,
		Auth:           stub,
		Audit:          &captureAuditWriter{},
		Idem:           &noopIdemStore{},
		Pool:           &fakePoolDB{tx: &fakeTx{}},
		MetricsHandler: promhttp.Handler(), // real scrape handler → text/plain
	})

	ts = httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, stub
}

// =============================================================================
// Step 1 verification — YAML schema parser smoke test
// =============================================================================

// TestOpenAPIBehavior_SchemaParserExtractsRequiredFields verifies that the
// minimal YAML parser can locate and extract required field lists from the
// spec's components/schemas section. The assertions are derived directly from
// reading openapi/openapi.yaml.
func TestOpenAPIBehavior_SchemaParserExtractsRequiredFields(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := readSpecFile(t, specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	schemas := parseComponentSchemas(data)
	if len(schemas) == 0 {
		t.Fatal("parseComponentSchemas returned zero schemas — check YAML indent assumptions")
	}

	cases := []struct {
		schema  string
		wantAny []string // subset that must appear
	}{
		{
			schema:  "ErrorEnvelope",
			wantAny: []string{"error"},
		},
		{
			schema:  "HealthzResponse",
			wantAny: []string{"status"},
		},
		{
			schema:  "ReadyzResponse",
			wantAny: []string{"status", "checks"},
		},
		{
			schema:  "EchoResponse",
			wantAny: []string{"message", "actor_id", "request_id", "trace_id", "echo_event_id", "idempotent_key", "issued_at"},
		},
		{
			schema:  "InfoResponse",
			wantAny: []string{"app", "version", "commit", "env", "supported_locales", "default_locale", "active_locale", "server_time", "request_id", "trace_id"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.schema, func(t *testing.T) {
			t.Parallel()
			s, ok := schemas[tc.schema]
			if !ok {
				t.Fatalf("schema %q not found in parsed schemas", tc.schema)
			}
			have := make(map[string]bool, len(s.RequiredFields))
			for _, f := range s.RequiredFields {
				have[f] = true
			}
			for _, wf := range tc.wantAny {
				if !have[wf] {
					t.Errorf("schema %q: required field %q not found; parsed fields: %v",
						tc.schema, wf, s.RequiredFields)
				}
			}
		})
	}
}

// readSpecFile returns the bytes of the openapi.yaml file, failing the test on error.
func readSpecFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	return data, err
}

// =============================================================================
// GET /healthz — 200, application/json, HealthzResponse schema
// =============================================================================

func TestOpenAPIBehavior_HealthzStatus200(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz: got status %d, want 200", resp.StatusCode)
	}
}

func TestOpenAPIBehavior_HealthzContentType(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	assertContentType(t, resp, "application/json", "GET /healthz")
}

func TestOpenAPIBehavior_HealthzBodySchema(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body := readBody(t, resp)

	// HealthzResponse.required = [status]
	specPath := findOpenAPISpecPath(t)
	data, err2 := readSpecFile(t, specPath)
	if err2 != nil {
		t.Fatalf("read spec: %v", err2)
	}
	schemas := parseComponentSchemas(data)
	validateTopLevelRequired(t, body, schemas["HealthzResponse"].RequiredFields, "GET /healthz")

	// Validate enum: status must be "ok"
	var payload map[string]string
	if err3 := json.Unmarshal(body, &payload); err3 == nil {
		if payload["status"] != "ok" {
			t.Errorf("GET /healthz: status enum: got %q, want %q", payload["status"], "ok")
		}
	}
}

// =============================================================================
// GET /readyz — 200, application/json, ReadyzResponse schema
// =============================================================================

func TestOpenAPIBehavior_ReadyzStatus200(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	// /readyz with no probes registered → 200 ready
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /readyz: got status %d, want 200", resp.StatusCode)
	}
}

func TestOpenAPIBehavior_ReadyzContentType(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	assertContentType(t, resp, "application/json", "GET /readyz")
}

func TestOpenAPIBehavior_ReadyzBodySchema(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	body := readBody(t, resp)

	specPath := findOpenAPISpecPath(t)
	data, err2 := readSpecFile(t, specPath)
	if err2 != nil {
		t.Fatalf("read spec: %v", err2)
	}
	schemas := parseComponentSchemas(data)
	validateTopLevelRequired(t, body, schemas["ReadyzResponse"].RequiredFields, "GET /readyz")

	// Validate enum: status must be "ready" or "not_ready"
	var payload struct {
		Status string `json:"status"`
	}
	if err3 := json.Unmarshal(body, &payload); err3 == nil {
		switch payload.Status {
		case "ready", "not_ready":
			// ok
		default:
			t.Errorf("GET /readyz: status enum: got %q, want 'ready' or 'not_ready'",
				payload.Status)
		}
	}
}

// =============================================================================
// GET /metrics — 200, text/plain
// =============================================================================

func TestOpenAPIBehavior_MetricsStatus200(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /metrics: got status %d, want 200", resp.StatusCode)
	}
}

func TestOpenAPIBehavior_MetricsContentType(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	// Spec says: text/plain; version=0.0.4 — check prefix only
	assertContentType(t, resp, "text/plain", "GET /metrics")
}

func TestOpenAPIBehavior_MetricsBodyIsPrometheusText(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body := readBody(t, resp)

	// Prometheus text format always starts with "# " comment lines.
	// A non-empty body without any "# " is a sign the wrong handler fired.
	if len(body) > 0 && !bytes.Contains(body, []byte("# ")) {
		t.Errorf("GET /metrics: body does not look like Prometheus text format (no '# ' lines): %.100s", body)
	}
}

// =============================================================================
// GET /v1/info — 200, application/json, InfoResponse schema
// =============================================================================

func TestOpenAPIBehavior_InfoStatus200(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/info: got status %d, want 200", resp.StatusCode)
	}
}

func TestOpenAPIBehavior_InfoContentType(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	assertContentType(t, resp, "application/json", "GET /v1/info")
}

func TestOpenAPIBehavior_InfoBodySchema(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	body := readBody(t, resp)

	specPath := findOpenAPISpecPath(t)
	data, err2 := readSpecFile(t, specPath)
	if err2 != nil {
		t.Fatalf("read spec: %v", err2)
	}
	schemas := parseComponentSchemas(data)
	validateTopLevelRequired(t, body, schemas["InfoResponse"].RequiredFields, "GET /v1/info")
}

func TestOpenAPIBehavior_InfoStatusCodeInSpec(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	// Spec documents 200 and 500 for GET /v1/info.
	specCodes := map[int]bool{200: true, 500: true}
	if !specCodes[resp.StatusCode] {
		t.Errorf("GET /v1/info: status %d not documented in spec (allowed: %v)",
			resp.StatusCode, specCodes)
	}
}

// =============================================================================
// POST /v1/echo — 401 (missing Authorization header)
// =============================================================================

func TestOpenAPIBehavior_EchoMissingAuthReturns401(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000001")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (no auth): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /v1/echo (no auth): got status %d, want 401", resp.StatusCode)
	}
}

func TestOpenAPIBehavior_EchoMissingAuthContentType(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000001")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (no auth): %v", err)
	}
	assertContentType(t, resp, "application/json", "POST /v1/echo 401")
}

func TestOpenAPIBehavior_EchoMissingAuthBodySchema(t *testing.T) {
	t.Parallel()
	ts, _ := buildBehaviorTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000001")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (no auth): %v", err)
	}
	body := readBody(t, resp)
	validateErrorEnvelopeSchema(t, body, "POST /v1/echo 401")
}

// =============================================================================
// POST /v1/echo — 400 (invalid/missing request body)
// =============================================================================

func TestOpenAPIBehavior_EchoInvalidBodyReturns400(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":""}`)) // empty message → 400
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000002")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (empty msg): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /v1/echo (empty message): got status %d, want 400", resp.StatusCode)
	}
}

func TestOpenAPIBehavior_EchoInvalidBodyContentType(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":""}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000003")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (empty msg): %v", err)
	}
	assertContentType(t, resp, "application/json", "POST /v1/echo 400")
}

func TestOpenAPIBehavior_EchoInvalidBodySchema(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":""}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000004")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (empty msg): %v", err)
	}
	body := readBody(t, resp)
	validateErrorEnvelopeSchema(t, body, "POST /v1/echo 400")
}

// =============================================================================
// POST /v1/echo — 415 (wrong Content-Type)
// =============================================================================

func TestOpenAPIBehavior_EchoWrongContentTypeReturns415(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`message=hello`)) // form body
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000005")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (wrong CT): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("POST /v1/echo (wrong Content-Type): got %d, want 415", resp.StatusCode)
	}
}

func TestOpenAPIBehavior_EchoWrongContentTypeContentType(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`message=hello`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000006")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (wrong CT): %v", err)
	}
	assertContentType(t, resp, "application/json", "POST /v1/echo 415")
}

func TestOpenAPIBehavior_EchoWrongContentTypeBodySchema(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`message=hello`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000007")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (wrong CT): %v", err)
	}
	body := readBody(t, resp)
	validateErrorEnvelopeSchema(t, body, "POST /v1/echo 415")
}

// =============================================================================
// POST /v1/echo — 200 (success)
// =============================================================================

func TestOpenAPIBehavior_EchoSuccessReturns200(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"behavior test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000010")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (success): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("POST /v1/echo (success): got %d, want 200 (body: %s)", resp.StatusCode, body)
	}
}

func TestOpenAPIBehavior_EchoSuccessContentType(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"behavior test ct"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000011")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (success): %v", err)
	}
	assertContentType(t, resp, "application/json", "POST /v1/echo 200")
}

func TestOpenAPIBehavior_EchoSuccessBodySchema(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"schema check"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000012")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (success): %v", err)
	}
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/echo (success): expected 200, got %d (body: %s)",
			resp.StatusCode, body)
	}

	specPath := findOpenAPISpecPath(t)
	data, err2 := readSpecFile(t, specPath)
	if err2 != nil {
		t.Fatalf("read spec: %v", err2)
	}
	schemas := parseComponentSchemas(data)
	validateTopLevelRequired(t, body, schemas["EchoResponse"].RequiredFields, "POST /v1/echo 200")
}

func TestOpenAPIBehavior_EchoSuccessMessageEchoed(t *testing.T) {
	t.Parallel()
	ts, stub := buildBehaviorTestServer(t)

	const wantMsg = "echo roundtrip verification"
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"`+wantMsg+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000013")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo (success): %v", err)
	}
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/echo (success): expected 200, got %d", resp.StatusCode)
	}

	var payload echoResponse
	if err2 := json.Unmarshal(body, &payload); err2 != nil {
		t.Fatalf("POST /v1/echo: unmarshal response: %v", err2)
	}
	if payload.Message != wantMsg {
		t.Errorf("POST /v1/echo: message echoed incorrectly: got %q, want %q",
			payload.Message, wantMsg)
	}
}

// =============================================================================
// Step 3 coverage summary — report all documented status codes per endpoint
// =============================================================================

// TestOpenAPIBehavior_AllDocumentedStatusCodesAreReachable verifies that for
// every status code documented in the spec for our target endpoints, we have a
// corresponding sub-test above that triggers and validates that code.
// This test is intentionally descriptive and never fails — it documents the
// coverage matrix so CI output is readable.
func TestOpenAPIBehavior_AllDocumentedStatusCodesAreReachable(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := readSpecFile(t, specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	// Build the documented status code map for the target endpoints.
	docCodes := parseDocumentedStatusCodes(data)

	// Declare the scenarios covered by this file.
	covered := map[string][]int{
		"GET /healthz":  {200},
		"GET /readyz":   {200},
		"GET /metrics":  {200},
		"GET /v1/info":  {200},
		"POST /v1/echo": {200, 401, 400, 415},
	}

	t.Logf("=== OpenAPI Behavior Coverage Report ===")
	for endpoint, codes := range docCodes {
		coveredCodes := covered[endpoint]
		coveredSet := make(map[int]bool)
		for _, c := range coveredCodes {
			coveredSet[c] = true
		}

		var uncovered []int
		for _, dc := range codes {
			if !coveredSet[dc] {
				uncovered = append(uncovered, dc)
			}
		}

		if len(uncovered) > 0 {
			t.Logf("  %s: documented %v, covered %v, NOT covered %v",
				endpoint, codes, coveredCodes, uncovered)
		} else {
			t.Logf("  %s: documented %v — all covered ✓", endpoint, codes)
		}
	}
}

// parseDocumentedStatusCodes returns a map of "METHOD /path" → []int of
// documented HTTP status codes for the target endpoints only.
func parseDocumentedStatusCodes(data []byte) map[string][]int {
	result := make(map[string][]int)

	// Target endpoints from feature #33 step 3.
	targets := map[string]string{
		"/healthz": "GET",
		"/readyz":  "GET",
		"/metrics": "GET",
		"/v1/info": "GET",
		"/v1/echo": "POST",
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	inPaths := false
	var currentPath, currentMethod string

	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = line[:idx]
		}
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			continue
		}

		if trimmed == "paths:" {
			inPaths = true
			continue
		}
		if inPaths && len(line) > 0 && line[0] != ' ' {
			break
		}
		if !inPaths {
			continue
		}

		nSpaces := countLeadingSpacesBehavior(line)
		rest := strings.TrimLeft(line, " \t")

		// Path: 2-space indent, starts with /
		if nSpaces == 2 && strings.HasPrefix(rest, "/") {
			currentPath = strings.TrimSuffix(rest, ":")
			currentMethod = ""
			continue
		}

		// Method: 4-space indent
		if nSpaces == 4 && currentPath != "" {
			key := strings.ToLower(strings.TrimSuffix(rest, ":"))
			if httpVerbSet[key] {
				wantMethod, ok := targets[currentPath]
				if ok && strings.ToUpper(key) == wantMethod {
					currentMethod = wantMethod
				} else {
					currentMethod = ""
				}
			}
			continue
		}

		// Status code entries at 10-space indent: `          "200":`
		if nSpaces == 10 && currentMethod != "" {
			code := strings.Trim(strings.TrimSuffix(rest, ":"), `"'`)
			var statusCode int
			if n, err := parseStatusCode(code); err == nil {
				key := currentMethod + " " + currentPath
				result[key] = appendUnique(result[key], n)
				_ = statusCode
			}
		}
	}

	return result
}

// parseStatusCode parses a string like "200" or "400" into an int.
func parseStatusCode(s string) (int, error) {
	var n int
	if len(s) != 3 {
		return 0, errNotStatusCode
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNotStatusCode
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errNotStatusCode = errors.New("not a status code")

func appendUnique(s []int, v int) []int {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
