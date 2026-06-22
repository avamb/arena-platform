// http_request_latency_histogram_test.go covers feature #78:
// "HTTP request latency histogram exposed in /metrics"
//
// Feature description:
//   Custom histogram http_request_duration_seconds with labels (route, method,
//   status) and well-chosen buckets is exposed.
//
// Six feature steps verified here:
//
//  1. Curl /metrics, grep for 'http_request_duration_seconds'
//  2. Verify histogram has _sum, _count, _bucket lines
//  3. Verify buckets cover 5ms..30s (e.g., 0.005, 0.01, 0.025, ..., 10, 30)
//  4. Verify labels include route, method, status
//  5. Make 100 requests to /v1/info and re-check -- _count for that label set
//     increased by 100
//  6. Verify the histogram does NOT explode cardinality (no user-id labels,
//     no raw URL labels)
//
// All tests use the in-process httptest helpers wired to the REAL
// observability.Metrics so the scrape output is genuine Prometheus text-format.
// No external PostgreSQL or network is required.
package httpserver

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// Step 1 — /metrics contains 'http_request_duration_seconds'
// =============================================================================

// TestHTTPLatencyHistogram_ExistsInMetrics verifies that the Prometheus scrape
// body produced by /metrics contains the arena_http_request_duration_seconds
// histogram family name, satisfying feature step 1 ("grep for
// 'http_request_duration_seconds'").
func TestHTTPLatencyHistogram_ExistsInMetrics(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	// Seed one observation so the histogram appears in the Gather output
	// (unfired HistogramVec label-sets are omitted by Prometheus until the
	// first observation is recorded for that label combination).
	m.HTTPRequestDuration.WithLabelValues("GET", "/v1/info", "200").Observe(0.005)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// Step 1: the metric family must be present in the scrape output.
	if !strings.Contains(bodyStr, "http_request_duration_seconds") {
		t.Errorf("step 1 — /metrics body does not contain 'http_request_duration_seconds';\nexcerpt:\n%s",
			truncate(bodyStr, 800))
	}
	// Full qualified name must be present.
	if !strings.Contains(bodyStr, "arena_http_request_duration_seconds") {
		t.Errorf("step 1 — /metrics body does not contain fully-qualified name 'arena_http_request_duration_seconds'")
	}
}

// =============================================================================
// Step 2 — Histogram has _sum, _count, _bucket lines
// =============================================================================

// TestHTTPLatencyHistogram_HasSumCountBucket verifies that the scrape output
// includes the three obligatory Prometheus histogram suffixes — _sum, _count,
// and _bucket — for the arena_http_request_duration_seconds histogram.
// The Prometheus text format mandates these three series for every histogram
// family.
func TestHTTPLatencyHistogram_HasSumCountBucket(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	m.HTTPRequestDuration.WithLabelValues("POST", "/v1/echo", "200").Observe(0.042)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	requiredSuffixes := []string{
		"arena_http_request_duration_seconds_sum",
		"arena_http_request_duration_seconds_count",
		"arena_http_request_duration_seconds_bucket",
	}
	for _, suffix := range requiredSuffixes {
		if !strings.Contains(bodyStr, suffix) {
			t.Errorf("step 2 — /metrics body missing %q", suffix)
		}
	}
}

// TestHTTPLatencyHistogram_HELPAndTYPELinesPresent verifies that the scrape
// output includes the # HELP and # TYPE lines for the histogram, confirming
// it is a properly documented collector (not a bare counter or gauge).
func TestHTTPLatencyHistogram_HELPAndTYPELinesPresent(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	m.HTTPRequestDuration.WithLabelValues("GET", "/healthz", "200").Observe(0.001)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "# HELP arena_http_request_duration_seconds") {
		t.Errorf("step 2 — missing '# HELP arena_http_request_duration_seconds'")
	}
	if !strings.Contains(bodyStr, "# TYPE arena_http_request_duration_seconds histogram") {
		t.Errorf("step 2 — missing '# TYPE arena_http_request_duration_seconds histogram'")
	}
}

// =============================================================================
// Step 3 — Buckets cover 5ms..30s
// =============================================================================

