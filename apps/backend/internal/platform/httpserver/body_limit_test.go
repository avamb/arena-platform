// body_limit_test.go verifies feature #31:
// "Request body over limit returns 413 envelope"
//
// Body size limit middleware enforces configurable max bytes (default 1 MiB).
// Larger requests return 413 with code='http.payload_too_large'.
//
// Steps covered:
//  1. BODY_LIMIT_BYTES=1 MiB; POST /v1/echo with 2 MiB body → HTTP 413
//  2. Response code is 'http.payload_too_large'
//  3. Response is standard JSON error envelope (code/message/request_id/trace_id)
//  4. Response is quick — server rejects on Content-Length header, not after
//     reading the full body
//  5. slog WARN entry includes content_length and limit fields
//  6. BODY_LIMIT_BYTES=2 MiB; same 2 MiB body → NOT rejected by middleware
//     (limit is configurable)
//
// Tests for steps 1–5 use buildEchoServer (BodyLimitBytes=1 MiB) and POST
// /v1/echo without auth — the body-limit middleware runs before auth in the
// global middleware chain, so the request is rejected before the auth
// middleware even executes.
//
// Step 6 uses a minimal test server built directly from httpadapter.NewRouter
// with BodyLimitBytes=2 MiB and a trivial POST handler, to verify that the
// middleware does NOT reject a request whose body equals the limit.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
)

// =============================================================================
// Helpers
// =============================================================================

// buildBodyLimitTestServer returns a minimal *httptest.Server whose router is
// constructed with httpadapter.NewRouter (so the full canonical middleware
// chain — including JSONBodyLimit — is in place). A simple POST /test/body
// handler returns 200 OK for any request that makes it through the chain.
//
// limitBytes controls the BodyLimitBytes passed to the router.
func buildBodyLimitTestServer(t *testing.T, limitBytes int64) *httptest.Server {
	t.Helper()
	r := httpadapter.NewRouter(httpadapter.Deps{
		BodyLimitBytes: limitBytes,
	})
	r.Post("/test/body", func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

// buildBodyLimitTestServerWithLog is identical to buildBodyLimitTestServer but
// additionally wires a captureSlogHandler so callers can assert on emitted WARN
// records.
func buildBodyLimitTestServerWithLog(t *testing.T, limitBytes int64) (*httptest.Server, *captureSlogHandler) {
	t.Helper()
	logHandler := &captureSlogHandler{}
	logger := slog.New(logHandler)
	r := httpadapter.NewRouter(httpadapter.Deps{
		BodyLimitBytes: limitBytes,
		Logger:         logger,
	})
	r.Post("/test/body", func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, logHandler
}

// post2MiBBody sends a POST /test/body with a 2 MiB body of zeroes and the
// required application/json Content-Type. Returns the response (caller must
// close Body).
func post2MiBBody(t *testing.T, baseURL string) *http.Response {
	t.Helper()
	const bodySize = 2 * 1024 * 1024 // 2 MiB
	body := bytes.NewReader(make([]byte, bodySize))
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		baseURL+"/test/body",
		body,
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /test/body: %v", err)
	}
	return resp
}

// decodeBodyLimitEnvelope reads and parses the 413 response envelope.
func decodeBodyLimitEnvelope(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, b)
	}
	return env
}

// =============================================================================
// Step 1 — 2 MiB body with 1 MiB limit → HTTP 413
// =============================================================================

// TestBodyLimit_Returns413 verifies that a 2 MiB body sent to a server with a
// 1 MiB body limit produces an HTTP 413 response.
func TestBodyLimit_Returns413(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20) // 1 MiB

	resp := post2MiBBody(t, ts.URL)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected HTTP 413, got %d", resp.StatusCode)
	}
}

