// Package compat_bil24_test — Bil24 compatibility regression tests (feature #158).
//
// These contract tests capture the request/response behavior of the Bil24
// gateway as experienced by the Vino&Co partner integration. Fixtures are
// loaded from testdata/vinoandco_fixtures.json; each fixture represents a
// real-world-style request that the Vino&Co WordPress plugin / widget sends.
//
// Goals:
//   - Pin the Bil24 wire protocol so future refactors cannot accidentally
//     break the Vino&Co integration.
//   - Document which behaviors differ from the legacy Bil24.pro API (see
//     BEHAVIOR_DIFFERENCES.md in this directory).
//   - Provide a fast, database-free suite that can run in CI without
//     external infrastructure.
//
// All tests in this file are named TestCompatBil24_158_* to match the
// feature number and make them easy to grep in CI output.
//
// Fixture schema (testdata/vinoandco_fixtures.json):
//
//	{
//	  "name":          string  — unique fixture identifier
//	  "description":   string  — why this fixture exists (Vino&Co context)
//	  "request":       object  — JSON body to POST to /compat/bil24/json
//	  "expect": {
//	    "http_status":           int     — expected HTTP status code
//	    "result_code_eq":        int     — expected resultCode (omit if not asserting equality)
//	    "result_code_not_eq":    int     — resultCode must NOT equal this value
//	    "result_code_not_eq_2":  int     — additional "not equal" constraint
//	    "command_echo":          string  — expected "command" field in response
//	    "content_type_prefix":   string  — Content-Type header must start with this
//	    "required_response_fields": []string — these keys must be present in the response
//	  }
//	}
package compat_bil24_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture types
// ─────────────────────────────────────────────────────────────────────────────

// fixtureExpect holds the assertion rules for a single fixture.
type fixtureExpect struct {
	// HTTPStatus is the expected HTTP response status code. Default 200.
	HTTPStatus int `json:"http_status"`
	// ResultCodeEq asserts that resultCode == this value (only checked if != 0
	// or if the fixture explicitly sets it alongside a zero value sentinel).
	ResultCodeEq *int `json:"result_code_eq,omitempty"`
	// ResultCodeNotEq asserts that resultCode != this value.
	ResultCodeNotEq *int `json:"result_code_not_eq,omitempty"`
	// ResultCodeNotEq2 is a second "not equal" constraint.
	ResultCodeNotEq2 *int `json:"result_code_not_eq_2,omitempty"`
	// CommandEcho asserts that the "command" field in the response equals this.
	CommandEcho string `json:"command_echo,omitempty"`
	// ContentTypePrefix asserts that Content-Type starts with this prefix.
	ContentTypePrefix string `json:"content_type_prefix,omitempty"`
	// RequiredResponseFields lists JSON keys that must be present in the response.
	RequiredResponseFields []string `json:"required_response_fields,omitempty"`
}

