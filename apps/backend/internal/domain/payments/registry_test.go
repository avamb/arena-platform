// registry_test.go — the module registry (PAY-01) that replaced
// PaymentRoutingPolicy: descriptors validate, names resolve in registration
// order, an unknown name is ErrUnknownProvider, a declared provider builds
// nothing, and SecretsFromJSON is the one decoder of a secrets blob.
package payments_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

type stubModule struct{ d payments.Descriptor }

func (s stubModule) Descriptor() payments.Descriptor { return s.d }

func entryFor(name string) payments.Entry {
	d := payments.Descriptor{
		Name:  name,
		Title: name,
		Secrets: []payments.SecretField{
			{Key: "api_key", Required: true, Credential: true, Hidden: true},
			{Key: "webhook_secret", Required: true, WebhookSecret: true, Hidden: true},
		},
		Capabilities: payments.Capabilities{HostedCheckout: true},
	}
	return payments.Entry{Descriptor: d, New: func(_ map[string]string, _ payments.Options) (payments.Module, error) {
		return stubModule{d}, nil
	}}
}

func TestRegistry_ResolvesInRegistrationOrder(t *testing.T) {
	reg, err := payments.NewRegistry(entryFor("alpha"), entryFor("beta"), payments.Declared(payments.Descriptor{Name: "gamma", Title: "Gamma"}))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if got := reg.Names(); !reflect.DeepEqual(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("Names = %v", got)
	}
	hosted := reg.NamesWhere(func(d payments.Descriptor) bool { return d.Capabilities.HostedCheckout })
	if !reflect.DeepEqual(hosted, []string{"alpha", "beta"}) {
		t.Fatalf("NamesWhere(hosted) = %v", hosted)
	}
	if _, ok := reg.Get(" Beta "); !ok {
		t.Fatal("Get must trim and fold case")
	}
	m, err := reg.Build("alpha", map[string]string{"api_key": "k"}, payments.Options{})
	if err != nil || m.Descriptor().Name != "alpha" {
		t.Fatalf("Build(alpha) = %v, %v", m, err)
	}
}

func TestRegistry_UnknownAndDeclaredProviders(t *testing.T) {
	reg, err := payments.NewRegistry(payments.Declared(payments.Descriptor{Name: "gamma", Title: "Gamma"}))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err := reg.Build("nonexistent_provider", nil, payments.Options{}); !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("unknown provider: got %v, want ErrUnknownProvider", err)
	}
	if _, err := reg.Build("", nil, payments.Options{}); !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("empty provider: got %v, want ErrUnknownProvider", err)
	}
	if _, err := reg.Build("gamma", nil, payments.Options{}); !errors.Is(err, payments.ErrModuleNotImplemented) {
		t.Fatalf("declared provider: got %v, want ErrModuleNotImplemented", err)
	}
	var nilReg *payments.Registry
	if _, ok := nilReg.Get("gamma"); ok || nilReg.Names() != nil {
		t.Fatal("a nil registry must answer nothing, not panic")
	}
}

func TestRegistry_RefusesInvalidAndDuplicateDescriptors(t *testing.T) {
	bad := []payments.Descriptor{
		{Name: "", Title: "x"},
		{Name: "Stripe", Title: "x"},
		{Name: "ok", Title: ""},
		{Name: "ok", Title: "x", Secrets: []payments.SecretField{{Key: "a"}, {Key: "a"}}},
		{Name: "ok", Title: "x", Secrets: []payments.SecretField{{Key: ""}}},
		{Name: "ok", Title: "x", Secrets: []payments.SecretField{{Key: "a", Pattern: "("}}},
		{Name: "ok", Title: "x", Secrets: []payments.SecretField{{Key: "a", WebhookSecret: true}, {Key: "b", WebhookSecret: true}}},
	}
	for i, d := range bad {
		if _, err := payments.NewRegistry(payments.Declared(d)); err == nil {
			t.Errorf("descriptor %d (%+v) was accepted", i, d)
		}
	}
	if _, err := payments.NewRegistry(entryFor("alpha"), entryFor("alpha")); err == nil {
		t.Error("a duplicate name was accepted")
	}
}

func TestDescriptor_KeyHelpers(t *testing.T) {
	d := entryFor("alpha").Descriptor
	if got := d.RequiredSecretKeys(); !reflect.DeepEqual(got, []string{"api_key", "webhook_secret"}) {
		t.Errorf("RequiredSecretKeys = %v", got)
	}
	if got := d.CredentialKeys(); !reflect.DeepEqual(got, []string{"api_key"}) {
		t.Errorf("CredentialKeys = %v", got)
	}
	if got := d.WebhookSecretKey(); got != "webhook_secret" {
		t.Errorf("WebhookSecretKey = %q", got)
	}
	if _, ok := d.Secret("nope"); ok {
		t.Error("Secret(nope) found something")
	}
}

func TestSecretsFromJSON(t *testing.T) {
	got := payments.SecretsFromJSON(json.RawMessage(`{"api_key":" sk_x ","n":1,"obj":{"a":1},"empty":""}`))
	want := map[string]string{"api_key": "sk_x", "empty": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SecretsFromJSON = %v, want %v", got, want)
	}
	if got := payments.SecretsFromJSON(nil); got == nil || len(got) != 0 {
		t.Errorf("nil blob = %v, want an empty map", got)
	}
	if got := payments.SecretsFromJSON(json.RawMessage(`not json`)); got == nil || len(got) != 0 {
		t.Errorf("garbage blob = %v, want an empty map", got)
	}
}

// PAY-02: the channel-provider rule. Only an entry with a module counts, and
// only by its exact canonical name.
func TestRegistry_ModuleNamesAndIsModule(t *testing.T) {
	reg := payments.MustRegistry(entryFor("alpha"), payments.Declared(payments.Descriptor{Name: "beta", Title: "Beta"}), entryFor("gamma"))
	if got, want := reg.ModuleNames(), []string{"alpha", "gamma"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ModuleNames = %v; want %v", got, want)
	}
	for name, want := range map[string]bool{"alpha": true, "gamma": true, "beta": false, "Alpha": false, " alpha": false, "": false, "delta": false} {
		if got := reg.IsModule(name); got != want {
			t.Errorf("IsModule(%q) = %v; want %v", name, got, want)
		}
	}
	var nilReg *payments.Registry
	if nilReg.ModuleNames() != nil || nilReg.IsModule("alpha") {
		t.Error("a nil registry must know no modules")
	}
}
