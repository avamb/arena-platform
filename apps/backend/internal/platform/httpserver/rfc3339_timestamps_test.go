// rfc3339_timestamps_test.go verifies feature #65:
// "Timestamps formatted as RFC 3339 UTC"
//
// All timestamps in JSON responses must be RFC 3339 strings in UTC with a
// 'Z' suffix (e.g. "2026-06-21T14:30:00.000Z"). The '+00:00' form is NOT
// acceptable.
//
// Steps verified:
//  1. GET /v1/info emits a 'server_time' timestamp field
//  2. Format matches /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/
//  3. Suffix is 'Z' (UTC), not '+00:00'
//  4. POST /v1/echo 'issued_at' field (a second timestamp source) — same format
//  5. Static scan: time package uses time.RFC3339Nano with UTC conversion
package httpserver

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reRFC3339UTC matches a valid RFC 3339 timestamp in UTC:
//   - date: YYYY-MM-DD
//   - time separator: T
//   - time: HH:MM:SS
//   - optional fractional seconds: .NNNNNNNNN (1-9 digits)
//   - UTC suffix: Z (exactly — not +00:00)
var reRFC3339UTC = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

// =============================================================================
// Step 1+2+3 — GET /v1/info server_time is RFC 3339 UTC with Z suffix
// =============================================================================

// TestTimestamp_InfoHasServerTimeField verifies step 1:
// GET /v1/info response body includes a 'server_time' field.
func TestTimestamp_InfoHasServerTimeField(t *testing.T) {
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
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if _, ok := body["server_time"]; !ok {
		t.Error("GET /v1/info: response body must include 'server_time' field")
	}
}

// TestTimestamp_InfoServerTimeMatchesRFC3339 verifies step 2:
// server_time value matches /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/.
func TestTimestamp_InfoServerTimeMatchesRFC3339(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	raw, ok := body["server_time"]
	if !ok {
		t.Fatal("'server_time' field missing from /v1/info response")
	}
	ts_val, ok := raw.(string)
	if !ok {
		t.Fatalf("'server_time' must be a string, got %T", raw)
	}
	if !reRFC3339UTC.MatchString(ts_val) {
		t.Errorf("server_time %q does not match RFC3339 UTC pattern (YYYY-MM-DDTHH:MM:SS[.NNN]Z)", ts_val)
	}
}

// TestTimestamp_InfoServerTimeHasZSuffix verifies step 3:
// server_time ends with 'Z', not '+00:00'.
func TestTimestamp_InfoServerTimeHasZSuffix(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	ts_val, _ := body["server_time"].(string)
	if ts_val == "" {
		t.Fatal("'server_time' field missing or empty")
	}

	if !strings.HasSuffix(ts_val, "Z") {
		t.Errorf("server_time %q must end with 'Z' (UTC), not '+00:00' or other offset", ts_val)
	}
	if strings.Contains(ts_val, "+00:00") {
		t.Errorf("server_time %q uses '+00:00' instead of the required 'Z' suffix", ts_val)
	}
	if strings.Contains(ts_val, "+0000") {
		t.Errorf("server_time %q uses '+0000' instead of the required 'Z' suffix", ts_val)
	}
}

// TestTimestamp_InfoServerTimeNotPlusOffset verifies step 3 from another angle:
// server_time must not contain any '+' offset indicator.
func TestTimestamp_InfoServerTimeNotPlusOffset(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	ts := buildTestHTTPServer(t, s)

	resp, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	ts_val, _ := body["server_time"].(string)
	if ts_val == "" {
		t.Fatal("'server_time' field missing or empty")
	}

	// A UTC timestamp must not contain any '+' sign (which would indicate a
	// non-zero UTC offset representation).
	if strings.Contains(ts_val, "+") {
		t.Errorf("server_time %q contains '+' sign; expected pure 'Z' suffix for UTC", ts_val)
	}
}

// =============================================================================
// Step 4 — POST /v1/echo issued_at is RFC 3339 UTC with Z suffix
// =============================================================================

// TestTimestamp_EchoIssuedAtMatchesRFC3339 verifies step 4:
// POST /v1/echo response 'issued_at' field has correct RFC3339 UTC format.
func TestTimestamp_EchoIssuedAtMatchesRFC3339(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000065")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"timestamp_test_rfc3339"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000065")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("POST /v1/echo: want 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode JSON: %v (body=%q)", err, string(b))
	}

	raw, ok := body["issued_at"]
	if !ok {
		t.Fatal("POST /v1/echo response missing 'issued_at' field")
	}
	ts_val, ok := raw.(string)
	if !ok {
		t.Fatalf("'issued_at' must be a string, got %T (%v)", raw, raw)
	}

	if !reRFC3339UTC.MatchString(ts_val) {
		t.Errorf("issued_at %q does not match RFC3339 UTC pattern (YYYY-MM-DDTHH:MM:SS[.NNN]Z)", ts_val)
	}
}

