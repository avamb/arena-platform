// external_allocations_test.go — table-driven coverage of the
// ExternalAllocation lifecycle state machine (AB-46).
package inventory

import "testing"

func TestAllAllocationStatuses_CoversTheValidTransitionsMap(t *testing.T) {
	t.Parallel()
	if len(AllAllocationStatuses) != len(ValidAllocationTransitions) {
		t.Fatalf("AllAllocationStatuses has %d entries, ValidAllocationTransitions has %d — they must match",
			len(AllAllocationStatuses), len(ValidAllocationTransitions))
	}
	for _, s := range AllAllocationStatuses {
		if _, ok := ValidAllocationTransitions[s]; !ok {
			t.Errorf("AllAllocationStatuses contains %q which is missing from ValidAllocationTransitions", s)
		}
	}
}

func TestValidAllocationTransitions_ContainsExactlyTheDocumentedEdges(t *testing.T) {
	t.Parallel()
	expected := map[AllocationStatus]map[AllocationStatus]bool{
		AllocationStatusPending: {
			AllocationStatusActive:     true,
			AllocationStatusReconciled: true,
		},
		AllocationStatusActive: {
			AllocationStatusReconciled: true,
			AllocationStatusDisputed:   true,
		},
		AllocationStatusDisputed: {
			AllocationStatusReconciled: true,
		},
		AllocationStatusReconciled: {},
	}
	if len(ValidAllocationTransitions) != len(expected) {
		t.Fatalf("keys: got %d want %d", len(ValidAllocationTransitions), len(expected))
	}
	for from, want := range expected {
		got := ValidAllocationTransitions[from]
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

func TestIsValidAllocationStatus(t *testing.T) {
	t.Parallel()
	for _, s := range AllAllocationStatuses {
		if !IsValidAllocationStatus(string(s)) {
			t.Errorf("IsValidAllocationStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "unknown", "PENDING", "settled"} {
		if IsValidAllocationStatus(s) {
			t.Errorf("IsValidAllocationStatus(%q) = true, want false", s)
		}
	}
}

func TestIsValidAllocationTransition(t *testing.T) {
	t.Parallel()
	all := []string{"pending", "active", "reconciled", "disputed"}
	allowed := map[string]map[string]bool{
		"pending":    {"active": true, "reconciled": true},
		"active":     {"reconciled": true, "disputed": true},
		"disputed":   {"reconciled": true},
		"reconciled": {},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			got := IsValidAllocationTransition(from, to)
			if got != want {
				t.Errorf("IsValidAllocationTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
			if from == to && got {
				t.Errorf("identity transition %q → %q must be rejected", from, to)
			}
		}
	}
}

func TestIsValidAllocationTransition_UnknownStates(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ from, to string }{
		{"", ""},
		{"pending", ""},
		{"reserved", "active"},
		{"active", "COMPLETED"},
	} {
		if IsValidAllocationTransition(c.from, c.to) {
			t.Errorf("IsValidAllocationTransition(%q, %q) must be false", c.from, c.to)
		}
	}
}

func TestIsTerminalAllocationStatus(t *testing.T) {
	t.Parallel()
	if !IsTerminalAllocationStatus("reconciled") {
		t.Error("reconciled must be terminal")
	}
	for _, s := range []string{"pending", "active", "disputed"} {
		if IsTerminalAllocationStatus(s) {
			t.Errorf("%q must NOT be terminal", s)
		}
	}
	for _, s := range []string{"", "settled"} {
		if IsTerminalAllocationStatus(s) {
			t.Errorf("unknown %q must NOT be terminal", s)
		}
	}
}
