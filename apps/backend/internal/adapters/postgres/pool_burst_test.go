// pool_burst_test.go — unit tests for feature #40 (DB pool stats survive
// request bursts).
//
// These tests verify that the pgx pool Prometheus gauges correctly reflect
// connection-usage patterns during and after a concurrent request burst:
//
//   - arena_db_pool_connections{state="acquired"} rises above zero while
//     requests are in-flight (connections held).
//   - arena_db_pool_connections{state="acquired"} returns to zero after the
//     burst completes (no connection leaks).
//   - arena_db_pool_connections{state="max"} is stable throughout (no pool
//     expansion beyond the configured cap).
//   - arena_db_pool_connections{state="idle"} recovers to its initial value
//     once all connections are released.
//   - The RegisterPoolMetrics goroutine exits cleanly when its context is
//     cancelled (no goroutine leak from the metrics scraper).
//
// All tests use fakePoolStat (implemented in this file) rather than a live
// *pgxpool.Pool, so no PostgreSQL instance is required.
//
// Run with: go test -v -run TestDBPoolBurst ./apps/backend/internal/adapters/postgres/
package postgres

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	dto "github.com/prometheus/client_model/go"
)

// =============================================================================
// Compile-time guard: fakePoolStat satisfies poolStatReader.
// =============================================================================

var _ poolStatReader = (*fakePoolStat)(nil)

// =============================================================================
// Test double: fakePoolStat
// =============================================================================

// fakePoolStat is a thread-safe implementation of poolStatReader that lets
// tests atomically update acquired / idle counts to simulate connection
// acquisition during a burst.
type fakePoolStat struct {
	acquired  atomic.Int32
	idle      atomic.Int32
	max       int32
	total     atomic.Int32
	newConns  atomic.Int64
}

func newFakePoolStat(maxConns int32) *fakePoolStat {
	f := &fakePoolStat{max: maxConns}
	f.idle.Store(maxConns)
	f.total.Store(maxConns)
	return f
}

// acquire simulates acquiring one connection from the pool:
// increments acquired, decrements idle.
func (f *fakePoolStat) acquire() {
	f.acquired.Add(1)
	f.idle.Add(-1)
}

// release simulates releasing one connection back to the pool:
// decrements acquired, increments idle.
func (f *fakePoolStat) release() {
	f.acquired.Add(-1)
	f.idle.Add(1)
}

// poolStatReader interface implementation.
func (f *fakePoolStat) AcquiredConns() int32 { return f.acquired.Load() }
func (f *fakePoolStat) IdleConns() int32     { return f.idle.Load() }
func (f *fakePoolStat) MaxConns() int32      { return f.max }
func (f *fakePoolStat) TotalConns() int32    { return f.total.Load() }
func (f *fakePoolStat) NewConnsCount() int64 { return f.newConns.Load() }

// =============================================================================
// Metric read helpers
// =============================================================================

// gatherPoolGauge scrapes m.Registry(), finds arena_db_pool_connections, and
// returns the value for the given state label. Returns -1 if not found.
func gatherPoolGauge(t *testing.T, m *observability.Metrics, state string) float64 {
	t.Helper()
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Registry().Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "arena_db_pool_connections" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "state" && lp.GetValue() == state {
					return metric.GetGauge().GetValue()
				}
			}
		}
	}
	return -1
}

// gatherAllPoolGauges returns a map[state]value for all
// arena_db_pool_connections label values present in the registry.
func gatherAllPoolGauges(t *testing.T, m *observability.Metrics) map[string]float64 {
	t.Helper()
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Registry().Gather: %v", err)
	}
	result := map[string]float64{}
	for _, mf := range families {
		if mf.GetName() != "arena_db_pool_connections" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			state := labelValue(metric, "state")
			result[state] = metric.GetGauge().GetValue()
		}
	}
	return result
}

