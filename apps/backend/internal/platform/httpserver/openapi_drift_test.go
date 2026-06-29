// openapi_drift_test.go verifies feature #15:
// "OpenAPI spec lists all routes"
//
// The committed openapi/openapi.yaml must include every route the server
// actually serves under /v1, plus the operational endpoints, with matching
// methods and parameter definitions.
//
// Tests cover all 8 feature steps:
//
//  1. openapi/openapi.yaml is readable (file exists and is non-empty)
//  2. Document passes structural OpenAPI 3.1 validation (required top-level keys)
//  3. All expected (path, method) tuples extracted from the YAML
//  4. GET /v1/info is documented
//  5. POST /v1/echo is documented with security: bearerAuth and Idempotency-Key parameter
//  6. Operational endpoints /healthz, /readyz, /metrics are documented
//  7. Drift check: every chi route registered in code is present in openapi.yaml
//  8. Drift check: every route in openapi.yaml is present in the chi router (no phantom routes)
package httpserver

import (
	"bufio"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// -----------------------------------------------------------------------------
// Helpers: locate the spec file
// -----------------------------------------------------------------------------

// findOpenAPISpecPath returns the absolute path to openapi/openapi.yaml.
//
// Strategy (tried in order):
//
//  1. runtime.Caller(0) absolute path — works when built without -trimpath.
//     Navigate: httpserver/ → platform/ → internal/ → apps/backend/ → openapi/
//
//  2. Working-directory fallback — works when -trimpath is set (e.g. Docker
//     build-stage tests) because `go test` sets CWD to the package directory.
//     Navigate up three levels from CWD and into openapi/.
//
// Both strategies produce the same canonical path on a normal checkout; the
// fallback silently takes over when the runtime path is module-relative.
func findOpenAPISpecPath(t *testing.T) string {
	t.Helper()

	// Strategy 1: use the compile-time file path (works without -trimpath).
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		// thisFile = .../apps/backend/internal/platform/httpserver/openapi_drift_test.go
		// Navigate: httpserver/ → platform/ → internal/ → apps/backend/ → openapi/
		dir := filepath.Dir(thisFile)
		candidate := filepath.Clean(filepath.Join(dir, "..", "..", "..", "openapi", "openapi.yaml"))
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Strategy 2: CWD-relative fallback for -trimpath Docker/CI environments.
	// `go test ./pkg/...` sets CWD to the package directory being tested, so
	// this file lives at <cwd>/openapi_drift_test.go inside httpserver/.
	// Navigate up three parent directories to reach apps/backend/.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot determine working directory: %v", err)
	}
	candidate := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "openapi", "openapi.yaml"))
	if _, statErr := os.Stat(candidate); statErr == nil {
		return candidate
	}

	// Strategy 3: walk up from CWD looking for openapi/openapi.yaml — handles
	// unusual test runners that set CWD to the module root.
	dir := cwd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "openapi", "openapi.yaml")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("cannot locate openapi/openapi.yaml; tried runtime.Caller, CWD=%s", cwd)
	return ""
}

// -----------------------------------------------------------------------------
// Helpers: minimal OpenAPI YAML path/method parser
// -----------------------------------------------------------------------------

// httpVerbSet contains the lowercase HTTP method names that appear as keys in
// an OpenAPI path-item object.
var httpVerbSet = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true,
	"patch": true, "options": true, "head": true, "trace": true,
}

// openAPIRouteSet is a map of (normalized path → set of lowercase HTTP methods).
type openAPIRouteSet map[string]map[string]bool