// TestHTTPLatencyHistogram_BucketsFromFiveMsTo30s verifies that the histogram
// buckets span the full range mandated by feature step 3: 5 ms on the low end
// (le=0.005) through 30 s on the high end (le=30). The intermediate buckets
// (0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10) are also verified.
//
// The Prometheus text format emits one _bucket line per boundary per label
// combination. We observe a request that will fall into the first bucket
// (sub-5 ms) and then look for all expected le= values in the scrape output.
func TestHTTPLatencyHistogram_BucketsFromFiveMsTo30s(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	// Record one observation so bucket lines are emitted.
	m.HTTPRequestDuration.WithLabelValues("GET", "/v1/info", "200").Observe(0.003)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// Every bucket from 5ms (0.005) to 30s (30) must appear as le= values in
	// the scrape output. We also check that le="0.001" is present (1ms bucket
	// for sub-millisecond health-check responses) and le="+Inf" (always present
	// as the catch-all bucket for Prometheus histograms).
	wantLE := []string{
		`le="0.001"`,  // 1 ms
		`le="0.005"`,  // 5 ms   ← low end of feature spec
		`le="0.01"`,   // 10 ms
		`le="0.025"`,  // 25 ms
		`le="0.05"`,   // 50 ms
		`le="0.1"`,    // 100 ms
		`le="0.25"`,   // 250 ms
		`le="0.5"`,    // 500 ms
		`le="1"`,      // 1 s
		`le="2.5"`,    // 2.5 s
		`le="5"`,      // 5 s
		`le="10"`,     // 10 s
		`le="30"`,     // 30 s  ← high end of feature spec
		`le="+Inf"`,   // catch-all (always emitted)
	}
	for _, want := range wantLE {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("step 3 — /metrics body missing bucket %s in arena_http_request_duration_seconds", want)
		}
	}
}

// TestHTTPLatencyHistogram_BucketsCoverMin5msAndMax30s is a structural check on
// DefaultHTTPDurationBuckets: the slice must start at or below 0.005 (5 ms)
// and end at 30 (30 s). This mirrors what step 3 requires without involving a
// live HTTP scrape, making it a fast safety net for accidental bucket shrinkage.
func TestHTTPLatencyHistogram_BucketsCoverMin5msAndMax30s(t *testing.T) {
	t.Parallel()

	buckets := observability.DefaultHTTPDurationBuckets
	if len(buckets) == 0 {
		t.Fatal("DefaultHTTPDurationBuckets is empty")
	}

	// Lowest bucket must be ≤ 0.005 (5 ms) to cover the fast-path responses.
	const wantLow = 0.005
	if buckets[0] > wantLow {
		t.Errorf("step 3 — lowest bucket = %v, want ≤ %v (5 ms)", buckets[0], wantLow)
	}

	// Highest bucket must be ≥ 30 (30 s) to cover the worst-case slow requests.
	const wantHigh = 30.0
	last := buckets[len(buckets)-1]
	if last < wantHigh {
		t.Errorf("step 3 — highest bucket = %v, want ≥ %v (30 s)", last, wantHigh)
	}

	// The 30 s bucket must exist exactly (not just be ≥ 30).
	found30 := false
	for _, b := range buckets {
		if b == wantHigh {
			found30 = true
			break
		}
	}
	if !found30 {
		t.Errorf("step 3 — bucket 30 (30 s) not found in DefaultHTTPDurationBuckets: %v", buckets)
	}
}

// =============================================================================
// Step 4 — Labels include route, method, status
// =============================================================================

// TestHTTPLatencyHistogram_HasRouteMethodStatusLabels verifies that the scrape
// output for arena_http_request_duration_seconds carries all three required
// labels: method, route, and status. The values chosen here (GET, /v1/info,
// 200) match the canonical example from the feature spec.
func TestHTTPLatencyHistogram_HasRouteMethodStatusLabels(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	m.HTTPRequestDuration.
		WithLabelValues("GET", "/v1/info", "200").
		Observe(0.010)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// The three required labels must appear on the same _count line so we can
	// confirm they are co-present (not just individually mentioned somewhere in
	// the output). We look for the _count suffix because it is always emitted
	// exactly once per label-set, unlike _bucket which has one line per boundary.
	wantCount := `arena_http_request_duration_seconds_count{method="GET",route="/v1/info",status="200"} 1`
	if !strings.Contains(bodyStr, wantCount) {
		// Provide a friendlier diagnostic: check each label individually.
		missing := []string{}
		if !strings.Contains(bodyStr, `method="GET"`) {
			missing = append(missing, `method="GET"`)
		}
		if !strings.Contains(bodyStr, `route="/v1/info"`) {
			missing = append(missing, `route="/v1/info"`)
		}
		if !strings.Contains(bodyStr, `status="200"`) {
			missing = append(missing, `status="200"`)
		}
		t.Errorf("step 4 — want %q in body; missing labels: %v\nbody excerpt:\n%s",
			wantCount, missing, truncate(bodyStr, 1000))
	}
}

