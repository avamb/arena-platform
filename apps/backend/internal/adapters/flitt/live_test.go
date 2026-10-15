//go:build flittlive

// live_test.go is the manual probe of Flitt's public sandbox (spec
// 08_architecture/29 section 4.2). It talks to the real pay.flitt.com, so it
// never runs in CI: it needs the build tag `flittlive` AND the credentials in
// the environment. They are read from the environment only and never printed.
//
//	FLITT_TEST_MERCHANT=... FLITT_TEST_KEY=... \
//	  go test -tags flittlive -run TestFlittLive -v ./apps/backend/internal/adapters/flitt/
//
// The documented sandbox merchant moves no money (spec 29 section 2). The
// probe creates two orders with throwaway ids and never pays them.
package flitt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// recordingTransport remembers the status code, the request's parameter names
// and Flitt's raw answer, so the probe can report exactly what crossed the wire.
type recordingTransport struct {
	next   http.RoundTripper
	status int
	sent   map[string]any
	answer string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(b))
		var env struct {
			Request map[string]any `json:"request"`
		}
		_ = json.Unmarshal(b, &env)
		r.sent = env.Request
	}
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	r.status = resp.StatusCode
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	r.answer = string(b)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, nil
}

func liveProbeAdapter(t *testing.T) (*Adapter, *recordingTransport) {
	t.Helper()
	merchant := strings.TrimSpace(os.Getenv("FLITT_TEST_MERCHANT"))
	key := strings.TrimSpace(os.Getenv("FLITT_TEST_KEY"))
	if merchant == "" || key == "" {
		t.Skip("FLITT_TEST_MERCHANT and FLITT_TEST_KEY are not set")
	}
	a := New(Config{MerchantID: merchant, PaymentKey: key})
	rt := &recordingTransport{next: http.DefaultTransport}
	a.client = &http.Client{Timeout: 30 * time.Second, Transport: rt}
	return a, rt
}

// probe runs one checkout/url call and logs everything but the keys.
func probe(t *testing.T, label string, req payments.CreateHostedCheckoutRequest) {
	t.Helper()
	a, rt := liveProbeAdapter(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	resp, err := a.CreateCheckoutSession(ctx, req)

	names := make([]string, 0, len(rt.sent))
	for k := range rt.sent {
		if k == "signature" || k == "merchant_id" {
			continue
		}
		names = append(names, k)
	}
	t.Logf("[%s] request parameters (besides merchant_id and signature): %v", label, names)
	if rd, ok := rt.sent["reservation_data"].(string); ok {
		raw, _ := base64.StdEncoding.DecodeString(rd)
		t.Logf("[%s] reservation_data decoded: %s", label, raw)
	} else {
		t.Logf("[%s] reservation_data: not sent", label)
	}
	t.Logf("[%s] HTTP status: %d", label, rt.status)

	var out apiResponse
	if jsonErr := json.Unmarshal([]byte(rt.answer), &out); jsonErr == nil {
		t.Logf("[%s] response_status=%q error_code=%q error_message=%q checkout_url_returned=%v",
			label, out.Response.ResponseStatus, string(out.Response.ErrorCode),
			out.Response.ErrorMessage, out.Response.CheckoutURL != "")
	} else {
		t.Logf("[%s] unreadable answer: %.200s", label, rt.answer)
	}
	if err != nil {
		t.Errorf("[%s] CreateCheckoutSession: %v", label, err)
		return
	}
	if resp.URL == "" {
		t.Errorf("[%s] no checkout URL", label)
	}
}

func probeRequest(withBuyer bool) payments.CreateHostedCheckoutRequest {
	req := payments.CreateHostedCheckoutRequest{
		Amount:            1000, // 10.00 in minor units
		Currency:          "EUR",
		ProductName:       "arena probe P1d",
		CustomerEmail:     "probe@example.com",
		ClientReferenceID: "arena-p1d-" + uuid.NewString(),
		SuccessURL:        "https://example.com/ok",
		CancelURL:         "https://example.com/cancel",
		Locale:            "en",
	}
	if withBuyer {
		req.CustomerName = "Maria Garcia"
		req.CustomerPhone = "+34 600 111 222"
	}
	return req
}

func TestFlittLive_ReservationDataProbe(t *testing.T) {
	t.Run("with_reservation_data", func(t *testing.T) { probe(t, "WITH", probeRequest(true)) })
	t.Run("without_reservation_data", func(t *testing.T) { probe(t, "WITHOUT", probeRequest(false)) })
}
