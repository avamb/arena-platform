// Package idempotency - tests for feature #60: Idempotency-Key length limit enforced.
//
// Verifies all 5 feature steps:
//
//	Step 1: POST with Idempotency-Key of 300 chars → HTTP 400
//	Step 2: code='idempotency.key_too_long'
//	Step 3: POST with key of exactly 255 chars → 200/401 (accepted by middleware)
//	Step 4: POST with key of 1 char → accepted
//	Step 5: error message includes the max_length value (255)
package idempotency

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// buildKeyLengthServer wires Middleware around a simple echo handler and
// returns a test server + call-count pointer for verifying handler invocations.
func buildKeyLengthServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: "POST /v1/echo"})
	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	t.Cleanup(ts.Close)
	return ts, &calls
}

// postWithKey sends a POST with the given Idempotency-Key header value.
func postWithKey(t *testing.T, ts *httptest.Server, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/echo", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderName, key)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// repeatChar returns a string of n copies of ch.
func repeatChar(ch byte, n int) string {
	return strings.Repeat(string(ch), n)
}

// readBodyJSON reads the response body into a map and returns it.
func readBodyJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal body %q: %v", b, err)
	}
	return m
}

// errorCode extracts error.code from the parsed JSON envelope.
func errorCode(t *testing.T, m map[string]any) string {
	t.Helper()
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no 'error' object: %v", m)
	}
	code, _ := errObj["code"].(string)
	return code
}

// errorMessage extracts error.message from the parsed JSON envelope.
func errorMessage(t *testing.T, m map[string]any) string {
	t.Helper()
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no 'error' object: %v", m)
	}
	msg, _ := errObj["message"].(string)
	return msg
}

// ---------------------------------------------------------------------------
// Step 1: POST with 300-char key → HTTP 400
// ---------------------------------------------------------------------------

// TestKeyLength_300CharKeyReturns400 verifies step 1: a key of 300 characters
// (> MaxKeyLength=255) is rejected with HTTP 400.
func TestKeyLength_300CharKeyReturns400(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('a', 300)
	resp := postWithKey(t, ts, key)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("step 1: want 400, got %d", resp.StatusCode)
	}
}

// TestKeyLength_300CharKeyIsRejected confirms that a 300-char key fails at
// the middleware before the downstream handler is ever called.
func TestKeyLength_300CharKeyIsRejected(t *testing.T) {
	ts, calls := buildKeyLengthServer(t)
	key := repeatChar('x', 300)
	resp := postWithKey(t, ts, key)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if calls.Load() != 0 {
		t.Errorf("handler must NOT be called when key is too long; got %d calls", calls.Load())
	}
}