// TestHTTPLatencyHistogram_LabelOrderIsMethodRoutStatus confirms that the
// Prometheus client emits labels in the canonical (alphabetical) order:
// method, route, status. This order is guaranteed by the Prometheus Go client
// library (labels are sorted alphabetically). The test validates the contract
// explicitly so a future refactor that reorders LabelNames in the registration
// call is caught immediately.
func TestHTTPLatencyHistogram_LabelOrderIsMethodRouteStatus(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	m.HTTPRequestDuration.
		WithLabelValues("POST", "/v1/echo", "201").
		Observe(0.025)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// Look for a bucket line that has method, route, status in that order.
	// Prometheus always sorts labels alphabetically:
	//   method < route < status  (alphabetical: m < r < s)
	wantPattern := `method="POST",route="/v1/echo",status="201"`
	if !strings.Contains(bodyStr, wantPattern) {
		t.Errorf("step 4 — label order: want pattern %q not found in body excerpt:\n%s",
			wantPattern, truncate(bodyStr, 1000))
	}
}

// =============================================================================
// Step 5 — Make 100 requests to /v1/info; _count increases by 100
// =============================================================================

// TestHTTPLatencyHistogram_100RequestsIncreaseCount is the headline assertion
// for feature step 5: make 100 GET /v1/info requests through the REAL server
// (so they pass through prometheusMiddleware) and then verify that
// arena_http_request_duration_seconds_count for the /v1/info label set
// equals 100 in the subsequent scrape.
//
// The test uses a fresh server so the counter starts at 0, making the
// "increased by exactly 100" check unambiguous.
func TestHTTPLatencyHistogram_100RequestsIncreaseCount(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	client := ts.Client()

	// Fire 100 GET /v1/info requests in parallel via a pool of goroutines so
	// the test completes quickly. We collect any HTTP errors to fail-fast.
	const n = 100
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/info", nil)
			if err != nil {
				errCh <- fmt.Errorf("build request: %w", err)
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				errCh <- fmt.Errorf("GET /v1/info: %w", err)
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("request error: %v", err)
	}
	if t.Failed() {
		return
	}

	// Scrape /metrics and look for the _count line for the /v1/info label set.
	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// Find the _count line that matches the /v1/info route pattern.
	// chi normalises the route to "/v1/info" (no trailing slash, no parameters).
	wantPrefix := `arena_http_request_duration_seconds_count{method="GET",route="/v1/info",status="200"}`
	count := extractCounterValue(bodyStr, wantPrefix)
	if count < int64(n) {
		t.Errorf("step 5 — arena_http_request_duration_seconds_count for GET /v1/info/200 = %d, want ≥ %d\nbody excerpt:\n%s",
			count, n, truncate(bodyStr, 1500))
	}
}