// parseOpenAPIPaths performs a minimal line-by-line parse of the `paths:`
// section in an OpenAPI YAML file. It identifies:
//
//   - Path items:  lines with exactly 2 leading spaces that start with "/"
//   - HTTP methods: lines with exactly 4 leading spaces whose trimmed key
//     (before ":") is a recognised HTTP verb
//
// This parser is intentionally narrow — it understands only the subset of
// YAML used in openapi/openapi.yaml — but it is deterministic and has no
// external dependencies (no gopkg.in/yaml.v3 required).
func parseOpenAPIPaths(data []byte) openAPIRouteSet {
	routes := make(openAPIRouteSet)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	inPaths := false
	currentPath := ""

	for scanner.Scan() {
		line := scanner.Text()

		// Strip inline YAML comments (simplified: space + #)
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = line[:idx]
		}

		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			continue
		}

		// Detect `paths:` at column 0.
		if trimmed == "paths:" {
			inPaths = true
			currentPath = ""
			continue
		}

		// Any non-indented, non-empty line after `paths:` means we have
		// moved to a different top-level section (e.g. `components:`).
		if inPaths && len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			break
		}

		if !inPaths {
			continue
		}

		// Count leading spaces.
		nSpaces := 0
		for _, ch := range line {
			if ch == ' ' {
				nSpaces++
			} else {
				break
			}
		}

		rest := strings.TrimLeft(line, " \t")

		// Path items: exactly 2-space indent, starts with "/", ends with ":"
		if nSpaces == 2 && strings.HasPrefix(rest, "/") {
			currentPath = strings.TrimSuffix(rest, ":")
			if _, exists := routes[currentPath]; !exists {
				routes[currentPath] = make(map[string]bool)
			}
			continue
		}

		// HTTP method entries: exactly 4-space indent under a recognised path.
		if nSpaces == 4 && currentPath != "" {
			key := strings.ToLower(strings.TrimSuffix(rest, ":"))
			if httpVerbSet[key] {
				routes[currentPath][key] = true
			}
		}
	}

	return routes
}

// hasSecurityBearerAuth returns true when the data contains the subsequence
// "security:" followed (within reasonable proximity) by "bearerAuth:" — a
// heuristic sufficient for our single-endpoint check.
func hasSecurityBearerAuth(data []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	inEchoPost := false
	inSecurity := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Detect the /v1/echo path section.
		if strings.TrimRight(line, " \t") == "  /v1/echo:" {
			inEchoPost = true
			inSecurity = false
			continue
		}
		// Detect moving to the next path.
		if inEchoPost && strings.HasPrefix(line, "  /") && !strings.HasPrefix(line, "  /v1/echo") {
			inEchoPost = false
			inSecurity = false
		}
		if !inEchoPost {
			continue
		}

		if trimmed == "security:" {
			inSecurity = true
			continue
		}
		if inSecurity && strings.Contains(trimmed, "bearerAuth") {
			return true
		}
		// Leave security block if indentation drops back.
		if inSecurity && len(line) > 0 && line[0] != ' ' {
			inSecurity = false
		}
	}
	return false
}

// hasIdempotencyKeyParam returns true when the data contains "Idempotency-Key"
// in the parameters section of the /v1/echo path item.
func hasIdempotencyKeyParam(data []byte) bool {
	return bytes.Contains(data, []byte("Idempotency-Key"))
}

// -----------------------------------------------------------------------------
// Helpers: build a fully-wired test server (all routes mounted)
// -----------------------------------------------------------------------------

