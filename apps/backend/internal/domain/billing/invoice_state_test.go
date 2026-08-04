// invoice_state_test.go — table-driven coverage of the platform-invoice
// state machine and period helpers (AB-46).
package billing

import (
	"testing"
	"time"
)

func TestAllInvoiceStates_MatchesValidTransitionsMap(t *testing.T) {
	t.Parallel()
	if len(AllInvoiceStates) != len(ValidInvoiceTransitions) {
		t.Fatalf("AllInvoiceStates has %d entries, ValidInvoiceTransitions has %d — must match",
			len(AllInvoiceStates), len(ValidInvoiceTransitions))
	}
	for _, s := range AllInvoiceStates {
		if _, ok := ValidInvoiceTransitions[s]; !ok {
			t.Errorf("AllInvoiceStates lists %q which is missing from ValidInvoiceTransitions", s)
		}
	}
}

func TestValidInvoiceTransitions_ContainsExactlyTheDocumentedEdges(t *testing.T) {
	t.Parallel()
	expected := map[string]map[string]bool{
		InvoiceStateDraft: {
			InvoiceStateIssued: true,
			InvoiceStateVoid:   true,
		},
		InvoiceStateIssued: {
			InvoiceStatePaid: true,
			InvoiceStateVoid: true,
		},
		InvoiceStatePaid: {},
		InvoiceStateVoid: {},
	}
	if len(ValidInvoiceTransitions) != len(expected) {
		t.Fatalf("keys: got %d want %d", len(ValidInvoiceTransitions), len(expected))
	}
	for from, want := range expected {
		got := ValidInvoiceTransitions[from]
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

func TestCanTransitionInvoice(t *testing.T) {
	t.Parallel()
	all := AllInvoiceStates
	allowed := map[string]map[string]bool{
		"draft":  {"issued": true, "void": true},
		"issued": {"paid": true, "void": true},
		"paid":   {},
		"void":   {},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			got := CanTransitionInvoice(from, to)
			if got != want {
				t.Errorf("CanTransitionInvoice(%q, %q) = %v, want %v", from, to, got, want)
			}
			if from == to && got {
				t.Errorf("identity transition %q → %q must be rejected", from, to)
			}
		}
	}
}

func TestCanTransitionInvoice_UnknownStates(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ from, to string }{
		{"", ""},
		{"pending", "issued"},
		{"draft", "REJECTED"},
		{"DRAFT", "issued"},
	} {
		if CanTransitionInvoice(c.from, c.to) {
			t.Errorf("CanTransitionInvoice(%q, %q) must be false", c.from, c.to)
		}
	}
}

func TestIsTerminalInvoiceState(t *testing.T) {
	t.Parallel()
	if !IsTerminalInvoiceState("paid") {
		t.Error("paid must be terminal")
	}
	if !IsTerminalInvoiceState("void") {
		t.Error("void must be terminal")
	}
	for _, s := range []string{"draft", "issued"} {
		if IsTerminalInvoiceState(s) {
			t.Errorf("%q must NOT be terminal", s)
		}
	}
	for _, s := range []string{"", "unknown", "PAID"} {
		if IsTerminalInvoiceState(s) {
			t.Errorf("unknown %q must NOT be terminal", s)
		}
	}
}

func TestPeriodForTime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), "2026-01"},
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), "2026-12"},
		// Non-UTC input is normalised to UTC.
		{time.Date(2026, 2, 1, 1, 0, 0, 0, time.FixedZone("MSK", 3*3600)), "2026-01"},
	}
	for _, tc := range cases {
		if got := PeriodForTime(tc.in); got != tc.want {
			t.Errorf("PeriodForTime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBillingLayouts ensures the exported layout constants stay in sync
// with the string forms callers rely on.
func TestBillingLayouts(t *testing.T) {
	t.Parallel()
	if BillingPeriodLayout != "2006-01" {
		t.Errorf("BillingPeriodLayout drift: got %q", BillingPeriodLayout)
	}
	if BillingDateLayout != "2006-01-02" {
		t.Errorf("BillingDateLayout drift: got %q", BillingDateLayout)
	}
}