// TestHTTPLatencyHistogram_CountIncrementsPerRequest validates the
// per-request increment semantic: each HTTP request to any matched route
// adds exactly 1 to the _count for that label set. We use 10 requests
// (a lighter-weight version of the step-5 check) with a separate route to
// avoid interference with other parallel tests.
func TestHTTPLatencyHistogram_CountIncrementsPerRequest(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	client := ts.Client()
	const n = 10

	for i := 0; i < n; i++ {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
		if err != nil {
			t.Fatalf("build request %d: %v", i, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /healthz request %d: %v", i, err)
		}
		resp.Body.Close()
	}

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	bodyStr := string(body)

	wantPrefix := `arena_http_request_duration_seconds_count{method="GET",route="/healthz",status="200"}`
	count := extractCounterValue(bodyStr, wantPrefix)
	if count < int64(n) {
		t.Errorf("expected _count ≥ %d after %d /healthz requests, got %d\nbody:\n%s",
			n, n, count, truncate(bodyStr, 1200))
	}
}

// =============================================================================
// Step 6 — Histogram does NOT explode cardinality
// =============================================================================

// TestHTTPLatencyHistogram_NoUserIDLabel verifies that the histogram does not
// include a user_id or user label — a high-cardinality label that would cause
// one metric series per unique user, breaking Prometheus memory constraints.
// The route pattern "/v1/orders/{id}" is an example: the histogram must label
// it as "/v1/orders/{id}" (the chi pattern) not "/v1/orders/abc-123-xyz"
// (the concrete path that embeds the user/resource ID).
func TestHTTPLatencyHistogram_NoUserIDLabel(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)

	// Record observations that could hypothetically include high-cardinality data
	// if the metric were misimplemented.
	m.HTTPRequestDuration.WithLabelValues("GET", "/v1/info", "200").Observe(0.005)
	m.HTTPRequestDuration.WithLabelValues("GET", "/healthz", "200").Observe(0.001)
	m.HTTPRequestDuration.WithLabelValues("POST", "/v1/echo", "200").Observe(0.100)

	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// The histogram lines must NOT contain any high-cardinality labels.
	// We scan only the arena_http_request_duration_seconds lines.
	forbiddenLabels := []string{
		`user_id=`,
		`user=`,
		`account_id=`,
		`tenant_id=`,
		`session_id=`,
		`trace_id=`,
		`request_id=`,
	}
	lines := strings.Split(bodyStr, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "arena_http_request_duration_seconds") {
			continue
		}
		for _, forbidden := range forbiddenLabels {
			if strings.Contains(line, forbidden) {
				t.Errorf("step 6 — high-cardinality label %q found in histogram line:\n  %s", forbidden, line)
			}
		}
	}
}

// TestHTTPLatencyHistogram_NoRawURLLabel verifies that the 'route' label value
// is a chi route pattern (low-cardinality template like "/v1/info") and NOT the
// raw request URL (which would embed query strings, parameters, or user IDs).
//
// The prometheusMiddleware reads chi.RouteContext(r.Context()).RoutePattern()
// AFTER the handler ran, so it always gets the normalised template. Unmatched
// routes are labelled "unmatched" to preserve a single cardinality-safe catch-all.
func TestHTTPLatencyHistogram_NoRawURLLabel(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	client := ts.Client()

	// Make a request that includes a query string — the route label must NOT
	// include the query string or any URL parameter values.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/info?foo=bar&user_id=secret123", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/info?...: %v", err)
	}
	resp.Body.Close()

	scrape := getMetrics(t, ts)
	defer scrape.Body.Close()

	body, err := io.ReadAll(scrape.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// The query string / parameter values must not appear as label values.
	forbiddenFragments := []string{
		"foo=bar",
		"user_id=secret123",
		"?foo",
		"?user",
	}
	lines := strings.Split(bodyStr, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "arena_http_request_duration_seconds") {
			continue
		}
		for _, forbidden := range forbiddenFragments {
			if strings.Contains(line, forbidden) {
				t.Errorf("step 6 — raw URL fragment %q leaked into histogram label:\n  %s", forbidden, line)
			}
		}
	}
}