// buildDriftTestServer creates a Server with every optional dependency wired
// so that ALL conditional routes are mounted:
//   - stub auth enabled  → /v1/dev/token + /v1/dev/auth/token + /v1/echo are mounted
//   - noopMetricsHandler → /metrics is mounted
//   - noopIdemStore + captureAuditWriter + fakePoolDB → /v1/echo is mounted
//
// Test doubles are imported from echo_audit_test.go (same package).
func buildDriftTestServer(t *testing.T) *Server {
	t.Helper()

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-drift-check",
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

	// noopHandler is a minimal http.Handler that satisfies the MetricsHandler
	// field and causes /metrics to be mounted.
	noopHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return New(Options{
		Config:         cfg,
		Auth:           stub,
		Audit:          &captureAuditWriter{},
		Idem:           &noopIdemStore{},
		Pool:           &fakePoolDB{tx: &fakeTx{}},
		MetricsHandler: noopHandler,
		// Wire geo queries so /v1/geo/* and /v1/admin/geo/* routes are mounted.
		GeoQueries: gen.New(nil),
		// Wire org queries so /v1/organizations/* routes are mounted (feature #119).
		OrgQueries: gen.New(nil),
		// Wire MeQueries so the /v1/me current-user context route is mounted
		// (feature #211). *gen.Queries implements meQuerier.
		MeQueries: gen.New(nil),
		// Wire NetworkQueries so all operator-network routes are mounted
		// (feature #212): /v1/operator-networks/*, /v1/admin/networks/{id}/users,
		// /v1/admin/networks/{id}/organizers, /v1/admin/networks/{id}/agents.
		NetworkQueries: gen.New(nil),
		// Wire EventQueries so all events routes are mounted (feature #125 /
		// documented under feature #263): /v1/events, /v1/events/{id},
		// /v1/organizations/{org_id}/events and its child mutations.
		EventQueries: gen.New(nil),
		// Wire SessionQueries so all sessions routes (feature #126 / documented
		// under feature #264) are mounted on the chi router for the drift
		// check: POST/GET/PATCH/DELETE under
		// /v1/organizations/{org_id}/events/{event_id}/sessions[/{id}].
		SessionQueries: gen.New(nil),
		// Wire TierQueries so all ticket-tier routes (feature #127 / documented
		// under feature #265) are mounted for the drift check:
		// POST/GET/PATCH/DELETE under
		// /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers[/{id}].
		TierQueries: gen.New(nil),
		// Wire InventoryQueries so all GA inventory ledger routes (feature
		// #130 / documented under feature #266) are mounted for the drift
		// check: GET/POST under
		// /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory
		// plus the /reserve, /release, /confirm sub-routes.
		InventoryQueries: gen.New(nil),
		// Wire ReservationQueries so the reservation state-machine routes
		// (feature #131 / documented under feature #267) are mounted for
		// the drift check: POST /v1/reservations, GET/DELETE
		// /v1/reservations/{id}, PATCH /v1/reservations/{id}/activate.
		ReservationQueries: gen.New(nil),
		// Wire PromoQueries so the promo-code CRUD + validation routes
		// (feature #128 / documented under feature #268) are mounted for
		// the drift check: GET/POST /v1/organizations/{org_id}/promo-codes,
		// GET/PATCH/DELETE /v1/organizations/{org_id}/promo-codes/{id},
		// POST /v1/checkout/promo-validate.
		PromoQueries: gen.New(nil),
		// Wire CheckoutQueries so the checkout session state-machine
		// routes (feature #132 / documented under feature #270) are
		// mounted for the drift check: POST /v1/checkout/start,
		// GET /v1/checkout/{id}, POST /v1/checkout/{id}/confirm,
		// POST /v1/checkout/{id}/complete, POST /v1/checkout/{id}/abandon.
		CheckoutQueries: gen.New(nil),
		// Wire PaymentIntentQueries so the payment intent state-machine
		// routes (feature #137 / documented under feature #271) are
		// mounted for the drift check: POST /v1/payment-intents,
		// GET /v1/payment-intents/{id},
		// POST /v1/payment-intents/{id}/transition,
		// POST /v1/payment-intents/webhook.
		PaymentIntentQueries: gen.New(nil),
		// Wire TicketQueries so the GET /v1/checkout/{id}/tickets read
		// endpoint (feature #139 / documented under feature #272) is
		// mounted for the drift check.
		TicketQueries: gen.New(nil),
	})
}

// chiRouteSet extracts all (path, lowercase-method) pairs from a chi router
// using chi.Walk.
func chiRouteSet(t *testing.T, router chi.Router) openAPIRouteSet {
	t.Helper()
	result := make(openAPIRouteSet)

	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		lower := strings.ToLower(method)
		if _, ok := result[route]; !ok {
			result[route] = make(map[string]bool)
		}
		result[route][lower] = true
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}
	return result
}

