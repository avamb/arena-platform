package hcatalog

import (
	"testing"
)

// TestAB48_ParsePriceWindows pins the shape rules of the schedule writer:
// RFC3339 bounds, to > from, non-negative amounts, overlap pre-check,
// back-to-back windows allowed ('[)' semantics), and sorting.
func TestAB48_ParsePriceWindows(t *testing.T) {
	t.Parallel()
	to := func(s string) *string { return &s }

	cases := []struct {
		name     string
		in       []priceWindowInput
		wantCode string
		wantLen  int
	}{
		{"empty schedule is fine", nil, "", 0},
		{"bad from", []priceWindowInput{{ValidFrom: "nope", PriceAmount: 1}}, "tier.invalid_price_window", 0},
		{"to before from", []priceWindowInput{{ValidFrom: "2026-02-01T00:00:00Z", ValidTo: to("2026-01-01T00:00:00Z"), PriceAmount: 1}}, "tier.invalid_price_window", 0},
		{"negative amount", []priceWindowInput{{ValidFrom: "2026-01-01T00:00:00Z", PriceAmount: -1}}, "tier.invalid_price_window", 0},
		{"overlap rejected", []priceWindowInput{
			{ValidFrom: "2026-01-01T00:00:00Z", ValidTo: to("2026-03-01T00:00:00Z"), PriceAmount: 10},
			{ValidFrom: "2026-02-01T00:00:00Z", PriceAmount: 20},
		}, "tier.price_windows_overlap", 0},
		{"open-ended then later window overlaps", []priceWindowInput{
			{ValidFrom: "2026-01-01T00:00:00Z", PriceAmount: 10},
			{ValidFrom: "2026-02-01T00:00:00Z", PriceAmount: 20},
		}, "tier.price_windows_overlap", 0},
		{"back-to-back is legal and gets sorted", []priceWindowInput{
			{ValidFrom: "2026-02-01T00:00:00Z", PriceAmount: 20},
			{ValidFrom: "2026-01-01T00:00:00Z", ValidTo: to("2026-02-01T00:00:00Z"), PriceAmount: 10},
		}, "", 2},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, code, _ := parsePriceWindows(tc.in)
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if code == "" && len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
			if code == "" && tc.wantLen == 2 && !got[0].from.Before(got[1].from) {
				t.Fatal("windows not sorted by from")
			}
		})
	}
	if _, code, _ := parsePriceWindows(make([]priceWindowInput, maxPriceWindows+1)); code != "tier.too_many_price_windows" {
		t.Fatalf("cap: code = %q", code)
	}
}
