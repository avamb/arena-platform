// pricing_mode_test.go — table-driven coverage of TicketTier pricing-mode
// invariants (AB-46).
package catalog

import "testing"

func TestValidPricingModes(t *testing.T) {
	t.Parallel()
	for _, m := range []PricingMode{PricingModeFixed, PricingModeFree, PricingModePWYW} {
		if !IsValidPricingMode(string(m)) {
			t.Errorf("IsValidPricingMode(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "gratis", "PWYW", "fixed_amount"} {
		if IsValidPricingMode(m) {
			t.Errorf("IsValidPricingMode(%q) = true, want false", m)
		}
	}
}

func i64p(v int64) *int64 { return &v }

func TestValidatePricingMode_Free(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		price    int64
		wantCode string
	}{
		{"free at zero passes", 0, ""},
		{"free at 1 cent rejected", 1, "tier.invalid_free_price"},
		{"free at negative rejected", -1, "tier.invalid_free_price"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, msg := ValidatePricingMode("free", tc.price, nil, nil)
			if code != tc.wantCode {
				t.Errorf("code: got %q want %q", code, tc.wantCode)
			}
			if tc.wantCode != "" && msg == "" {
				t.Errorf("expected non-empty message on failure code %q", tc.wantCode)
			}
		})
	}
}

func TestValidatePricingMode_Fixed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		price    int64
		wantCode string
	}{
		{"fixed at zero rejected", 0, "tier.invalid_fixed_price"},
		{"fixed at negative rejected", -1, "tier.invalid_fixed_price"},
		{"fixed at 1 cent passes", 1, ""},
		{"fixed at high price passes", 999_999_99, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, msg := ValidatePricingMode("fixed", tc.price, nil, nil)
			if code != tc.wantCode {
				t.Errorf("code: got %q want %q", code, tc.wantCode)
			}
			if tc.wantCode != "" && msg == "" {
				t.Errorf("expected non-empty message on failure code %q", tc.wantCode)
			}
		})
	}
}

func TestValidatePricingMode_PWYW(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		price    int64
		min, max *int64
		wantCode string
	}{
		{"pwyw at zero passes (no bounds)", 0, nil, nil, ""},
		{"pwyw at positive passes (no bounds)", 500, nil, nil, ""},
		{"pwyw at negative rejected", -1, nil, nil, "tier.invalid_pwyw_price"},
		{"pwyw min > max rejected", 0, i64p(1000), i64p(500), "tier.invalid_pwyw_range"},
		{"pwyw min == max accepted", 500, i64p(500), i64p(500), ""},
		// Individual bound checks fire only when the range check does not.
		{"pwyw negative min alone rejected", 0, i64p(-1), nil, "tier.invalid_pwyw_min"},
		{"pwyw negative max alone rejected", 0, nil, i64p(-1), "tier.invalid_pwyw_max"},
		// When BOTH bounds are set the pair range-check runs first
		// (documented invariant: min<=max), so min=0, max=-1 fails the
		// range check, not the individual max<0 check.
		{"pwyw negative max with min=0 fails range first", 0, i64p(0), i64p(-1), "tier.invalid_pwyw_range"},
		// Only min set (max=nil) — bounds pair-check is skipped, min>=0 still enforced
		{"pwyw only min positive passes", 0, i64p(100), nil, ""},
		{"pwyw only max positive passes", 0, nil, i64p(100), ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, msg := ValidatePricingMode("pwyw", tc.price, tc.min, tc.max)
			if code != tc.wantCode {
				t.Errorf("code: got %q want %q", code, tc.wantCode)
			}
			if tc.wantCode != "" && msg == "" {
				t.Errorf("expected non-empty message on failure")
			}
		})
	}
}

func TestValidatePricingMode_UnknownModeIsSilent(t *testing.T) {
	t.Parallel()
	// Documented: unknown modes pass this check because a separate gate
	// enforces mode-recognition earlier.
	if code, _ := ValidatePricingMode("", 100, nil, nil); code != "" {
		t.Errorf("unknown mode must be silent, got code %q", code)
	}
	if code, _ := ValidatePricingMode("subscription", 100, nil, nil); code != "" {
		t.Errorf("unknown mode must be silent, got code %q", code)
	}
}
