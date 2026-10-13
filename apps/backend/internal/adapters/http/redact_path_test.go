package http

import "testing"

func TestRedactPath_HidesTheBrevoWebhookToken(t *testing.T) {
	cases := map[string]string{
		"/v1/webhooks/brevo/s3cr3t-token-value":  "/v1/webhooks/brevo/{token}",
		"/v1/webhooks/brevo/s3cr3t/extra":        "/v1/webhooks/brevo/{token}",
		"/v1/webhooks/subscribers/123":           "/v1/webhooks/subscribers/123",
		"/v1/organizations/abc/orders":           "/v1/organizations/abc/orders",
		"/v1/payment-intents/webhook/config-id1": "/v1/payment-intents/webhook/config-id1",
	}
	for in, want := range cases {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
	}
}
