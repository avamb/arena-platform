package hcheckout

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func cfgRow(provider, mode, status string, active bool) gen.PaymentProviderConfigRow {
	return gen.PaymentProviderConfigRow{Provider: provider, Mode: mode, Status: status, IsActive: active}
}

// TestAB41_SelectProviderConfig pins the go/no-go table: never a silent
// fall-through; live needs KYB; live preferred over test when both usable.
func TestAB41_SelectProviderConfig(t *testing.T) {
	t.Parallel()
	deleted := cfgRow("stripe", "test", "configured", true)
	now := time.Now()
	deleted.DeletedAt = &now

	cases := []struct {
		name     string
		rows     []gen.PaymentProviderConfigRow
		provider string
		kyb      string
		wantCode string
		wantMode string
	}{
		{"no config", nil, "stripe", "verified", ErrCodeProviderNotConfigured, ""},
		{"other provider only", []gen.PaymentProviderConfigRow{cfgRow("allpay", "test", "configured", true)}, "stripe", "verified", ErrCodeProviderNotConfigured, ""},
		{"inactive", []gen.PaymentProviderConfigRow{cfgRow("stripe", "test", "configured", false)}, "stripe", "verified", ErrCodeProviderInactive, ""},
		{"missing fields", []gen.PaymentProviderConfigRow{cfgRow("stripe", "test", "missing_required_fields", true)}, "stripe", "verified", ErrCodeProviderMissingFields, ""},
		{"live without kyb", []gen.PaymentProviderConfigRow{cfgRow("stripe", "live", "configured", true)}, "stripe", "unverified", ErrCodeProviderKYBRequired, ""},
		{"test without kyb is fine", []gen.PaymentProviderConfigRow{cfgRow("stripe", "test", "configured", true)}, "stripe", "unverified", "", "test"},
		{"live wins when verified", []gen.PaymentProviderConfigRow{cfgRow("stripe", "test", "configured", true), cfgRow("stripe", "live", "configured", true)}, "stripe", "verified", "", "live"},
		{"falls back to test when live needs kyb", []gen.PaymentProviderConfigRow{cfgRow("stripe", "live", "configured", true), cfgRow("stripe", "test", "configured", true)}, "stripe", "pending", "", "test"},
		{"soft-deleted ignored, case-insensitive provider", []gen.PaymentProviderConfigRow{deleted}, "Stripe", "verified", ErrCodeProviderNotConfigured, ""},
		{"manual provider needs a row too", []gen.PaymentProviderConfigRow{cfgRow("manual", "test", "configured", true)}, "manual", "unverified", "", "test"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := SelectProviderConfig(tc.rows, tc.provider, tc.kyb)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				if got.Mode != tc.wantMode {
					t.Fatalf("mode = %q, want %q", got.Mode, tc.wantMode)
				}
				return
			}
			if err == nil || err.Code != tc.wantCode {
				t.Fatalf("err = %v, want code %q", err, tc.wantCode)
			}
		})
	}
}

func TestAB41_WebhookSecretFromConfig(t *testing.T) {
	t.Parallel()
	stripe := cfgRow("stripe", "live", "configured", true)
	stripe.Secrets = json.RawMessage(`{"api_key":"sk_live_x","webhook_secret":" whsec_1 "}`)
	if got := WebhookSecretFromConfig(stripe); got != "whsec_1" {
		t.Fatalf("stripe secret = %q", got)
	}
	allpay := cfgRow("allpay", "live", "configured", true)
	allpay.Secrets = json.RawMessage(`{"merchant_id":"m","secret_key":"s3"}`)
	if got := WebhookSecretFromConfig(allpay); got != "s3" {
		t.Fatalf("allpay secret = %q", got)
	}
	if got := WebhookSecretFromConfig(cfgRow("stripe", "test", "configured", true)); got != "" {
		t.Fatalf("empty secrets = %q", got)
	}
}