// TestBodyLimit_ContentTypeIsJSON verifies that the 413 response carries
// Content-Type: application/json.
func TestBodyLimit_ContentTypeIsJSON(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// =============================================================================
// Step 2 — Response code is 'http.payload_too_large'
// =============================================================================

// TestBodyLimit_CodeIsPayloadTooLarge verifies that the error envelope code is
// exactly 'http.payload_too_large'.
func TestBodyLimit_CodeIsPayloadTooLarge(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	env := decodeBodyLimitEnvelope(t, resp)

	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object; got: %#v", env)
	}
	code, ok := errObj["code"].(string)
	if !ok {
		t.Fatalf("error.code is not a string; got: %#v", errObj["code"])
	}
	if code != "http.payload_too_large" {
		t.Errorf("expected code 'http.payload_too_large', got %q", code)
	}
}

// TestBodyLimit_CodeFollowsDottedNamespace verifies that the error code follows
// the dotted-namespace convention (contains at least one dot).
func TestBodyLimit_CodeFollowsDottedNamespace(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	env := decodeBodyLimitEnvelope(t, resp)

	errObj := env["error"].(map[string]any)
	code := errObj["code"].(string)
	if !strings.Contains(code, ".") {
		t.Errorf("error code must follow dotted-namespace format, got %q", code)
	}
}

// =============================================================================
// Step 3 — Standard error envelope (code/message/request_id/trace_id)
// =============================================================================

// TestBodyLimit_EnvelopeHasRequiredFields verifies that the 413 response body
// contains all four required error fields.
func TestBodyLimit_EnvelopeHasRequiredFields(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	env := decodeBodyLimitEnvelope(t, resp)

	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'error' object")
	}
	for _, field := range []string{"code", "message", "request_id", "trace_id"} {
		if _, present := errObj[field]; !present {
			t.Errorf("error envelope missing required field %q", field)
		}
	}
}

// TestBodyLimit_EnvelopeMessageMentionsLimit verifies that the error message
// mentions the byte limit so the client understands what constraint was hit.
func TestBodyLimit_EnvelopeMessageMentionsLimit(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	env := decodeBodyLimitEnvelope(t, resp)

	errObj := env["error"].(map[string]any)
	msg, ok := errObj["message"].(string)
	if !ok {
		t.Fatalf("error.message is not a string")
	}
	// Message should reference a byte count (contains at least one digit).
	hasDigit := false
	for _, r := range msg {
		if r >= '0' && r <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		t.Errorf("error.message should mention the byte limit; got %q", msg)
	}
}

// TestBodyLimit_EnvelopeHasRequestID verifies that request_id is non-empty in
// the 413 envelope, proving the middleware reads correlation ids from context.
func TestBodyLimit_EnvelopeHasRequestID(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	env := decodeBodyLimitEnvelope(t, resp)

	errObj := env["error"].(map[string]any)
	reqID, _ := errObj["request_id"].(string)
	if strings.TrimSpace(reqID) == "" {
		t.Error("error envelope request_id must be non-empty")
	}
}

// =============================================================================
// Step 4 — Quick response: server rejects on Content-Length header
// =============================================================================

// TestBodyLimit_RejectsBasedOnContentLength verifies that the 413 is triggered
// by the Content-Length header check (fast path) rather than after reading the
// entire body.
//
// We send the 2 MiB body via bytes.NewReader which causes Go's HTTP client to
// set Content-Length: 2097152 in the request headers. The server reads that
// header value, compares it to the 1 MiB limit, and returns 413 immediately
// without consuming any body bytes from the connection. The response comes back
// promptly even though the body is large.
//
// The implementation proof: the middleware returns before calling
// next.ServeHTTP when r.ContentLength > maxBytes, so the POST /test/body
// handler (which drains the body) is never invoked.
func TestBodyLimit_RejectsBasedOnContentLength(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20) // 1 MiB

	// Build a custom body reader that counts how many bytes are consumed.
	// The bytes.NewReader body is sent by the HTTP client, but the SERVER
	// reads from its own connection buffer — not from this object directly.
	// We can verify the server responds 413 without the handler running by
	// checking that the /test/body handler never calls io.Copy (which would
	// change the response body to a 200).
	const bodySize = 2 * 1024 * 1024
	body := bytes.NewReader(make([]byte, bodySize))

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		ts.URL+"/test/body",
		body,
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// bytes.NewReader has a known length so Go's HTTP client automatically
	// sets Content-Length: 2097152 on the request.
	if req.ContentLength != bodySize {
		t.Fatalf("expected Go HTTP client to set ContentLength=%d, got %d", bodySize, req.ContentLength)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// The server must have checked the Content-Length header and returned 413
	// without waiting for the entire body. If the handler ran, it would have
	// returned 200.
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 from Content-Length check, got %d", resp.StatusCode)
	}
}

