package payplus

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

const (
	testAPIKey  = "11111111-2222-4333-8444-555555555555"
	testSecret  = "secret-key-of-the-merchant"
	testPageUID = "7a0bc4d4-f35f-4301-a945-926378a2416d"
)

func TestMoney_MinorToMajor(t *testing.T) {
	for minor, want := range map[int64]string{
		0: "0", 1: "0.01", 10: "0.1", 29: "0.29", 100: "1", 1250: "12.5", 1205: "12.05", 189000: "1890", -1250: "-12.5",
	} {
		if got := MinorToMajor(minor); got != want {
			t.Errorf("MinorToMajor(%d) = %q; want %q", minor, got, want)
		}
	}
}

func TestMoney_MajorToMinor(t *testing.T) {
	for in, want := range map[string]int64{
		"0": 0, "1": 100, "0.29": 29, "12.5": 1250, "12.05": 1205, "12.50": 1250, "1890": 189000,
		"1e1": 1000, "12.049999999999999": 1205, ".5": 50, "-12.5": -1250, " 7 ": 700,
	} {
		got, ok := MajorToMinor(in)
		if !ok || got != want {
			t.Errorf("MajorToMinor(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "1,5", "--1"} {
		if _, ok := MajorToMinor(bad); ok {
			t.Errorf("MajorToMinor(%q) accepted", bad)
		}
	}
	// Round trip over every agora of the first hundred shekels.
	for m := int64(0); m < 10_000; m++ {
		if got, ok := MajorToMinor(MinorToMajor(m)); !ok || got != m {
			t.Fatalf("round trip %d → %q → %d", m, MinorToMajor(m), got)
		}
	}
}

func TestBaseURLFor(t *testing.T) {
	if got := BaseURLFor("test", ""); got != StagingBaseURL {
		t.Errorf("test mode = %q; want the staging host", got)
	}
	if got := BaseURLFor("live", ""); got != ProductionBaseURL {
		t.Errorf("live mode = %q; want the production host", got)
	}
	if got := BaseURLFor("", ""); got != ProductionBaseURL {
		t.Errorf("unknown mode = %q; want production (a live key never works on staging)", got)
	}
	if got := BaseURLFor("live", "http://stub:1/api/v1.0/"); got != "http://stub:1/api/v1.0" {
		t.Errorf("override = %q; want it for every mode, without the trailing slash", got)
	}
}

// ─── signature ──────────────────────────────────────────────────────────────

// The documented callback example, as PayPlus's Node service would send it:
// JSON.stringify output, Hebrew literal, numbers as JS prints them.
const callbackBody = `{"transaction_type":"Charge","transaction":{"uid":"dcb11c1e7-a1231-37cf-6311-f5111eeb69c7",` +
	`"payment_request_uid":"ef76432c-769a-43a6-ba7a-6f70272539d8","number":"fd138","type":"internal_page",` +
	`"date":"2021-01-21 12:32:52","status_code":"000","amount":12.5,"currency":"ILS","credit_terms":"regular",` +
	`"approval_number":"002341","voucher_number":"15-901-901","more_info":"6f1c0f6e-0000-4000-8000-000000000001"},` +
	`"data":{"customer_uid":"3bcc6b2d","items":[{"name":"מנוי שנתי","quantity":1,"quantity_price":12.5}],` +
	`"card_information":{"four_digits":"9844","card_holder_name":"Moshe Cohen"}},"invoice":{"status":"Success"}}`

func TestVerifyHash_RawBytes(t *testing.T) {
	body := []byte(callbackBody)
	v, err := VerifyHash(body, Sign(testSecret, body), testSecret)
	if err != nil || v != VariantRaw {
		t.Fatalf("VerifyHash = %q, %v; want raw", v, err)
	}
}

func TestVerifyHash_RefusesWrongKeyTamperingAndEmpty(t *testing.T) {
	body := []byte(callbackBody)
	good := Sign(testSecret, body)
	if _, err := VerifyHash(body, Sign("another-key", body), testSecret); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Errorf("wrong key = %v; want ErrInvalidWebhookSignature", err)
	}
	tampered := bytes.Replace(body, []byte(`"amount":12.5`), []byte(`"amount":1.25`), 1)
	if _, err := VerifyHash(tampered, good, testSecret); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Errorf("tampered amount = %v; want ErrInvalidWebhookSignature", err)
	}
	if _, err := VerifyHash(body, "", testSecret); err == nil {
		t.Error("an empty hash header verified")
	}
	if _, err := VerifyHash(body, good, ""); err == nil {
		t.Error("an empty secret key verified")
	}
	if _, err := VerifyHash(body, "%%%not-base64", testSecret); err == nil {
		t.Error("a non-base64 hash verified")
	}
}

