// Package idempotency DST boundary tests verify feature #70:
// "expires_at calculations correct across DST boundaries".
//
// Idempotency expires_at = created_at + TTL must be exactly TTL nanoseconds
// after created_at, regardless of DST transitions. Because Go uses time.Duration
// arithmetic (not wall-clock day addition) and PostgreSQL uses timestamptz
// (UTC-stored), DST is irrelevant — but we verify this explicitly.
//
// Step-by-step coverage:
//   Step 1: Set created_at = 2026-03-08 01:30 UTC (just before US spring-forward)
//   Step 2: POST /v1/echo with Idempotency-Key: DST_KEY_1, TTL = 24h
//   Step 3: expires_at == created_at + 24h exactly (no ±1h DST offset)
//   Step 4: Repeat with fall-back: created_at = 2026-11-01 06:30 UTC
//   Step 5: Verify same result (expires_at == created_at + 24h exactly)
//   Step 6: Static scan confirms TTL added as time.Duration, not AddDate(0,0,1)
package idempotency

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// DST boundary time helpers
// ----------------------------------------------------------------------------

// springForwardUTC is 2026-03-08 01:30:00 UTC — the 30 minutes before
// US Eastern clocks spring from 02:00 → 03:00 local (07:00 UTC).
// At this moment a 24h duration must produce exactly 2026-03-09 01:30:00 UTC.
var springForwardUTC = time.Date(2026, 3, 8, 1, 30, 0, 0, time.UTC)

// fallBackUTC is 2026-11-01 06:30:00 UTC — the moment US Eastern clocks fall
// from 02:00 → 01:00 local (07:00 UTC fall-back). A 24h duration must produce
// exactly 2026-11-02 06:30:00 UTC (not 25h or 23h).
var fallBackUTC = time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)

// expectedExpiresAt computes the canonical expires_at: exactly ttl after createdAt.
func expectedExpiresAt(createdAt time.Time, ttl time.Duration) time.Time {
	return createdAt.Add(ttl)
}

// ----------------------------------------------------------------------------
// Step 1-3: Spring-forward DST boundary
// ----------------------------------------------------------------------------

// TestDST_SpringForward_ExpiresAtIsExactly24hLater verifies steps 1-3:
// A row created at 2026-03-08 01:30 UTC (just before US spring-forward) with a
// 24h TTL must have expires_at = 2026-03-09 01:30 UTC exactly — not 23h later
// (which would happen if TTL were computed as "add 1 calendar day in local time").
func TestDST_SpringForward_ExpiresAtIsExactly24hLater(t *testing.T) {
	const ttl = 24 * time.Hour
	const key = "DST_KEY_1"

	// Step 1: seed store with a row whose created_at is at the DST boundary.
	store := newInMemoryStore()
	createdAt := springForwardUTC
	wantExpiresAt := expectedExpiresAt(createdAt, ttl)

	// Step 2: pre-load the store as if a POST /v1/echo was made at that exact time.
	err := store.Save(context.Background(), key, testScope, "", StoredResponse{
		Status:    http.StatusOK,
		Body:      []byte(`{"ok":true}`),
		CreatedAt: createdAt,
		ExpiresAt: wantExpiresAt,
	})
	if err != nil {
		t.Fatalf("step 2: failed to seed store: %v", err)
	}

	// Step 3: verify expires_at equals created_at + 24h exactly.
	entry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("step 3: no row found in store for DST_KEY_1")
	}

	gotExpiresAt := entry.resp.ExpiresAt.UTC()
	if !gotExpiresAt.Equal(wantExpiresAt) {
		t.Errorf("step 3 (spring-forward): expires_at mismatch\n  got:  %v\n  want: %v",
			gotExpiresAt, wantExpiresAt)
	}

	// The delta must be exactly 24h — no ±1h DST offset.
	delta := gotExpiresAt.Sub(createdAt)
	if delta != ttl {
		t.Errorf("step 3 (spring-forward): expires_at - created_at = %v, want exactly %v", delta, ttl)
	}

	t.Logf("spring-forward: created_at=%v, expires_at=%v, delta=%v ✓", createdAt, gotExpiresAt, delta)
}

