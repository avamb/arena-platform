// x_request_id_test.go verifies feature #61:
// "X-Request-Id header present in every response"
//
// Every HTTP response — success or error, including 404, 405, 500 — must carry
// an X-Request-Id header whose value is a valid UUID. A valid UUID supplied by
// the client is preserved; an invalid (non-UUID) inbound value is replaced by a
// fresh server-generated UUIDv7.
//
// Steps verified:
//
//  1. GET /v1/info              → X-Request-Id header present
//  2. Header value is a valid UUID (uuid.Parse succeeds)
//  3. GET /does-not-exist       → 404 with X-Request-Id header
//  4. GET /v1/debug/panic       → 500 with X-Request-Id header
//  5. Send X-Request-Id: <valid-uuid> → response echoes the same value
//  6. Send X-Request-Id: not-a-uuid   → server generates a fresh UUID
//  7. slog log lines include request_id matching the X-Request-Id header
package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// =============================================================================
// Helpers
// =============================================================================

// buildRequestIDTestServer creates a minimal Server suitable for most X-Request-Id
// tests — no database, no auth, no debug routes.
func buildRequestIDTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	return New(Options{Config: cfg})
}

// buildRequestIDTestServerWithLogger creates a Server that writes structured
// JSON log output to buf so tests can parse log lines and assert on request_id.
func buildRequestIDTestServerWithLogger(t *testing.T, logger *slog.Logger) *Server {
	t.Helper()
	cfg := &config.Config{
		HTTPListenAddr: "127.0.0.1:0",
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	return New(Options{Config: cfg, Logger: logger})
}

// isValidUUID returns true when s parses as a valid UUID (any version).
func isValidUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// =============================================================================
// Step 1 — GET /v1/info has X-Request-Id header
// =============================================================================

// TestXRequestID_InfoResponseHasHeader verifies step 1: GET /v1/info response
// carries a non-empty X-Request-Id header.
func TestXRequestID_InfoResponseHasHeader(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if got := rr.Header().Get(httpadapter.HeaderRequestID); got == "" {
		t.Fatal("GET /v1/info: X-Request-Id response header must be non-empty")
	}
}

// TestXRequestID_InfoResponseHasHeaderOnAnyStatus verifies that the header is
// present regardless of whether /v1/info returns 200 or an error status.
func TestXRequestID_InfoResponseHasHeaderOnAnyStatus(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Header().Get(httpadapter.HeaderRequestID) == "" {
		t.Errorf("GET /v1/info (status=%d): X-Request-Id must be non-empty", rr.Code)
	}
}

// =============================================================================
// Step 2 — X-Request-Id value is a valid UUID
// =============================================================================

// TestXRequestID_HeaderValueIsValidUUID verifies step 2: the auto-generated
// X-Request-Id value is a valid UUID (parseable by uuid.Parse).
func TestXRequestID_HeaderValueIsValidUUID(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatal("X-Request-Id header is empty")
	}
	if !isValidUUID(got) {
		t.Errorf("X-Request-Id %q is not a valid UUID", got)
	}
}

// TestXRequestID_HeaderValueIsUUIDv7 verifies that the auto-generated request
// ID is specifically a UUIDv7 (version nibble == 7).
func TestXRequestID_HeaderValueIsUUIDv7(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatal("X-Request-Id header is empty")
	}
	id, err := uuid.Parse(got)
	if err != nil {
		t.Fatalf("X-Request-Id %q is not a valid UUID: %v", got, err)
	}
	if id.Version() != 7 {
		t.Errorf("X-Request-Id UUID version: want 7, got %d (value=%q)", id.Version(), got)
	}
}

// TestXRequestID_EachRequestGetsDistinctUUID verifies that two separate requests
// receive different X-Request-Id values (not the same UUID minted once).
func TestXRequestID_EachRequestGetsDistinctUUID(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	fire := func() string {
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)
		return rr.Header().Get(httpadapter.HeaderRequestID)
	}

	id1 := fire()
	id2 := fire()

	if id1 == "" || id2 == "" {
		t.Fatalf("got empty X-Request-Id (id1=%q id2=%q)", id1, id2)
	}
	if id1 == id2 {
		t.Errorf("expected distinct X-Request-Id per request, both were %q", id1)
	}
}

// =============================================================================
// Step 3 — GET /does-not-exist (404) has X-Request-Id
// =============================================================================

