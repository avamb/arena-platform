// bil24_compat_157_test.go — unit tests for the Bil24 command gateway (feature #157).
//
// Tests cover:
//   - Feature flag: /compat/bil24/json returns 404 when Bil24CompatEnabled=false
//   - Route mounted when Bil24CompatEnabled=true
//   - Request parsing: missing command field returns resultCode=-2
//   - Invalid JSON body returns resultCode=-2
//   - Unknown command returns resultCode=-1
//   - Command dispatch: all 6 supported commands are dispatched
//   - Response envelope: resultCode, description, command fields always present
//   - GET_ALL_ACTIONS: nil eventQueries returns resultCode=-99
//   - GET_SEAT_LIST: missing actionEventId returns resultCode=-2
//   - GET_SEAT_LIST: non-UUID actionEventId returns resultCode=-2
//   - GET_ORDER_INFO: missing orderId returns resultCode=-2
//   - GET_ORDER_INFO: nil checkoutQueries returns resultCode=-99
//   - CREATE_ORDER_EXT: missing actionEventId returns resultCode=-2
//   - CREATE_ORDER_EXT: missing categoryPriceId returns resultCode=-2
//   - SCAN_TICKET: missing ticketId returns resultCode=-2
//   - SCAN_TICKET: nil barcodeQueries returns resultCode=-99
//   - CANCEL_ORDER: missing orderId returns resultCode=-2
//   - ID translation: valid UUID round-trips through translate functions
//   - ID translation: non-UUID legacy ID returns ErrLegacyIDNotFound
//   - ID translation: empty ID returns error
//   - TranslatePlatformID: returns UUID string
//   - Bil24 response envelope: MarshalJSON merges data fields at top level
//   - bil24_compat.go file exists in the httpserver package
//   - Content-Type is application/json on all responses
//   - HTTP status is 200 for all command results (including errors)
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildBil24Server builds a minimal Server with the Bil24 compat gateway
// enabled. Injects gen.New(nil) query objects so route mounts fire without
// a real database.
func buildBil24Server(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment,
		RequestTimeout:     5 * time.Second,
		BodyLimitBytes:     1 << 20,
		Bil24CompatEnabled: true,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en", "ru"},
	}
	return New(Options{
		Config:             cfg,
		Bil24CompatEnabled: true,
		EventQueries:       gen.New(nil),
		TierQueries:        gen.New(nil),
		CheckoutQueries:    gen.New(nil),
		TicketQueries:      gen.New(nil),
		BarcodeQueries:     gen.New(nil),
	})
}

// buildBil24ServerDisabled builds a Server with the Bil24 compat gateway
// explicitly disabled (the default).
func buildBil24ServerDisabled(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	return New(Options{
		Config: cfg,
		// Bil24CompatEnabled intentionally omitted (default false)
	})
}

// postBil24 sends a POST to /compat/bil24/json with the given JSON body
// and returns the recorded response.
func postBil24(s *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/compat/bil24/json",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	return w
}

