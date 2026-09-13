// auth_payment_window_test.go — unit tests for the payment-window gateway
// settings (owner decision 2026-09-13): parseGatewaySettings must always
// populate PaymentWindowSeconds / PaymentGraceSeconds, defaulting when the
// channel sets neither and clamping to sane bounds when it sets an
// out-of-range value. No live PostgreSQL required.
package hbil24

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseGatewaySettings_PaymentWindowDefaults(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"nil_settings", ""},
		{"empty_object", `{}`},
		{"no_gateway_block", `{"other":"stuff"}`},
		{"gateway_block_without_payment_fields", `{"gateway":{"enabled":true,"token_hash":"h"}}`},
		{"legacy_top_level_hash_only", `{"gateway_token_hash":"h"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGatewaySettings(json.RawMessage(tc.raw))
			if got.PaymentWindowSeconds != DefaultPaymentWindowSeconds {
				t.Errorf("PaymentWindowSeconds = %d, want default %d", got.PaymentWindowSeconds, DefaultPaymentWindowSeconds)
			}
			if got.PaymentGraceSeconds != DefaultPaymentGraceSeconds {
				t.Errorf("PaymentGraceSeconds = %d, want default %d", got.PaymentGraceSeconds, DefaultPaymentGraceSeconds)
			}
			if got.PaymentWindow() != 1200*time.Second {
				t.Errorf("PaymentWindow() = %v, want 1200s", got.PaymentWindow())
			}
			if got.PaymentGrace() != 120*time.Second {
				t.Errorf("PaymentGrace() = %v, want 120s", got.PaymentGrace())
			}
		})
	}
}

func TestParseGatewaySettings_PaymentWindowChannelOverride(t *testing.T) {
	raw := json.RawMessage(`{"gateway":{"enabled":true,"token_hash":"h",
		"payment_window_seconds":600,"payment_grace_seconds":30}}`)
	got := parseGatewaySettings(raw)
	if got.PaymentWindowSeconds != 600 {
		t.Errorf("PaymentWindowSeconds = %d, want 600", got.PaymentWindowSeconds)
	}
	if got.PaymentGraceSeconds != 30 {
		t.Errorf("PaymentGraceSeconds = %d, want 30", got.PaymentGraceSeconds)
	}
}

// A channel setting ONLY the payment window (no token_hash/enabled/locale)
// must still be picked up — the "has content" branch test in
// parseGatewaySettingsBlock must consider the payment fields too.
func TestParseGatewaySettings_PaymentWindowOnlyNoOtherGatewayFields(t *testing.T) {
	raw := json.RawMessage(`{"gateway":{"payment_window_seconds":900}}`)
	got := parseGatewaySettings(raw)
	if got.PaymentWindowSeconds != 900 {
		t.Errorf("PaymentWindowSeconds = %d, want 900", got.PaymentWindowSeconds)
	}
	if got.PaymentGraceSeconds != DefaultPaymentGraceSeconds {
		t.Errorf("PaymentGraceSeconds = %d, want default %d", got.PaymentGraceSeconds, DefaultPaymentGraceSeconds)
	}
}

func TestParseGatewaySettings_PaymentWindowBoundsClamped(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantWindow int
		wantGrace  int
	}{
		{
			name:       "window_too_small",
			raw:        `{"gateway":{"payment_window_seconds":10}}`,
			wantWindow: minPaymentWindowSeconds,
			wantGrace:  DefaultPaymentGraceSeconds,
		},
		{
			name:       "window_too_large",
			raw:        `{"gateway":{"payment_window_seconds":100000}}`,
			wantWindow: maxPaymentWindowSeconds,
			wantGrace:  DefaultPaymentGraceSeconds,
		},
		{
			name:       "grace_negative",
			raw:        `{"gateway":{"payment_grace_seconds":-5}}`,
			wantWindow: DefaultPaymentWindowSeconds,
			wantGrace:  minPaymentGraceSeconds,
		},
		{
			name:       "grace_too_large",
			raw:        `{"gateway":{"payment_grace_seconds":3600}}`,
			wantWindow: DefaultPaymentWindowSeconds,
			wantGrace:  maxPaymentGraceSeconds,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGatewaySettings(json.RawMessage(tc.raw))
			if got.PaymentWindowSeconds != tc.wantWindow {
				t.Errorf("PaymentWindowSeconds = %d, want %d", got.PaymentWindowSeconds, tc.wantWindow)
			}
			if got.PaymentGraceSeconds != tc.wantGrace {
				t.Errorf("PaymentGraceSeconds = %d, want %d", got.PaymentGraceSeconds, tc.wantGrace)
			}
		})
	}
}

func TestParseGatewaySettings_PaymentWindowZeroGraceIsValid(t *testing.T) {
	raw := json.RawMessage(`{"gateway":{"payment_grace_seconds":0}}`)
	got := parseGatewaySettings(raw)
	if got.PaymentGraceSeconds != 0 {
		t.Errorf("PaymentGraceSeconds = %d, want 0 (an explicit zero grace is valid, not \"unset\")", got.PaymentGraceSeconds)
	}
}

func TestClampPaymentSeconds(t *testing.T) {
	def, lo, hi := 100, 10, 1000
	cases := []struct {
		name string
		v    *int
		want int
	}{
		{"nil_is_default", nil, def},
		{"in_range", intPtr(500), 500},
		{"below_lo_clamps", intPtr(1), lo},
		{"above_hi_clamps", intPtr(5000), hi},
		{"exactly_lo", intPtr(lo), lo},
		{"exactly_hi", intPtr(hi), hi},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampPaymentSeconds(tc.v, def, lo, hi); got != tc.want {
				t.Errorf("clampPaymentSeconds(%v) = %d, want %d", tc.v, got, tc.want)
			}
		})
	}
}

func intPtr(v int) *int { return &v }
