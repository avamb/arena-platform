// pool_db_pool_gauges_test.go — unit tests for feature #79
// "DB pool gauges (open, idle, in-use) exposed in /metrics"
//
// Feature spec: Custom gauges for pgx pool stats:
//
//	db_pool_open_connections, db_pool_idle, db_pool_in_use,
//	db_pool_wait_count, db_pool_wait_duration_seconds.
//
// Test steps covered:
//
//	Step 1: /metrics contains lines matching 'db_pool_' prefix
//	Step 2: Each gauge has a proper '# TYPE ... gauge' line in the output
//	Step 3: db_pool_in_use spikes then returns to ~0 after burst
//	Step 4: db_pool_open_connections is between min and max pool size
//	Step 5: burst test (200 parallel requests) — wait_count increments
//	        when max pool is small
//
// All tests use fakePoolStat (defined in pool_burst_test.go) instead of a
// live PostgreSQL instance — no DB connection required.
//
// Run with: go test -v -run TestDBPoolGauges ./apps/backend/internal/adapters/postgres/
package postgres

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// =============================================================================
// Helpers
// =============================================================================

// expectedIndividualGaugeNames lists the five individual metric names required
// by feature #79 (full Prometheus name with arena_ namespace prefix).
var expectedIndividualGaugeNames = []string{
	"arena_db_pool_open_connections",
	"arena_db_pool_idle",
	"arena_db_pool_in_use",
	"arena_db_pool_wait_count",
	"arena_db_pool_wait_duration_seconds",
}

// scrapeMetrics calls publishPoolStatSnapshot on stat and m, then performs an
// HTTP scrape of the Prometheus registry, returning the body as a string.
func scrapeMetrics(t *testing.T, stat poolStatReader, m *observability.Metrics) string {
	t.Helper()
	publishPoolStatSnapshot(stat, m)

	req, _ := newTestRequest(t, "GET", "/metrics", nil)
	rec := newTestResponseRecorder()
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// =============================================================================
// Step 1: grep for 'db_pool_'
// =============================================================================

// TestDBPoolGauges_AllFiveGaugePrefixesPresent verifies that the /metrics
// output contains each of the five 'arena_db_pool_*' gauge names required by
// feature #79 (step 1: "curl /metrics, grep for 'db_pool_'").
func TestDBPoolGauges_AllFiveGaugePrefixesPresent(t *testing.T) {
	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(10)
	body := scrapeMetrics(t, stat, m)

	for _, name := range expectedIndividualGaugeNames {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics body missing gauge %q", name)
		}
	}
}

// =============================================================================
// Step 2: TYPE lines
// =============================================================================

// TestDBPoolGauges_EachGaugeHasTypeLine verifies that each of the five metrics
// has a Prometheus '# TYPE <name> gauge' comment line in the scrape output
// (step 2: "verify each gauge present with proper TYPE line").
func TestDBPoolGauges_EachGaugeHasTypeLine(t *testing.T) {
	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(5)
	body := scrapeMetrics(t, stat, m)

	for _, name := range expectedIndividualGaugeNames {
		typeLine := "# TYPE " + name + " gauge"
		if !strings.Contains(body, typeLine) {
			t.Errorf("/metrics body missing TYPE line %q\n---BODY (first 3000 chars)---\n%s", typeLine, truncate(body, 3000))
		}
	}
}

// =============================================================================
// Step 3: db_pool_in_use spikes then returns to ~0
// =============================================================================

// TestDBPoolGauges_InUseSpikesAndDrains verifies that arena_db_pool_in_use
// rises above zero while connections are acquired and returns to 0 after all
// connections are released (step 3).
func TestDBPoolGauges_InUseSpikesAndDrains(t *testing.T) {
	const maxConns = 20

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)

	// Baseline: in_use == 0.
	body := scrapeMetrics(t, stat, m)
	inUseVal := extractGaugeValue(t, body, "arena_db_pool_in_use")
	if inUseVal != 0 {
		t.Errorf("baseline arena_db_pool_in_use = %v, want 0", inUseVal)
	}

	// Simulate burst: acquire half the connections.
	const burst = maxConns / 2
	for i := 0; i < burst; i++ {
		stat.acquire()
	}

	// Mid-burst: in_use should be > 0.
	body = scrapeMetrics(t, stat, m)
	inUseVal = extractGaugeValue(t, body, "arena_db_pool_in_use")
	if inUseVal <= 0 {
		t.Errorf("mid-burst arena_db_pool_in_use = %v, want > 0", inUseVal)
	}
	t.Logf("mid-burst arena_db_pool_in_use = %v", inUseVal)

	// Release all connections.
	for i := 0; i < burst; i++ {
		stat.release()
	}

	// Post-burst: in_use should return to 0.
	body = scrapeMetrics(t, stat, m)
	inUseVal = extractGaugeValue(t, body, "arena_db_pool_in_use")
	if inUseVal != 0 {
		t.Errorf("post-burst arena_db_pool_in_use = %v, want 0 (no connection leak)", inUseVal)
	}
	t.Logf("post-burst arena_db_pool_in_use = %v (clean)", inUseVal)
}

