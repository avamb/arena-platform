// Package httpserver — tests for feature #71:
// "occurred_at on audit_events recorded at write time, not request time"
//
// Verifies that audit_events.occurred_at is the server-side write time
// (time.Now().UTC()) and is not influenced by any client-supplied timestamp
// (e.g. X-Request-Time header or other request metadata).
//
// All steps are exercised without a live PostgreSQL connection by using the
// in-memory captureAuditWriter (defined in echo_audit_test.go in this package).
package httpserver

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Step 1–3: occurred_at is within ±2s of server clock at request time
// =============================================================================

// TestOccurredAt_WithinTwoSecondsOfServerClock verifies step 3:
// occurred_at is within ±2s of T0 (the moment the request was sent).
func TestOccurredAt_WithinTwoSecondsOfServerClock(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

	t0 := time.Now().UTC()
	resp := postEchoAudit(t, ts, token, "OCCURRED_AT_TIMING_1", `{"message":"server clock test"}`)
	t1 := time.Now().UTC()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 1: want 200, got %d — body: %s", resp.StatusCode, body)
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("step 2: no audit events captured")
	}
	ev := events[0]

	if ev.OccurredAt.IsZero() {
		t.Fatal("step 3: occurred_at must not be the zero value")
	}

	tolerance := 2 * time.Second
	if ev.OccurredAt.Before(t0.Add(-tolerance)) {
		t.Errorf("step 3: occurred_at %v is more than 2s before T0 (%v)", ev.OccurredAt, t0)
	}
	if ev.OccurredAt.After(t1.Add(tolerance)) {
		t.Errorf("step 3: occurred_at %v is more than 2s after T1 (%v)", ev.OccurredAt, t1)
	}
}

