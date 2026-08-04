// line_category_test.go — coverage of the event-report line-category
// enumeration (AB-46).
package reporting

import "testing"

func TestAllLineCategories_HasCanonicalOrderAndIsExhaustive(t *testing.T) {
	t.Parallel()
	expected := []string{
		CategorySales,
		CategoryRefunds,
		CategoryComplimentary,
		CategoryScans,
		CategoryCommissions,
		CategoryPayouts,
	}
	if len(AllLineCategories) != len(expected) {
		t.Fatalf("AllLineCategories: got %d entries, want %d", len(AllLineCategories), len(expected))
	}
	for i, want := range expected {
		if AllLineCategories[i] != want {
			t.Errorf("AllLineCategories[%d]: got %q, want %q", i, AllLineCategories[i], want)
		}
	}
}

func TestIsKnownLineCategory(t *testing.T) {
	t.Parallel()
	for _, c := range AllLineCategories {
		if !IsKnownLineCategory(c) {
			t.Errorf("IsKnownLineCategory(%q) = false, want true", c)
		}
	}
}

func TestIsKnownLineCategory_Unknown(t *testing.T) {
	t.Parallel()
	for _, c := range []string{
		"",
		"SALES",     // case-sensitive
		"discounts", // wrong word
		"refund",    // singular vs plural
		"platform_fee",
	} {
		if IsKnownLineCategory(c) {
			t.Errorf("IsKnownLineCategory(%q) = true, want false", c)
		}
	}
}

// TestCategoryStringConstants_AreStableWireStrings pins the wire-format
// string values — the values are stored in event_report_lines.category and
// a change requires a data migration.
func TestCategoryStringConstants_AreStableWireStrings(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"CategorySales":         CategorySales,
		"CategoryRefunds":       CategoryRefunds,
		"CategoryComplimentary": CategoryComplimentary,
		"CategoryScans":         CategoryScans,
		"CategoryCommissions":   CategoryCommissions,
		"CategoryPayouts":       CategoryPayouts,
	}
	want := map[string]string{
		"CategorySales":         "sales",
		"CategoryRefunds":       "refunds",
		"CategoryComplimentary": "complimentary",
		"CategoryScans":         "scans",
		"CategoryCommissions":   "commissions",
		"CategoryPayouts":       "payouts",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s drift: got %q, want %q", name, got, want[name])
		}
	}
}
