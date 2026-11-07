package conversions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	mu       sync.Mutex
	settings Settings
	consent  bool
	order    OrderData
	due      []Delivery
	status   string
	reason   string
	retryAt  time.Time
}

func (f *fakeStore) Enqueue(context.Context, uuid.UUID) error { return nil }
func (f *fakeStore) ClaimDue(context.Context, int) ([]Delivery, error) {
	d := f.due
	f.due = nil
	return d, nil
}
func (f *fakeStore) Settings(context.Context, uuid.UUID) (Settings, bool, error) {
	return f.settings, true, nil
}
func (f *fakeStore) Order(context.Context, uuid.UUID) (OrderData, error) { return f.order, nil }
func (f *fakeStore) ConsentForOrder(context.Context, uuid.UUID) (bool, error) {
	return f.consent, nil
}
func (f *fakeStore) MarkSent(context.Context, uuid.UUID) error { f.status = "sent"; return nil }
func (f *fakeStore) MarkSkipped(_ context.Context, _ uuid.UUID, r string) error {
	f.status, f.reason = "skipped", r
	return nil
}
func (f *fakeStore) MarkRetry(_ context.Context, _ uuid.UUID, at time.Time, r string) error {
	f.status, f.reason, f.retryAt = "retry", r, at
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, _ uuid.UUID, r string) error {
	f.status, f.reason = "failed", r
	return nil
}

func newFake(provider string, consent bool) (*fakeStore, Delivery) {
	d := Delivery{ID: uuid.New(), OrderID: uuid.New(), OrgID: uuid.New(), Provider: provider, CreatedAt: time.Unix(1700000000, 0)}
	f := &fakeStore{
		settings: Settings{MetaPixelID: "123", MetaCAPIToken: "tok-secret", GA4MeasurementID: "G-X", GA4APISecret: "ga-secret"},
		consent:  consent,
		order: OrderData{Number: "1000000501", Total: 1890, Currency: "ILS", Email: "Buyer@Example.com", Phone: "+972 50-123",
			Context: BuyerContext{FBP: "fb.1.1.2", FBCLID: "abc", GAClientID: "111.222", ClientIP: "203.0.113.5", UserAgent: "UA/1"}},
		due: []Delivery{d},
	}
	return f, d
}

func TestMetaSender_PayloadHashedAndTokenInBody(t *testing.T) {
	var got map[string]any
	var path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	f, d := newFake("meta", true)
	_, err := RunOnce(context.Background(), f, Senders{Meta: MetaSender{BaseURL: srv.URL}}, slog.Default(), time.Now())
	if err != nil || f.status != "sent" {
		t.Fatalf("status=%s err=%v reason=%s", f.status, err, f.reason)
	}
	if path != "/123/events" || query != "" {
		t.Fatalf("path=%q query=%q (token must not be in the URL)", path, query)
	}
	if got["access_token"] != "tok-secret" {
		t.Fatalf("token missing from body")
	}
	ev := got["data"].([]any)[0].(map[string]any)
	if ev["event_name"] != "Purchase" || ev["event_id"] != d.OrderID.String() {
		t.Fatalf("event: %v", ev)
	}
	ud := ev["user_data"].(map[string]any)
	em := ud["em"].([]any)[0].(string)
	if len(em) != 64 || strings.Contains(em, "@") {
		t.Fatalf("email not hashed: %q", em)
	}
	if ud["fbc"] == nil || !strings.HasPrefix(ud["fbc"].(string), "fb.1.") || ud["client_ip_address"] != "203.0.113.5" {
		t.Fatalf("user_data: %v", ud)
	}
	cd := ev["custom_data"].(map[string]any)
	if cd["value"].(float64) != 18.9 || cd["currency"] != "ILS" {
		t.Fatalf("custom_data: %v", cd)
	}
}

func TestGA4Sender_PurchaseMajorUnits(t *testing.T) {
	var got map[string]any
	var path string
	var q map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, q = r.URL.Path, r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	f, _ := newFake("ga4", true)
	if _, err := RunOnce(context.Background(), f, Senders{GA4: GA4Sender{BaseURL: srv.URL}}, slog.Default(), time.Now()); err != nil || f.status != "sent" {
		t.Fatalf("status=%s err=%v", f.status, err)
	}
	if path != "/mp/collect" || q["measurement_id"][0] != "G-X" || q["api_secret"][0] != "ga-secret" {
		t.Fatalf("path=%s q=%v", path, q)
	}
	if got["client_id"] != "111.222" {
		t.Fatalf("client_id: %v", got["client_id"])
	}
	ev := got["events"].([]any)[0].(map[string]any)
	p := ev["params"].(map[string]any)
	if ev["name"] != "purchase" || p["transaction_id"] != "1000000501" || p["value"].(float64) != 18.9 {
		t.Fatalf("event: %v", ev)
	}
}