// TestTimestamp_EchoIssuedAtHasZSuffix verifies step 4 — Z suffix only.
func TestTimestamp_EchoIssuedAtHasZSuffix(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000066")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"z_suffix_check"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000066")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	ts_val, _ := body["issued_at"].(string)
	if ts_val == "" {
		t.Fatal("POST /v1/echo: 'issued_at' field missing or empty")
	}

	if !strings.HasSuffix(ts_val, "Z") {
		t.Errorf("issued_at %q must end with 'Z' (UTC suffix), not '+00:00'", ts_val)
	}
	if strings.Contains(ts_val, "+00:00") {
		t.Errorf("issued_at %q uses '+00:00'; 'Z' is required", ts_val)
	}
}

// TestTimestamp_EchoIssuedAtIsNonEmpty verifies step 4 — basic presence check.
func TestTimestamp_EchoIssuedAtIsNonEmpty(t *testing.T) {
	t.Parallel()

	ts, stub, _ := buildEchoServer(t)
	token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000067")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/echo",
		strings.NewReader(`{"message":"non_empty_check"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000067")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/echo: %v", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	ts_val, _ := body["issued_at"].(string)
	if ts_val == "" {
		t.Error("POST /v1/echo: 'issued_at' must be a non-empty timestamp string")
	}
	if len(ts_val) < 20 {
		t.Errorf("POST /v1/echo: 'issued_at' %q seems too short for a valid RFC3339 timestamp", ts_val)
	}
}

// =============================================================================
// Step 5 — Static scan: time package uses time.RFC3339Nano with UTC conversion
// =============================================================================

// scanGoFilesForTimestampFormatting walks the given directory tree and checks
// that all timestamp formatting in production Go source uses:
//   - time.RFC3339Nano (preferred) or time.RFC3339 for string formatting, and
//   - .UTC() before formatting (no bare time.Now().Format() without UTC)
//
// Returns a list of violation descriptions.
func scanGoFilesForTimestampFormatting(t *testing.T, root string) []string {
	t.Helper()
	var violations []string

	// Pattern: .Format(anything) where the anything is NOT RFC3339Nano or RFC3339
	// We look for .Format(" calls that use non-RFC3339 format strings.
	reFormatCall := regexp.MustCompile(`\.Format\(([^)]+)\)`)
	// Known-good format constants.
	goodFormats := map[string]bool{
		"time.RFC3339Nano": true,
		"time.RFC3339":     true,
	}

	// Pattern: time.Now().Format( without .UTC() in between — bare formatting without UTC.
	// We detect "time.Now().Format(" (no UTC() in chain) as a heuristic.
	reNowDirectFormat := regexp.MustCompile(`time\.Now\(\)\.Format\(`)

	// Escape hatch mirroring the feature-#176 `// allow:panic` marker: a line
	// (or its immediate predecessor) carrying `// allow:timeformat: <reason>`
	// is exempt. Legitimate uses are wire formats mandated by external
	// protocols (e.g. AWS SigV4 X-Amz-Date) and human-facing display strings
	// rendered in a venue-local timezone (email bodies, PDF tickets) — those
	// are deliberately NOT machine-readable API timestamps.
	reAllowMarker := regexp.MustCompile(`//\s*allow:timeformat\b`)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "vendor" || base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		f, ferr := os.Open(path)
		if ferr != nil {
			return ferr
		}
		defer f.Close()

		lineNum := 0
		prevLine := ""
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()

			// Skip comment lines.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				prevLine = line
				continue
			}

			// Honour the `// allow:timeformat` escape hatch on the same or
			// immediately preceding line.
			if reAllowMarker.MatchString(line) || reAllowMarker.MatchString(prevLine) {
				prevLine = line
				continue
			}
			prevLine = line

			// Check for .Format() calls using non-RFC3339 format strings.
			matches := reFormatCall.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				arg := strings.TrimSpace(m[1])
				// Skip if it's a variable reference (not a literal format string).
				if !strings.HasPrefix(arg, `"`) && !strings.HasPrefix(arg, "time.") {
					continue
				}
				if !goodFormats[arg] {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, rel+":"+itoa(lineNum)+
						": .Format("+arg+") — use time.RFC3339Nano or time.RFC3339 for consistency")
				}
			}

			// Check for time.Now().Format( — should be time.Now().UTC().Format(
			if reNowDirectFormat.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel+":"+itoa(lineNum)+
					": time.Now().Format(...) without .UTC() — use time.Now().UTC().Format(...) to ensure Z suffix")
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("filepath.Walk(%s): %v", root, err)
	}
	return violations
}

