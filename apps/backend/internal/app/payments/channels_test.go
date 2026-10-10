// channels_test.go — PAY-02: the channel provider rule comes from the
// registry, and a module registered tomorrow is accepted without a migration.
package payments

import (
	"reflect"
	"testing"

	domain "github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func TestChannelProviders_AreTheModulesWithCode(t *testing.T) {
	// Exactly what sales_channels_provider_check allowed before 0132, in
	// registration order.
	if got, want := ChannelProviders(), []string{"stripe", "flitt", "allpay"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ChannelProviders = %v; want %v", got, want)
	}
	if DefaultChannelProvider() != "stripe" {
		t.Errorf("DefaultChannelProvider = %q; want stripe", DefaultChannelProvider())
	}
	for _, p := range []string{"stripe", "flitt", "allpay"} {
		if !IsChannelProvider(p) {
			t.Errorf("IsChannelProvider(%q) = false", p)
		}
	}
	for _, p := range []string{"", "manual", "cloudpayments", "yookassa", "Stripe", " flitt", "mock"} {
		if IsChannelProvider(p) {
			t.Errorf("IsChannelProvider(%q) = true", p)
		}
	}
}

func TestChannelProviderForChoice_HostedCheckoutModulesOnly(t *testing.T) {
	cases := []struct {
		choice   string
		want     string
		accepted bool
	}{
		{"stripe", "stripe", true},
		{"flitt", "flitt", true},
		{" flitt ", "flitt", true},
		// AllPay has a module but no hosted page: a new workspace cannot
		// sell through it, so it starts on the default.
		{"allpay", "stripe", false},
		{"other", "stripe", false},
		{"undecided", "stripe", false},
		{"", "stripe", false},
		{"yookassa", "stripe", false},
	}
	for _, c := range cases {
		got, ok := ChannelProviderForChoice(c.choice)
		if got != c.want || ok != c.accepted {
			t.Errorf("%q: got (%q, %v); want (%q, %v)", c.choice, got, ok, c.want, c.accepted)
		}
	}
}

type fakeHosted struct{}

func (fakeHosted) Descriptor() domain.Descriptor {
	return domain.Descriptor{Name: "pay02hosted", Title: "Fake", Capabilities: domain.Capabilities{HostedCheckout: true}}
}

func TestChannelProviderForChoice_NewModuleNeedsNoEdit(t *testing.T) {
	reg := domain.MustRegistry(append(Modules(), domain.Entry{
		Descriptor: fakeHosted{}.Descriptor(),
		New:        func(map[string]string, domain.Options) (domain.Module, error) { return fakeHosted{}, nil },
	})...)
	if got, ok := channelProviderForChoice(reg, "pay02hosted"); got != "pay02hosted" || !ok {
		t.Fatalf("a registered hosted-checkout module: got (%q, %v)", got, ok)
	}
	if !reg.IsModule("pay02hosted") {
		t.Fatal("a registered module is not a channel provider")
	}
}