func TestNoConsentSendsNothing(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hit = true }))
	defer srv.Close()
	f, _ := newFake("meta", false)
	_, _ = RunOnce(context.Background(), f, Senders{Meta: MetaSender{BaseURL: srv.URL}}, slog.Default(), time.Now())
	if hit || f.status != "skipped" || f.reason != "no_consent" {
		t.Fatalf("hit=%v status=%s reason=%s", hit, f.status, f.reason)
	}
}

func TestRetryThenPermanent(t *testing.T) {
	code := 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
	defer srv.Close()
	f, _ := newFake("ga4", true)
	now := time.Now()
	_, _ = RunOnce(context.Background(), f, Senders{GA4: GA4Sender{BaseURL: srv.URL}}, slog.Default(), now)
	if f.status != "retry" || !f.retryAt.After(now) || f.reason != "http 500" {
		t.Fatalf("status=%s reason=%s", f.status, f.reason)
	}
	code = 400
	f2, _ := newFake("ga4", true)
	_, _ = RunOnce(context.Background(), f2, Senders{GA4: GA4Sender{BaseURL: srv.URL}}, slog.Default(), now)
	if f2.status != "failed" {
		t.Fatalf("4xx should be permanent, got %s", f2.status)
	}
	// A transport error must not leak the api_secret held in the URL.
	f3, _ := newFake("ga4", true)
	_, _ = RunOnce(context.Background(), f3, Senders{GA4: GA4Sender{BaseURL: "http://127.0.0.1:1"}}, slog.Default(), now)
	if strings.Contains(f3.reason, "ga-secret") || f3.status != "retry" {
		t.Fatalf("status=%s reason=%q", f3.status, f3.reason)
	}
}

func TestTestOrderMetaNeedsTestEventCode(t *testing.T) {
	f, _ := newFake("meta", true)
	f.order.IsTest = true
	_, _ = RunOnce(context.Background(), f, Senders{}, slog.Default(), time.Now())
	if f.status != "skipped" {
		t.Fatalf("status=%s", f.status)
	}
}

func TestCleanBuyerContext(t *testing.T) {
	raw := json.RawMessage(`{"fbp":"fb.1.2.3","fbclid":"` + strings.Repeat("x", 300) + `","tracking_consent":true,"ga_client_id":"1.2","extra":"ignored"}`)
	out := CleanBuyerContext(raw, "198.51.100.1", "Mozilla")
	var bc BuyerContext
	if err := json.Unmarshal(out, &bc); err != nil {
		t.Fatal(err)
	}
	if bc.FBP != "fb.1.2.3" || bc.FBCLID != "" || bc.ClientIP != "198.51.100.1" || bc.UserAgent != "Mozilla" {
		t.Fatalf("%+v", bc)
	}
	// The consent is never kept in the context row: it lives on
	// checkout_sessions (package trackingconsent).
	if strings.Contains(string(out), "tracking_consent") {
		t.Fatalf("the stored context carries the consent: %s", out)
	}
	if CleanBuyerContext(nil, "1.1.1.1", "x") != nil || CleanBuyerContext(json.RawMessage(`[1]`), "1.1.1.1", "x") != nil {
		t.Fatal("empty or non-object must be nil")
	}
}

func TestConsentFromBuyerContext(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		nilWant bool
		granted bool
		version string
	}{
		{`{"tracking_consent":true,"tracking_consent_text_version":"2026-11"}`, false, true, "2026-11"},
		{`{"tracking_consent":false,"tracking_consent_text_version":"2026-11"}`, false, false, "2026-11"},
		{`{"tracking_consent":false}`, false, false, UnstatedTextVersion},
		{`{"tracking_consent":true}`, true, false, ""}, // a yes needs its text version
		{`{"fbp":"fb.1.2.3"}`, true, false, ""},
		{`{"tracking_consent":"yes","tracking_consent_text_version":"v"}`, true, false, ""},
		{`[1]`, true, false, ""},
		{``, true, false, ""},
	} {
		got := ConsentFromBuyerContext(json.RawMessage(tc.raw))
		if tc.nilWant {
			if got != nil {
				t.Errorf("%s: want nil, got %+v", tc.raw, got)
			}
			continue
		}
		if got == nil || got.Granted == nil || *got.Granted != tc.granted || got.Version != tc.version {
			t.Errorf("%s: got %+v", tc.raw, got)
		}
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	if Backoff(0) != 30*time.Second || Backoff(1) != time.Minute || Backoff(30) != 6*time.Hour {
		t.Fatalf("%v %v %v", Backoff(0), Backoff(1), Backoff(30))
	}
}