// TestTimestamp_StaticScan_RFC3339NanoWithUTC verifies step 5:
// all production Go source files use time.RFC3339Nano (or time.RFC3339) for
// timestamp formatting, and always call .UTC() before formatting so the output
// has a 'Z' suffix rather than a timezone offset.
func TestTimestamp_StaticScan_RFC3339NanoWithUTC(t *testing.T) {
	t.Parallel()

	root := findBackendRoot(t)
	violations := scanGoFilesForTimestampFormatting(t, root)

	if len(violations) > 0 {
		t.Errorf("found %d timestamp formatting issue(s) in production Go source:", len(violations))
		for _, v := range violations {
			t.Errorf("  %s", v)
		}
		t.Log("Fix: use time.Now().UTC().Format(time.RFC3339Nano) for all timestamp formatting")
	}
}

// TestTimestamp_StaticScan_NoHardcodedOffset verifies step 5 from another angle:
// no production Go source file uses literal "+00:00" in format strings or
// hardcodes a non-UTC timezone.
func TestTimestamp_StaticScan_NoHardcodedOffset(t *testing.T) {
	t.Parallel()

	root := findBackendRoot(t)

	badPatterns := []struct {
		re   *regexp.Regexp
		desc string
	}{
		{regexp.MustCompile(`"\+00:00"`), `hardcoded "+00:00" offset string (use "Z" instead)`},
		{regexp.MustCompile(`time\.FixedZone`), `time.FixedZone() call (use .UTC() for UTC timestamps)`},
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "vendor" || base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
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
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, bp := range badPatterns {
				if bp.re.MatchString(line) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%s: %s", rel, itoa(lineNum), bp.desc)
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatalf("filepath.Walk(%s): %v", root, err)
	}
}

// =============================================================================
// Full verification sweep
// =============================================================================

// TestTimestamp_FullVerification exercises all 5 feature steps as sub-tests.
func TestTimestamp_FullVerification(t *testing.T) {
	t.Parallel()

	s := buildV1TestServer(t)
	infoTS := buildTestHTTPServer(t, s)

	// Fetch /v1/info once and reuse
	getInfoBody := func(t *testing.T) map[string]any {
		t.Helper()
		resp, err := infoTS.Client().Get(infoTS.URL + "/v1/info")
		if err != nil {
			t.Fatalf("GET /v1/info: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("decode JSON: %v", err)
		}
		return body
	}

	t.Run("Step1_InfoHasServerTime", func(t *testing.T) {
		body := getInfoBody(t)
		if _, ok := body["server_time"]; !ok {
			t.Error("GET /v1/info must include 'server_time' field")
		}
	})

	t.Run("Step2_ServerTimeMatchesRFC3339Pattern", func(t *testing.T) {
		body := getInfoBody(t)
		ts_val, _ := body["server_time"].(string)
		if !reRFC3339UTC.MatchString(ts_val) {
			t.Errorf("server_time %q does not match RFC3339 UTC pattern", ts_val)
		}
	})

	t.Run("Step3_ServerTimeHasZSuffix", func(t *testing.T) {
		body := getInfoBody(t)
		ts_val, _ := body["server_time"].(string)
		if !strings.HasSuffix(ts_val, "Z") {
			t.Errorf("server_time %q must end with 'Z', not '+00:00'", ts_val)
		}
	})

	t.Run("Step4_EchoIssuedAtIsRFC3339UTC", func(t *testing.T) {
		echoTS, stub, _ := buildEchoServer(t)
		token := mintJWT(t, stub, "00000000-0000-0000-0000-000000000068")

		req, err := http.NewRequest(http.MethodPost, echoTS.URL+"/v1/echo",
			strings.NewReader(`{"message":"full_verification"}`))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "00000000-0000-0000-0000-000000000068")

		resp, err := echoTS.Client().Do(req)
		if err != nil {
			t.Fatalf("POST /v1/echo: %v", err)
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("decode JSON: %v", err)
		}

		ts_val, _ := body["issued_at"].(string)
		if ts_val == "" {
			t.Fatal("POST /v1/echo: 'issued_at' missing")
		}
		if !reRFC3339UTC.MatchString(ts_val) {
			t.Errorf("issued_at %q does not match RFC3339 UTC pattern", ts_val)
		}
		if !strings.HasSuffix(ts_val, "Z") {
			t.Errorf("issued_at %q must end with 'Z'", ts_val)
		}
	})

	t.Run("Step5_StaticScan_RFC3339NanoWithUTC", func(t *testing.T) {
		root := findBackendRoot(t)
		violations := scanGoFilesForTimestampFormatting(t, root)
		if len(violations) > 0 {
			t.Errorf("found %d timestamp formatting issue(s): %v", len(violations), violations)
		}
	})
}