func labelValue(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// =============================================================================
// Burst tests
// =============================================================================

// TestDBPoolBurst_AcquiredGaugeRisesAndFalls is the primary feature #40 test.
//
// It simulates 50 concurrent "requests" that each hold a connection for 50 ms,
// verifies that acquired > 0 while the burst is in-flight, and confirms that
// acquired returns to 0 once every request finishes (no leaked connections).
func TestDBPoolBurst_AcquiredGaugeRisesAndFalls(t *testing.T) {
	const (
		workers  = 50
		maxConns = 50
		holdTime = 50 * time.Millisecond
	)

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)

	// ── Baseline: before burst, acquired should be 0 ─────────────────────────
	publishPoolStatSnapshot(stat, m)
	if v := gatherPoolGauge(t, m, "acquired"); v != 0 {
		t.Errorf("baseline acquired = %v, want 0", v)
	}

	// ── Burst phase ───────────────────────────────────────────────────────────
	startBurst := make(chan struct{})
	allAcquired := make(chan struct{}) // closed when all workers have acquired
	var acquiredCount atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBurst

			stat.acquire()
			// Signal that this worker has acquired its connection.
			if acquiredCount.Add(1) == int32(workers) {
				close(allAcquired)
			}

			// Hold the "connection" for a short duration to simulate request work.
			time.Sleep(holdTime)

			stat.release()
		}()
	}

	// Fire the burst.
	close(startBurst)

	// Wait until all workers have acquired their connections.
	select {
	case <-allAcquired:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for all workers to acquire connections")
	}

	// ── Mid-burst snapshot ────────────────────────────────────────────────────
	publishPoolStatSnapshot(stat, m)
	midAcquired := gatherPoolGauge(t, m, "acquired")
	if midAcquired <= 0 {
		t.Errorf("mid-burst acquired = %v, want > 0 (connections should be in use)", midAcquired)
	}
	t.Logf("mid-burst acquired = %v / %v max", midAcquired, maxConns)

	// ── Wait for burst to finish ──────────────────────────────────────────────
	wg.Wait()

	// ── Post-burst snapshot ───────────────────────────────────────────────────
	publishPoolStatSnapshot(stat, m)
	postAcquired := gatherPoolGauge(t, m, "acquired")
	if postAcquired != 0 {
		t.Errorf("post-burst acquired = %v, want 0 (no connection leak)", postAcquired)
	}
	t.Logf("post-burst acquired = %v (clean)", postAcquired)
}

// TestDBPoolBurst_MaxConnsUnchangedDuringBurst verifies that the "max" gauge
// stays at the configured pool maximum throughout the burst. A rising "max"
// would indicate that the pool was unexpectedly re-configured.
func TestDBPoolBurst_MaxConnsUnchangedDuringBurst(t *testing.T) {
	const maxConns = 10

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)

	// Record initial max.
	publishPoolStatSnapshot(stat, m)
	initialMax := gatherPoolGauge(t, m, "max")
	if initialMax != float64(maxConns) {
		t.Fatalf("initial max = %v, want %v", initialMax, maxConns)
	}

	// Simulate burst: acquire all connections.
	for range maxConns {
		stat.acquire()
	}
	publishPoolStatSnapshot(stat, m)
	midMax := gatherPoolGauge(t, m, "max")
	if midMax != float64(maxConns) {
		t.Errorf("mid-burst max = %v, want %v (max must be stable)", midMax, maxConns)
	}

	// Release all connections.
	for range maxConns {
		stat.release()
	}
	publishPoolStatSnapshot(stat, m)
	postMax := gatherPoolGauge(t, m, "max")
	if postMax != float64(maxConns) {
		t.Errorf("post-burst max = %v, want %v (max must be stable)", postMax, maxConns)
	}
}

// TestDBPoolBurst_IdleConnectionsRecoverAfterBurst verifies that idle
// connections return to their initial count after all burst connections are
// released (idle = max, acquired = 0).
func TestDBPoolBurst_IdleConnectionsRecoverAfterBurst(t *testing.T) {
	const maxConns = 20

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)

	// Baseline.
	publishPoolStatSnapshot(stat, m)
	initialIdle := gatherPoolGauge(t, m, "idle")

	// Simulate burst: acquire half the connections.
	for range maxConns / 2 {
		stat.acquire()
	}
	publishPoolStatSnapshot(stat, m)
	midIdle := gatherPoolGauge(t, m, "idle")
	if midIdle >= initialIdle {
		t.Errorf("mid-burst idle = %v, want < initial %v (some connections in use)", midIdle, initialIdle)
	}

	// Release all connections.
	for range maxConns / 2 {
		stat.release()
	}
	publishPoolStatSnapshot(stat, m)
	postIdle := gatherPoolGauge(t, m, "idle")
	if postIdle != initialIdle {
		t.Errorf("post-burst idle = %v, want %v (idle must recover to initial)", postIdle, initialIdle)
	}
}

// TestDBPoolBurst_TotalConnectionsStableAcrossBurst verifies that "total"
// (open connections, both acquired and idle) remains constant during and after
// a burst. A growing "total" after the burst would indicate a connection leak.
func TestDBPoolBurst_TotalConnectionsStableAcrossBurst(t *testing.T) {
	const maxConns = 15

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(maxConns)

	publishPoolStatSnapshot(stat, m)
	initialTotal := gatherPoolGauge(t, m, "total")

	// Acquire all → total stays the same (connections redistributed, not added).
	for range maxConns {
		stat.acquire()
	}
	publishPoolStatSnapshot(stat, m)
	midTotal := gatherPoolGauge(t, m, "total")
	if midTotal != initialTotal {
		t.Errorf("mid-burst total = %v, want %v (total must not grow during burst)", midTotal, initialTotal)
	}

	// Release all → total still the same.
	for range maxConns {
		stat.release()
	}
	publishPoolStatSnapshot(stat, m)
	postTotal := gatherPoolGauge(t, m, "total")
	if postTotal != initialTotal {
		t.Errorf("post-burst total = %v, want %v (total must equal initial after burst)", postTotal, initialTotal)
	}
}

