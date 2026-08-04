// reservations_test.go — table-driven coverage of the Reservation lifecycle
// state machine (AB-46).
package inventory

import "testing"

func TestValidReservationTransitions_ContainsExactlyTheDocumentedEdges(t *testing.T) {
	t.Parallel()
	expected := map[ReservationState]map[ReservationState]bool{
		ReservationStateDraft: {
			ReservationStateActive:    true,
			ReservationStateCancelled: true,
		},
		ReservationStateActive: {
			ReservationStateConverted: true,
			ReservationStateExpired:   true,
			ReservationStateCancelled: true,
		},
		ReservationStateConverted: {},
		ReservationStateExpired:   {},
		ReservationStateCancelled: {},
	}
	if len(ValidReservationTransitions) != len(expected) {
		t.Fatalf("keys: got %d want %d", len(ValidReservationTransitions), len(expected))
	}
	for from, want := range expected {
		got := ValidReservationTransitions[from]
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

func TestIsValidReservationTransition(t *testing.T) {
	t.Parallel()
	all := []string{"draft", "active", "converted", "expired", "cancelled"}
	allowed := map[string]map[string]bool{
		"draft":     {"active": true, "cancelled": true},
		"active":    {"converted": true, "expired": true, "cancelled": true},
		"converted": {},
		"expired":   {},
		"cancelled": {},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			got := IsValidReservationTransition(from, to)
			if got != want {
				t.Errorf("IsValidReservationTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
			if from == to && got {
				t.Errorf("identity transition %q → %q must be rejected", from, to)
			}
		}
	}
}

func TestIsValidReservationTransition_UnknownStates(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ from, to string }{
		{"", ""},
		{"DRAFT", "active"}, // case-sensitive
		{"active", "settled"},
		{"pending", "active"},
	} {
		if IsValidReservationTransition(c.from, c.to) {
			t.Errorf("IsValidReservationTransition(%q, %q) must be false", c.from, c.to)
		}
	}
}

func TestIsTerminalReservationState(t *testing.T) {
	t.Parallel()
	terminal := map[string]bool{"converted": true, "expired": true, "cancelled": true}
	nonTerminal := map[string]bool{"draft": true, "active": true}
	for s := range terminal {
		if !IsTerminalReservationState(s) {
			t.Errorf("IsTerminalReservationState(%q) = false, want true", s)
		}
	}
	for s := range nonTerminal {
		if IsTerminalReservationState(s) {
			t.Errorf("IsTerminalReservationState(%q) = true, want false", s)
		}
	}
	// Unknown states are neither terminal nor valid.
	for _, s := range []string{"", "unknown", "ACTIVE"} {
		if IsTerminalReservationState(s) {
			t.Errorf("IsTerminalReservationState(%q) must be false for unknown state", s)
		}
	}
}