// A pretty-printed delivery whose hash covers the compact form (what the
// documented sample hashes when the sender pretty-prints).
func TestVerifyHash_FallsBackToCompact(t *testing.T) {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(callbackBody), "", "  "); err != nil {
		t.Fatal(err)
	}
	v, err := VerifyHash(pretty.Bytes(), Sign(testSecret, []byte(callbackBody)), testSecret)
	if err != nil || v != VariantCompact {
		t.Fatalf("VerifyHash(pretty) = %q, %v; want compact", v, err)
	}
}

// A delivery whose bytes escape what JSON.stringify writes literally (a PHP
// or Go serializer: מ for Hebrew, \/ for slash, a trailing zero), while
// the hash covers JSON.stringify(JSON.parse(body)).
func TestVerifyHash_FallsBackToJSStringify(t *testing.T) {
	wire := []byte(`{"transaction_type":"Charge","transaction":{"uid":"u1","status_code":"000","amount":12.50,` +
		`"more_info":"a\/b","note":"מנוי <&>"}}`)
	signed := []byte(`{"transaction_type":"Charge","transaction":{"uid":"u1","status_code":"000","amount":12.5,` +
		`"more_info":"a/b","note":"מנוי <&>"}}`)
	js, err := JSStringify(wire)
	if err != nil || !bytes.Equal(js, signed) {
		t.Fatalf("JSStringify = %s, %v; want %s", js, err, signed)
	}
	v, err := VerifyHash(wire, Sign(testSecret, signed), testSecret)
	if err != nil || v != VariantJSStringify {
		t.Fatalf("VerifyHash = %q, %v; want js_stringify", v, err)
	}
}

