// lag_probe_test.go — unit tests for OutboxLagProbe (feature #112).
//
// All tests are hermetic: the outbox table is never accessed. A fakeLagCounter
// test double controls the simulated backlog count and error state.
package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// fakeLagCounter — controllable LagCounter test double
// =============================================================================

type fakeLagCounter struct {
	count   int64
	err     error
	captCtx context.Context // captured by CountUndispatched for deadline checks
}

func (f *fakeLagCounter) CountUndispatched(ctx context.Context) (int64, error) {
	f.captCtx = ctx
	return f.count, f.err
}

// Compile-time guard: fakeLagCounter must satisfy LagCounter.
var _ LagCounter = (*fakeLagCounter)(nil)

// =============================================================================
// Constructor tests
// =============================================================================

// TestOutboxLagProbe112_DefaultName verifies that an empty probeName defaults
// to "outbox".
func TestOutboxLagProbe112_DefaultName(t *testing.T) {
	t.Parallel()
	probe := NewOutboxLagProbe(&fakeLagCounter{}, 0, "")
	if got := probe.ProbeName(); got != "outbox" {
		t.Errorf("ProbeName() = %q, want %q", got, "outbox")
	}
}

// TestOutboxLagProbe112_CustomName verifies that a non-empty probeName is
// preserved verbatim.
func TestOutboxLagProbe112_CustomName(t *testing.T) {
	t.Parallel()
	probe := NewOutboxLagProbe(&fakeLagCounter{}, 50, "domain-events")
	if got := probe.ProbeName(); got != "domain-events" {
		t.Errorf("ProbeName() = %q, want %q", got, "domain-events")
	}
}

// TestOutboxLagProbe112_DefaultThreshold verifies that a threshold ≤ 0 is
// replaced by DefaultOutboxLagThreshold (100).
func TestOutboxLagProbe112_DefaultThreshold(t *testing.T) {
	t.Parallel()
	for _, threshold := range []int64{0, -1, -100} {
		probe := NewOutboxLagProbe(&fakeLagCounter{}, threshold, "")
		if probe.threshold != DefaultOutboxLagThreshold {
			t.Errorf("threshold(%d): got %d, want %d",
				threshold, probe.threshold, DefaultOutboxLagThreshold)
		}
	}
}

// TestOutboxLagProbe112_CustomThreshold verifies a positive threshold is
// preserved.
func TestOutboxLagProbe112_CustomThreshold(t *testing.T) {
	t.Parallel()
	probe := NewOutboxLagProbe(&fakeLagCounter{}, 250, "")
	if probe.threshold != 250 {
		t.Errorf("threshold = %d, want 250", probe.threshold)
	}
}

// =============================================================================
// Ping behaviour tests
// =============================================================================

// TestOutboxLagProbe112_BelowThresholdReturnsNil verifies that a backlog
// strictly below the threshold is considered healthy (nil error).
func TestOutboxLagProbe112_BelowThresholdReturnsNil(t *testing.T) {
	t.Parallel()
	counter := &fakeLagCounter{count: 50}
	probe := NewOutboxLagProbe(counter, 100, "outbox")

	if err := probe.Ping(context.Background()); err != nil {
		t.Errorf("expected nil for backlog=50 < threshold=100, got %v", err)
	}
}

// TestOutboxLagProbe112_ZeroBacklogReturnsNil verifies that an empty outbox
// (backlog=0) always passes.
func TestOutboxLagProbe112_ZeroBacklogReturnsNil(t *testing.T) {
	t.Parallel()
	counter := &fakeLagCounter{count: 0}
	probe := NewOutboxLagProbe(counter, 100, "outbox")

	if err := probe.Ping(context.Background()); err != nil {
		t.Errorf("expected nil for backlog=0, got %v", err)
	}
}

// TestOutboxLagProbe112_AtThresholdFails verifies that reaching the exact
// threshold is treated as unhealthy (probe fails).
func TestOutboxLagProbe112_AtThresholdFails(t *testing.T) {
	t.Parallel()
	counter := &fakeLagCounter{count: 100}
	probe := NewOutboxLagProbe(counter, 100, "outbox")

	err := probe.Ping(context.Background())
	if err == nil {
		t.Error("expected error for backlog == threshold (100 >= 100), got nil")
	}
}

// TestOutboxLagProbe112_AboveThresholdFails verifies that exceeding the
// threshold returns an error that mentions both the actual count and the
// threshold in the message.
func TestOutboxLagProbe112_AboveThresholdFails(t *testing.T) {
	t.Parallel()
	counter := &fakeLagCounter{count: 150}
	probe := NewOutboxLagProbe(counter, 100, "outbox")

	err := probe.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error for backlog=150 > threshold=100, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "150") {
		t.Errorf("error message should contain backlog count 150; got %q", msg)
	}
	if !strings.Contains(msg, "100") {
		t.Errorf("error message should contain threshold 100; got %q", msg)
	}
}

// TestOutboxLagProbe112_QueryErrorPropagated verifies that a counter error is
// wrapped and returned, preserving the error chain for errors.Is checks.
func TestOutboxLagProbe112_QueryErrorPropagated(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("pq: connection reset by peer")
	counter := &fakeLagCounter{err: sentinel}
	probe := NewOutboxLagProbe(counter, 100, "outbox")

	err := probe.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error for counter failure, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain must contain sentinel; got %v", err)
	}
}

// TestOutboxLagProbe112_TimeoutApplied verifies that Ping applies a ≤3s
// deadline to the context passed to CountUndispatched, independently of any
// deadline on the parent context.
func TestOutboxLagProbe112_TimeoutApplied(t *testing.T) {
	t.Parallel()
	counter := &fakeLagCounter{count: 0}
	probe := NewOutboxLagProbe(counter, 100, "outbox")

	_ = probe.Ping(context.Background())

	deadline, hasDeadline := counter.captCtx.Deadline()
	if !hasDeadline {
		t.Fatal("Ping did not apply a deadline to the context")
	}
	// Allow 150 ms scheduling slack around the 3-second budget.
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 3*time.Second+150*time.Millisecond {
		t.Errorf("deadline remaining = %v; want in (0, 3.15s]", remaining)
	}
}

// TestOutboxLagProbe112_FullVerification is a table-driven summary test
// covering all backlog/threshold combinations required by the feature spec.
func TestOutboxLagProbe112_FullVerification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		backlog   int64
		threshold int64
		wantOK    bool
	}{
		{"zero_backlog", 0, 100, true},
		{"half_threshold", 50, 100, true},
		{"just_below", 99, 100, true},
		{"at_threshold", 100, 100, false},
		{"above_threshold", 150, 100, false},
		{"way_above", 1000, 100, false},
		{"custom_threshold_ok", 9, 10, true},
		{"custom_threshold_fail", 10, 10, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counter := &fakeLagCounter{count: tc.backlog}
			probe := NewOutboxLagProbe(counter, tc.threshold, "outbox")
			err := probe.Ping(context.Background())
			if tc.wantOK && err != nil {
				t.Errorf("backlog=%d threshold=%d: expected nil, got %v",
					tc.backlog, tc.threshold, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("backlog=%d threshold=%d: expected error, got nil",
					tc.backlog, tc.threshold)
			}
		})
	}
}