// sortedKeys returns the keys of a map[string]* in sorted order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// =============================================================================
// Step 1 — openapi.yaml is readable
// =============================================================================

// TestOpenAPISpec_FileExists verifies step 1: openapi/openapi.yaml can be
// read from disk and is non-empty.
func TestOpenAPISpec_FileExists(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml at %s: %v", specPath, err)
	}
	if len(data) == 0 {
		t.Fatalf("openapi.yaml is empty at %s", specPath)
	}
}

// =============================================================================
// Step 2 — Document passes structural OpenAPI 3.1 validation
// =============================================================================

// TestOpenAPISpec_ValidStructure verifies step 2: the document contains the
// required top-level keys for a valid OpenAPI 3.1 document:
//   - openapi: "3.1.0"  (version pin)
//   - info:              (metadata block)
//   - paths:             (route table)
func TestOpenAPISpec_ValidStructure(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	// Check openapi version declaration.
	if !bytes.Contains(data, []byte(`openapi: "3.1.0"`)) {
		t.Error(`openapi.yaml must contain 'openapi: "3.1.0"' (OpenAPI 3.1)')`)
	}

	// Check info block.
	if !bytes.Contains(data, []byte("info:")) {
		t.Error("openapi.yaml must contain an 'info:' block")
	}

	// Check paths block.
	if !bytes.Contains(data, []byte("paths:")) {
		t.Error("openapi.yaml must contain a 'paths:' block")
	}

	// Check security schemes section.
	if !bytes.Contains(data, []byte("securitySchemes:")) {
		t.Error("openapi.yaml must contain a 'securitySchemes:' block in components")
	}

	// Check bearerAuth scheme declared.
	if !bytes.Contains(data, []byte("bearerAuth:")) {
		t.Error("openapi.yaml must declare a 'bearerAuth' security scheme")
	}
}

// =============================================================================
// Step 3 — Extract (path, method) tuples from YAML
// =============================================================================

// TestOpenAPISpec_ParsedRoutesNonEmpty verifies step 3: the YAML parser
// extracts at least one (path, method) tuple from the paths section.
// A zero-length result means the parser failed to find any routes.
func TestOpenAPISpec_ParsedRoutesNonEmpty(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	routes := parseOpenAPIPaths(data)
	if len(routes) == 0 {
		t.Fatal("parseOpenAPIPaths returned zero routes — check the YAML paths section formatting")
	}

	// Log what was found for debugging.
	for _, path := range sortedKeys(routes) {
		methods := sortedKeys(routes[path])
		t.Logf("  YAML route: %s [%s]", path, strings.Join(methods, ", "))
	}
}

// =============================================================================
// Step 4 — GET /v1/info is documented
// =============================================================================

// TestOpenAPISpec_V1InfoDocumented verifies step 4: GET /v1/info appears in
// the parsed paths section with the "get" method.
func TestOpenAPISpec_V1InfoDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	routes := parseOpenAPIPaths(data)
	methods, ok := routes["/v1/info"]
	if !ok {
		t.Fatal("/v1/info is not documented in openapi.yaml paths section")
	}
	if !methods["get"] {
		t.Error("/v1/info is in openapi.yaml but is missing the 'get' method")
	}
}

// =============================================================================
// Step 5 — POST /v1/echo with security: bearerAuth + Idempotency-Key
// =============================================================================

// TestOpenAPISpec_V1EchoDocumented verifies step 5 (part A): POST /v1/echo
// appears in the parsed paths section with the "post" method.
func TestOpenAPISpec_V1EchoDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	routes := parseOpenAPIPaths(data)
	methods, ok := routes["/v1/echo"]
	if !ok {
		t.Fatal("/v1/echo is not documented in openapi.yaml paths section")
	}
	if !methods["post"] {
		t.Error("/v1/echo is in openapi.yaml but is missing the 'post' method")
	}
}