// TestKeyLength_300CharKeyIsNot200 ensures the response code is exactly 400,
// not 200 or any other success code.
func TestKeyLength_300CharKeyIsNot200(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('z', 300)
	resp := postWithKey(t, ts, key)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Error("step 1: key of 300 chars must not return 200")
	}
	if resp.StatusCode/100 == 2 {
		t.Errorf("step 1: expected 4xx, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Step 2: error code must be 'idempotency.key_too_long'
// ---------------------------------------------------------------------------

// TestKeyLength_CodeIsIdempotencyKeyTooLong verifies step 2: the error.code
// field is exactly "idempotency.key_too_long".
func TestKeyLength_CodeIsIdempotencyKeyTooLong(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('k', 300)
	resp := postWithKey(t, ts, key)
	body := readBodyJSON(t, resp)

	got := errorCode(t, body)
	if got != "idempotency.key_too_long" {
		t.Errorf("step 2: want code='idempotency.key_too_long', got %q", got)
	}
}

// TestKeyLength_CodeUsesDotSeparator checks that the code uses dot notation,
// not underscore notation (idempotency_key_too_long was a previous bug).
func TestKeyLength_CodeUsesDotSeparator(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('d', 300)
	resp := postWithKey(t, ts, key)
	body := readBodyJSON(t, resp)

	code := errorCode(t, body)
	if !strings.Contains(code, ".") {
		t.Errorf("step 2: code must use dot separator, got %q", code)
	}
	if strings.Contains(code, "idempotency_key") {
		t.Errorf("step 2: code must NOT use underscore form 'idempotency_key...', got %q", code)
	}
}

// TestKeyLength_ResponseContentTypeIsJSON verifies that the 400 error is JSON.
func TestKeyLength_ResponseContentTypeIsJSON(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('j', 300)
	resp := postWithKey(t, ts, key)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want Content-Type application/json, got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// Step 3: POST with exactly 255 chars → accepted (200 or 401, not 400)
// ---------------------------------------------------------------------------

// TestKeyLength_Exactly255CharsAccepted verifies step 3: a key of exactly
// MaxKeyLength (255) bytes is accepted by the middleware and passed downstream.
func TestKeyLength_Exactly255CharsAccepted(t *testing.T) {
	ts, calls := buildKeyLengthServer(t)
	key := repeatChar('m', MaxKeyLength) // exactly 255 chars
	if len(key) != 255 {
		t.Fatalf("test setup: key length %d != 255", len(key))
	}

	resp := postWithKey(t, ts, key)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		t.Errorf("step 3: 255-char key must NOT return 400; got %d", resp.StatusCode)
	}
	// Handler must have been invoked (middleware let it through).
	if calls.Load() < 1 {
		t.Error("step 3: handler must be called when key is exactly 255 chars")
	}
}

// TestKeyLength_Exactly255CharsIsNotRejected confirms the middleware does not
// return an error response for a 255-char key.
func TestKeyLength_Exactly255CharsIsNotRejected(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('n', 255)
	resp := postWithKey(t, ts, key)
	body := readBodyJSON(t, resp)
	resp.Body.Close()

	// If the key is rejected, the JSON would have error.code.
	if errObj, ok := body["error"]; ok {
		m, _ := errObj.(map[string]any)
		code, _ := m["code"].(string)
		if strings.Contains(code, "key_too_long") {
			t.Errorf("step 3: 255-char key must NOT be rejected, got code=%q", code)
		}
	}
}

// TestKeyLength_256CharsRejected verifies that 256 chars (one over the limit)
// IS rejected, establishing the boundary exactly at 255.
func TestKeyLength_256CharsRejected(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('p', 256)
	resp := postWithKey(t, ts, key)
	body := readBodyJSON(t, resp)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("256-char key must return 400, got %d", resp.StatusCode)
	}
	code := errorCode(t, body)
	if code != "idempotency.key_too_long" {
		t.Errorf("256-char key: want code='idempotency.key_too_long', got %q", code)
	}
}

// ---------------------------------------------------------------------------
// Step 4: POST with 1-char key → accepted
// ---------------------------------------------------------------------------

// TestKeyLength_1CharKeyAccepted verifies step 4: a 1-character key is
// accepted by the middleware.
func TestKeyLength_1CharKeyAccepted(t *testing.T) {
	ts, calls := buildKeyLengthServer(t)

	resp := postWithKey(t, ts, "X")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		t.Errorf("step 4: 1-char key must NOT return 400; got %d", resp.StatusCode)
	}
	if calls.Load() < 1 {
		t.Error("step 4: handler must be called when key is 1 char")
	}
}

// TestKeyLength_SingleCharVariants tries several 1-char keys to confirm
// any single ASCII character is accepted.
func TestKeyLength_SingleCharVariants(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)

	cases := []string{"A", "1", "-", "_", "Z"}
	for _, key := range cases {
		resp := postWithKey(t, ts, key)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusBadRequest {
			t.Errorf("step 4: 1-char key %q must NOT return 400; got %d", key, resp.StatusCode)
		}
	}
}

// ---------------------------------------------------------------------------
// Step 5: error message includes max_length value (255)
// ---------------------------------------------------------------------------

