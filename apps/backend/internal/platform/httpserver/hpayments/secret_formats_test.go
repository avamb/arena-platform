package hpayments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// The case this whole mechanism was built for: a live organization's Stripe
// config held a twenty-character string that was not a key of any kind, the
// admin reported it as configured, and the only party told otherwise was a
// buyer getting a 503 on the pay button.
func TestValidateSecretFormats_RejectsAValueThatIsNotAKey(t *testing.T) {
	err := ValidateSecretFormats("stripe", "test", map[string]string{
		"api_key": "ZNF5jn3xAbCdEfGhQdJv",
	})
	if err == nil {
		t.Fatal("a non-key was accepted; this is exactly the failure the check exists to stop")
	}
	if !strings.Contains(err.Error(), "sk_test_") {
		t.Errorf("error must tell the operator what a key looks like, got: %v", err)
	}
	// An error message is the one place a secret reliably ends up in a log.
	if strings.Contains(err.Error(), "ZNF5jn3x") {
		t.Errorf("the rejected value must never be echoed back: %v", err)
	}
}

func TestValidateSecretFormats_AcceptsRealKeyShapes(t *testing.T) {
	cases := []struct {
		mode, field, value string
	}{
		{"test", "api_key", "sk_test_51ABCdefGHIjklMNO"},
		{"live", "api_key", "sk_live_51ABCdefGHIjklMNO"},
		// Restricted keys are valid API credentials; an org is entitled to
		// scope one down and must not be refused for it.
		{"test", "api_key", "rk_test_51ABCdefGHIjklMNO"},
		{"live", "api_key", "rk_live_51ABCdefGHIjklMNO"},
		{"test", "webhook_secret", "whsec_ABCdefGHIjklMNOpqr"},
	}
	for _, c := range cases {
		if err := ValidateSecretFormats("stripe", c.mode, map[string]string{c.field: c.value}); err != nil {
			t.Errorf("%s/%s rejected a legitimate value: %v", c.mode, c.field, err)
		}
	}
}

// Filing a live key under a test config (or the reverse) is a silent way to
// charge real cards from a "test" row, or to take no money at all.
func TestValidateSecretFormats_RejectsAKeyFromTheOtherMode(t *testing.T) {
	err := ValidateSecretFormats("stripe", "test", map[string]string{
		"api_key": "sk_live_51ABCdefGHIjklMNO",
	})
	if err == nil {
		t.Fatal("a live key was accepted into a test configuration")
	}
	if !strings.Contains(err.Error(), "live") || !strings.Contains(err.Error(), "test") {
		t.Errorf("the error must name both modes so the operator can tell what happened: %v", err)
	}
}

// A partial update that does not touch the credential must not be refused
// because of it, and an empty value is the documented delete marker.
func TestValidateSecretFormats_IgnoresUntouchedAndClearedFields(t *testing.T) {
	if err := ValidateSecretFormats("stripe", "test", map[string]string{"webhook_secret": "whsec_x"}); err != nil {
		t.Errorf("a patch without api_key was refused: %v", err)
	}
	if err := ValidateSecretFormats("stripe", "test", map[string]string{"api_key": ""}); err != nil {
		t.Errorf("clearing a secret was refused: %v", err)
	}
}

// Guessing at a format we have not verified would reject working
// credentials — a worse failure than the one this file prevents.
func TestValidateSecretFormats_LeavesUnknownProvidersAlone(t *testing.T) {
	for _, provider := range []string{"allpay", "cloudpayments", "yookassa", "manual"} {
		if err := ValidateSecretFormats(provider, "live", map[string]string{"secret_key": "anything at all"}); err != nil {
			t.Errorf("%s: an unmodelled provider must accept its own credential shapes, got %v", provider, err)
		}
	}
	// And an unknown FIELD of a modelled provider, for the same reason.
	if err := ValidateSecretFormats("stripe", "live", map[string]string{"some_future_field": "xyz"}); err != nil {
		t.Errorf("an unmodelled field was refused: %v", err)
	}
}

// A key copied out of a web page or an e-mail routinely carries a trailing
// newline. Stored verbatim it becomes a 401 nobody can explain.
func TestNormalizeSecretValue_DropsPastedWhitespace(t *testing.T) {
	got := NormalizeSecretValue("  sk_test_51ABC\n")
	if got != "sk_test_51ABC" {
		t.Errorf("NormalizeSecretValue = %q, want the trimmed key", got)
	}
	if err := ValidateSecretFormats("stripe", "test", normalizeSecretPatch(map[string]string{
		"api_key": "\tsk_test_51ABC ",
	})); err != nil {
		t.Errorf("a key with pasted whitespace was refused after normalization: %v", err)
	}
}

// The distinction the badge depends on: only a refusal is a verdict on the
// KEY. Recording an unreachable provider as "failed" would teach operators
// to distrust a red state that is really about our own connectivity.
func TestClassifyVerification_SeparatesRefusalFromUnreachable(t *testing.T) {
	if status, detail := classifyVerification(nil); status != VerificationOK || detail != "" {
		t.Errorf("accepted credential => %q/%q, want ok with no detail", status, detail)
	}

	status, detail := classifyVerification(wrapErr(payments.ErrCredentialRefused, "Invalid API Key provided: sk_test_****abcd"))
	if status != VerificationFailed {
		t.Errorf("a refusal => %q, want failed", status)
	}
	if !strings.Contains(detail, "Invalid API Key provided") {
		t.Errorf("the provider's own wording must survive: %q", detail)
	}

	status, detail = classifyVerification(wrapErr(payments.ErrProviderUnreachable, "dial tcp: i/o timeout"))
	if status != VerificationUnverified {
		t.Errorf("an unreachable provider => %q, want unverified — it says nothing about the key", status)
	}
	if !strings.Contains(detail, "timeout") {
		t.Errorf("the reason we could not check must be kept: %q", detail)
	}
}

func TestClassifyVerification_TruncatesARunawayMessage(t *testing.T) {
	_, detail := classifyVerification(wrapErr(payments.ErrCredentialRefused, strings.Repeat("x", 5000)))
	if len(detail) > verificationErrorLimit+4 {
		t.Errorf("stored detail is %d bytes; the column must not take an unbounded body", len(detail))
	}
}

// A row from before migration 0107, or one a test builds by hand, carries an
// empty string. A UI must never be handed a blank it might render as a pass.
func TestPaymentConfigFromRow_EmptyVerificationReadsAsUnverified(t *testing.T) {
	resp := PaymentConfigFromRow(gen.PaymentProviderConfigRow{
		Provider:     "stripe",
		Mode:         "test",
		Secrets:      json.RawMessage(`{"api_key":"sk_test_x","webhook_secret":"whsec_x"}`),
		Status:       "configured",
		PublicConfig: json.RawMessage(`{}`),
	})
	if resp.VerificationStatus != VerificationUnverified {
		t.Errorf("verification_status = %q, want %q", resp.VerificationStatus, VerificationUnverified)
	}
	if resp.VerifiedAt != nil {
		t.Errorf("verified_at = %v, want null for a credential nobody has checked", *resp.VerifiedAt)
	}
	// And the response still must not leak the credential itself.
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "sk_test_x") {
		t.Fatalf("the api_key reached the response body: %s", body)
	}
}

func wrapErr(sentinel error, detail string) error {
	return errJoin{sentinel: sentinel, detail: detail}
}

type errJoin struct {
	sentinel error
	detail   string
}

func (e errJoin) Error() string { return e.sentinel.Error() + ": " + e.detail }
func (e errJoin) Unwrap() error { return e.sentinel }
