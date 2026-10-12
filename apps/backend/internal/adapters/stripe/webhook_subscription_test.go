package stripe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckWebhookSubscription(t *testing.T) {
	const cb = "https://api.example.test/v1/payment-intents/webhook/cfg-1"
	cases := []struct {
		name          string
		body          string
		found, refund bool
	}{
		{"no endpoints", `{"data":[]}`, false, false},
		{"other url only", `{"data":[{"url":"https://other.test/x","status":"enabled","enabled_events":["*"]}]}`, false, false},
		{"checkout events only", `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["checkout.session.completed"]}]}`, true, false},
		{"refund.updated only", `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["checkout.session.completed","refund.updated"]}]}`, true, false},
		{"created and updated without failed", `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.created","refund.updated"]}]}`, true, false},
		{"every refund event", `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.created","refund.updated","refund.failed"]}]}`, true, true},
		{"the older update event", `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["refund.created","charge.refund.updated","refund.failed"]}]}`, true, true},
		{"all events", `{"data":[{"url":"` + cb + `","status":"enabled","enabled_events":["*"]}]}`, true, true},
		{"disabled endpoint", `{"data":[{"url":"` + cb + `","status":"disabled","enabled_events":["*"]}]}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.Method + " " + r.URL.Path
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			a := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL + "/v1"})
			got, err := a.CheckWebhookSubscription(context.Background(), cb)
			if err != nil {
				t.Fatal(err)
			}
			if path != "GET /v1/webhook_endpoints" {
				t.Fatalf("called %q; want a read-only list", path)
			}
			if got.EndpointFound != c.found || got.RefundEventsSubscribed != c.refund {
				t.Fatalf("got %+v; want found=%v refund=%v", got, c.found, c.refund)
			}
		})
	}
}

func TestCheckWebhookSubscription_KeyWithoutPermissionIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"permission"}}`))
	}))
	defer srv.Close()
	a := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL + "/v1"})
	if _, err := a.CheckWebhookSubscription(context.Background(), "u"); err == nil {
		t.Fatal("an unreadable endpoint list must be an error so the caller shows no warning")
	}
}
