package hpayments

import "testing"

func TestValidateSecretFormats_PublishableKeyByMode(t *testing.T) {
	if err := ValidateSecretFormats("stripe", "test", map[string]string{"publishable_key": "pk_test_51ABCdef"}); err != nil {
		t.Errorf("pk_test_ rejected in test mode: %v", err)
	}
	if err := ValidateSecretFormats("stripe", "live", map[string]string{"publishable_key": "pk_live_51ABCdef"}); err != nil {
		t.Errorf("pk_live_ rejected in live mode: %v", err)
	}
	if err := ValidateSecretFormats("stripe", "test", map[string]string{"publishable_key": "pk_live_51ABCdef"}); err == nil {
		t.Error("pk_live_ accepted in test mode")
	}
	if err := ValidateSecretFormats("stripe", "live", map[string]string{"publishable_key": "pk_test_51ABCdef"}); err == nil {
		t.Error("pk_test_ accepted in live mode")
	}
	if err := ValidateSecretFormats("stripe", "test", map[string]string{"publishable_key": "sk_test_51ABCdef"}); err == nil {
		t.Error("a secret key accepted as the publishable key")
	}
}

func TestRequiredSecretFields_StripeIgnoresPublishableKey(t *testing.T) {
	req, ok := requiredSecretFields("stripe")
	if !ok {
		t.Fatal("stripe unknown")
	}
	for _, k := range req {
		if k == "publishable_key" {
			t.Fatal("publishable_key became a required field")
		}
	}
}