func TestJSStringify_KeepsKeyOrderAndJSFormatting(t *testing.T) {
	cases := map[string]string{
		`{"b":1,"a":2}`:                       `{"b":1,"a":2}`,
		`{ "x" : [ 1.0 , 2.50, 1e2, -0.0 ] }`: `{"x":[1,2.5,100,0]}`,
		`{"s":"tab\there\u0001"}`:             `{"s":"tab\there\u0001"}`,
		`{"n":null,"t":true,"f":false}`:       `{"n":null,"t":true,"f":false}`,
		`{"a":1,"a":2}`:                       `{"a":2}`,
		`{"big":1e21,"small":0.0000001}`:      `{"big":1e+21,"small":1e-7}`,
	}
	for in, want := range cases {
		got, err := JSStringify([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("JSStringify(%s) = %s, %v; want %s", in, got, err, want)
		}
	}
	if _, err := JSStringify([]byte(`{"a":1} trailing`)); err == nil {
		t.Error("trailing data accepted")
	}
}

// ─── callback normalization ─────────────────────────────────────────────────

func TestNormalizeCallback_ApprovedCharge(t *testing.T) {
	cb, err := ParseCallback([]byte(callbackBody))
	if err != nil {
		t.Fatal(err)
	}
	ev := NormalizeCallback(cb, []byte(callbackBody))
	if ev.Kind != payments.EventPaymentSucceeded || ev.Type != EventTypeApproved {
		t.Fatalf("kind/type = %s/%s; want succeeded/%s", ev.Kind, ev.Type, EventTypeApproved)
	}
	if ev.ProviderPaymentID != "ef76432c-769a-43a6-ba7a-6f70272539d8" {
		t.Errorf("ProviderPaymentID = %q; want the payment_request_uid", ev.ProviderPaymentID)
	}
	if ev.ProviderChargeRef != "dcb11c1e7-a1231-37cf-6311-f5111eeb69c7" || ev.ProviderEventID != ev.ProviderChargeRef {
		t.Errorf("charge ref / event id = %q / %q; want transaction.uid", ev.ProviderChargeRef, ev.ProviderEventID)
	}
	if ev.AmountMinor != 1250 || ev.Currency != "ILS" {
		t.Errorf("amount = %d %s; want 1250 ILS (major 12.5 converted once)", ev.AmountMinor, ev.Currency)
	}
}

// A callback naming the link under payment_page_request_uid still finds it.
func TestNormalizeCallback_PaymentPageRequestUIDAlias(t *testing.T) {
	body := strings.Replace(callbackBody, `"payment_request_uid":`, `"payment_page_request_uid":`, 1)
	if !strings.Contains(body, `"payment_page_request_uid":"ef76432c`) {
		t.Fatal("fixture did not take the alias")
	}
	cb, err := ParseCallback([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ev := NormalizeCallback(cb, []byte(body))
	if ev.Kind != payments.EventPaymentSucceeded || ev.ProviderPaymentID != "ef76432c-769a-43a6-ba7a-6f70272539d8" {
		t.Fatalf("alias callback = %s %q; want succeeded for the link", ev.Kind, ev.ProviderPaymentID)
	}
}

func TestNormalizeCallback_DeclinedAttemptIsNotAFailure(t *testing.T) {
	body := strings.Replace(callbackBody, `"status_code":"000"`, `"status_code":"003","status_description":"call the issuer"`, 1)
	cb, _ := ParseCallback([]byte(body))
	ev := NormalizeCallback(cb, []byte(body))
	if ev.Kind != payments.EventPaymentDeclined || ev.Type != EventTypeDeclined {
		t.Fatalf("kind = %s; a declined card must leave the link open (EventPaymentDeclined)", ev.Kind)
	}
	if ev.FailureCode != "003" || ev.FailureMessage != "call the issuer" {
		t.Errorf("failure = %q %q", ev.FailureCode, ev.FailureMessage)
	}
}

func TestNormalizeCallback_CancelledChargeIsNotAPayment(t *testing.T) {
	body := strings.Replace(callbackBody, `"status_code":"000"`, `"status_code":"000","transaction_is_cancelled":true`, 1)
	cb, _ := ParseCallback([]byte(body))
	if ev := NormalizeCallback(cb, []byte(body)); ev.Kind != payments.EventUnknown {
		t.Fatalf("cancelled charge = %s; want unknown, it must not pay the order", ev.Kind)
	}
}

func TestNormalizeCallback_RefundIsNeverAPayment(t *testing.T) {
	body := strings.Replace(callbackBody, `"transaction_type":"Charge"`, `"transaction_type":"Refund"`, 1)
	cb, _ := ParseCallback([]byte(body))
	ev := NormalizeCallback(cb, []byte(body))
	if ev.Kind == payments.EventPaymentSucceeded || ev.Kind != payments.EventUnknown || ev.Type != EventTypeRefund {
		t.Fatalf("refund callback = %s/%s; want unknown/%s", ev.Kind, ev.Type, EventTypeRefund)
	}
}

func TestNormalizeCallback_OtherKindsAndNoPageMoveNothing(t *testing.T) {
	approval := strings.Replace(callbackBody, `"transaction_type":"Charge"`, `"transaction_type":"Approval"`, 1)
	cb, _ := ParseCallback([]byte(approval))
	if ev := NormalizeCallback(cb, []byte(approval)); ev.Kind != payments.EventUnknown {
		t.Errorf("J5 approval = %s; want unknown (only a J4 charge settles money)", ev.Kind)
	}
	noPage := strings.Replace(callbackBody, `"payment_request_uid":"ef76432c-769a-43a6-ba7a-6f70272539d8",`, ``, 1)
	cb, _ = ParseCallback([]byte(noPage))
	if ev := NormalizeCallback(cb, []byte(noPage)); ev.Kind != payments.EventUnknown || ev.ProviderPaymentID != "" {
		t.Errorf("charge without a payment page = %s %q; want unknown", ev.Kind, ev.ProviderPaymentID)
	}
}

func TestLooksLikeCallback_OnlyItsShape(t *testing.T) {
	if !LooksLikeCallback([]byte(callbackBody)) {
		t.Fatal("PayPlus callback not recognized")
	}
	for _, other := range []string{
		`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1"}}}`,
		`{"order_id":"x","order_status":"approved","merchant_id":1,"signature":"s"}`,
		`{"order_id":"x","status":1,"sign":"s"}`,
		`{"transaction":"not an object"}`,
		`not json`,
	} {
		if LooksLikeCallback([]byte(other)) {
			t.Errorf("recognized a non-PayPlus body: %s", other)
		}
	}
}

// ─── hosted checkout ────────────────────────────────────────────────────────

func TestCreateCheckoutSession_SendsMajorUnitsAndEveryRef(t *testing.T) {
	stub := newStubPayPlus(t, testAPIKey, testSecret)
	a := New(Config{APIKey: testAPIKey, SecretKey: testSecret, PaymentPageUID: testPageUID, BaseURL: stub.baseURL()})
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	resp, err := a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{
		Amount: 189050, Currency: "ils", ProductName: "Wine tasting", CustomerEmail: "buyer@example.test",
		CustomerName: "Dana Levi", CustomerPhone: "+972500000000", ClientReferenceID: "cs-1",
		SuccessURL: "https://api.test/v1/public/payment-return?r=x", CancelURL: "https://api.test/v1/public/payment-return?r=x",
		CallbackURL: "https://api.test/v1/payment-intents/webhook/cfg", ExpiresAtUnix: now.Unix() + 1860 + 59, Locale: "ru-RU",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if !strings.HasPrefix(resp.SessionID, "00000000-0000-4000-8000-") || !strings.HasSuffix(resp.URL, resp.SessionID) {
		t.Errorf("response = %+v; want SessionID = page_request_uid and the link", resp)
	}
	req := stub.last("generateLink")
	want := map[string]string{
		"payment_page_uid": testPageUID, "amount": "1890.5", "currency_code": "ILS", "charge_method": "1",
		"more_info": "cs-1", "refURL_callback": "https://api.test/v1/payment-intents/webhook/cfg",
		"refURL_success": "https://api.test/v1/public/payment-return?r=x", "refURL_failure": "https://api.test/v1/public/payment-return?r=x",
		"refURL_cancel": "https://api.test/v1/public/payment-return?r=x", "send_failure_callback": "true",
		"sendEmailApproval": "false", "expiry_datetime": "31", "language_code": "ru",
	}
	for k, v := range want {
		if got := scalar(req[k]); got != v {
			t.Errorf("generateLink %s = %q; want %q", k, got, v)
		}
	}
	cust, _ := req["customer"].(map[string]any)
	if cust["customer_name"] != "Dana Levi" || cust["email"] != "buyer@example.test" {
		t.Errorf("customer = %v", cust)
	}
	items, _ := req["items"].([]any)
	if len(items) != 1 || scalar(items[0].(map[string]any)["price"]) != "1890.5" {
		t.Errorf("items = %v; want one line with the whole amount", items)
	}
}

func TestCreateCheckoutSession_RefusesWithoutPageAndReportsAPIError(t *testing.T) {
	stub := newStubPayPlus(t, testAPIKey, testSecret)
	if _, err := New(Config{APIKey: testAPIKey, SecretKey: testSecret, BaseURL: stub.baseURL()}).CreateCheckoutSession(t.Context(),
		payments.CreateHostedCheckoutRequest{Amount: 100, Currency: "ILS", ClientReferenceID: "x"}); err == nil {
		t.Error("a link without payment_page_uid was requested")
	}
	_, err := New(Config{APIKey: testAPIKey, SecretKey: "wrong", PaymentPageUID: testPageUID, BaseURL: stub.baseURL()}).CreateCheckoutSession(t.Context(),
		payments.CreateHostedCheckoutRequest{Amount: 100, Currency: "ILS", ClientReferenceID: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.authRefusal() {
		t.Errorf("wrong secret = %v; want an auth APIError", err)
	}
}

func TestPageLanguage(t *testing.T) {
	for in, want := range map[string]string{"": "", "he": "he", "he-IL": "he", "ru": "ru", "en": "en", "cs": "en", "es-ES": "en"} {
		if got := pageLanguage(in); got != want {
			t.Errorf("pageLanguage(%q) = %q; want %q", in, got, want)
		}
	}
}

// ─── credentials ────────────────────────────────────────────────────────────

func TestVerifyCredentials(t *testing.T) {
	stub := newStubPayPlus(t, testAPIKey, testSecret)
	good := New(Config{APIKey: testAPIKey, SecretKey: testSecret, BaseURL: stub.baseURL()})
	if err := good.VerifyCredentials(t.Context()); err != nil {
		t.Errorf("good keys = %v; want accepted (the probe's not-found is fine)", err)
	}
	bad := New(Config{APIKey: testAPIKey, SecretKey: "nope", BaseURL: stub.baseURL()})
	if err := bad.VerifyCredentials(t.Context()); !errors.Is(err, payments.ErrCredentialRefused) {
		t.Errorf("wrong secret = %v; want ErrCredentialRefused", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	if err := New(Config{APIKey: "a", SecretKey: "b", BaseURL: down.URL}).VerifyCredentials(t.Context()); !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Errorf("5xx = %v; want ErrProviderUnreachable, never a verdict on the key", err)
	}
}

// ─── refunds ────────────────────────────────────────────────────────────────

func refundAdapter(t *testing.T) (*Adapter, *stubPayPlus) {
	t.Helper()
	stub := newStubPayPlus(t, testAPIKey, testSecret)
	stub.addCharge("charge-1", "page-1", 10_000, "ILS")
	return New(Config{APIKey: testAPIKey, SecretKey: testSecret, PaymentPageUID: testPageUID, BaseURL: stub.baseURL()}), stub
}

func TestRefund_PartialSendsMajorUnitsAndMarker(t *testing.T) {
	a, stub := refundAdapter(t)
	res, err := a.Refund(t.Context(), payments.RefundRequest{ProviderChargeRef: "charge-1", AmountMinor: 5_000,
		PaymentAmountMinor: 10_000, Currency: "ILS", IdempotencyKey: "r-1", Attempt: 1})
	if err != nil || res.Status != payments.RefundSucceeded || res.ProviderRefundID == "" {
		t.Fatalf("Refund = %+v, %v", res, err)
	}
	req := stub.last("RefundByTransactionUID")
	if scalar(req["amount"]) != "50" || req["transaction_uid"] != "charge-1" || req["more_info"] != RefundMarker("r-1") {
		t.Errorf("refund request = %v", req)
	}
	if stub.count("Transactions/View") != 0 {
		t.Error("a first call searched for an earlier one")
	}
}

func TestRefund_LostAnswerIsFoundNotRepeated(t *testing.T) {
	a, stub := refundAdapter(t)
	stub.loseNextRefund = true
	req := payments.RefundRequest{ProviderChargeRef: "charge-1", AmountMinor: 3_000, PaymentAmountMinor: 10_000,
		Currency: "ILS", IdempotencyKey: "r-lost", Attempt: 1}
	if _, err := a.Refund(t.Context(), req); err == nil {
		t.Fatal("a lost answer reported success")
	} else if _, _, declined := payments.RefundDeclined(err); declined {
		t.Fatalf("a lost answer = %v; must be an unknown outcome, not a refusal", err)
	}
	req.Attempt = 2
	req.FirstAttemptAt = time.Now().Add(-time.Minute)
	res, err := a.Refund(t.Context(), req)
	if err != nil || res.Status != payments.RefundSucceeded {
		t.Fatalf("retry = %+v, %v; want the earlier refund found", res, err)
	}
	if stub.refundCount() != 1 {
		t.Fatalf("PayPlus booked %d refunds; the money must go back once", stub.refundCount())
	}
}

func TestRefund_RepeatWithNothingFoundWaitsThenSends(t *testing.T) {
	a, stub := refundAdapter(t)
	req := payments.RefundRequest{ProviderChargeRef: "charge-1", AmountMinor: 1_000, PaymentAmountMinor: 10_000,
		Currency: "ILS", IdempotencyKey: "r-wait", Attempt: 2, FirstAttemptAt: time.Now().Add(-time.Minute)}
	if _, err := a.Refund(t.Context(), req); err == nil || stub.refundCount() != 0 {
		t.Fatalf("a repeat inside SettleWait = %v, %d refunds; want nothing sent", err, stub.refundCount())
	}
	req.FirstAttemptAt = time.Now().Add(-SettleWait - time.Minute)
	if res, err := a.Refund(t.Context(), req); err != nil || res.Status != payments.RefundSucceeded || stub.refundCount() != 1 {
		t.Fatalf("a repeat after SettleWait = %+v, %v, %d refunds; want sent once", res, err, stub.refundCount())
	}
}

// The lost-answer path when PayPlus does NOT find the refund by more_info:
// the charge's refunded total decides, so a partial refund is never paid
// twice, whatever the time since the first call.
func TestRefund_LostPartialAnswerIsAccountedForByTheChargeTotal(t *testing.T) {
	base := payments.RefundRequest{ProviderChargeRef: "charge-1", AmountMinor: 3_000, PaymentAmountMinor: 10_000,
		Currency: "ILS", IdempotencyKey: "r-partial", Attempt: 1}
	lose := func(t *testing.T) (*Adapter, *stubPayPlus) {
		t.Helper()
		a, stub := refundAdapter(t)
		stub.viewBlind = true
		stub.loseNextRefund = true
		if _, err := a.Refund(t.Context(), base); err == nil {
			t.Fatal("a lost answer reported success")
		}
		return a, stub
	}
	retry := base
	retry.Attempt = 2
	retry.FirstAttemptAt = time.Now().Add(-SettleWait - time.Hour)

	t.Run("found under the marker among the related refunds", func(t *testing.T) {
		a, stub := lose(t)
		res, err := a.Refund(t.Context(), retry)
		if err != nil || res.Status != payments.RefundSucceeded || res.ProviderRefundID == "" {
			t.Fatalf("retry = %+v, %v; want the landed refund", res, err)
		}
		if stub.refundCount() != 1 {
			t.Fatalf("PayPlus booked %d refunds; want 1", stub.refundCount())
		}
	})
	t.Run("landed without the marker", func(t *testing.T) {
		a, stub := lose(t)
		stub.relatedUnmarked = true
		_, err := a.Refund(t.Context(), retry)
		if code, _, declined := payments.RefundDeclined(err); !declined || code != RefundCodeLandedUnmarked || !payments.RefundDeclineNeedsReview(err) {
			t.Fatalf("retry = %v; want manual review %s", err, RefundCodeLandedUnmarked)
		}
		if stub.refundCount() != 1 {
			t.Fatalf("PayPlus booked %d refunds; want 1", stub.refundCount())
		}
	})
	t.Run("total cannot be read", func(t *testing.T) {
		a, stub := lose(t)
		stub.relatedFails = true
		_, err := a.Refund(t.Context(), retry)
		if code, _, declined := payments.RefundDeclined(err); !declined || code != RefundCodeTotalUnknown || !payments.RefundDeclineNeedsReview(err) {
			t.Fatalf("retry = %v; want manual review %s", err, RefundCodeTotalUnknown)
		}
		if stub.refundCount() != 1 {
			t.Fatalf("PayPlus booked %d refunds; want 1", stub.refundCount())
		}
	})
	t.Run("another call without an answer is pending", func(t *testing.T) {
		a, stub := lose(t)
		r := retry
		r.OtherUncertainMinor = 2_000
		if _, err := a.Refund(t.Context(), r); !payments.RefundDeclineNeedsReview(err) {
			t.Fatalf("retry = %v; want manual review", err)
		}
		if stub.refundCount() != 1 {
			t.Fatalf("PayPlus booked %d refunds; want 1", stub.refundCount())
		}
	})
}

func TestRefund_PayPlusShowsLessThanArenaKnows(t *testing.T) {
	a, stub := refundAdapter(t)
	_, err := a.Refund(t.Context(), payments.RefundRequest{ProviderChargeRef: "charge-1", AmountMinor: 1_000,
		PaymentAmountMinor: 10_000, IdempotencyKey: "r-less", Attempt: 2, OtherRefundedMinor: 2_000})
	if code, _, declined := payments.RefundDeclined(err); !declined || code != RefundCodeKnowsLess {
		t.Fatalf("Refund = %v; want %s", err, RefundCodeKnowsLess)
	}
	if stub.refundCount() != 0 {
		t.Fatal("a refund was sent")
	}
}

func TestFindRefund_RequiresTheExactMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, okEnv([]any{
			map[string]any{"transaction": map[string]any{"transaction_uid": "foreign", "status_code": "000", "amount": 30}},
			map[string]any{"transaction": map[string]any{"transaction_uid": "other", "status_code": "000", "more_info": "arena-refund:x", "amount": 30}},
		}))
	}))
	defer srv.Close()
	a := New(Config{APIKey: "a", SecretKey: "b", BaseURL: srv.URL})
	if _, found, err := a.findRefund(t.Context(), RefundMarker("mine")); err != nil || found {
		t.Fatalf("findRefund = %v, %v; a transaction without our exact marker is not ours", found, err)
	}
}

func TestRefund_RefusalIsDeclinedForReview(t *testing.T) {
	a, stub := refundAdapter(t)
	stub.refuseRefund = "transaction already refunded"
	_, err := a.Refund(t.Context(), payments.RefundRequest{ProviderChargeRef: "charge-1", AmountMinor: 1_000,
		PaymentAmountMinor: 10_000, IdempotencyKey: "r-ref", Attempt: 1})
	code, _, declined := payments.RefundDeclined(err)
	if !declined || code != RefundCodeRefused || !payments.RefundDeclineNeedsReview(err) {
		t.Fatalf("refusal = %v; want a declined refund a human reviews", err)
	}
}

func TestResolveChargeRef(t *testing.T) {
	a, _ := refundAdapter(t)
	if ref, err := a.ResolveChargeRef(t.Context(), "page-1"); err != nil || ref != "charge-1" {
		t.Fatalf("ResolveChargeRef = %q, %v; want charge-1", ref, err)
	}
	_, err := a.ResolveChargeRef(t.Context(), "page-unknown")
	if _, _, declined := payments.RefundDeclined(err); !declined {
		t.Fatalf("unknown link = %v; want a declined lookup", err)
	}
}