// fixture is a single request/response contract test case.
type fixture struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Request     json.RawMessage `json:"request"`
	Expect      fixtureExpect   `json:"expect"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory
// ─────────────────────────────────────────────────────────────────────────────

// buildCompatServer builds a minimal httpserver.Server with the Bil24
// compatibility gateway enabled. Uses gen.New(nil) query stubs so the
// server constructs successfully without a real database.
func buildCompatServer(t *testing.T) *httpserver.Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment,
		RequestTimeout:     5 * time.Second,
		BodyLimitBytes:     1 << 20,
		Bil24CompatEnabled: true,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en", "ru"},
	}
	return httpserver.New(httpserver.Options{
		Config:             cfg,
		Bil24CompatEnabled: true,
		// Inject stub query objects so route mounts succeed.
		// DB calls will panic with nil pool, which handleBil24Command recovers.
		EventQueries:    gen.New(nil),
		TierQueries:     gen.New(nil),
		CheckoutQueries: gen.New(nil),
		TicketQueries:   gen.New(nil),
		BarcodeQueries:  gen.New(nil),
	})
}

// postToCompat sends a POST to /compat/bil24/json with the given raw JSON body
// and returns the recorder.
func postToCompat(srv *httpserver.Server, body json.RawMessage) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/compat/bil24/json", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Router().ServeHTTP(w, req)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixture loader
// ─────────────────────────────────────────────────────────────────────────────

// loadFixtures reads and parses testdata/vinoandco_fixtures.json.
func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	path := filepath.Join("testdata", "vinoandco_fixtures.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("loadFixtures: cannot read %s: %v", path, err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("loadFixtures: parse error in %s: %v", path, err)
	}
	if len(fixtures) == 0 {
		t.Fatalf("loadFixtures: no fixtures found in %s", path)
	}
	return fixtures
}

// ─────────────────────────────────────────────────────────────────────────────
// Core contract test: run every fixture
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_158_VinoAndCoFixtures runs all fixtures from
// testdata/vinoandco_fixtures.json against the Bil24 gateway.
// This is the primary regression guard for the Vino&Co integration.
func TestCompatBil24_158_VinoAndCoFixtures(t *testing.T) {
	srv := buildCompatServer(t)
	fixtures := loadFixtures(t)

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			t.Parallel()
			w := postToCompat(srv, fx.Request)

			// ── HTTP status ──────────────────────────────────────────────────
			wantStatus := fx.Expect.HTTPStatus
			if wantStatus == 0 {
				wantStatus = http.StatusOK // Bil24 protocol default
			}
			if w.Code != wantStatus {
				t.Errorf("[%s] HTTP status: got %d, want %d (body: %s)",
					fx.Name, w.Code, wantStatus, w.Body.String())
			}

			// ── Content-Type ────────────────────────────────────────────────
			if prefix := fx.Expect.ContentTypePrefix; prefix != "" {
				ct := w.Header().Get("Content-Type")
				if !strings.HasPrefix(ct, prefix) {
					t.Errorf("[%s] Content-Type: got %q, want prefix %q",
						fx.Name, ct, prefix)
				}
			}

			// ── Parse JSON response ──────────────────────────────────────────
			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("[%s] response is not valid JSON: %v (body: %s)",
					fx.Name, err, w.Body.String())
			}

			// ── Required fields ──────────────────────────────────────────────
			for _, field := range fx.Expect.RequiredResponseFields {
				if _, ok := resp[field]; !ok {
					t.Errorf("[%s] response missing required field %q; got keys: %v",
						fx.Name, field, mapKeys(resp))
				}
			}

			// ── resultCode ───────────────────────────────────────────────────
			rcRaw, hasRC := resp["resultCode"]
			if !hasRC {
				t.Errorf("[%s] response missing 'resultCode' field", fx.Name)
				return
			}
			rc, ok := rcRaw.(float64)
			if !ok {
				t.Errorf("[%s] 'resultCode' is not a number: %T %v", fx.Name, rcRaw, rcRaw)
				return
			}
			resultCode := int(rc)

			if fx.Expect.ResultCodeEq != nil {
				if resultCode != *fx.Expect.ResultCodeEq {
					t.Errorf("[%s] resultCode: got %d, want %d",
						fx.Name, resultCode, *fx.Expect.ResultCodeEq)
				}
			}
			if fx.Expect.ResultCodeNotEq != nil {
				if resultCode == *fx.Expect.ResultCodeNotEq {
					t.Errorf("[%s] resultCode must not be %d, but got %d",
						fx.Name, *fx.Expect.ResultCodeNotEq, resultCode)
				}
			}
			if fx.Expect.ResultCodeNotEq2 != nil {
				if resultCode == *fx.Expect.ResultCodeNotEq2 {
					t.Errorf("[%s] resultCode must not be %d (second constraint), but got %d",
						fx.Name, *fx.Expect.ResultCodeNotEq2, resultCode)
				}
			}

			// ── command echo ─────────────────────────────────────────────────
			if want := fx.Expect.CommandEcho; want != "" {
				got, _ := resp["command"].(string)
				if got != want {
					t.Errorf("[%s] command echo: got %q, want %q", fx.Name, got, want)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixture file integrity tests
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_158_FixtureFileExists verifies that the fixture file is
// present and parseable — guard against accidental deletion.
func TestCompatBil24_158_FixtureFileExists(t *testing.T) {
	fixtures := loadFixtures(t)
	t.Logf("loaded %d Vino&Co fixtures", len(fixtures))
	if len(fixtures) < 10 {
		t.Errorf("expected at least 10 Vino&Co fixtures, got %d — add more coverage", len(fixtures))
	}
}

// TestCompatBil24_158_FixtureNamesAreUnique checks that each fixture has a
// unique name — duplicate names cause t.Run subtests to collide.
func TestCompatBil24_158_FixtureNamesAreUnique(t *testing.T) {
	fixtures := loadFixtures(t)
	seen := make(map[string]bool, len(fixtures))
	for _, fx := range fixtures {
		if fx.Name == "" {
			t.Errorf("fixture with empty name found; each fixture must have a unique non-empty name")
			continue
		}
		if seen[fx.Name] {
			t.Errorf("duplicate fixture name: %q", fx.Name)
		}
		seen[fx.Name] = true
	}
}

// TestCompatBil24_158_FixtureRequestsAreValidJSON verifies that every fixture
// request field is valid JSON (not accidentally truncated or malformed).
func TestCompatBil24_158_FixtureRequestsAreValidJSON(t *testing.T) {
	fixtures := loadFixtures(t)
	for _, fx := range fixtures {
		var m map[string]any
		if err := json.Unmarshal(fx.Request, &m); err != nil {
			t.Errorf("fixture %q has invalid JSON request: %v", fx.Name, err)
		}
	}
}

// TestCompatBil24_158_FixtureDescriptionsPresent verifies every fixture has a
// description — these serve as living documentation of the Vino&Co integration.
func TestCompatBil24_158_FixtureDescriptionsPresent(t *testing.T) {
	fixtures := loadFixtures(t)
	for _, fx := range fixtures {
		if strings.TrimSpace(fx.Description) == "" {
			t.Errorf("fixture %q missing description — add context for why this fixture exists", fx.Name)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Protocol invariant tests (not fixture-driven)
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_158_AllKnownCommandsReturn200 verifies that every known Bil24
// command returns HTTP 200 regardless of data availability. Vino&Co legacy
// clients check HTTP 200 before reading resultCode.
func TestCompatBil24_158_AllKnownCommandsReturn200(t *testing.T) {
	srv := buildCompatServer(t)
	commands := []string{
		"GET_ALL_ACTIONS",
		"GET_SEAT_LIST",
		"GET_ORDER_INFO",
		"CREATE_ORDER_EXT",
		"SCAN_TICKET",
		"CANCEL_ORDER",
	}
	for _, cmd := range commands {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"command": cmd,
				"fid":     "1271",
				"token":   "7c696b4af364928202dd",
				"locale":  "ru-RU",
			})
			w := postToCompat(srv, body)
			if w.Code != http.StatusOK {
				t.Errorf("%s: expected HTTP 200, got %d (body: %s)",
					cmd, w.Code, w.Body.String())
			}
		})
	}
}

// TestCompatBil24_158_GatewayDisabledReturns404 verifies that when
// BIL24_COMPAT_ENABLED is false, the /compat/bil24/json route does not exist.
// This prevents accidental exposure of the legacy gateway in production
// environments that haven't opted in.
func TestCompatBil24_158_GatewayDisabledReturns404(t *testing.T) {
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	srv := httpserver.New(httpserver.Options{
		Config: cfg,
		// Bil24CompatEnabled intentionally omitted (default false)
	})
	body, _ := json.Marshal(map[string]string{"command": "GET_ALL_ACTIONS"})
	w := postToCompat(srv, body)
	if w.Code != http.StatusNotFound {
		t.Errorf("disabled gateway: expected 404, got %d", w.Code)
	}
}

// TestCompatBil24_158_ResponseEnvelopeShape verifies that the response envelope
// shape matches the documented Bil24 wire format: resultCode (int), description
// (string), command (string). This is the minimal shape all Vino&Co clients
// depend on.
func TestCompatBil24_158_ResponseEnvelopeShape(t *testing.T) {
	srv := buildCompatServer(t)
	body, _ := json.Marshal(map[string]string{
		"command": "GET_ALL_ACTIONS",
		"fid":     "1271",
		"token":   "7c696b4af364928202dd",
		"locale":  "ru-RU",
	})
	w := postToCompat(srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// resultCode must be a number (JSON number → float64 in Go)
	if rc, ok := resp["resultCode"].(float64); !ok {
		t.Errorf("resultCode is missing or not a number: %T %v", resp["resultCode"], resp["resultCode"])
	} else {
		_ = rc // value checked by individual fixtures
	}

	// description must be a string
	if _, ok := resp["description"].(string); !ok {
		t.Errorf("description is missing or not a string: %T %v", resp["description"], resp["description"])
	}

	// command must be a string and must echo the request command
	if cmd, ok := resp["command"].(string); !ok {
		t.Errorf("command is missing or not a string: %T %v", resp["command"], resp["command"])
	} else if cmd != "GET_ALL_ACTIONS" {
		t.Errorf("command echo: got %q, want %q", cmd, "GET_ALL_ACTIONS")
	}
}

// TestCompatBil24_158_MalformedJSONReturnsResultCode_Neg2 verifies that
// malformed JSON from a buggy Vino&Co client returns resultCode=-2 and
// HTTP 200, not a 400/500 error that the legacy client would not handle.
func TestCompatBil24_158_MalformedJSONReturnsResultCode_Neg2(t *testing.T) {
	srv := buildCompatServer(t)

	cases := []struct {
		name string
		body string
	}{
		{"truncated_json", `{"command":"GET_ALL_ACTIONS"`},
		{"invalid_json", `not-json-at-all`},
		{"json_array_not_object", `[1,2,3]`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := postToCompat(srv, json.RawMessage(tc.body))
			if w.Code != http.StatusOK {
				t.Errorf("[%s] HTTP status: got %d, want 200", tc.name, w.Code)
			}
			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("[%s] response is not valid JSON: %v", tc.name, err)
			}
			rc := int(resp["resultCode"].(float64))
			if rc != -2 {
				t.Errorf("[%s] resultCode: got %d, want -2 (invalid request)", tc.name, rc)
			}
		})
	}
}

// TestCompatBil24_158_CommandDispatchIsNotUnknown verifies that each supported
// Bil24 command is actually dispatched (does not return resultCode=-1 "unknown
// command"). This catches the case where a new command mapping is accidentally
// removed.
func TestCompatBil24_158_CommandDispatchIsNotUnknown(t *testing.T) {
	srv := buildCompatServer(t)
	type testcase struct {
		cmd    string
		extras map[string]any
	}
	cases := []testcase{
		{"GET_ALL_ACTIONS", nil},
		{"GET_SEAT_LIST", map[string]any{"actionEventId": "018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2b3"}},
		{"GET_ORDER_INFO", map[string]any{"orderId": "018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2b4"}},
		{"CREATE_ORDER_EXT", map[string]any{
			"actionEventId":   "018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2b5",
			"categoryPriceId": "018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2b6",
		}},
		{"SCAN_TICKET", map[string]any{"ticketId": "BCR-VNC-001"}},
		{"CANCEL_ORDER", map[string]any{"orderId": "018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2b8"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.cmd, func(t *testing.T) {
			payload := map[string]any{
				"command": tc.cmd,
				"fid":     "1271",
				"token":   "7c696b4af364928202dd",
				"locale":  "ru-RU",
			}
			for k, v := range tc.extras {
				payload[k] = v
			}
			body, _ := json.Marshal(payload)
			w := postToCompat(srv, body)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: HTTP status %d", tc.cmd, w.Code)
			}
			var resp map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			rc := int(resp["resultCode"].(float64))
			if rc == -1 {
				t.Errorf("%s: resultCode=-1 means command is not dispatched — check switch statement", tc.cmd)
			}
		})
	}
}

// TestCompatBil24_158_BehaviorDifferencesDocExists verifies that the
// BEHAVIOR_DIFFERENCES.md document exists alongside the test fixture.
// This document is required so developers understand intentional deviations
// from the legacy Bil24.pro API.
func TestCompatBil24_158_BehaviorDifferencesDocExists(t *testing.T) {
	path := filepath.Join("BEHAVIOR_DIFFERENCES.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("BEHAVIOR_DIFFERENCES.md not found: %v — create this file to document intentional behavior changes", err)
	}
	if info.Size() < 100 {
		t.Errorf("BEHAVIOR_DIFFERENCES.md is suspiciously small (%d bytes) — it should document known differences", info.Size())
	}
}

// TestCompatBil24_158_FixtureFileTestdataDir verifies the testdata directory
// structure is correct — future contributors adding fixtures need to know where
// to place them.
func TestCompatBil24_158_FixtureFileTestdataDir(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("testdata directory not found: %v", err)
	}
	var hasFixtures bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_fixtures.json") {
			hasFixtures = true
		}
	}
	if !hasFixtures {
		t.Error("testdata/ directory exists but contains no *_fixtures.json files")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper utilities
// ─────────────────────────────────────────────────────────────────────────────

// mapKeys returns the sorted key names of a map (for error messages).
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