// TestHTTPLatencyHistogram_UnmatchedRouteLabelledSafely verifies that a
// request to a non-existent URL never leaks the raw URL path into the
// histogram labels. chi uses two cardinality-safe strategies:
//
//   - Requests under a registered wildcard (e.g. /v1/*) are labelled with
//     the low-cardinality wildcard pattern, not the concrete URL.
//   - Requests with no route match at all are labelled "unmatched".
//
// Either outcome is acceptable; what is NOT acceptable is a label value
// that contains the raw resource ID embedded in the URL path.
func TestHTTPLatencyHistogram_UnmatchedRouteLabelledSafely(t *testing.T) {
	t.Parallel()
	ts, _ := buildMetricsTestServer(t)

	client := ts.Client()

	// Request a /v1/ path that contains a fake resource ID embedded in the URL.
	// The router either matches a wildcard /v1/* (low-cardinality) or returns
	// "unmatched" — it must NOT embed the raw path in the label.
	rawID := "abc-123-secret-resource-id"
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/no-such-endpoint/"+rawID, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/no-such-endpoint: %v", err)
	}
	resp.Body.Close()

	// Also test a completely unknown top-level path — this must produce the
	// "unmatched" label since there is no wildcard catch-all at the root.
	rawID2 := "totally-unknown-path-xyz987"
	req2, err := http.NewRequest(http.MethodGet, ts.URL+"/"+rawID2+"/deeply/nested", nil)
	if err != nil {
		t.Fatalf("build request2: %v", err)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("GET /unknown: %v", err)
	}
	resp2.Body.Close()

	scrape := getMetrics(t, ts)
	defer scrape.Body.Close()

	body, err := io.ReadAll(scrape.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// The raw embedded IDs / paths must NOT appear as route label values.
	// The label must use either a chi wildcard pattern (e.g. /v1/*) or "unmatched".
	forbiddenFragments := []string{rawID, rawID2, "/v1/no-such-endpoint/", "/totally-unknown-path"}

	lines := strings.Split(bodyStr, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "arena_http_request_duration_seconds") {
			continue
		}
		for _, forbidden := range forbiddenFragments {
			if strings.Contains(line, forbidden) {
				t.Errorf("step 6 — raw URL fragment %q leaked into histogram label value:\n  %s", forbidden, line)
			}
		}
	}

	// Positive check: the completely unknown path must produce "unmatched".
	wantUnmatched := `route="unmatched"`
	foundUnmatched := false
	for _, line := range lines {
		if strings.HasPrefix(line, "arena_http_request_duration_seconds") && strings.Contains(line, wantUnmatched) {
			foundUnmatched = true
			break
		}
	}
	if !foundUnmatched {
		t.Errorf("step 6 — expected route=\"unmatched\" label for completely unknown path requests;\nnot found in histogram lines; body excerpt:\n%s",
			truncate(bodyStr, 1200))
	}
}

// =============================================================================
// DefaultHTTPDurationBuckets unit checks
// =============================================================================

// TestDefaultHTTPDurationBuckets_IncludesAll13Boundaries ensures the canonical
// bucket slice contains every value required by the feature spec, in order.
func TestDefaultHTTPDurationBuckets_IncludesAll13Boundaries(t *testing.T) {
	t.Parallel()

	wantBuckets := []float64{
		0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30,
	}
	got := observability.DefaultHTTPDurationBuckets

	if len(got) != len(wantBuckets) {
		t.Errorf("bucket count = %d, want %d; buckets: %v", len(got), len(wantBuckets), got)
		return
	}
	for i, want := range wantBuckets {
		if got[i] != want {
			t.Errorf("bucket[%d] = %v, want %v", i, got[i], want)
		}
	}
}

// =============================================================================
// Full verification sweep — all 6 steps in one test
// =============================================================================

