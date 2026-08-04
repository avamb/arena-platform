// event_state_test.go — table-driven coverage of the Event lifecycle
// state machine (AB-46).
//
// The tests are exhaustive: they enumerate every documented state ×
// every documented state pair and assert the expected true/false result
// of IsValidEventTransition against the exported transition map, so a
// silent change to ValidEventTransitions cannot land without either
// updating this table or breaking the test.
package catalog

import "testing"

func TestValidEventTransitions_ContainsExactlyTheDocumentedEdges(t *testing.T) {
	t.Parallel()
	expected := map[EventStatus]map[EventStatus]bool{
		EventStatusDraft: {
			EventStatusPublished: true,
			EventStatusCancelled: true,
		},
		EventStatusPublished: {
			EventStatusCancelled: true,
			EventStatusArchived:  true,
		},
		EventStatusCancelled: {
			EventStatusArchived: true,
		},
		EventStatusArchived: {},
	}
	if len(ValidEventTransitions) != len(expected) {
		t.Fatalf("ValidEventTransitions has %d keys, want %d", len(ValidEventTransitions), len(expected))
	}
	for from, want := range expected {
		got, ok := ValidEventTransitions[from]
		if !ok {
			t.Errorf("ValidEventTransitions missing %q", from)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("ValidEventTransitions[%q]: got %d entries, want %d", from, len(got), len(want))
		}
		for to := range want {
			if !got[to] {
				t.Errorf("ValidEventTransitions[%q][%q] = false, want true", from, to)
			}
		}
	}
}

func TestIsValidEventTransition(t *testing.T) {
	t.Parallel()
	all := []string{
		string(EventStatusDraft),
		string(EventStatusPublished),
		string(EventStatusCancelled),
		string(EventStatusArchived),
	}
	// Documented allowed edges. Every other from×to pair MUST be false.
	allowed := map[string]map[string]bool{
		"draft":     {"published": true, "cancelled": true},
		"published": {"cancelled": true, "archived": true},
		"cancelled": {"archived": true},
		"archived":  {},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			got := IsValidEventTransition(from, to)
			if got != want {
				t.Errorf("IsValidEventTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
			// Identity transitions must never be allowed (documented).
			if from == to && got {
				t.Errorf("identity transition %q → %q must be rejected", from, to)
			}
		}
	}
}

func TestIsValidEventTransition_UnknownStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		from, to string
	}{
		{"", ""},
		{"draft", ""},
		{"", "published"},
		{"pending", "published"},   // unknown from
		{"draft", "settled"},       // unknown to
		{"WHATEVER", "cancelled"},  // unknown from
		{"draft", "DRAFT"},         // case-sensitive
		{"PUBLISHED", "cancelled"}, // case-sensitive
	}
	for _, c := range cases {
		if IsValidEventTransition(c.from, c.to) {
			t.Errorf("IsValidEventTransition(%q, %q) must be false", c.from, c.to)
		}
	}
}

func TestEventStatus_TerminalStateHasNoExits(t *testing.T) {
	t.Parallel()
	if exits := ValidEventTransitions[EventStatusArchived]; len(exits) != 0 {
		t.Errorf("archived must be terminal, got %d exits", len(exits))
	}
}