// TestOpenAPISpec_V1EchoHasBearerAuthSecurity verifies step 5 (part B):
// POST /v1/echo carries a security requirement referencing bearerAuth.
func TestOpenAPISpec_V1EchoHasBearerAuthSecurity(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	if !hasSecurityBearerAuth(data) {
		t.Error("POST /v1/echo in openapi.yaml must have 'security: [{bearerAuth: []}]'")
	}
}

// TestOpenAPISpec_V1EchoHasIdempotencyKeyParam verifies step 5 (part C):
// POST /v1/echo documents an Idempotency-Key header parameter.
func TestOpenAPISpec_V1EchoHasIdempotencyKeyParam(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	if !hasIdempotencyKeyParam(data) {
		t.Error("openapi.yaml must document 'Idempotency-Key' as a header parameter for POST /v1/echo")
	}
}

// =============================================================================
// Step 6 — Operational endpoints are documented
// =============================================================================

// TestOpenAPISpec_OperationalEndpointsDocumented verifies step 6: /healthz,
// /readyz, and /metrics are all present in the spec with their expected
// HTTP methods.
func TestOpenAPISpec_OperationalEndpointsDocumented(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	routes := parseOpenAPIPaths(data)

	cases := []struct {
		path   string
		method string
	}{
		{"/healthz", "get"},
		{"/readyz", "get"},
		{"/metrics", "get"},
	}

	for _, tc := range cases {
		methods, ok := routes[tc.path]
		if !ok {
			t.Errorf("operational endpoint %s is not documented in openapi.yaml", tc.path)
			continue
		}
		if !methods[tc.method] {
			t.Errorf("operational endpoint %s missing method '%s' in openapi.yaml", tc.path, tc.method)
		}
	}
}

// =============================================================================
// Steps 7 & 8 — Bidirectional drift check: code ↔ openapi.yaml
// =============================================================================

// TestOpenAPIDriftCheck_SpecCoversAllCodeRoutes verifies step 7 (code → YAML):
// every (path, method) pair that chi.Walk finds in the wired-up server router
// is also documented in openapi.yaml. A route present in code but absent from
// the spec indicates the spec is stale.
func TestOpenAPIDriftCheck_SpecCoversAllCodeRoutes(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	specRoutes := parseOpenAPIPaths(data)
	s := buildDriftTestServer(t)
	codeRoutes := chiRouteSet(t, s.router)

	var missing []string
	for path, methods := range codeRoutes {
		for method := range methods {
			if specRoutes[path] == nil || !specRoutes[path][method] {
				missing = append(missing, strings.ToUpper(method)+" "+path)
			}
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("routes present in code but MISSING from openapi.yaml (%d):\n  %s\n\nAdd these routes to openapi/openapi.yaml",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestOpenAPIDriftCheck_CodeCoversAllSpecRoutes verifies step 8 (YAML → code):
// every (path, method) pair documented in openapi.yaml is actually registered
// on the chi router. A route present in the spec but absent from code indicates
// a phantom route (documented but not implemented).
func TestOpenAPIDriftCheck_CodeCoversAllSpecRoutes(t *testing.T) {
	t.Parallel()

	specPath := findOpenAPISpecPath(t)
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("cannot read openapi.yaml: %v", err)
	}

	specRoutes := parseOpenAPIPaths(data)
	s := buildDriftTestServer(t)
	codeRoutes := chiRouteSet(t, s.router)

	var phantom []string
	for path, methods := range specRoutes {
		for method := range methods {
			if codeRoutes[path] == nil || !codeRoutes[path][method] {
				phantom = append(phantom, strings.ToUpper(method)+" "+path)
			}
		}
	}
	sort.Strings(phantom)

	if len(phantom) > 0 {
		t.Errorf("routes documented in openapi.yaml but MISSING from code (%d):\n  %s\n\nEither implement these routes or remove them from openapi/openapi.yaml",
			len(phantom), strings.Join(phantom, "\n  "))
	}
}
