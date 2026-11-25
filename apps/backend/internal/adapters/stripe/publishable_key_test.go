package stripe_test

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
)

func TestDescriptor_PublishableKeyIsOptionalAndNotACredential(t *testing.T) {
	d := stripe.ProviderDescriptor()
	f, ok := d.Secret(stripe.SecretPublishableKey)
	if !ok {
		t.Fatal("publishable_key missing from the descriptor")
	}
	if f.Required || f.Credential || f.Hidden || f.WebhookSecret {
		t.Fatalf("publishable_key must be optional, public, non-credential: %+v", f)
	}
	for _, k := range d.RequiredSecretKeys() {
		if k == stripe.SecretPublishableKey {
			t.Fatal("publishable_key must not be a required key")
		}
	}
	for _, k := range d.CredentialKeys() {
		if k == stripe.SecretPublishableKey {
			t.Fatal("publishable_key must not be a credential key")
		}
	}
	if len(d.RequiredSecretKeys()) != 2 {
		t.Fatalf("required keys = %v, want api_key + webhook_secret only", d.RequiredSecretKeys())
	}
}