// =============================================================================
// Step 4: db_pool_open_connections is between min and max
// =============================================================================

// TestDBPoolGauges_OpenConnectionsBetweenMinAndMax verifies that
// arena_db_pool_open_connections reflects the TotalConns value and lies
// between minConns and maxConns (step 4).
func TestDBPoolGauges_OpenConnectionsBetweenMinAndMax(t *testing.T) {
	const (
		minConns int32 = 2
		maxConns int32 = 20
	)

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)
	// Simulate a pool that has opened minConns connections (not yet at max).
	stat.total.Store(minConns)
	stat.idle.Store(minConns)
	stat.acquired.Store(0)

	body := scrapeMetrics(t, stat, m)
	openVal := extractGaugeValue(t, body, "arena_db_pool_open_connections")

	if openVal < float64(minConns) || openVal > float64(maxConns) {
		t.Errorf("arena_db_pool_open_connections = %v, want between %d and %d",
			openVal, minConns, maxConns)
	}
	t.Logf("arena_db_pool_open_connections = %v (within [%d, %d])", openVal, minConns, maxConns)
}

// =============================================================================
// Step 5: burst test — wait_count increments when max pool is small
// =============================================================================

// TestDBPoolGauges_WaitCountIncrementsUnderContention verifies that
// arena_db_pool_wait_count increases when 200 virtual requests contend for a
// small pool (step 5: "run a burst test (200 parallel requests) -- verify
// wait_count increments if max pool is small").
func TestDBPoolGauges_WaitCountIncrementsUnderContention(t *testing.T) {
	const (
		totalRequests = 200
		maxConns      = 5 // deliberately small to force contention
	)

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)

	// Baseline wait_count.
	body := scrapeMetrics(t, stat, m)
	baseWait := extractGaugeValue(t, body, "arena_db_pool_wait_count")
	t.Logf("baseline wait_count = %v", baseWait)

	// Simulate 200 concurrent "acquire" calls where only maxConns are available.
	// When a worker finds the pool exhausted it increments emptyAcquire (wait_count).
	var wg sync.WaitGroup
	var poolSemaphore atomic.Int32 // tracks "in use" slots (max = maxConns)

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Try to acquire a slot. If full, record a wait.
			for {
				current := poolSemaphore.Load()
				if current >= maxConns {
					// Pool is full — this request must wait (simulate wait_count bump).
					stat.emptyAcquire.Add(1)
					stat.acquireDuration.Add(int64(1 * time.Millisecond))
					time.Sleep(1 * time.Millisecond)
					continue
				}
				if poolSemaphore.CompareAndSwap(current, current+1) {
					break
				}
			}

			// Simulate holding the connection briefly.
			time.Sleep(2 * time.Millisecond)

			// Release.
			poolSemaphore.Add(-1)
		}()
	}

	wg.Wait()

	// After burst: wait_count should be > baseline.
	body = scrapeMetrics(t, stat, m)
	postWait := extractGaugeValue(t, body, "arena_db_pool_wait_count")
	t.Logf("post-burst wait_count = %v", postWait)

	if postWait <= baseWait {
		t.Errorf("arena_db_pool_wait_count after burst = %v, want > %v (contention must register waits)",
			postWait, baseWait)
	}

	// wait_duration_seconds should also be > 0 after contention.
	waitDur := extractGaugeValue(t, body, "arena_db_pool_wait_duration_seconds")
	if waitDur <= 0 {
		t.Errorf("arena_db_pool_wait_duration_seconds = %v, want > 0 after contention", waitDur)
	}
	t.Logf("post-burst wait_duration_seconds = %v", waitDur)
}

// =============================================================================
// Additional correctness tests
// =============================================================================

// TestDBPoolGauges_IdleMatchesFakePoolStat verifies that arena_db_pool_idle
// reflects the fakePoolStat idle count exactly.
func TestDBPoolGauges_IdleMatchesFakePoolStat(t *testing.T) {
	const maxConns = 8

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)
	stat.acquire() // idle = 7, in_use = 1

	body := scrapeMetrics(t, stat, m)

	idleVal := extractGaugeValue(t, body, "arena_db_pool_idle")
	if idleVal != float64(maxConns-1) {
		t.Errorf("arena_db_pool_idle = %v, want %v", idleVal, float64(maxConns-1))
	}

	inUseVal := extractGaugeValue(t, body, "arena_db_pool_in_use")
	if inUseVal != 1 {
		t.Errorf("arena_db_pool_in_use = %v, want 1", inUseVal)
	}
}