// TestOccurredAt_IsUTC verifies occurred_at is in UTC (Location name == "UTC").
func TestOccurredAt_IsUTC(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")
	resp := postEchoAudit(t, ts, token, "OCCURRED_AT_UTC_CHECK", `{"message":"utc check"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("no audit events captured")
	}

	loc := events[0].OccurredAt.Location().String()
	if loc != "UTC" {
		t.Errorf("occurred_at location is %q, want \"UTC\"", loc)
	}
}

// =============================================================================
// Step 4: X-Request-Time header is ignored (server clock is the truth)
// =============================================================================

// postEchoWithRequestTimeHeader sends POST /v1/echo with an X-Request-Time header
// set to a clearly wrong timestamp (2020-01-01T00:00:00Z) to verify the server
// ignores it for occurred_at computation.
func postEchoWithRequestTimeHeader(t *testing.T, ts *httptest.Server, token, idemKey, body, requestTime string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", idemKey)
	req.Header.Set("X-Request-Time", requestTime)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// TestOccurredAt_IgnoresXRequestTimeHeader is step 4:
// A request with X-Request-Time: 2020-01-01T00:00:00Z must NOT influence
// occurred_at — the server must use its own clock.
func TestOccurredAt_IgnoresXRequestTimeHeader(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

	// Inject a clearly-wrong client timestamp: Jan 1, 2020 UTC
	const clientTimestamp = "2020-01-01T00:00:00Z"
	clientTime, err := time.Parse(time.RFC3339, clientTimestamp)
	if err != nil {
		t.Fatalf("parse clientTimestamp: %v", err)
	}

	t0 := time.Now().UTC()
	resp := postEchoWithRequestTimeHeader(t, ts, token, "OCCURRED_AT_IGNORE_HDR", `{"message":"ignore header test"}`, clientTimestamp)
	t1 := time.Now().UTC()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("step 4: want 200, got %d — body: %s", resp.StatusCode, body)
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("step 4: no audit events captured")
	}
	ev := events[0]

	// occurred_at must NOT match the client-supplied 2020-01-01 timestamp.
	// Allow a 1-second window around the client time as the error margin.
	if ev.OccurredAt.After(clientTime.Add(-time.Second)) && ev.OccurredAt.Before(clientTime.Add(time.Second)) {
		t.Errorf("step 4: occurred_at=%v appears to match the client-supplied X-Request-Time=%s — server must use its own clock",
			ev.OccurredAt, clientTimestamp)
	}

	// occurred_at must be within 2s of the actual server clock at send time.
	tolerance := 2 * time.Second
	if ev.OccurredAt.Before(t0.Add(-tolerance)) || ev.OccurredAt.After(t1.Add(tolerance)) {
		t.Errorf("step 4: occurred_at=%v is not within 2s of server clock window [%v, %v]",
			ev.OccurredAt, t0, t1)
	}
}

// TestOccurredAt_XRequestTimeFarPastIsRejected verifies a far-past date
// (year 2000) in X-Request-Time is also ignored.
func TestOccurredAt_XRequestTimeFarPastIsRejected(t *testing.T) {
	ts, stub, aw := buildEchoServer(t)

	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

	const ancientTimestamp = "2000-06-15T12:00:00Z"
	ancientTime, _ := time.Parse(time.RFC3339, ancientTimestamp)

	resp := postEchoWithRequestTimeHeader(t, ts, token, "OCCURRED_AT_FAR_PAST", `{"message":"ancient header"}`, ancientTimestamp)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d — body: %s", resp.StatusCode, body)
	}

	events := aw.getEvents()
	if len(events) == 0 {
		t.Fatal("no audit events captured")
	}
	ev := events[0]

	// occurred_at must not be anywhere near the year 2000.
	if ev.OccurredAt.Year() <= ancientTime.Year()+1 {
		t.Errorf("occurred_at year=%d looks like the client-supplied ancient timestamp (year %d) was used",
			ev.OccurredAt.Year(), ancientTime.Year())
	}

	// Must be a recent year (at least 2024).
	if ev.OccurredAt.Year() < 2024 {
		t.Errorf("occurred_at year=%d is unexpectedly old — server clock not used", ev.OccurredAt.Year())
	}
}

// =============================================================================
// Step 5: Static source scan — no client-supplied timestamp trusted for audit
// =============================================================================

// TestOccurredAt_StaticScan_NoClientTimestampInAuditWrite performs step 5:
// scans the production source of audit.go and echo.go to confirm:
//   - occurred_at is always set from time.Now() or clock_timestamp() (server-side)
//   - X-Request-Time header is never read and used for audit purposes
//   - No header value is passed as OccurredAt
func TestOccurredAt_StaticScan_NoClientTimestampInAuditWrite(t *testing.T) {
	// Locate the repo root relative to the test working directory.
	root := findRepoRoot(t)

	// Files where audit OccurredAt is populated.
	filesToScan := []string{
		filepath.Join(root, "apps", "backend", "internal", "platform", "httpserver", "echo.go"),
		filepath.Join(root, "apps", "backend", "internal", "platform", "audit", "audit.go"),
	}

	// Patterns that would indicate client-supplied timestamp trust.
	// These are regex patterns that should NOT appear in the scanned lines
	// that also reference OccurredAt.
	suspiciousPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)X-Request-Time`),
		regexp.MustCompile(`(?i)request[-_]time`),
		regexp.MustCompile(`(?i)client[-_]time`),
		regexp.MustCompile(`(?i)r\.Header\.Get.*[Tt]ime`),
	}

	for _, path := range filesToScan {
		f, err := os.Open(path)
		if err != nil {
			t.Errorf("cannot open %s: %v", path, err)
			continue
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			// Only flag lines that are NOT comments.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, pat := range suspiciousPatterns {
				if pat.MatchString(line) {
					t.Errorf("step 5: %s:%d — found suspicious client-time pattern %q in production code: %s",
						filepath.Base(path), lineNum, pat.String(), trimmed)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			t.Errorf("scan error for %s: %v", path, err)
		}
	}
}