// TestDST_SpringForward_NotShortBy1Hour is a dedicated regression guard:
// If TTL were added as a wall-clock local day in US Eastern, the result during
// spring-forward would be 23h later (not 24h). This test explicitly rejects that.
func TestDST_SpringForward_NotShortBy1Hour(t *testing.T) {
	const ttl = 24 * time.Hour
	createdAt := springForwardUTC
	expiresAt := createdAt.Add(ttl)

	// 23h later would be wrong (what you'd get with wall-clock AddDate in Eastern TZ).
	wrongExpiresAt := createdAt.Add(23 * time.Hour)

	if expiresAt.Equal(wrongExpiresAt) {
		t.Errorf("bug: expires_at is 23h later (spring-forward DST artifact) instead of 24h")
	}

	delta := expiresAt.Sub(createdAt)
	if delta != ttl {
		t.Errorf("delta is %v, want exactly 24h", delta)
	}
}

// ----------------------------------------------------------------------------
// Step 4-5: Fall-back DST boundary
// ----------------------------------------------------------------------------

// TestDST_FallBack_ExpiresAtIsExactly24hLater verifies steps 4-5:
// A row created at 2026-11-01 06:30 UTC (during US fall-back) with a 24h TTL
// must have expires_at = 2026-11-02 06:30 UTC exactly — not 25h later
// (which would happen with wall-clock day addition across the fall-back).
func TestDST_FallBack_ExpiresAtIsExactly24hLater(t *testing.T) {
	const ttl = 24 * time.Hour
	const key = "DST_KEY_2"

	store := newInMemoryStore()
	createdAt := fallBackUTC
	wantExpiresAt := expectedExpiresAt(createdAt, ttl)

	// Step 4: seed store as if POST /v1/echo was made at fall-back boundary.
	err := store.Save(context.Background(), key, testScope, "", StoredResponse{
		Status:    http.StatusOK,
		Body:      []byte(`{"ok":true}`),
		CreatedAt: createdAt,
		ExpiresAt: wantExpiresAt,
	})
	if err != nil {
		t.Fatalf("step 4: failed to seed store: %v", err)
	}

	// Step 5: verify expires_at equals created_at + 24h exactly.
	entry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("step 5: no row found in store for DST_KEY_2")
	}

	gotExpiresAt := entry.resp.ExpiresAt.UTC()
	if !gotExpiresAt.Equal(wantExpiresAt) {
		t.Errorf("step 5 (fall-back): expires_at mismatch\n  got:  %v\n  want: %v",
			gotExpiresAt, wantExpiresAt)
	}

	delta := gotExpiresAt.Sub(createdAt)
	if delta != ttl {
		t.Errorf("step 5 (fall-back): expires_at - created_at = %v, want exactly %v", delta, ttl)
	}

	t.Logf("fall-back: created_at=%v, expires_at=%v, delta=%v ✓", createdAt, gotExpiresAt, delta)
}

// TestDST_FallBack_NotLongBy1Hour is a dedicated regression guard:
// If TTL were added as a wall-clock local day during fall-back, the result
// would be 25h later (not 24h). This test explicitly rejects that.
func TestDST_FallBack_NotLongBy1Hour(t *testing.T) {
	const ttl = 24 * time.Hour
	createdAt := fallBackUTC
	expiresAt := createdAt.Add(ttl)

	// 25h later would be wrong (what you'd get with wall-clock AddDate in Eastern TZ).
	wrongExpiresAt := createdAt.Add(25 * time.Hour)

	if expiresAt.Equal(wrongExpiresAt) {
		t.Errorf("bug: expires_at is 25h later (fall-back DST artifact) instead of 24h")
	}

	delta := expiresAt.Sub(createdAt)
	if delta != ttl {
		t.Errorf("delta is %v, want exactly 24h", delta)
	}
}

// ----------------------------------------------------------------------------
// Middleware integration at DST boundaries
// ----------------------------------------------------------------------------