// TestXRequestID_404ResponseHasHeader verifies step 3: a request to an unknown
// path returns 404 AND still carries a non-empty X-Request-Id header. This
// confirms the middleware chain runs before the NotFound handler.
func TestXRequestID_404ResponseHasHeader(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	if got := rr.Header().Get(httpadapter.HeaderRequestID); got == "" {
		t.Fatal("404 response must carry a non-empty X-Request-Id header")
	}
}

// TestXRequestID_404HeaderValueIsUUID verifies that the X-Request-Id on a 404
// response is also a valid UUID (not chi's raw counter format).
func TestXRequestID_404HeaderValueIsUUID(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatal("X-Request-Id header is empty on 404 response")
	}
	if !isValidUUID(got) {
		t.Errorf("404 X-Request-Id %q is not a valid UUID", got)
	}
}

// =============================================================================
// Step 4 — GET /v1/debug/panic (500) has X-Request-Id
// =============================================================================

// TestXRequestID_500PanicResponseHasHeader verifies step 4: the panic-recovery
// middleware intercepts a panicking handler and returns 500 with X-Request-Id.
func TestXRequestID_500PanicResponseHasHeader(t *testing.T) {
	t.Parallel()
	srv, _, _ := panicTestServer(t, config.EnvProduction, true /* debugRoutes */)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if got := rr.Header().Get(httpadapter.HeaderRequestID); got == "" {
		t.Fatal("500 panic response must carry a non-empty X-Request-Id header")
	}
}

// TestXRequestID_500PanicHeaderValueIsUUID verifies that the X-Request-Id on a
// 500 panic response is a valid UUID.
func TestXRequestID_500PanicHeaderValueIsUUID(t *testing.T) {
	t.Parallel()
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatal("X-Request-Id header is empty on 500 panic response")
	}
	if !isValidUUID(got) {
		t.Errorf("500 X-Request-Id %q is not a valid UUID", got)
	}
}

// TestXRequestID_500BodyIncludesRequestID verifies that the JSON error envelope
// returned on panic also carries the same request_id as the response header.
func TestXRequestID_500BodyIncludesRequestID(t *testing.T) {
	t.Parallel()
	srv, _, _ := panicTestServer(t, config.EnvProduction, true)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	headerID := rr.Header().Get(httpadapter.HeaderRequestID)
	if headerID == "" {
		t.Fatal("X-Request-Id header is empty")
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no 'error' object: %v", body)
	}
	bodyID, _ := errObj["request_id"].(string)
	if bodyID == "" {
		t.Fatal("error envelope missing request_id field")
	}
	if bodyID != headerID {
		t.Errorf("body request_id %q != header X-Request-Id %q", bodyID, headerID)
	}
}

// =============================================================================
// Step 5 — Client-supplied valid UUID is preserved
// =============================================================================

// TestXRequestID_ValidClientUUIDIsEchoed verifies step 5: when the client sends
// a valid UUID as X-Request-Id, the response echoes the same value exactly.
func TestXRequestID_ValidClientUUIDIsEchoed(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	clientUUID := "01234567-89ab-cdef-0123-456789abcdef"
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("X-Request-Id", clientUUID)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got != clientUUID {
		t.Errorf("client-supplied UUID %q: response X-Request-Id = %q, want echo", clientUUID, got)
	}
}

// TestXRequestID_ValidClientUUIDv4IsEchoed verifies step 5 with a standard
// UUIDv4 (most common format clients send).
func TestXRequestID_ValidClientUUIDv4IsEchoed(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	clientUUID := "550e8400-e29b-41d4-a716-446655440000"
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("X-Request-Id", clientUUID)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got != clientUUID {
		t.Errorf("client-supplied UUIDv4 %q: response X-Request-Id = %q, want echo", clientUUID, got)
	}
}

// TestXRequestID_ValidClientUUIDv7IsEchoed verifies step 5 with UUIDv7 format.
func TestXRequestID_ValidClientUUIDv7IsEchoed(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	// A valid UUIDv7 (version nibble = 7, variant = 10xx)
	clientUUID := "018f1b3a-c5d7-7a1b-9e2c-4f8a1d2b3e4f"
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("X-Request-Id", clientUUID)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got != clientUUID {
		t.Errorf("client-supplied UUIDv7 %q: response X-Request-Id = %q, want echo", clientUUID, got)
	}
}