// TestDBPoolGauges_WaitDurationInSeconds verifies that AcquireDuration (in
// nanoseconds internally) is correctly converted to seconds for the metric.
func TestDBPoolGauges_WaitDurationInSeconds(t *testing.T) {
	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(5)
	// Set acquire duration to exactly 500ms.
	stat.acquireDuration.Store(int64(500 * time.Millisecond))

	body := scrapeMetrics(t, stat, m)

	durVal := extractGaugeValue(t, body, "arena_db_pool_wait_duration_seconds")
	const wantSeconds = 0.5
	if durVal < 0.499 || durVal > 0.501 {
		t.Errorf("arena_db_pool_wait_duration_seconds = %v, want ~%v (500ms)", durVal, wantSeconds)
	}
	t.Logf("arena_db_pool_wait_duration_seconds = %v (correct)", durVal)
}

// =============================================================================
// Full verification (all steps as sub-tests)
// =============================================================================

// TestDBPoolGauges_FullVerification runs all 5 feature steps as named
// sub-tests to provide a clear verification report.
func TestDBPoolGauges_FullVerification(t *testing.T) {
	t.Run("Step1_AllGaugePrefixesPresent", func(t *testing.T) {
		m, _ := observability.New(nil)
		stat := newFakePoolStat(10)
		body := scrapeMetrics(t, stat, m)
		for _, name := range expectedIndividualGaugeNames {
			if !strings.Contains(body, name) {
				t.Errorf("missing gauge %q in /metrics output", name)
			}
		}
	})

	t.Run("Step2_TypeLinesPresent", func(t *testing.T) {
		m, _ := observability.New(nil)
		stat := newFakePoolStat(5)
		body := scrapeMetrics(t, stat, m)
		for _, name := range expectedIndividualGaugeNames {
			typeLine := "# TYPE " + name + " gauge"
			if !strings.Contains(body, typeLine) {
				t.Errorf("missing TYPE line for %q", name)
			}
		}
	})

	t.Run("Step3_InUseSpikesAndDrains", func(t *testing.T) {
		m, _ := observability.New(nil)
		stat := newFakePoolStat(10)
		stat.acquire()
		stat.acquire()
		body := scrapeMetrics(t, stat, m)
		if v := extractGaugeValue(t, body, "arena_db_pool_in_use"); v != 2 {
			t.Errorf("mid-burst in_use = %v, want 2", v)
		}
		stat.release()
		stat.release()
		body = scrapeMetrics(t, stat, m)
		if v := extractGaugeValue(t, body, "arena_db_pool_in_use"); v != 0 {
			t.Errorf("post-burst in_use = %v, want 0", v)
		}
	})

	t.Run("Step4_OpenConnectionsBetweenMinAndMax", func(t *testing.T) {
		const (
			minConns int32 = 1
			maxConns int32 = 20
		)
		m, _ := observability.New(nil)
		stat := newFakePoolStat(maxConns)
		stat.total.Store(minConns)
		stat.idle.Store(minConns)
		body := scrapeMetrics(t, stat, m)
		openVal := extractGaugeValue(t, body, "arena_db_pool_open_connections")
		if openVal < float64(minConns) || openVal > float64(maxConns) {
			t.Errorf("open_connections = %v, want in [%d, %d]", openVal, minConns, maxConns)
		}
	})

	t.Run("Step5_WaitCountIncrementsUnderContention", func(t *testing.T) {
		m, _ := observability.New(nil)
		stat := newFakePoolStat(5)
		// Simulate 10 forced waits.
		stat.emptyAcquire.Store(10)
		stat.acquireDuration.Store(int64(10 * time.Millisecond))
		body := scrapeMetrics(t, stat, m)
		waitCount := extractGaugeValue(t, body, "arena_db_pool_wait_count")
		if waitCount < 10 {
			t.Errorf("wait_count = %v, want >= 10 after contention", waitCount)
		}
		waitDur := extractGaugeValue(t, body, "arena_db_pool_wait_duration_seconds")
		if waitDur <= 0 {
			t.Errorf("wait_duration_seconds = %v, want > 0", waitDur)
		}
	})
}

// =============================================================================
// Parse helpers
// =============================================================================

// extractGaugeValue parses a Prometheus text scrape body and returns the
// float64 value for the given metric name (without labels). Returns -1 if not
// found or not parseable.
func extractGaugeValue(t *testing.T, body, metricName string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Match lines like "arena_db_pool_in_use 3" (no labels)
		// or "arena_db_pool_in_use{} 3" — use prefix + space/brace.
		if strings.HasPrefix(line, metricName+" ") || strings.HasPrefix(line, metricName+"{") {
			// The value is the last space-separated token.
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				var v float64
				_, err := fmt.Sscanf(parts[len(parts)-1], "%g", &v)
				if err == nil {
					return v
				}
			}
		}
	}
	return -1
}

// truncate returns at most n characters of s (to keep test failure messages
// manageable).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
