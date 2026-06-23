// promtool_format_test.go verifies feature #77:
// "Prometheus /metrics output parseable by promtool"
//
// The five feature steps covered here:
//
//  1. GET /metrics and capture the scrape body.
//  2. Parse with expfmt.TextParser — the same parser used by
//     `promtool check metrics`; expect no parse error (≡ exit 0).
//  3. Every metric data line is preceded by both a # HELP and a # TYPE
//     comment for its metric family name.
//  4. Label values are properly escaped; no raw (unescaped) newlines
//     appear inside label value strings.
//  5. Counter metric families carry _total suffix in their data lines;
//     histogram metric families expose _sum, _count, and _bucket series.
//
// All steps use in-process httptest helpers wired to the REAL
// observability.Metrics (same registry pattern as metrics_endpoint_test.go).
// No external services are required.
package httpserver

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
)

// =============================================================================
// helpers shared by this file
// =============================================================================

// scrapeMetrics returns the full /metrics scrape body with all baseline arena
// metric vectors pre-seeded so they appear in the Gather output.
func scrapeMetrics(t *testing.T) string {
	t.Helper()
	ts, m := buildMetricsTestServer(t)

	// Pre-seed every Vec/Counter/Gauge so they appear in the scrape output
	// (Prometheus omits label combinations with zero observations unless
	// Add(0) / Set(0) is called explicitly).
	m.HTTPRequestDuration.WithLabelValues("GET", "/v1/info", "200").Observe(0.005)
	m.HTTPRequestDuration.WithLabelValues("POST", "/v1/echo", "200").Observe(0.042)
	m.DBPoolConnections.WithLabelValues("acquired").Set(1)
	m.DBPoolConnections.WithLabelValues("idle").Set(4)
	m.WorkerJobsLagSeconds.WithLabelValues("default").Set(0)
	m.OutboxBacklog.Set(0)
	m.HTTPPanicsTotal.Add(0)
	m.IdempotencyReplaysTotal.Add(0)
	m.IdempotencyCleanupDeletedTotal.Add(0)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	return string(body)
}

// parseTextFormat uses expfmt.TextParser to decode the body and returns the
// resulting family map. Fails the test immediately on any parse error so
// callers can use the returned map unconditionally.
func parseTextFormat(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var p expfmt.TextParser
	families, err := p.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expfmt.TextParser.TextToMetricFamilies: %v\n\nRaw /metrics (first 1000 chars):\n%s",
			err, truncate(body, 1000))
	}
	// Convert to map[string]interface{} to avoid importing the dto package.
	out := make(map[string]interface{}, len(families))
	for k, v := range families {
		out[k] = v
	}
	return out
}

// =============================================================================
// Step 1 — GET /metrics, capture body
// =============================================================================

// TestPromtoolFormat_Step1_GetMetrics verifies that GET /metrics returns a
// non-empty body that contains at least one # HELP line, confirming the
// endpoint is up and serving Prometheus text-format output.
func TestPromtoolFormat_Step1_GetMetrics(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	if len(body) == 0 {
		t.Fatal("step 1: /metrics body is empty")
	}
	if !strings.Contains(body, "# HELP ") {
		t.Error("step 1: /metrics body contains no # HELP lines; scrape output looks invalid")
	}
	if !strings.Contains(body, "# TYPE ") {
		t.Error("step 1: /metrics body contains no # TYPE lines; scrape output looks invalid")
	}
}

// =============================================================================
// Step 2 — Parse with expfmt.TextParser (≡ promtool check metrics exit 0)
// =============================================================================

// TestPromtoolFormat_Step2_ParseableByPromtool verifies that the /metrics
// output is accepted by expfmt.TextParser without error. This is the Go-level
// equivalent of running `promtool check metrics < /tmp/metrics.txt` and
// expecting exit code 0: if TextToMetricFamilies returns nil error the format
// is syntactically valid Prometheus text format.
func TestPromtoolFormat_Step2_ParseableByPromtool(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	var p expfmt.TextParser
	families, err := p.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatalf("step 2: promtool-equivalent parse returned error: %v\n\nFirst 1000 chars of /metrics:\n%s",
			err, truncate(body, 1000))
	}
	if len(families) == 0 {
		t.Error("step 2: parser returned zero metric families; body may be empty or all-comment")
	}
}