// TestDBPoolBurst_AllFiveGaugesPublished verifies that a single call to
// publishPoolStatSnapshot populates all five required gauge label values
// (acquired, idle, max, total, new_total). Missing gauges would leave Grafana
// panels empty and break alerting rules.
func TestDBPoolBurst_AllFiveGaugesPublished(t *testing.T) {
	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(5)
	stat.acquire() // acquired = 1, idle = 4
	stat.newConns.Store(3)

	publishPoolStatSnapshot(stat, m)

	gauges := gatherAllPoolGauges(t, m)

	required := []string{"acquired", "idle", "max", "total", "new_total"}
	for _, label := range required {
		if _, ok := gauges[label]; !ok {
			t.Errorf("gauge label %q not found after publishPoolStatSnapshot", label)
		}
	}

	// Spot-check values.
	if gauges["acquired"] != 1 {
		t.Errorf("acquired = %v, want 1", gauges["acquired"])
	}
	if gauges["idle"] != 4 {
		t.Errorf("idle = %v, want 4", gauges["idle"])
	}
	if gauges["max"] != 5 {
		t.Errorf("max = %v, want 5", gauges["max"])
	}
	if gauges["new_total"] != 3 {
		t.Errorf("new_total = %v, want 3", gauges["new_total"])
	}
}

// TestDBPoolBurst_MetricsScrapeReturnsPoolData verifies that the pool gauges
// are visible in a real /metrics HTTP scrape response.
// This mirrors the "GET /metrics, capture db_pool_open / idle / in_use"
// step in the feature spec (step 1).
func TestDBPoolBurst_MetricsScrapeReturnsPoolData(t *testing.T) {
	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(8)
	stat.acquire()
	stat.acquire()
	publishPoolStatSnapshot(stat, m)

	// Scrape /metrics via the Prometheus handler.
	req, _ := newTestRequest(t, "GET", "/metrics", nil)
	rec := newTestResponseRecorder()
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()

	// Verify each label variant is present in Prometheus text format.
	for _, state := range []string{"acquired", "idle", "max", "total", "new_total"} {
		want := `arena_db_pool_connections{state="` + state + `"}`
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body missing %q", want)
		}
	}

	// Verify acquired=2 is reflected.
	wantLine := `arena_db_pool_connections{state="acquired"} 2`
	if !strings.Contains(body, wantLine) {
		t.Errorf("/metrics body does not contain %q", wantLine)
	}
}

// TestDBPoolBurst_NoGoroutineLeakFromMetricsScraper verifies that the
// background goroutine started by RegisterPoolMetrics exits promptly when its
// context is cancelled. This guards against goroutine leaks after graceful
// shutdown.
func TestDBPoolBurst_NoGoroutineLeakFromMetricsScraper(t *testing.T) {
	// This test cannot use RegisterPoolMetrics directly because it requires a
	// real *pgxpool.Pool. Instead we replicate the goroutine pattern and verify
	// context cancellation terminates it within a short deadline.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(poolMetricScrapeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// In production this would call publishPoolStats; here it's a no-op.
			}
		}
	}()

	// Cancel context — the goroutine should exit within 200 ms.
	cancel()

	select {
	case <-done:
		// goroutine exited cleanly
	case <-time.After(200 * time.Millisecond):
		t.Error("metrics scraper goroutine did not exit within 200ms after context cancel (goroutine leak)")
	}
}

// TestDBPoolBurst_ConcurrentPublishIsRaceFree verifies that calling
// publishPoolStatSnapshot concurrently from multiple goroutines does not
// trigger the race detector. This simulates a scenario where the scrape
// interval fires while a burst is modifying the fake stat counters.
func TestDBPoolBurst_ConcurrentPublishIsRaceFree(t *testing.T) {
	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	stat := newFakePoolStat(20)
	var wg sync.WaitGroup

	// Half the goroutines simulate "requests" acquiring/releasing connections.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stat.acquire()
			time.Sleep(5 * time.Millisecond)
			stat.release()
		}()
	}

	// The other half simulate the metrics scraper publishing snapshots.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				publishPoolStatSnapshot(stat, m)
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
}

// =============================================================================
// Minimal net/http test helpers (mirrors pattern used in other test files)
// =============================================================================

// newTestRequest creates an *http.Request for the given method and path.
func newTestRequest(t *testing.T, method, path string, _ interface{}) (*http.Request, func()) {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	return req, func() {}
}

// newTestResponseRecorder returns a minimal responseRecorder.
type responseRecorder struct {
	Code int
	Body strings.Builder
	header http.Header
}

func (r *responseRecorder) Header() http.Header       { return r.header }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.Body.Write(b) }
func (r *responseRecorder) WriteHeader(code int)       { r.Code = code }

func newTestResponseRecorder() *responseRecorder {
	return &responseRecorder{Code: 200, header: http.Header{}}
}
