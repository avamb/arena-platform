package hcheckout

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// The legacy webhook route parses a Stripe envelope with its own struct
// (stripeEventEnvelope) and the Stripe module normalizes the same bytes with
// another (PAY-01 review M1, item 3). Folding them into one would touch the
// path every paid purchase takes, so until that is done this test pins the
// two to the same answer on the events arena handles: the payment id, the
// charge reference, the event type, and whether a checkout.session.completed
// counts as money received.
func TestStripeEnvelopeParsers_AgreeOnEveryHandledEvent(t *testing.T) {
	const secret = "whsec_parity"
	module, err := stripe.Entry().Build(map[string]string{
		stripe.SecretAPIKey: "sk_test_parity", stripe.SecretWebhookSecret: secret,
	}, payments.Options{})
	if err != nil {
		t.Fatal(err)
	}
	parser := module.(payments.WebhookParser)

	bodies := map[string]string{
		"payment_intent.succeeded":    `{"id":"evt_1","type":"payment_intent.succeeded","data":{"object":{"id":"pi_1","status":"succeeded","amount":1890,"currency":"eur"}}}`,
		"payment_intent.failed":       `{"id":"evt_2","type":"payment_intent.payment_failed","data":{"object":{"id":"pi_2","last_payment_error":{"code":"card_declined","message":"declined"}}}}`,
		"session paid":                `{"id":"evt_3","type":"checkout.session.completed","data":{"object":{"id":"cs_3","payment_status":"paid","payment_intent":"pi_3","amount_total":500,"currency":"eur"}}}`,
		"session unpaid":              `{"id":"evt_4","type":"checkout.session.completed","data":{"object":{"id":"cs_4","payment_status":"unpaid","payment_intent":"pi_4"}}}`,
		"session no payment required": `{"id":"evt_5","type":"checkout.session.completed","data":{"object":{"id":"cs_5","payment_status":"no_payment_required"}}}`,
		"session async succeeded":     `{"id":"evt_6","type":"checkout.session.async_payment_succeeded","data":{"object":{"id":"cs_6","payment_status":"paid","payment_intent":"pi_6"}}}`,
		"session expired":             `{"id":"evt_7","type":"checkout.session.expired","data":{"object":{"id":"cs_7"}}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			core, err := parseWebhookPaymentIntentRequest([]byte(body))
			if err != nil {
				t.Fatalf("core parser: %v", err)
			}
			ts := time.Now().Unix()
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(strconv.FormatInt(ts, 10) + "." + body))
			h := http.Header{}
			h.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil))))
			ev, err := parser.VerifyAndParse(t.Context(), payments.WebhookInput{Body: []byte(body), Header: h})
			if err != nil {
				t.Fatalf("module parser: %v", err)
			}
			if core.ProviderPaymentID != ev.ProviderPaymentID || core.EventType != ev.Type || core.HostedPaymentID != ev.ProviderChargeRef {
				t.Errorf("core (payment=%q type=%q charge=%q) != module (payment=%q type=%q charge=%q)",
					core.ProviderPaymentID, core.EventType, core.HostedPaymentID, ev.ProviderPaymentID, ev.Type, ev.ProviderChargeRef)
			}
			// "money received" must mean the same thing in both.
			coreMoney := core.EventType == "checkout.session.completed" && core.PaymentStatus == "paid"
			moduleMoney := ev.Kind == payments.EventPaymentSucceeded
			if core.EventType == "checkout.session.completed" && coreMoney != moduleMoney {
				t.Errorf("session completed: core says money=%v, module says money=%v", coreMoney, moduleMoney)
			}
			if core.FailureCode != nil && *core.FailureCode != ev.FailureCode {
				t.Errorf("failure code core=%q module=%q", *core.FailureCode, ev.FailureCode)
			}
		})
	}
}
