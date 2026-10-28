//go:build paypluslive

// live_test.go is the manual probe of PayPlus's STAGING API
// (restapidev.payplus.co.il) with real test keys, for the checks listed in
// docs/ops/payplus.md §5. It never runs in CI: it needs the build tag
// `paypluslive` AND the keys in the environment, which are never printed.
//
//	PAYPLUS_TEST_API_KEY=... PAYPLUS_TEST_SECRET_KEY=... PAYPLUS_TEST_PAGE_UID=... \
//	  go test -tags paypluslive -run TestPayPlusLive -v ./apps/backend/internal/adapters/payplus/
//
// It creates one unpaid payment link (no money moves) and logs the raw
// answers of the read-only calls, so their shapes can be compared with what
// the adapter parses.
package payplus

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

type loggingTransport struct {
	t    *testing.T
	next http.RoundTripper
}

func (l loggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := l.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	l.t.Logf("%s %s → HTTP %d: %s", r.Method, r.URL.Path, resp.StatusCode, truncate(string(raw), 1500))
	return resp, nil
}

func TestPayPlusLive(t *testing.T) {
	apiKey, secret, page := os.Getenv("PAYPLUS_TEST_API_KEY"), os.Getenv("PAYPLUS_TEST_SECRET_KEY"), os.Getenv("PAYPLUS_TEST_PAGE_UID")
	if apiKey == "" || secret == "" || page == "" {
		t.Skip("PAYPLUS_TEST_API_KEY, PAYPLUS_TEST_SECRET_KEY and PAYPLUS_TEST_PAGE_UID are required")
	}
	a := New(Config{APIKey: apiKey, SecretKey: secret, PaymentPageUID: page, BaseURL: StagingBaseURL})
	a.client.Transport = loggingTransport{t: t, next: http.DefaultTransport}

	t.Run("credentials", func(t *testing.T) {
		if err := a.VerifyCredentials(t.Context()); err != nil {
			t.Errorf("good keys: %v", err)
		}
		bad := New(Config{APIKey: apiKey, SecretKey: secret + "x", BaseURL: StagingBaseURL})
		bad.client.Transport = a.client.Transport
		t.Logf("wrong secret: %v", bad.VerifyCredentials(t.Context()))
	})

	var link *payments.CreateHostedCheckoutResponse
	t.Run("generateLink", func(t *testing.T) {
		var err error
		link, err = a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{
			Amount: 1050, Currency: "ILS", ProductName: "arena probe", CustomerEmail: "probe@example.test",
			ClientReferenceID: "probe-" + uuid.NewString(), SuccessURL: "https://example.test/ok", CancelURL: "https://example.test/back",
			ExpiresAtUnix: time.Now().Add(31 * time.Minute).Unix(), Locale: "ru",
		})
		if err != nil {
			t.Fatalf("generateLink: %v", err)
		}
		t.Logf("pay on %s with 5326-1402-8077-9844 05/30 000, then re-run the ipn step", link.URL)
	})

	t.Run("ipn and view", func(t *testing.T) {
		if link != nil {
			txs, err := a.PaymentRequestTransactions(t.Context(), link.SessionID)
			t.Logf("ipn of the new link: %+v, %v", txs, err)
		}
		txs, err := a.ViewByMoreInfo(t.Context(), RefundMarker(uuid.NewString()))
		t.Logf("view by an unknown more_info: %+v, %v", txs, err)
	})
}