// TestXRequestID_ValidClientUUIDPreservedOn404 verifies that a valid
// client-supplied UUID is also preserved on 404 responses.
func TestXRequestID_ValidClientUUIDPreservedOn404(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	clientUUID := "01234567-89ab-cdef-0123-456789abcdef"
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	req.Header.Set("X-Request-Id", clientUUID)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got != clientUUID {
		t.Errorf("client UUID on 404: response X-Request-Id = %q, want %q", got, clientUUID)
	}
}

// =============================================================================
// Step 6 — Invalid X-Request-Id is replaced by a fresh UUID
// =============================================================================

// TestXRequestID_InvalidClientValueIsReplaced verifies step 6: when the client
// sends "not-a-uuid", the server generates a fresh UUID instead.
func TestXRequestID_InvalidClientValueIsReplaced(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("X-Request-Id", "not-a-uuid")
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatal("X-Request-Id must be non-empty even when client sent invalid value")
	}
	if got == "not-a-uuid" {
		t.Fatal("server must NOT echo back an invalid (non-UUID) X-Request-Id value")
	}
	if !isValidUUID(got) {
		t.Errorf("replacement X-Request-Id %q is not a valid UUID", got)
	}
}

// TestXRequestID_RandomStringIsReplaced verifies step 6 with a random string.
func TestXRequestID_RandomStringIsReplaced(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("X-Request-Id", "abc123xyz")
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "abc123xyz" {
		t.Fatal("server must NOT echo back an invalid X-Request-Id value")
	}
	if !isValidUUID(got) {
		t.Errorf("replacement X-Request-Id %q is not a valid UUID", got)
	}
}

// TestXRequestID_EmptyHeaderGeneratesUUID verifies step 6: when the header is
// absent the server generates a fresh UUID.
func TestXRequestID_EmptyHeaderGeneratesUUID(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	// No X-Request-Id header set.
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == "" {
		t.Fatal("X-Request-Id must be non-empty when header is absent")
	}
	if !isValidUUID(got) {
		t.Errorf("server-generated X-Request-Id %q is not a valid UUID", got)
	}
}

// TestXRequestID_PartialUUIDIsReplaced verifies that a value that looks like a
// truncated UUID but fails uuid.Parse is replaced, not preserved.
func TestXRequestID_PartialUUIDIsReplaced(t *testing.T) {
	t.Parallel()
	srv := buildRequestIDTestServer(t)

	partial := "01234567-89ab-cdef-0123" // truncated — not a valid UUID
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("X-Request-Id", partial)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	got := rr.Header().Get(httpadapter.HeaderRequestID)
	if got == partial {
		t.Fatal("server must NOT echo back a truncated (invalid) UUID")
	}
	if !isValidUUID(got) {
		t.Errorf("replacement X-Request-Id %q is not a valid UUID", got)
	}
}

// =============================================================================
// Step 7 — slog includes request_id in every log line
// =============================================================================

// TestXRequestID_SlogIncludesRequestID verifies step 7: log lines emitted
// during request processing include a "request_id" attribute that matches the
// X-Request-Id response header value.
//
// The test routes slog output through a JSON handler writing to a strings.Builder
// so each newline-delimited JSON object can be parsed and inspected.
func TestXRequestID_SlogIncludesRequestID(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	srv := buildRequestIDTestServerWithLogger(t, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	headerID := rr.Header().Get(httpadapter.HeaderRequestID)
	if headerID == "" {
		t.Fatal("X-Request-Id response header is empty")
	}

	// Parse every JSON log line and look for request_id == headerID.
	logOutput := buf.String()
	lines := strings.Split(strings.TrimSpace(logOutput), "\n")

	var foundLines []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if rid, ok := record["request_id"].(string); ok && rid == headerID {
			foundLines = append(foundLines, line)
		}
	}

	if len(foundLines) == 0 {
		t.Errorf("no slog record found with request_id=%q\nfull log output:\n%s",
			headerID, logOutput)
	}
}

