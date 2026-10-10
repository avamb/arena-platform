// channel_provider_test.go — PAY-02: a channel's provider is validated by the
// payment module registry. Pure unit tests; the live-database proof that the
// table no longer lists providers is channel_provider_integration_test.go.
package hcatalog

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// pay02FakeModule is a provider registered tomorrow: a descriptor and a
// factory, nothing else.
type pay02FakeModule struct{ name string }

func (m pay02FakeModule) Descriptor() payments.Descriptor {
	return payments.Descriptor{Name: m.name, Title: "PAY-02 fake"}
}

func pay02Registry(t *testing.T, fake string) *payments.Registry {
	t.Helper()
	reg, err := payments.NewRegistry(
		stripe.Entry(),
		payments.Entry{
			Descriptor: payments.Descriptor{Name: fake, Title: "PAY-02 fake"},
			New: func(map[string]string, payments.Options) (payments.Module, error) {
				return pay02FakeModule{name: fake}, nil
			},
		},
		payments.Declared(payments.Descriptor{Name: "pay02declared", Title: "Declared only"}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

func TestValidateChannelProviderWith_FakeRegisteredModuleIsAccepted(t *testing.T) {
	reg := pay02Registry(t, "pay02fake")
	for _, p := range []string{"stripe", "pay02fake"} {
		if msg := ValidateChannelProviderWith(reg, p); msg != "" {
			t.Errorf("%q: got %q, want accepted", p, msg)
		}
	}
}

func TestValidateChannelProviderWith_RefusesWhatHasNoModule(t *testing.T) {
	reg := pay02Registry(t, "pay02fake")
	cases := map[string]string{
		"":              "provider is required",
		"   ":           "provider is required",
		"pay02declared": "provider must be 'stripe' or 'pay02fake', got \"pay02declared\"",
		"paypal":        "provider must be 'stripe' or 'pay02fake', got \"paypal\"",
		// Stored values are canonical: no case folding, no trimming.
		"Stripe":  "provider must be 'stripe' or 'pay02fake', got \"Stripe\"",
		" stripe": "provider must be 'stripe' or 'pay02fake', got \" stripe\"",
	}
	for in, want := range cases {
		if got := ValidateChannelProviderWith(reg, in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The production registry keeps exactly what the dropped CHECK allowed.
func TestValidateChannelConfig_ProductionRegistryKeepsThePrePAY02List(t *testing.T) {
	for _, p := range []string{"stripe", "flitt", "allpay"} {
		if msg := ValidateChannelConfig("merchant_of_record", p, ""); msg != "" {
			t.Errorf("%q: got %q, want accepted", p, msg)
		}
	}
	for _, p := range []string{"manual", "cloudpayments", "yookassa", "mock", "paypal"} {
		msg := ValidateChannelConfig("merchant_of_record", p, "")
		if !strings.HasPrefix(msg, "provider must be 'stripe', 'flitt' or 'allpay'") {
			t.Errorf("%q: got %q, want the provider refusal", p, msg)
		}
	}
	if msg := ValidateChannelConfig("direct_merchant", "", "acct_1"); msg != "provider is required" {
		t.Errorf("empty provider: got %q", msg)
	}
}

func TestQuotedAlternatives(t *testing.T) {
	cases := map[string][]string{
		"a registered payment provider": nil,
		"'a'":                           {"a"},
		"'a' or 'b'":                    {"a", "b"},
		"'a', 'b' or 'c'":               {"a", "b", "c"},
	}
	for want, in := range cases {
		if got := quotedAlternatives(in); got != want {
			t.Errorf("%v: got %q, want %q", in, got, want)
		}
	}
}