// TestOccurredAt_StaticScan_AuditUsesTimeNow verifies echo.go sets OccurredAt
// with time.Now().UTC() (not any other source).
func TestOccurredAt_StaticScan_AuditUsesTimeNow(t *testing.T) {
	root := findRepoRoot(t)
	echoPath := filepath.Join(root, "apps", "backend", "internal", "platform", "httpserver", "echo.go")

	content, err := os.ReadFile(echoPath)
	if err != nil {
		t.Fatalf("cannot read echo.go: %v", err)
	}
	src := string(content)

	// Look for the audit event construction block with OccurredAt: time.Now().UTC()
	if !strings.Contains(src, "OccurredAt:") {
		t.Error("echo.go: expected OccurredAt field to be set in the audit event struct")
	}
	if !strings.Contains(src, "time.Now().UTC()") {
		t.Error("echo.go: expected time.Now().UTC() for server-side timestamp, but it was not found")
	}

	// Confirm the fallback in audit.go also uses time.Now().UTC()
	auditPath := filepath.Join(root, "apps", "backend", "internal", "platform", "audit", "audit.go")
	auditContent, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("cannot read audit.go: %v", err)
	}
	auditSrc := string(auditContent)

	if !strings.Contains(auditSrc, "time.Now().UTC()") {
		t.Error("audit.go: expected time.Now().UTC() fallback for OccurredAt when zero")
	}
}

// =============================================================================
// Full verification sweep
// =============================================================================

// TestOccurredAt_FullVerification runs all feature steps as sub-tests.
func TestOccurredAt_FullVerification(t *testing.T) {
	t.Run("Step1_3_WithinTwoSecondsOfServerClock", func(t *testing.T) {
		ts, stub, aw := buildEchoServer(t)
		token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

		t0 := time.Now().UTC()
		resp := postEchoAudit(t, ts, token, "FULL_VER_TIMING", `{"message":"full verification timing"}`)
		t1 := time.Now().UTC()
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", resp.StatusCode)
		}
		events := aw.getEvents()
		if len(events) == 0 {
			t.Fatal("no audit events captured")
		}
		ev := events[0]
		tolerance := 2 * time.Second
		if ev.OccurredAt.Before(t0.Add(-tolerance)) || ev.OccurredAt.After(t1.Add(tolerance)) {
			t.Errorf("occurred_at=%v not within 2s window [%v, %v]", ev.OccurredAt, t0, t1)
		}
	})

	t.Run("Step4_XRequestTimeHeaderIgnored", func(t *testing.T) {
		ts, stub, aw := buildEchoServer(t)
		token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000001")

		clientTime, _ := time.Parse(time.RFC3339, "2020-01-01T00:00:00Z")
		resp := postEchoWithRequestTimeHeader(t, ts, token, "FULL_VER_IGNORE_HDR",
			`{"message":"ignore header full"}`, "2020-01-01T00:00:00Z")
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", resp.StatusCode)
		}
		events := aw.getEvents()
		if len(events) == 0 {
			t.Fatal("no audit events captured")
		}
		ev := events[0]
		if ev.OccurredAt.After(clientTime.Add(-time.Second)) && ev.OccurredAt.Before(clientTime.Add(time.Second)) {
			t.Errorf("occurred_at=%v matches client-supplied X-Request-Time — server must use its own clock", ev.OccurredAt)
		}
		if ev.OccurredAt.Year() < 2024 {
			t.Errorf("occurred_at year=%d is unexpectedly old — client timestamp may have been used", ev.OccurredAt.Year())
		}
	})

	t.Run("Step5_NoClientTimestampInSource", func(t *testing.T) {
		root := findRepoRoot(t)
		echoPath := filepath.Join(root, "apps", "backend", "internal", "platform", "httpserver", "echo.go")
		content, err := os.ReadFile(echoPath)
		if err != nil {
			t.Fatalf("cannot read echo.go: %v", err)
		}
		if !strings.Contains(string(content), "OccurredAt:") || !strings.Contains(string(content), "time.Now().UTC()") {
			t.Error("echo.go: OccurredAt must be set with time.Now().UTC()")
		}
		// Verify no X-Request-Time parsing in production code (excluding test files).
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(strings.ToLower(line), "x-request-time") {
				t.Errorf("echo.go:%d — X-Request-Time found in production code: %s", lineNum, trimmed)
			}
		}
	})
}

// =============================================================================
// Helper: find the repo root
// =============================================================================

// findRepoRoot walks up from the current working directory to find go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod — repo root not found")
		}
		dir = parent
	}
}