// TestPromtoolFormat_Step2_ArenaFamiliesParseable verifies that specifically
// the arena_* metric families (our custom metrics) are parseable, not just the
// standard go_* and process_* families that the Go runtime collector injects.
func TestPromtoolFormat_Step2_ArenaFamiliesParseable(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	var p expfmt.TextParser
	families, err := p.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatalf("step 2: parse error: %v", err)
	}

	wantFamilies := []string{
		"arena_http_request_duration_seconds",
		"arena_http_requests_total",
		"arena_db_pool_connections",
		"arena_worker_jobs_lag_seconds",
		"arena_outbox_backlog",
		"arena_http_panics_total",
	}
	for _, want := range wantFamilies {
		if _, ok := families[want]; !ok {
			t.Errorf("step 2: metric family %q missing from parsed output", want)
		}
	}
}

// =============================================================================
// Step 3 — # HELP and # TYPE precede every metric data line
// =============================================================================

// metricFamilyNameFromLine extracts the Prometheus metric family name from a
// raw data line. Prometheus text format lines look like:
//
//	metric_name{label="value"} 1.0 [timestamp]
//
// For histogram / summary variants the series name carries a known suffix
// (_bucket, _count, _sum, _created) that must be stripped to recover the
// family name that appears on # TYPE / # HELP lines.
func metricFamilyNameFromLine(line string) string {
	// Find end of metric name (before '{' or whitespace)
	end := strings.IndexAny(line, "{ \t")
	var name string
	if end == -1 {
		name = line
	} else {
		name = line[:end]
	}
	// Strip known suffixes (longest first to avoid partial strip of _count vs _count)
	for _, sfx := range []string{"_bucket", "_created", "_count", "_sum"} {
		if strings.HasSuffix(name, sfx) {
			return strings.TrimSuffix(name, sfx)
		}
	}
	return name
}

// TestPromtoolFormat_Step3_HELPAndTYPEPrecedeDefinitions walks every line of
// the /metrics scrape output and verifies that no data line appears before a
// # TYPE (and # HELP) comment for its metric family. This mirrors the
// invariant that promtool check metrics enforces.
func TestPromtoolFormat_Step3_HELPAndTYPEPrecedeDefinitions(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	seenHELP := make(map[string]bool)
	seenTYPE := make(map[string]bool)
	failures := 0

	scanner := bufio.NewScanner(strings.NewReader(body))
	lineNum := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			parts := strings.Fields(line) // ["#", "HELP", "name", ...]
			if len(parts) >= 3 {
				seenHELP[parts[2]] = true
			}
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.Fields(line) // ["#", "TYPE", "name", "type"]
			if len(parts) >= 3 {
				seenTYPE[parts[2]] = true
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue // other comment lines (e.g. EOF marker)
		}
		// This is a metric data line.
		familyName := metricFamilyNameFromLine(line)
		if familyName == "" {
			continue
		}
		// Resolve the actual family name used in TYPE/HELP declarations.
		// metricFamilyNameFromLine strips _count/_sum/_bucket/_created suffixes to
		// recover the histogram/summary base name. However, some standalone Gauge
		// metrics legitimately end in _count (e.g. arena_db_pool_wait_count).
		// For those, the TYPE/HELP declaration uses the full name; we fall back to
		// the raw metric name when the stripped family is not in the TYPE map.
		resolvedFamily := familyName
		if !seenTYPE[familyName] {
			// Extract the raw metric name from the line (before labels or spaces).
			rawEnd := strings.IndexAny(line, "{ \t")
			rawName := line
			if rawEnd != -1 {
				rawName = line[:rawEnd]
			}
			if seenTYPE[rawName] {
				resolvedFamily = rawName
			}
		}
		if !seenTYPE[resolvedFamily] {
			t.Errorf("step 3 (line %d): data line for %q has no preceding # TYPE:\n  %s",
				lineNum, resolvedFamily, truncate(line, 120))
			failures++
			if failures >= 5 {
				t.Log("step 3: stopping after 5 failures; fix the above before continuing")
				break
			}
		}
		if !seenHELP[resolvedFamily] {
			// HELP is technically optional in Prometheus text format but
			// client_golang always emits it; flag the absence as an error.
			t.Errorf("step 3 (line %d): data line for %q has no preceding # HELP:\n  %s",
				lineNum, resolvedFamily, truncate(line, 120))
			failures++
			if failures >= 5 {
				break
			}
		}
	}
}

