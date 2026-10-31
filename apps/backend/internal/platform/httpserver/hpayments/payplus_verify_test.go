package hpayments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// A payment_page_uid that is not a UUID is refused when the config is
// saved, not at the first buyer's checkout.
func TestValidateSecretFormats_PayPlusPaymentPageUID(t *testing.T) {
	if err := ValidateSecretFormats("payplus", "test", map[string]string{"payment_page_uid": "7a0bc4d4-f35f-4301-a945-926378a2416d"}); err != nil {
		t.Errorf("a payment page uid was refused: %v", err)
	}
	if err := ValidateSecretFormats("payplus", "live", map[string]string{"payment_page_uid": "my-page"}); err == nil {
		t.Error("a payment_page_uid that is not a UUID was accepted")
	}
}

// The credential check builds the PayPlus module with the ROW's mode and the
// test seam, and tells refused keys from an accepted pair.
func TestCredentialVerifierFor_PayPlusUsesTheRowAndTheSeam(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("secret-key") != "good-secret" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"MISSING API_KEY"}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":{"status":"error","code":1,"description":"payment request not found"}}`))
	}))
	defer srv.Close()
	h := (&Handler{}).WithPayPlusBaseURL(srv.URL + "/api/v1.0")

	row := func(secret string) gen.PaymentProviderConfigRow {
		s, _ := json.Marshal(map[string]string{"api_key": "the-api-key", "secret_key": secret, "payment_page_uid": "7a0bc4d4-f35f-4301-a945-926378a2416d"})
		return gen.PaymentProviderConfigRow{Provider: "payplus", Mode: "test", Secrets: s}
	}
	v, ok := h.CredentialVerifierFor(row("good-secret"))
	if !ok {
		t.Fatal("PayPlus has no credential verifier")
	}
	if err := v.VerifyCredentials(context.Background()); err != nil {
		t.Errorf("accepted keys = %v; want nil", err)
	}
	if gotKey != "the-api-key" {
		t.Errorf("api-key header = %q; want the stored api_key", gotKey)
	}
	v, _ = h.CredentialVerifierFor(row("bad-secret"))
	if err := v.VerifyCredentials(context.Background()); !errors.Is(err, payments.ErrCredentialRefused) {
		t.Errorf("refused keys = %v; want ErrCredentialRefused", err)
	}
}
