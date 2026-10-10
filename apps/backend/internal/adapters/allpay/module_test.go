// module_test.go — AllPay against the payment-module contract (PAY-01,
// domain/payments/contracttest). AllPay declares no hosted checkout and no
// refund yet (PAY-14), so only the descriptor and webhook parts apply.
package allpay_test

import (
	"net/http"
	"testing"

	allpayadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/allpay"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments/contracttest"
)

func TestAllPayModule_Contract(t *testing.T) {
	const secret = "allpay-contract-secret"
	module, err := allpayadapter.Entry().Build(map[string]string{
		allpayadapter.SecretMerchantID: "m-1",
		allpayadapter.SecretKey:        secret,
	}, payments.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	body := []byte(`{"event_type":"payment.captured","payment_id":"ap_1","status":"captured"}`)
	signed := func(key string) http.Header {
		h := http.Header{}
		h.Set("X-AllPay-Signature", payments.ComputeHMACSHA256(key, body))
		return h
	}
	contracttest.Run(t, contracttest.Case{
		Module:              module,
		GoodWebhook:         payments.WebhookInput{Body: body, Header: signed(secret)},
		BadSignatureWebhook: payments.WebhookInput{Body: body, Header: signed("someone-else")},
		WantEvent: payments.NormalizedEvent{
			Kind:              payments.EventPaymentSucceeded,
			Type:              "payment.captured",
			ProviderPaymentID: "ap_1",
		},
	})
}