// TestPromtoolFormat_Step3_ArenaMetricsHaveHELPBeforeTYPE verifies that for
// every arena_* metric family the # HELP line appears before the # TYPE line
// (the Prometheus spec requires this ordering when HELP is present).
func TestPromtoolFormat_Step3_ArenaMetricsHaveHELPBeforeTYPE(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	// For each arena_* family record the line number of HELP and TYPE.
	type linePos struct{ help, typ int }
	positions := make(map[string]*linePos)

	scanner := bufio.NewScanner(strings.NewReader(body))
	lineNum := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++
		if strings.HasPrefix(line, "# HELP arena_") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				lp := positions[parts[2]]
				if lp == nil {
					lp = &linePos{}
					positions[parts[2]] = lp
				}
				lp.help = lineNum
			}
		} else if strings.HasPrefix(line, "# TYPE arena_") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				lp := positions[parts[2]]
				if lp == nil {
					lp = &linePos{}
					positions[parts[2]] = lp
				}
				lp.typ = lineNum
			}
		}
	}

	for family, pos := range positions {
		if pos.help == 0 {
			t.Errorf("step 3: arena family %q has # TYPE but no # HELP line", family)
			continue
		}
		if pos.typ == 0 {
			t.Errorf("step 3: arena family %q has # HELP but no # TYPE line", family)
			continue
		}
		if pos.help >= pos.typ {
			t.Errorf("step 3: arena family %q — # HELP (line %d) must appear before # TYPE (line %d)",
				family, pos.help, pos.typ)
		}
	}
}

// =============================================================================
// Step 4 — Label values are properly escaped
// =============================================================================

// TestPromtoolFormat_Step4_LabelValuesEscaped verifies that no label value in
// the scrape output contains a raw (unescaped) newline character. Raw newlines
// in label values are forbidden by the Prometheus text format spec and would
// cause promtool to report a syntax error. The expfmt.TextParser used in step 2
// already validates this, but we also do an explicit character-level scan so the
// failure message is specific to the offending label value.
func TestPromtoolFormat_Step4_LabelValuesEscaped(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	scanner := bufio.NewScanner(strings.NewReader(body))
	lineNum := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineNum++
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		// Each metric data line must fit on one line. The scanner splits on \n,
		// so if we reach here the line itself has no embedded newline — that's
		// the guarantee. We also verify that no label value contains a literal
		// backslash followed by an invalid escape character.
		if err := validateLabelEscapes(line); err != nil {
			t.Errorf("step 4 (line %d): invalid label escape: %v\n  line: %s",
				lineNum, err, truncate(line, 200))
		}
	}
}

// validateLabelEscapes scans the label set in a metric data line and reports
// an error if any label value contains an invalid escape sequence. Valid
// sequences are: \\ (backslash), \n (newline), \" (double-quote).
func validateLabelEscapes(line string) error {
	// Extract the label set: the portion between '{' and '}'.
	open := strings.Index(line, "{")
	close := strings.LastIndex(line, "}")
	if open == -1 || close == -1 || close <= open {
		return nil // no labels on this line
	}
	labelSet := line[open+1 : close]

	// Walk through quoted values only.
	inValue := false
	i := 0
	for i < len(labelSet) {
		ch := labelSet[i]
		if !inValue {
			if ch == '"' {
				inValue = true
			}
			i++
			continue
		}
		// Inside a quoted label value.
		if ch == '\\' {
			if i+1 >= len(labelSet) {
				return &labelEscapeError{raw: labelSet, pos: i, msg: "trailing backslash"}
			}
			next := labelSet[i+1]
			switch next {
			case '\\', 'n', '"':
				i += 2 // valid escape
			default:
				return &labelEscapeError{
					raw: labelSet, pos: i,
					msg: "invalid escape sequence \\" + string(next),
				}
			}
			continue
		}
		if ch == '"' {
			inValue = false
			i++
			continue
		}
		// Any other character inside a quoted value is allowed.
		i++
	}
	return nil
}

