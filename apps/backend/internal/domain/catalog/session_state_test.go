// session_state_test.go — table-driven coverage of the Session lifecycle
// state machine and DetectOverlaps predicate (AB-46).
package catalog

import (
	"testing"
	"time"
)

func TestValidSessionStatuses(t *testing.T) {
	t.Parallel()
	for _, s := range []SessionStatus{
		SessionStatusDraft,
		SessionStatusScheduled,
		SessionStatusCancelled,
		SessionStatusCompleted,
	} {
		if !IsValidSessionStatus(string(s)) {
			t.Errorf("IsValidSessionStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "pending", "DRAFT", "expired"} {
		if IsValidSessionStatus(s) {
			t.Errorf("IsValidSessionStatus(%q) = true, want false", s)
		}
	}
}

func TestValidSessionTransitions_ContainsExactlyTheDocumentedEdges(t *testing.T) {
	t.Parallel()
	expected := map[SessionStatus]map[SessionStatus]bool{
		SessionStatusDraft: {
			SessionStatusScheduled: true,
			SessionStatusCancelled: true,
		},
		SessionStatusScheduled: {
			SessionStatusCancelled: true,
			SessionStatusCompleted: true,
		},
		SessionStatusCancelled: {},
		SessionStatusCompleted: {},
	}
	if len(ValidSessionTransitions) != len(expected) {
		t.Fatalf("keys: got %d want %d", len(ValidSessionTransitions), len(expected))
	}
	for from, want := range expected {
		got := ValidSessionTransitions[from]
		if len(got) != len(want) {
			t.Errorf("[%q]: got %d entries want %d", from, len(got), len(want))
		}
		for to := range want {
			if !got[to] {
				t.Errorf("[%q][%q] = false, want true", from, to)
			}
		}
	}
}

func TestIsValidSessionTransition(t *testing.T) {
	t.Parallel()
	all := []string{"draft", "scheduled", "cancelled", "completed"}
	allowed := map[string]map[string]bool{
		"draft":     {"scheduled": true, "cancelled": true},
		"scheduled": {"cancelled": true, "completed": true},
		"cancelled": {},
		"completed": {},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			got := IsValidSessionTransition(from, to)
			if got != want {
				t.Errorf("IsValidSessionTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
			if from == to && got {
				t.Errorf("identity transition %q → %q must be rejected", from, to)
			}
		}
	}
}

func TestIsValidSessionTransition_UnknownStates(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ from, to string }{
		{"", ""},
		{"draft", "COMPLETED"},
		{"pending", "scheduled"},
		{"cancelled", "scheduled"},
	} {
		if IsValidSessionTransition(c.from, c.to) {
			t.Errorf("IsValidSessionTransition(%q, %q) must be false", c.from, c.to)
		}
	}
}

func TestSessionStatus_TerminalStatesHaveNoExits(t *testing.T) {
	t.Parallel()
	for _, s := range []SessionStatus{SessionStatusCancelled, SessionStatusCompleted} {
		if exits := ValidSessionTransitions[s]; len(exits) != 0 {
			t.Errorf("%q must be terminal, got %d exits", s, len(exits))
		}
	}
}

// interval helper for readable table entries.
func iv(startISO, endISO string) SessionInterval {
	s, _ := time.Parse(time.RFC3339, startISO)
	e, _ := time.Parse(time.RFC3339, endISO)
	return SessionInterval{StartAt: s, EndAt: e}
}

func TestDetectOverlaps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ivs  []SessionInterval
		want bool
	}{
		{"empty list", nil, false},
		{"single interval never overlaps itself", []SessionInterval{iv("2026-01-01T10:00:00Z", "2026-01-01T12:00:00Z")}, false},
		{
			"non-overlapping (gap)",
			[]SessionInterval{
				iv("2026-01-01T10:00:00Z", "2026-01-01T12:00:00Z"),
				iv("2026-01-01T13:00:00Z", "2026-01-01T15:00:00Z"),
			},
			false,
		},
		{
			"adjacent (end == other start) does NOT overlap (half-open)",
			[]SessionInterval{
				iv("2026-01-01T10:00:00Z", "2026-01-01T12:00:00Z"),
				iv("2026-01-01T12:00:00Z", "2026-01-01T14:00:00Z"),
			},
			false,
		},
		{
			"overlap by 1 minute",
			[]SessionInterval{
				iv("2026-01-01T10:00:00Z", "2026-01-01T12:01:00Z"),
				iv("2026-01-01T12:00:00Z", "2026-01-01T13:00:00Z"),
			},
			true,
		},
		{
			"one interval fully contains another",
			[]SessionInterval{
				iv("2026-01-01T10:00:00Z", "2026-01-01T16:00:00Z"),
				iv("2026-01-01T12:00:00Z", "2026-01-01T13:00:00Z"),
			},
			true,
		},
		{
			"identical intervals overlap",
			[]SessionInterval{
				iv("2026-01-01T10:00:00Z", "2026-01-01T12:00:00Z"),
				iv("2026-01-01T10:00:00Z", "2026-01-01T12:00:00Z"),
			},
			true,
		},
		{
			"overlap detected among 3+ intervals in the middle of the list",
			[]SessionInterval{
				iv("2026-01-01T08:00:00Z", "2026-01-01T09:00:00Z"),
				iv("2026-01-01T10:00:00Z", "2026-01-01T12:00:00Z"),
				iv("2026-01-01T11:30:00Z", "2026-01-01T13:00:00Z"),
				iv("2026-01-01T14:00:00Z", "2026-01-01T15:00:00Z"),
			},
			true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DetectOverlaps(tc.ivs); got != tc.want {
				t.Errorf("DetectOverlaps: got %v want %v", got, tc.want)
			}
		})
	}
}