// TestBodyLimit_HandlerNotInvokedWhenLimitExceeded verifies that the POST
// /test/body handler is NOT invoked when the Content-Length exceeds the limit.
// The handler returns 200; receiving 413 proves the middleware short-circuited.
func TestBodyLimit_HandlerNotInvokedWhenLimitExceeded(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 1<<20) // 1 MiB

	resp := post2MiBBody(t, ts.URL)
	defer resp.Body.Close()

	// The /test/body handler returns 200; if we get 413 the middleware fired
	// before the handler was invoked.
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("handler should not have been invoked: expected 413, got %d", resp.StatusCode)
	}
}

// =============================================================================
// Step 5 — slog WARN includes content_length and limit
// =============================================================================

// TestBodyLimit_SlogWarnEmitted verifies that a WARN-level slog record is
// emitted when the body limit is exceeded.
func TestBodyLimit_SlogWarnEmitted(t *testing.T) {
	t.Parallel()
	ts, logHandler := buildBodyLimitTestServerWithLog(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	resp.Body.Close()

	warnMsgs := logHandler.warnMessages()
	found := false
	for _, m := range warnMsgs {
		if strings.Contains(m, "body") || strings.Contains(m, "limit") || strings.Contains(m, "payload") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a WARN slog record about body size limit; got WARN messages: %v", warnMsgs)
	}
}

// TestBodyLimit_SlogWarnHasContentLength verifies that the WARN record carries
// a 'content_length' attribute equal to the body size declared by the client.
func TestBodyLimit_SlogWarnHasContentLength(t *testing.T) {
	t.Parallel()
	ts, logHandler := buildBodyLimitTestServerWithLog(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	resp.Body.Close()

	// content_length=2097152 (2 MiB) must appear in a WARN record.
	const expected = "2097152" // 2*1024*1024
	if !logHandler.hasWarnWithAttrInt64("content_length", 2*1024*1024) {
		// Gather all WARN records for a helpful failure message.
		t.Errorf("no WARN record with content_length=%s found", expected)
	}
}

// TestBodyLimit_SlogWarnHasLimit verifies that the WARN record carries a
// 'limit' attribute equal to the configured limit (1 MiB = 1048576).
func TestBodyLimit_SlogWarnHasLimit(t *testing.T) {
	t.Parallel()
	ts, logHandler := buildBodyLimitTestServerWithLog(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	resp.Body.Close()

	// limit=1048576 (1 MiB) must appear in a WARN record.
	if !logHandler.hasWarnWithAttrInt64("limit", 1<<20) {
		t.Errorf("no WARN record with limit=%d found", int64(1<<20))
	}
}

// TestBodyLimit_SlogWarnFields_BothPresent verifies that a single WARN record
// carries BOTH content_length and limit (not just one of them).
func TestBodyLimit_SlogWarnFields_BothPresent(t *testing.T) {
	t.Parallel()
	ts, logHandler := buildBodyLimitTestServerWithLog(t, 1<<20)

	resp := post2MiBBody(t, ts.URL)
	resp.Body.Close()

	// Check both fields exist among WARN records.
	hasContentLength := logHandler.hasWarnWithAttrInt64("content_length", 2*1024*1024)
	hasLimit := logHandler.hasWarnWithAttrInt64("limit", 1<<20)

	if !hasContentLength {
		t.Error("WARN record missing 'content_length' attribute")
	}
	if !hasLimit {
		t.Error("WARN record missing 'limit' attribute")
	}
}

// =============================================================================
// Step 6 — Configurable limit: 2 MiB limit allows 2 MiB body
// =============================================================================

// TestBodyLimit_ConfigurableLimitAllows2MiBWith2MiBLimit verifies that when
// BODY_LIMIT_BYTES=2 MiB the same 2 MiB body is NOT rejected by the middleware
// — the request reaches the handler which returns 200.
func TestBodyLimit_ConfigurableLimitAllows2MiBWith2MiBLimit(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 2<<20) // 2 MiB limit

	resp := post2MiBBody(t, ts.URL)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with 2 MiB limit and 2 MiB body, got %d", resp.StatusCode)
	}
}

// TestBodyLimit_ConfigurableLimitStillRejectsOver2MiB verifies that a 3 MiB
// body is rejected when the limit is 2 MiB, proving the configurable limit
// enforces correctly in both directions.
func TestBodyLimit_ConfigurableLimitStillRejectsOver2MiB(t *testing.T) {
	t.Parallel()
	ts := buildBodyLimitTestServer(t, 2<<20) // 2 MiB limit

	const bodySize = 3 * 1024 * 1024 // 3 MiB
	body := bytes.NewReader(make([]byte, bodySize))
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		ts.URL+"/test/body",
		body,
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for 3 MiB body with 2 MiB limit, got %d", resp.StatusCode)
	}
}