// labelEscapeError is the error type returned by validateLabelEscapes.
type labelEscapeError struct {
	raw string
	pos int
	msg string
}

func (e *labelEscapeError) Error() string {
	return e.msg + " at position " + string(rune('0'+e.pos%10)) + " in label set: " + truncate(e.raw, 80)
}

// TestPromtoolFormat_Step4_ParseValidatesEscaping verifies that the expfmt
// parser itself would reject label values with bad escape sequences. We
// construct a deliberately malformed body and confirm the parser returns an
// error, establishing that our step 2 check catches any real escaping problems
// in production output.
func TestPromtoolFormat_Step4_ParseValidatesEscaping(t *testing.T) {
	t.Parallel()
	// Metric with an invalid escape \x in a label value.
	malformed := `# HELP arena_test_total A synthetic counter.
# TYPE arena_test_total counter
arena_test_total{label="bad\xvalue"} 1
`
	var p expfmt.TextParser
	_, err := p.TextToMetricFamilies(strings.NewReader(malformed))
	if err == nil {
		t.Error("step 4: expfmt.TextParser accepted malformed label escape \\x — expected an error")
	}
}

// =============================================================================
// Step 5 — Counter and histogram suffixes correctly applied
// =============================================================================

// TestPromtoolFormat_Step5_CounterNamesEndInTotal verifies that every metric
// family declared as TYPE counter has a name that ends with _total. This is
// the Prometheus naming convention enforced by client_golang v1.x for counters,
// and promtool flags counters whose names lack the _total suffix.
func TestPromtoolFormat_Step5_CounterNamesEndInTotal(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "# TYPE ") {
			continue
		}
		parts := strings.Fields(line) // ["#", "TYPE", "name", "type"]
		if len(parts) < 4 {
			continue
		}
		name, typ := parts[2], parts[3]
		if typ != "counter" {
			continue
		}
		if !strings.HasSuffix(name, "_total") {
			t.Errorf("step 5: counter family %q does not end with _total", name)
		}
	}
}

// TestPromtoolFormat_Step5_ArenaCountersSuffixedTotal verifies that all
// known arena counter metric families carry the _total suffix. This is
// an explicit whitelist check complementing the generic scan above.
func TestPromtoolFormat_Step5_ArenaCountersSuffixedTotal(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	wantCounters := []string{
		"arena_http_requests_total",
		"arena_http_panics_total",
		"arena_idempotency_replays_total",
		"arena_idempotency_cleanup_deleted_total",
	}
	for _, name := range wantCounters {
		typeLine := "# TYPE " + name + " counter"
		if !strings.Contains(body, typeLine) {
			t.Errorf("step 5: expected '# TYPE %s counter' in /metrics output", name)
		}
	}
}

// TestPromtoolFormat_Step5_HistogramHasSumCountBucket verifies that every
// histogram metric family exposes the three required series: _sum, _count,
// and at least one _bucket. Without these, Prometheus cannot compute rate()
// on histogram quantiles. promtool check metrics would warn on their absence.
func TestPromtoolFormat_Step5_HistogramHasSumCountBucket(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	// Collect histogram family names from # TYPE lines.
	var histFamilies []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "# TYPE ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		if parts[3] == "histogram" {
			histFamilies = append(histFamilies, parts[2])
		}
	}

	if len(histFamilies) == 0 {
		t.Error("step 5: no histogram families found in /metrics output")
		return
	}

	for _, family := range histFamilies {
		t.Run(family, func(t *testing.T) {
			if !strings.Contains(body, family+"_sum") {
				t.Errorf("step 5: histogram %q missing _sum series", family)
			}
			if !strings.Contains(body, family+"_count") {
				t.Errorf("step 5: histogram %q missing _count series", family)
			}
			if !strings.Contains(body, family+`_bucket{`) {
				t.Errorf("step 5: histogram %q missing _bucket series", family)
			}
		})
	}
}

