package hpayments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestWebhookWarnings(t *testing.T) {
	id := uuid.New()
	cb := "https://api.example.test/v1/payment-intents/webhook/" + id.String()
	status, body := http.StatusOK, ""
	disputeStatus := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/disputes") {
			w.WriteHeader(disputeStatus)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	h := (&Handler{}).WithStripeBaseURL(srv.URL + "/v1").WithCallbackBase("https://api.example.test/")
	secrets, _ := json.Marshal(map[string]string{"api_key": "sk_test_x", "webhook_secret": "whsec_x"})
	row := gen.PaymentProviderConfigRow{ID: id, Provider: "stripe", Secrets: secrets, VerificationStatus: VerificationOK}

	const disputeEvents = `,"charge.dispute.created","charge.dispute.updated","charge.dispute.closed"`
	cases := []struct {
		name          string
		status        int
		disputeStatus int
		body          string
		want          []string
	}{
		{"subscribed", 200, 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.created","refund.updated","refund.failed"` + disputeEvents + `]}]}`, nil},
		{"all events", 200, 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["*"]}]}`, nil},
		{"refund.updated only", 200, 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.updated"` + disputeEvents + `]}]}`, []string{WarningRefundEventsNotSubscribed}},
		{"no dispute events", 200, 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.created","refund.updated","refund.failed"]}]}`, []string{WarningDisputeEventsNotSubscribed}},
		{"not subscribed", 200, 200, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["checkout.session.completed"]}]}`, []string{WarningRefundEventsNotSubscribed, WarningDisputeEventsNotSubscribed}},
		{"no endpoint", 200, 200, `{"data":[]}`, []string{WarningWebhookEndpointNotFound}},
		{"key cannot list", 403, 200, `{"error":{"message":"no"}}`, nil},
		{"key cannot read disputes", 200, 403, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["*"]}]}`, []string{WarningDisputeAccessMissing}},
		{"disputes unreachable", 200, 500, `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["*"]}]}`, nil},
	}
	for _, c := range cases {
		status, body, disputeStatus = c.status, c.body, c.disputeStatus
		got := h.webhookWarnings(context.Background(), row)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
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
