package hpayments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestWebhookWarnings(t *testing.T) {
	id := uuid.New()
	cb := "https://api.example.test/v1/payment-intents/webhook/" + id.String()
	status, body := http.StatusOK, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	h := (&Handler{}).WithStripeBaseURL(srv.URL + "/v1").WithCallbackBase("https://api.example.test/")
	secrets, _ := json.Marshal(map[string]string{"api_key": "sk_test_x", "webhook_secret": "whsec_x"})
	row := gen.PaymentProviderConfigRow{ID: id, Provider: "stripe", Secrets: secrets, VerificationStatus: VerificationOK}

	cases := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"subscribed", 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.created","refund.updated","refund.failed"]}]}`, nil},
		{"refund.updated only", 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.updated"]}]}`, []string{WarningRefundEventsNotSubscribed}},
		{"not subscribed", 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["checkout.session.completed"]}]}`, []string{WarningRefundEventsNotSubscribed}},
		{"no endpoint", 200, `{"data":[]}`, []string{WarningWebhookEndpointNotFound}},
		{"key cannot list", 403, `{"error":{"message":"no"}}`, nil},
	}
	for _, c := range cases {
		status, body = c.status, c.body
		got := h.webhookWarnings(context.Background(), row)
		if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
			t.Errorf("%s: warnings = %v; want %v", c.name, got, c.want)
		}
	}
	// A credential the provider did not accept is not asked about endpoints.
	row.VerificationStatus = VerificationFailed
	status, body = 200, `{"data":[]}`
	if got := h.webhookWarnings(context.Background(), row); len(got) != 0 {
		t.Errorf("unverified credential produced %v", got)
	}
}