// TestPromtoolFormat_Step5_ArenaHTTPDurationHistogram specifically verifies
// the arena_http_request_duration_seconds histogram (the most important custom
// histogram) exposes all required series with the expected le label on buckets.
func TestPromtoolFormat_Step5_ArenaHTTPDurationHistogram(t *testing.T) {
	t.Parallel()
	body := scrapeMetrics(t)

	const family = "arena_http_request_duration_seconds"

	checks := []struct {
		label   string
		pattern string
	}{
		{"TYPE histogram", "# TYPE " + family + " histogram"},
		{"_sum series", family + "_sum{"},
		{"_count series", family + "_count{"},
		{"_bucket with le label", family + `_bucket{`},
		{"le=\"+Inf\" bucket", `le="+Inf"`},
	}
	for _, c := range checks {
		if !strings.Contains(body, c.pattern) {
			t.Errorf("step 5: arena HTTP duration histogram — %s: pattern %q not found in /metrics", c.label, c.pattern)
		}
	}
}

// =============================================================================
// Full verification — all five steps in one sweep
// =============================================================================

// TestPromtoolFormat_FullVerification exercises all five feature steps in a
// single test run, providing the canonical acceptance check for feature #77.
func TestPromtoolFormat_FullVerification(t *testing.T) {
	t.Parallel()

	body := scrapeMetrics(t)

	t.Run("step1_get_metrics", func(t *testing.T) {
		if len(body) == 0 {
			t.Fatal("body is empty")
		}
		if !strings.Contains(body, "# HELP ") {
			t.Error("body contains no # HELP lines")
		}
	})

	t.Run("step2_parseable_by_promtool", func(t *testing.T) {
		var p expfmt.TextParser
		families, err := p.TextToMetricFamilies(strings.NewReader(body))
		if err != nil {
			t.Fatalf("expfmt parse error: %v", err)
		}
		if len(families) == 0 {
			t.Error("zero metric families returned")
		}
	})

	t.Run("step3_help_type_precede_definitions", func(t *testing.T) {
		seenHELP := make(map[string]bool)
		seenTYPE := make(map[string]bool)
		scanner := bufio.NewScanner(strings.NewReader(body))
		lineNum := 0
		for scanner.Scan() {
			line := scanner.Text()
			lineNum++
			if line == "" || strings.HasPrefix(line, "#") {
				if strings.HasPrefix(line, "# HELP ") {
					parts := strings.Fields(line)
					if len(parts) >= 3 {
						seenHELP[parts[2]] = true
					}
				}
				if strings.HasPrefix(line, "# TYPE ") {
					parts := strings.Fields(line)
					if len(parts) >= 3 {
						seenTYPE[parts[2]] = true
					}
				}
				continue
			}
			family := metricFamilyNameFromLine(line)
			if family != "" {
				resolvedFamily := family
				if !seenTYPE[family] {
					// Standalone Gauge metrics whose names end in _count (e.g.
					// arena_db_pool_wait_count) declare TYPE under their full name.
					// Fall back to the raw metric name before stripping suffixes.
					rawEnd := strings.IndexAny(line, "{ \t")
					rawName := line
					if rawEnd != -1 {
						rawName = line[:rawEnd]
					}
					if seenTYPE[rawName] {
						resolvedFamily = rawName
					}
				}
				if !seenTYPE[resolvedFamily] {
					t.Errorf("line %d: data line for %q has no preceding # TYPE", lineNum, resolvedFamily)
				}
			}
		}
	})

	t.Run("step4_label_values_escaped", func(t *testing.T) {
		scanner := bufio.NewScanner(strings.NewReader(body))
		lineNum := 0
		for scanner.Scan() {
			line := scanner.Text()
			lineNum++
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			if err := validateLabelEscapes(line); err != nil {
				t.Errorf("line %d: %v", lineNum, err)
			}
		}
	})

	t.Run("step5_counter_and_histogram_suffixes", func(t *testing.T) {
		// Counters must end in _total
		scanner := bufio.NewScanner(strings.NewReader(body))
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "# TYPE ") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) < 4 {
				continue
			}
			name, typ := parts[2], parts[3]
			if typ == "counter" && !strings.HasSuffix(name, "_total") {
				t.Errorf("counter %q does not end with _total", name)
			}
			if typ == "histogram" {
				for _, sfx := range []string{"_sum", "_count", "_bucket{"} {
					if !strings.Contains(body, name+sfx) {
						t.Errorf("histogram %q missing %s series", name, sfx)
					}
				}
			}
		}
	})
}