// TestXRequestID_SlogRequestIDMatchesHeader verifies that when a valid UUID is
// supplied by the client, slog records also carry THAT exact UUID as request_id.
func TestXRequestID_SlogRequestIDMatchesHeader(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	srv := buildRequestIDTestServerWithLogger(t, logger)

	clientUUID := "01234567-89ab-cdef-0123-456789abcdef"
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	req.Header.Set("X-Request-Id", clientUUID)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	// Confirm the header was echoed.
	if got := rr.Header().Get(httpadapter.HeaderRequestID); got != clientUUID {
		t.Fatalf("X-Request-Id response header: want %q, got %q", clientUUID, got)
	}

	// Confirm at least one log line has request_id == clientUUID.
	logOutput := buf.String()
	lines := strings.Split(strings.TrimSpace(logOutput), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record["request_id"] == clientUUID {
			return // pass
		}
	}
	t.Errorf("no slog record found with request_id=%q\nfull log output:\n%s",
		clientUUID, logOutput)
}

// =============================================================================
// Full verification sweep — all 7 steps in one test
// =============================================================================

// TestXRequestID_FullVerification exercises all seven feature steps in a single
// sequential test to confirm they all hold together.
func TestXRequestID_FullVerification(t *testing.T) {
	t.Parallel()

	// --- Step 1 & 2: GET /v1/info has a valid UUID as X-Request-Id ---
	t.Run("step1_2_info_has_valid_uuid", func(t *testing.T) {
		srv := buildRequestIDTestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)

		got := rr.Header().Get(httpadapter.HeaderRequestID)
		if got == "" {
			t.Fatal("X-Request-Id header is empty")
		}
		if !isValidUUID(got) {
			t.Errorf("X-Request-Id %q is not a valid UUID", got)
		}
	})

	// --- Step 3: GET /does-not-exist (404) has X-Request-Id ---
	t.Run("step3_404_has_request_id", func(t *testing.T) {
		srv := buildRequestIDTestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d", rr.Code)
		}
		got := rr.Header().Get(httpadapter.HeaderRequestID)
		if got == "" || !isValidUUID(got) {
			t.Errorf("404 X-Request-Id %q is empty or not a valid UUID", got)
		}
	})

	// --- Step 4: GET /v1/debug/panic (500) has X-Request-Id ---
	t.Run("step4_500_has_request_id", func(t *testing.T) {
		srv, _, _ := panicTestServer(t, config.EnvProduction, true)
		req := httptest.NewRequest(http.MethodGet, "/v1/debug/panic", nil)
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", rr.Code)
		}
		got := rr.Header().Get(httpadapter.HeaderRequestID)
		if got == "" || !isValidUUID(got) {
			t.Errorf("500 X-Request-Id %q is empty or not a valid UUID", got)
		}
	})

	// --- Step 5: valid client UUID is echoed ---
	t.Run("step5_valid_client_uuid_echoed", func(t *testing.T) {
		srv := buildRequestIDTestServer(t)
		clientUUID := "01234567-89ab-cdef-0123-456789abcdef"
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		req.Header.Set("X-Request-Id", clientUUID)
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)

		got := rr.Header().Get(httpadapter.HeaderRequestID)
		if got != clientUUID {
			t.Errorf("valid client UUID: want %q, got %q", clientUUID, got)
		}
	})

	// --- Step 6: invalid client value is replaced ---
	t.Run("step6_invalid_client_value_replaced", func(t *testing.T) {
		srv := buildRequestIDTestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		req.Header.Set("X-Request-Id", "not-a-uuid")
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)

		got := rr.Header().Get(httpadapter.HeaderRequestID)
		if got == "" {
			t.Fatal("X-Request-Id must be non-empty even when client sent invalid value")
		}
		if got == "not-a-uuid" {
			t.Fatal("server must NOT echo back an invalid X-Request-Id")
		}
		if !isValidUUID(got) {
			t.Errorf("replacement X-Request-Id %q is not a valid UUID", got)
		}
	})

	// --- Step 7: slog records include request_id ---
	t.Run("step7_slog_includes_request_id", func(t *testing.T) {
		var buf strings.Builder
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
		srv := buildRequestIDTestServerWithLogger(t, logger)
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)

		headerID := rr.Header().Get(httpadapter.HeaderRequestID)
		logOutput := buf.String()
		lines := strings.Split(strings.TrimSpace(logOutput), "\n")

		for _, line := range lines {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if record["request_id"] == headerID {
				return // pass
			}
		}
		t.Errorf("no slog record with request_id=%q found\nlog:\n%s", headerID, logOutput)
	})
}
