// modules_test.go — the registered module list (PAY-01). Pins what the
// tables it replaced said, so moving them into descriptors changed nothing
// an operator or a client can observe.
package payments_test

import (
	"reflect"
	"testing"

	apppayments "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func TestModules_OrderAndRequiredSecretsMatchThePrePAY01Tables(t *testing.T) {
	reg := apppayments.Registry()
	if got, want := reg.Names(), []string{"stripe", "flitt", "allpay", "cloudpayments", "yookassa", "manual"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v; want %v", got, want)
	}
	// hpayments.requiredSecretFields before PAY-01.
	required := map[string][]string{
		"stripe":        {"api_key", "webhook_secret"},
		"allpay":        {"merchant_id", "secret_key"},
		"flitt":         {"merchant_id", "payment_key"},
		"cloudpayments": {"public_id", "api_secret"},
		"yookassa":      {"shop_id", "secret_key"},
		"manual":        {},
	}
	for name, want := range required {
		e, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if got := e.Descriptor.RequiredSecretKeys(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s required secrets = %v; want %v", name, got, want)
		}
	}
	// hfeed.credentialFields / hostedProviders before PAY-01.
	hosted := reg.NamesWhere(func(d payments.Descriptor) bool { return d.Capabilities.HostedCheckout })
	if !reflect.DeepEqual(hosted, []string{"stripe", "flitt"}) {
		t.Errorf("hosted-checkout providers = %v; want [stripe flitt]", hosted)
	}
	creds := map[string][]string{"stripe": {"api_key"}, "flitt": {"merchant_id", "payment_key"}}
	for name, want := range creds {
		e, _ := reg.Get(name)
		if got := e.Descriptor.CredentialKeys(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s credential keys = %v; want %v", name, got, want)
		}
	}
	// hcheckout.WebhookSecretFromConfig before PAY-01.
	webhook := map[string]string{"stripe": "webhook_secret", "allpay": "secret_key", "flitt": "payment_key"}
	for name, want := range webhook {
		e, _ := reg.Get(name)
		if got := e.Descriptor.WebhookSecretKey(); got != want {
			t.Errorf("%s webhook secret key = %q; want %q", name, got, want)
		}
	}
}

func TestModules_EveryModuleBuildsAndMatchesItsDescriptor(t *testing.T) {
	reg := apppayments.Registry()
	for _, name := range reg.Names() {
		e, _ := reg.Get(name)
		if e.New == nil {
			continue // declared only
		}
		m, err := e.Build(nil, payments.Options{})
		if err != nil {
			t.Fatalf("Build(%s): %v", name, err)
		}
		if m.Descriptor().Name != name {
			t.Errorf("module %s describes itself as %q", name, m.Descriptor().Name)
		}
		if e.Descriptor.Capabilities.HostedCheckout {
			if _, ok := m.(payments.HostedCheckoutProvider); !ok {
				t.Errorf("%s declares HostedCheckout but is not a HostedCheckoutProvider", name)
			}
		}
		if e.Descriptor.Capabilities.Refund {
			if _, ok := m.(payments.Refunder); !ok {
				t.Errorf("%s declares Refund but is not a Refunder", name)
			}
		}
	}
}

func TestPlatformWebhookSecrets_NamesRegisteredParsers(t *testing.T) {
	for name := range apppayments.PlatformWebhookSecrets("a", "b") {
		e, ok := apppayments.Registry().Get(name)
		if !ok || e.Descriptor.WebhookSecretKey() == "" {
			t.Errorf("PlatformWebhookSecrets names %q, which is not a registered provider with a webhook secret", name)
		}
	}
}