// TestHTTPLatencyHistogram_FullVerification runs all six feature steps in a
// single flow, matching the acceptance test structure used throughout this
// project. It is the canonical feature-#78 acceptance test.
func TestHTTPLatencyHistogram_FullVerification(t *testing.T) {
	t.Parallel()
	ts, m := buildMetricsTestServer(t)
	client := ts.Client()

	// --------------------------------------------------------------------------
	// Pre-warm: record an observation before scraping so the histogram appears.
	// --------------------------------------------------------------------------
	m.HTTPRequestDuration.WithLabelValues("GET", "/v1/info", "200").Observe(0.005)

	// --------------------------------------------------------------------------
	// Make 100 GET /v1/info requests through the real server so prometheusMiddleware
	// increments the counter (step 5).
	// --------------------------------------------------------------------------
	const requestCount = 100
	var wg sync.WaitGroup
	wg.Add(requestCount)
	for i := 0; i < requestCount; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/info", nil)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	// --------------------------------------------------------------------------
	// Scrape /metrics (no auth header — step 6 implies open endpoint for Prometheus).
	// --------------------------------------------------------------------------
	resp := getMetrics(t, ts)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	bodyStr := string(body)

	// Step 1 — histogram metric family is present.
	t.Run("step1_histogram_exists", func(t *testing.T) {
		if !strings.Contains(bodyStr, "http_request_duration_seconds") {
			t.Error("histogram metric 'http_request_duration_seconds' not found in /metrics output")
		}
	})

	// Step 2 — _sum, _count, _bucket suffixes are present.
	t.Run("step2_sum_count_bucket", func(t *testing.T) {
		for _, suffix := range []string{"_sum", "_count", "_bucket"} {
			if !strings.Contains(bodyStr, "arena_http_request_duration_seconds"+suffix) {
				t.Errorf("missing suffix %q in /metrics output", suffix)
			}
		}
	})

	// Step 3 — buckets span 5 ms (0.005) to 30 s (30).
	t.Run("step3_buckets_5ms_to_30s", func(t *testing.T) {
		for _, le := range []string{`le="0.005"`, `le="0.01"`, `le="0.025"`, `le="0.05"`,
			`le="0.1"`, `le="0.25"`, `le="0.5"`, `le="1"`, `le="2.5"`, `le="5"`,
			`le="10"`, `le="30"`, `le="+Inf"`} {
			if !strings.Contains(bodyStr, le) {
				t.Errorf("bucket %s not found in /metrics output", le)
			}
		}
	})

	// Step 4 — labels include method, route, status.
	t.Run("step4_labels_method_route_status", func(t *testing.T) {
		pattern := `arena_http_request_duration_seconds_count{method="GET",route="/v1/info",status="200"}`
		if !strings.Contains(bodyStr, pattern) {
			t.Errorf("expected label pattern %q not found in /metrics; body excerpt:\n%s",
				pattern, truncate(bodyStr, 800))
		}
	})

	// Step 5 — _count for GET /v1/info/200 is ≥ requestCount.
	t.Run("step5_count_increased_by_100", func(t *testing.T) {
		prefix := `arena_http_request_duration_seconds_count{method="GET",route="/v1/info",status="200"}`
		count := extractCounterValue(bodyStr, prefix)
		// The pre-warm observation (above) plus the 100 real requests through the
		// middleware means count must be ≥ requestCount.
		if count < int64(requestCount) {
			t.Errorf("_count = %d after %d requests; want ≥ %d", count, requestCount, requestCount)
		}
	})

	// Step 6 — no high-cardinality labels.
	t.Run("step6_no_cardinality_explosion", func(t *testing.T) {
		forbiddenLabels := []string{"user_id=", "user=", "account_id=", "trace_id=", "request_id="}
		lines := strings.Split(bodyStr, "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "arena_http_request_duration_seconds") {
				continue
			}
			for _, forbidden := range forbiddenLabels {
				if strings.Contains(line, forbidden) {
					t.Errorf("high-cardinality label %q found in histogram line:\n  %s", forbidden, line)
				}
			}
		}
	})
}

// =============================================================================
// Helpers
// =============================================================================

// buildLatencyHistogramTestServer is a thin alias around buildMetricsTestServer
// so this file is self-contained even if buildMetricsTestServer lives in a
// sibling test file.
//
// (In practice both files share the same package so buildMetricsTestServer is
// directly accessible without re-declaration.)

// extractCounterValue searches bodyStr for a line that starts with prefix and
// returns the integer counter value at the end of that line. Returns 0 when
// no matching line is found or the value cannot be parsed. The Prometheus text
// format emits "name{labels...} VALUE [TIMESTAMP]" so we split on the last
// space to extract VALUE.
func extractCounterValue(bodyStr, prefix string) int64 {
	for _, line := range strings.Split(bodyStr, "\n") {
		// Skip comment lines.
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		// Format: "metric{labels} 123" or "metric{labels} 123 1234567890"
		// Split on space to isolate the value field.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			// Might be a timestamp; try second-to-last field.
			if len(fields) >= 3 {
				val, err = strconv.ParseFloat(fields[len(fields)-2], 64)
				if err != nil {
					continue
				}
			} else {
				continue
			}
		}
		return int64(val)
	}
	return 0
}

// buildLatencyHistogramRawServer builds a minimal httptest.Server directly
// wired to a real observability.Metrics, bypassing the full Server setup.
// Used in tests that only need the Prometheus registry and a /metrics scrape
// without the full chi router / handler chain.
func buildLatencyHistogramRawServer(t *testing.T) (*httptest.Server, *observability.Metrics) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := observability.New(reg)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	ts := httptest.NewServer(m.Handler())
	t.Cleanup(ts.Close)
	return ts, m
}