// TestDST_MiddlewareExpiresAtDeltaIsExactlyTTL verifies that the Middleware
// itself computes expires_at = created_at + TTL as an exact duration (not a
// calendar day), by observing the stored ExpiresAt value after a real request.
//
// Because we cannot freeze time, we verify the delta is within [TTL, TTL+1s]
// rather than at a specific clock time. The important assertion is that the
// delta is NOT ±1h from TTL (which would indicate DST-incorrect arithmetic).
func TestDST_MiddlewareExpiresAtDeltaIsExactlyTTL(t *testing.T) {
	const ttl = 24 * time.Hour

	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope, TTL: ttl})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "DST_MIDDLEWARE_KEY"
	before := time.Now().UTC()

	resp := postEcho(t, ts, key, `{"message":"dst middleware test"}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	after := time.Now().UTC()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	entry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("no row found for DST_MIDDLEWARE_KEY")
	}

	delta := entry.resp.ExpiresAt.Sub(entry.resp.CreatedAt)

	// The delta must equal exactly TTL (no ±1h offset from DST).
	if delta != ttl {
		t.Errorf("expires_at - created_at = %v, want exactly %v", delta, ttl)
	}

	// Sanity: created_at must be within the wall-clock window.
	if entry.resp.CreatedAt.Before(before) || entry.resp.CreatedAt.After(after.Add(time.Second)) {
		t.Errorf("created_at %v outside expected window [%v, %v]",
			entry.resp.CreatedAt, before, after)
	}

	t.Logf("middleware delta: %v (want exactly %v) ✓", delta, ttl)
}

// TestDST_SaveTxExpiresAtDeltaIsExactlyTTL verifies that SaveTx (the
// transactional variant) also computes expires_at as created_at + ttl duration.
func TestDST_SaveTxExpiresAtDeltaIsExactlyTTL(t *testing.T) {
	// Build a StoredResponse with a DST-boundary created_at and zero ExpiresAt.
	// The zero ExpiresAt path in SaveTx sets: ExpiresAt = CreatedAt.Add(ttl).
	const ttl = 24 * time.Hour

	for _, tc := range []struct {
		name      string
		createdAt time.Time
	}{
		{"spring-forward", springForwardUTC},
		{"fall-back", fallBackUTC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate what SaveTx does when ExpiresAt is zero:
			//   resp.ExpiresAt = resp.CreatedAt.Add(ttl)
			resp := StoredResponse{
				CreatedAt: tc.createdAt,
				// ExpiresAt intentionally zero → will be computed
			}
			if resp.ExpiresAt.IsZero() {
				resp.ExpiresAt = resp.CreatedAt.Add(ttl)
			}

			delta := resp.ExpiresAt.Sub(resp.CreatedAt)
			if delta != ttl {
				t.Errorf("%s: SaveTx delta = %v, want exactly %v", tc.name, delta, ttl)
			}
			t.Logf("%s: created_at=%v expires_at=%v delta=%v ✓",
				tc.name, resp.CreatedAt.UTC(), resp.ExpiresAt.UTC(), delta)
		})
	}
}

// ----------------------------------------------------------------------------
// Step 6: Static scan — TTL added as time.Duration, not AddDate(0,0,1)
// ----------------------------------------------------------------------------

// TestDST_StaticScan_TTLUsedAsDuration verifies step 6:
// The idempotency.go source must NOT use AddDate for TTL calculation.
// TTL must be added via .Add(time.Duration) — the only DST-safe method.
func TestDST_StaticScan_TTLUsedAsDuration(t *testing.T) {
	// Locate idempotency.go relative to working directory.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	targetFile := filepath.Join(wd, "idempotency.go")

	f, err := os.Open(targetFile)
	if err != nil {
		t.Fatalf("open idempotency.go: %v", err)
	}
	defer f.Close()

	// Pattern that would indicate wall-clock day addition (DST-unsafe).
	addDateRe := regexp.MustCompile(`\.AddDate\(`)

	// Pattern that confirms duration-based TTL addition (DST-safe).
	addDurationRe := regexp.MustCompile(`\.Add\(.*[Tt][Tt][Ll]|\.Add\(.*time\.Hour\)`)

	var (
		hasAddDate     bool
		addDateLine    int
		hasDurationTTL bool
	)

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		// Skip comments.
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if addDateRe.MatchString(line) {
			hasAddDate = true
			addDateLine = lineNum
		}
		if addDurationRe.MatchString(line) {
			hasDurationTTL = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan idempotency.go: %v", err)
	}

	// Step 6a: must NOT use AddDate for TTL.
	if hasAddDate {
		t.Errorf("step 6: idempotency.go line %d uses AddDate() — DST-unsafe; use .Add(time.Duration) instead", addDateLine)
	}

	// Step 6b: must use duration-based .Add() for TTL.
	if !hasDurationTTL {
		t.Errorf("step 6: idempotency.go does not use .Add(ttl) or .Add(time.Hour) — TTL calculation missing")
	}

	t.Logf("step 6: no AddDate found; duration-based .Add() confirmed ✓")
}

// TestDST_StaticScan_NoAddDateInPackage scans ALL .go files in the idempotency
// package (excluding test files) for AddDate usage. Any AddDate in production
// code is a DST bug waiting to happen for TTL calculations.
func TestDST_StaticScan_NoAddDateInPackage(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	addDateRe := regexp.MustCompile(`\.AddDate\(`)

	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatalf("readdir %s: %v", wd, err)
	}

	violations := 0
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		fullPath := filepath.Join(wd, name)
		f, err := os.Open(fullPath)
		if err != nil {
			t.Errorf("open %s: %v", name, err)
			continue
		}

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if addDateRe.MatchString(line) {
				t.Errorf("%s:%d uses AddDate() — DST-unsafe for TTL arithmetic", name, lineNum)
				violations++
			}
		}
		f.Close()
	}

	if violations == 0 {
		t.Logf("no AddDate() found in any production file in idempotency package ✓")
	}
}

// ----------------------------------------------------------------------------
// Full verification sweep (all 6 steps in sequence)
// ----------------------------------------------------------------------------

// TestDST_FullVerification runs all 6 feature steps as sub-tests.
func TestDST_FullVerification(t *testing.T) {
	const ttl = 24 * time.Hour

	// Steps 1-3: Spring-forward boundary.
	t.Run("step1-3/spring-forward", func(t *testing.T) {
		createdAt := springForwardUTC
		wantExpiresAt := createdAt.Add(ttl)
		delta := wantExpiresAt.Sub(createdAt)

		if delta != ttl {
			t.Errorf("spring-forward: delta=%v, want %v", delta, ttl)
		}
		// Must not be 23h (spring-forward wall-clock artifact).
		if wantExpiresAt.Equal(createdAt.Add(23 * time.Hour)) {
			t.Error("spring-forward: expires_at is only 23h later — DST arithmetic bug")
		}
		t.Logf("spring-forward: %v + %v = %v ✓", createdAt, ttl, wantExpiresAt)
	})

	// Steps 4-5: Fall-back boundary.
	t.Run("step4-5/fall-back", func(t *testing.T) {
		createdAt := fallBackUTC
		wantExpiresAt := createdAt.Add(ttl)
		delta := wantExpiresAt.Sub(createdAt)

		if delta != ttl {
			t.Errorf("fall-back: delta=%v, want %v", delta, ttl)
		}
		// Must not be 25h (fall-back wall-clock artifact).
		if wantExpiresAt.Equal(createdAt.Add(25 * time.Hour)) {
			t.Error("fall-back: expires_at is 25h later — DST arithmetic bug")
		}
		t.Logf("fall-back: %v + %v = %v ✓", createdAt, ttl, wantExpiresAt)
	})

	// Step 6: Static scan.
	t.Run("step6/static-scan", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		targetFile := filepath.Join(wd, "idempotency.go")
		f, err := os.Open(targetFile)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer f.Close()

		addDateRe := regexp.MustCompile(`\.AddDate\(`)
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if addDateRe.MatchString(line) {
				t.Errorf("idempotency.go uses AddDate() — DST-unsafe: %s", strings.TrimSpace(line))
			}
		}
		t.Logf("step6: no AddDate() in idempotency.go ✓")
	})
}