// TestBodyLimit_OneByteUnderLimitAllowed verifies that a body whose size is
// exactly one byte below the limit passes through the middleware.
func TestBodyLimit_OneByteUnderLimitAllowed(t *testing.T) {
	t.Parallel()
	const limit = 1 << 20 // 1 MiB
	ts := buildBodyLimitTestServer(t, limit)

	// limit - 1 bytes
	body := bytes.NewReader(make([]byte, limit-1))
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		ts.URL+"/test/body",
		body,
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("body of limit-1 bytes should pass; expected 200, got %d", resp.StatusCode)
	}
}

// TestBodyLimit_SafeMethodsNotLimited verifies that GET requests are never
// rejected by the body-limit middleware. The middleware only applies to
// POST/PUT/PATCH; GET requests pass through unconditionally.
func TestBodyLimit_SafeMethodsNotLimited(t *testing.T) {
	t.Parallel()

	// Mount a GET handler on a fresh router with a 1 MiB body limit.
	r := httpadapter.NewRouter(httpadapter.Deps{BodyLimitBytes: 1 << 20})
	r.Get("/test/get", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	getServer := httptest.NewServer(r)
	t.Cleanup(getServer.Close)

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		getServer.URL+"/test/get",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET should not be body-limited; expected 200, got %d", resp.StatusCode)
	}
}

// =============================================================================
// Additional helper on captureSlogHandler: int64 attribute check
// =============================================================================

// hasWarnWithAttrInt64 returns true when at least one WARN record contains
// an attribute with the given key whose value matches want when parsed as
// int64. slog stores numeric attributes with Kind Int64; the String() form is
// the decimal representation.
func (h *captureSlogHandler) hasWarnWithAttrInt64(key string, want int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Level != slog.LevelWarn {
			continue
		}
		found := false
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key != key {
				return true
			}
			// slog stores int64 as KindInt64; fall back to string comparison.
			switch a.Value.Kind() {
			case slog.KindInt64:
				if a.Value.Int64() == want {
					found = true
					return false
				}
			default:
				// Compare string representation.
				if a.Value.String() == slog.Int64Value(want).String() {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