// TestKeyLength_MessageIncludesMaxLength verifies step 5: the error.message
// field in the JSON response includes the max_length value (255).
func TestKeyLength_MessageIncludesMaxLength(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('q', 300)
	resp := postWithKey(t, ts, key)
	body := readBodyJSON(t, resp)
	resp.Body.Close()

	msg := errorMessage(t, body)
	if !strings.Contains(msg, "255") {
		t.Errorf("step 5: error message must include max_length (255), got %q", msg)
	}
}

// TestKeyLength_MessageMentionsMaxKeyLength ensures the error message
// is descriptive and mentions the boundary clearly.
func TestKeyLength_MessageMentionsMaxKeyLength(t *testing.T) {
	ts, _ := buildKeyLengthServer(t)
	key := repeatChar('r', 400)
	resp := postWithKey(t, ts, key)
	body := readBodyJSON(t, resp)
	resp.Body.Close()

	msg := errorMessage(t, body)
	if msg == "" {
		t.Error("step 5: error message must not be empty")
	}
	// The message must include "255" somewhere.
	if !strings.Contains(msg, "255") {
		t.Errorf("step 5: message %q does not include max_length value 255", msg)
	}
}

// TestKeyLength_MaxKeyLengthConstantIs255 directly verifies that MaxKeyLength
// matches the documented limit of 255 bytes.
func TestKeyLength_MaxKeyLengthConstantIs255(t *testing.T) {
	if MaxKeyLength != 255 {
		t.Errorf("MaxKeyLength must be 255, got %d", MaxKeyLength)
	}
}

// ---------------------------------------------------------------------------
// Full sweep: all 5 steps in one table-driven test
// ---------------------------------------------------------------------------

// TestKeyLength_FullVerification runs all 5 feature steps in order as sub-tests,
// providing a single summary pass/fail for the whole feature.
func TestKeyLength_FullVerification(t *testing.T) {
	t.Run("Step1_300CharKeyReturns400", func(t *testing.T) {
		ts, _ := buildKeyLengthServer(t)
		key := repeatChar('a', 300)
		resp := postWithKey(t, ts, key)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("want 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Step2_CodeIsIdempotencyKeyTooLong", func(t *testing.T) {
		ts, _ := buildKeyLengthServer(t)
		key := repeatChar('b', 300)
		resp := postWithKey(t, ts, key)
		body := readBodyJSON(t, resp)
		resp.Body.Close()
		code := errorCode(t, body)
		if code != "idempotency.key_too_long" {
			t.Errorf("want code='idempotency.key_too_long', got %q", code)
		}
	})

	t.Run("Step3_Exactly255CharsAccepted", func(t *testing.T) {
		ts, calls := buildKeyLengthServer(t)
		key := repeatChar('c', 255)
		resp := postWithKey(t, ts, key)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest {
			t.Errorf("255-char key must not return 400; got %d", resp.StatusCode)
		}
		if calls.Load() < 1 {
			t.Error("handler must be called for 255-char key")
		}
	})

	t.Run("Step4_1CharKeyAccepted", func(t *testing.T) {
		ts, calls := buildKeyLengthServer(t)
		resp := postWithKey(t, ts, "X")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest {
			t.Errorf("1-char key must not return 400; got %d", resp.StatusCode)
		}
		if calls.Load() < 1 {
			t.Error("handler must be called for 1-char key")
		}
	})

	t.Run("Step5_MessageIncludesMaxLength", func(t *testing.T) {
		ts, _ := buildKeyLengthServer(t)
		key := repeatChar('e', 300)
		resp := postWithKey(t, ts, key)
		body := readBodyJSON(t, resp)
		resp.Body.Close()
		msg := errorMessage(t, body)
		if !strings.Contains(msg, "255") {
			t.Errorf("error message must include '255' (max_length), got %q", msg)
		}
	})
}