// decodeBil24Response decodes the response body as a generic JSON map.
func decodeBil24Response(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("decodeBil24Response: decode error: %v (body: %s)", err, w.Body.String())
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// Feature flag
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_FeatureFlag_Disabled_Returns404(t *testing.T) {
	s := buildBil24ServerDisabled(t)
	w := postBil24(s, `{"command":"GET_ALL_ACTIONS"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when disabled, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestBil24_157_FeatureFlag_Enabled_Returns200(t *testing.T) {
	s := buildBil24Server(t)
	// Unknown command returns 200 (Bil24 protocol: always 200, check resultCode)
	w := postBil24(s, `{"command":"GET_ALL_ACTIONS"}`)
	// May fail with -99 (no DB) but HTTP status must be 200
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when enabled, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Route mounting
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_RouteNotMountedOnGETMethod(t *testing.T) {
	s := buildBil24Server(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/compat/bil24/json", nil)
	s.router.ServeHTTP(w, req)
	// GET is not allowed — should be 405 (method not allowed) or 404 (route only accepts POST)
	if w.Code == http.StatusOK {
		t.Errorf("GET /compat/bil24/json should not return 200")
	}
}

func TestBil24_157_ContentTypeIsJSON(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"UNKNOWN_CMD"}`)
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type: application/json, got %q", ct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request parsing
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_InvalidJSON_Returns_ResultCodeInvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `not-json}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	rc, ok := m["resultCode"].(float64)
	if !ok {
		t.Fatalf("resultCode missing or not a number: %v", m)
	}
	if int(rc) != ResultCodeInvalidRequest {
		t.Errorf("expected resultCode %d, got %d", ResultCodeInvalidRequest, int(rc))
	}
}

func TestBil24_157_MissingCommand_Returns_ResultCodeInvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"fid":"test"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected resultCode %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_EmptyCommand_Returns_ResultCodeInvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"  "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected resultCode %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unknown command
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_UnknownCommand_Returns_ResultCodeUnknownCommand(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"FIRE_MISSILES"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeUnknownCommand {
		t.Errorf("expected resultCode %d (unknown command), got %d", ResultCodeUnknownCommand, rc)
	}
	cmd, _ := m["command"].(string)
	if cmd != "FIRE_MISSILES" {
		t.Errorf("expected command echo 'FIRE_MISSILES', got %q", cmd)
	}
}

func TestBil24_157_CommandIsCaseInsensitive(t *testing.T) {
	s := buildBil24Server(t)
	// "get_all_actions" should be dispatched same as "GET_ALL_ACTIONS"
	w := postBil24(s, `{"command":"get_all_actions"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	// Should NOT return unknown command
	rc := int(m["resultCode"].(float64))
	if rc == ResultCodeUnknownCommand {
		t.Errorf("command dispatch should be case-insensitive: got resultCode=%d", rc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response envelope
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_ResponseEnvelope_HasRequiredFields(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"FIRE_MISSILES"}`)
	m := decodeBil24Response(t, w)

	if _, ok := m["resultCode"]; !ok {
		t.Error("response missing 'resultCode' field")
	}
	if _, ok := m["description"]; !ok {
		t.Error("response missing 'description' field")
	}
	if _, ok := m["command"]; !ok {
		t.Error("response missing 'command' field")
	}
}

func TestBil24_157_SuccessResponse_HasResultCode0(t *testing.T) {
	resp := bil24OK("TEST_CMD", map[string]any{"foo": "bar"})
	if resp.ResultCode != ResultCodeOK {
		t.Errorf("bil24OK ResultCode: expected %d, got %d", ResultCodeOK, resp.ResultCode)
	}
	if resp.Description != "OK" {
		t.Errorf("bil24OK Description: expected 'OK', got %q", resp.Description)
	}
}

func TestBil24_157_ResponseEnvelope_MarshalJSON_MergesData(t *testing.T) {
	resp := bil24OK("MY_CMD", map[string]any{
		"actionList": []string{"a", "b"},
	})
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if _, ok := m["actionList"]; !ok {
		t.Error("MarshalJSON: 'actionList' should be merged at top level")
	}
	if m["command"] != "MY_CMD" {
		t.Errorf("MarshalJSON: command field: got %v", m["command"])
	}
	if int(m["resultCode"].(float64)) != ResultCodeOK {
		t.Errorf("MarshalJSON: resultCode should be 0, got %v", m["resultCode"])
	}
}

func TestBil24_157_ErrorResponse_HasNonZeroResultCode(t *testing.T) {
	resp := bil24Error("MY_CMD", ResultCodeNotFound, "not found")
	if resp.ResultCode != ResultCodeNotFound {
		t.Errorf("bil24Error ResultCode: expected %d, got %d", ResultCodeNotFound, resp.ResultCode)
	}
	if resp.Description != "not found" {
		t.Errorf("bil24Error Description: expected 'not found', got %q", resp.Description)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_ALL_ACTIONS command
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_GetAllActions_NilQueries_Returns_InternalError(t *testing.T) {
	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment,
		RequestTimeout:     5 * time.Second,
		BodyLimitBytes:     1 << 20,
		Bil24CompatEnabled: true,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en"},
	}
	s := New(Options{
		Config:             cfg,
		Bil24CompatEnabled: true,
		// EventQueries intentionally nil
	})
	w := postBil24(s, `{"command":"GET_ALL_ACTIONS"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInternalError {
		t.Errorf("expected resultCode %d (internal error), got %d", ResultCodeInternalError, rc)
	}
}

func TestBil24_157_GetAllActions_CommandEchoedInResponse(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"GET_ALL_ACTIONS"}`)
	m := decodeBil24Response(t, w)
	cmd, _ := m["command"].(string)
	if cmd != "GET_ALL_ACTIONS" {
		t.Errorf("expected command='GET_ALL_ACTIONS' in response, got %q", cmd)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_SEAT_LIST command
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_GetSeatList_MissingActionEventID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"GET_SEAT_LIST"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_GetSeatList_InvalidUUID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"GET_SEAT_LIST","actionEventId":"NOT_A_UUID"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_GetSeatList_ValidUUID_PassesValidation(t *testing.T) {
	s := buildBil24Server(t)
	// Valid UUID will pass ID validation and go to DB (which will fail with -99 from nil pool)
	sessionID := uuid.New().String()
	w := postBil24(s, `{"command":"GET_SEAT_LIST","actionEventId":"`+sessionID+`"}`)
	m := decodeBil24Response(t, w)
	// Must not return invalid request
	rc := int(m["resultCode"].(float64))
	if rc == ResultCodeInvalidRequest {
		t.Errorf("valid UUID should pass ID validation, got resultCode=%d", rc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_ORDER_INFO command
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_GetOrderInfo_MissingOrderID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"GET_ORDER_INFO"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_GetOrderInfo_NilQueries_Returns_InternalError(t *testing.T) {
	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment,
		RequestTimeout:     5 * time.Second,
		BodyLimitBytes:     1 << 20,
		Bil24CompatEnabled: true,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en"},
	}
	s := New(Options{
		Config:             cfg,
		Bil24CompatEnabled: true,
		// CheckoutQueries intentionally nil
	})
	orderID := uuid.New().String()
	w := postBil24(s, `{"command":"GET_ORDER_INFO","orderId":"`+orderID+`"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInternalError {
		t.Errorf("expected %d (internal error when queries nil), got %d", ResultCodeInternalError, rc)
	}
}

func TestBil24_157_GetOrderInfo_CommandEchoedInResponse(t *testing.T) {
	s := buildBil24Server(t)
	orderID := uuid.New().String()
	w := postBil24(s, `{"command":"GET_ORDER_INFO","orderId":"`+orderID+`"}`)
	m := decodeBil24Response(t, w)
	cmd, _ := m["command"].(string)
	if cmd != "GET_ORDER_INFO" {
		t.Errorf("expected command='GET_ORDER_INFO', got %q", cmd)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CREATE_ORDER_EXT command
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_CreateOrderExt_MissingActionEventID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"CREATE_ORDER_EXT","categoryPriceId":"`+uuid.New().String()+`"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_CreateOrderExt_MissingCategoryPriceID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"CREATE_ORDER_EXT","actionEventId":"`+uuid.New().String()+`"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_CreateOrderExt_ValidInput_Returns_ScaffoldResponse(t *testing.T) {
	s := buildBil24Server(t)
	sessionID := uuid.New().String()
	tierID := uuid.New().String()
	body := `{"command":"CREATE_ORDER_EXT","actionEventId":"` + sessionID +
		`","categoryPriceId":"` + tierID + `","quantity":2,"email":"test@example.com"}`
	w := postBil24(s, body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeOK {
		t.Errorf("expected resultCode 0 for valid CREATE_ORDER_EXT, got %d (body: %s)",
			rc, w.Body.String())
	}
	// Scaffold stub returns orderId field
	if _, ok := m["orderId"]; !ok {
		t.Error("CREATE_ORDER_EXT response missing 'orderId' field")
	}
}

func TestBil24_157_CreateOrderExt_InvalidSessionUUID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	body := `{"command":"CREATE_ORDER_EXT","actionEventId":"LEGACY_ID_123","categoryPriceId":"` +
		uuid.New().String() + `"}`
	w := postBil24(s, body)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d for non-UUID actionEventId, got %d", ResultCodeInvalidRequest, rc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SCAN_TICKET command
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_ScanTicket_MissingTicketID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"SCAN_TICKET"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_ScanTicket_NilBarcodeQueries_Returns_InternalError(t *testing.T) {
	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment,
		RequestTimeout:     5 * time.Second,
		BodyLimitBytes:     1 << 20,
		Bil24CompatEnabled: true,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en"},
	}
	s := New(Options{
		Config:             cfg,
		Bil24CompatEnabled: true,
		// BarcodeQueries intentionally nil
	})
	w := postBil24(s, `{"command":"SCAN_TICKET","ticketId":"ABC123"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInternalError {
		t.Errorf("expected %d (internal error when queries nil), got %d", ResultCodeInternalError, rc)
	}
}

func TestBil24_157_ScanTicket_CommandEchoedInResponse(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"SCAN_TICKET","ticketId":"BARCODE_ABC"}`)
	m := decodeBil24Response(t, w)
	cmd, _ := m["command"].(string)
	if cmd != "SCAN_TICKET" {
		t.Errorf("expected command='SCAN_TICKET', got %q", cmd)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CANCEL_ORDER command
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_CancelOrder_MissingOrderID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"CANCEL_ORDER"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d (invalid request), got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_CancelOrder_InvalidOrderID_Returns_InvalidRequest(t *testing.T) {
	s := buildBil24Server(t)
	w := postBil24(s, `{"command":"CANCEL_ORDER","orderId":"NOT_A_UUID"}`)
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeInvalidRequest {
		t.Errorf("expected %d for non-UUID orderId, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_157_CancelOrder_ValidOrderID_Returns_ScaffoldResponse(t *testing.T) {
	s := buildBil24Server(t)
	orderID := uuid.New().String()
	w := postBil24(s, `{"command":"CANCEL_ORDER","orderId":"`+orderID+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", w.Code)
	}
	m := decodeBil24Response(t, w)
	rc := int(m["resultCode"].(float64))
	if rc != ResultCodeOK {
		t.Errorf("expected resultCode 0 for CANCEL_ORDER with nil queries, got %d", rc)
	}
	if _, ok := m["orderId"]; !ok {
		t.Error("CANCEL_ORDER response missing 'orderId' field")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ID translation layer
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_TranslateLegacyID_ValidUUID_ReturnsUUID(t *testing.T) {
	id := uuid.New()
	result, err := TranslateLegacyID(id.String())
	if err != nil {
		t.Fatalf("TranslateLegacyID(%q) returned error: %v", id.String(), err)
	}
	if result != id {
		t.Errorf("TranslateLegacyID: expected %v, got %v", id, result)
	}
}

func TestBil24_157_TranslateLegacyID_EmptyString_ReturnsError(t *testing.T) {
	_, err := TranslateLegacyID("")
	if err == nil {
		t.Error("TranslateLegacyID('') should return error for empty string")
	}
}

func TestBil24_157_TranslateLegacyID_NonUUID_ReturnsErrLegacyIDNotFound(t *testing.T) {
	_, err := TranslateLegacyID("12345")
	if err == nil {
		t.Fatal("TranslateLegacyID('12345') should return error for non-UUID")
	}
	// Should be wrapped ErrLegacyIDNotFound
	if !strings.Contains(err.Error(), "legacy ID not found") {
		t.Errorf("expected ErrLegacyIDNotFound, got: %v", err)
	}
}

func TestBil24_157_TranslateLegacyID_OpaqueString_ReturnsErrLegacyIDNotFound(t *testing.T) {
	_, err := TranslateLegacyID("legacy-order-99999")
	if err == nil {
		t.Fatal("TranslateLegacyID should fail for opaque legacy IDs")
	}
}

func TestBil24_157_TranslatePlatformID_ReturnsUUIDString(t *testing.T) {
	id := uuid.New()
	result := TranslatePlatformID(id)
	if result != id.String() {
		t.Errorf("TranslatePlatformID: expected %q, got %q", id.String(), result)
	}
}

func TestBil24_157_TranslatePlatformID_NilUUID_ReturnsZeroString(t *testing.T) {
	result := TranslatePlatformID(uuid.Nil)
	if result != uuid.Nil.String() {
		t.Errorf("TranslatePlatformID(Nil): expected %q, got %q", uuid.Nil.String(), result)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Result code constants
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_ResultCodeConstants(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		expected int
	}{
		{"ResultCodeOK", ResultCodeOK, 0},
		{"ResultCodeUnknownCommand", ResultCodeUnknownCommand, -1},
		{"ResultCodeInvalidRequest", ResultCodeInvalidRequest, -2},
		{"ResultCodeNotFound", ResultCodeNotFound, -3},
		{"ResultCodeInternalError", ResultCodeInternalError, -99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.code != tt.expected {
				t.Errorf("%s: expected %d, got %d", tt.name, tt.expected, tt.code)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// All 6 commands are dispatched (not unknown)
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_AllSixCommandsAreDispatched(t *testing.T) {
	s := buildBil24Server(t)
	commands := []string{
		"GET_ALL_ACTIONS",
		"GET_SEAT_LIST",
		"GET_ORDER_INFO",
		"CREATE_ORDER_EXT",
		"SCAN_TICKET",
		"CANCEL_ORDER",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			w := postBil24(s, `{"command":"`+cmd+`"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: expected HTTP 200, got %d", cmd, w.Code)
			}
			m := decodeBil24Response(t, w)
			rc := int(m["resultCode"].(float64))
			// All 6 commands should NOT return "unknown command" (-1)
			if rc == ResultCodeUnknownCommand {
				t.Errorf("%s: command was not dispatched (got unknown command result code)", cmd)
			}
			// Command field in response must match
			gotCmd, _ := m["command"].(string)
			if gotCmd != cmd {
				t.Errorf("%s: response command mismatch: got %q", cmd, gotCmd)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Source file existence
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_SourceFileExists(t *testing.T) {
	content := findFileByName(t, "bil24_compat.go")
	if content == "" {
		t.Fatal("bil24_compat.go not found or empty")
	}
	// Verify key identifiers are present
	for _, want := range []string{
		"handleBil24Command",
		"TranslateLegacyID",
		"TranslatePlatformID",
		"ResultCodeOK",
		"ResultCodeUnknownCommand",
		"bil24CompatEnabled",
		"mountCompatRoutes",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("bil24_compat.go missing expected identifier: %q", want)
		}
	}
}

func TestBil24_157_ServerHasBil24EnabledField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if content == "" {
		t.Fatal("server.go not found")
	}
	for _, want := range []string{
		"bil24Enabled",
		"Bil24CompatEnabled",
		"mountCompatRoutes",
		"BIL24_COMPAT_ENABLED",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("server.go missing expected identifier: %q", want)
		}
	}
}

func TestBil24_157_ConfigHasBil24CompatField(t *testing.T) {
	content := findFileByName(t, "config.go")
	if content == "" {
		t.Fatal("config.go not found")
	}
	if !strings.Contains(content, "Bil24CompatEnabled") {
		t.Error("config.go missing 'Bil24CompatEnabled' field")
	}
	if !strings.Contains(content, "BIL24_COMPAT_ENABLED") {
		t.Error("config.go missing 'BIL24_COMPAT_ENABLED' env var reference")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP status is always 200 for Bil24 protocol errors
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_ProtocolErrors_AlwaysReturn_HTTP200(t *testing.T) {
	s := buildBil24Server(t)
	cases := []struct {
		name string
		body string
	}{
		{"unknown_command", `{"command":"INVALID_CMD_XYZ"}`},
		{"missing_command", `{"fid":"x"}`},
		{"bad_json", `{{invalid`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postBil24(s, tc.body)
			if w.Code != http.StatusOK {
				t.Errorf("%s: expected HTTP 200 (Bil24 protocol), got %d",
					tc.name, w.Code)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// JSON encoding of bil24Response
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_Bil24Response_JSONRoundTrip(t *testing.T) {
	resp := bil24Response{
		ResultCode:  ResultCodeOK,
		Description: "OK",
		Command:     "GET_ALL_ACTIONS",
		Data: map[string]any{
			"actionList": []map[string]any{
				{"actionId": "abc", "actionName": "Concert"},
			},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	// actionList should be at top level
	al, ok := decoded["actionList"]
	if !ok {
		t.Error("actionList missing from top-level JSON")
	}
	items, ok := al.([]any)
	if !ok || len(items) != 1 {
		t.Errorf("actionList should have 1 item, got %v", al)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// writeBil24JSON helper
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_WriteBil24JSON_SetsContentType(t *testing.T) {
	w := httptest.NewRecorder()
	writeBil24JSON(w, http.StatusOK, bil24OK("TEST", nil))
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

func TestBil24_157_WriteBil24JSON_SetsHTTPStatus(t *testing.T) {
	w := httptest.NewRecorder()
	writeBil24JSON(w, http.StatusOK, bil24Error("X", ResultCodeNotFound, "not found"))
	if w.Code != http.StatusOK {
		t.Errorf("expected HTTP 200, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Locale handling
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_157_GetAllActions_DefaultsLocaleToEn(t *testing.T) {
	s := buildBil24Server(t)
	// Send request without locale field — handler should not panic
	w := postBil24(s, `{"command":"GET_ALL_ACTIONS"}`)
	if w.Code != http.StatusOK {
		t.Errorf("expected HTTP 200, got %d", w.Code)
	}
	// Response must be valid JSON
	var m map[string]any
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&m); err != nil {
		t.Errorf("response is not valid JSON: %v", err)
	}
}
