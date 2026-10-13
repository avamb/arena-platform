package allpay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// stubOrder is one order of the stub's ledger, in minor units.
type stubOrder struct {
	Status   int // 0 unpaid, 1 paid; 3/4 are derived from Refunded
	Amount   int64
	Discount int64
	Refunded int64
	Currency string
	// Refunds lists every refund the stub carried out, in order.
	Refunds []int64
}

// ledger answers paymentstatus and refund for an order the test put into
// s.orders. It reports false when the request is not for such an order.
func (s *stubAllPay) ledger(w http.ResponseWriter, show string, body map[string]any) bool {
	if show != methodPaymentStatus && show != methodRefund {
		return false
	}
	orderID, _ := body["order_id"].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orders[orderID]
	if o == nil {
		// A test that keeps a ledger knows every order it has.
		if show == methodRefund || len(s.orders) > 0 {
			_, _ = w.Write([]byte(`{"error_code":6,"error_msg":"Order not found"}`))
			return true
		}
		return false
	}
	charged := o.Amount - o.Discount
	status := o.Status
	if status == StatusPaid && o.Refunded > 0 {
		status = StatusPartiallyRefunded
		if o.Refunded >= charged {
			status = StatusRefunded
		}
	}
	if show == methodPaymentStatus {
		ans := map[string]any{"order_id": orderID, "status": status, "amount": json.Number(MinorToMajor(o.Amount)), "currency": o.Currency}
		if o.Discount > 0 {
			ans["discount"] = json.Number(MinorToMajor(o.Discount))
		}
		if status == StatusPartiallyRefunded {
			ans["refunded"] = json.Number(MinorToMajor(o.Refunded))
		}
		raw, _ := json.Marshal(ans)
		_, _ = w.Write(raw)
		return true
	}
	if status != StatusPaid && status != StatusPartiallyRefunded {
		_, _ = w.Write([]byte(`{"error_code":16,"error_msg":"Wrong order status"}`))
		return true
	}
	amount, ok := MajorToMinor(fmt.Sprint(body["amount"]))
	if !ok || amount <= 0 || amount > charged-o.Refunded {
		_, _ = w.Write([]byte(`{"error_code":2,"error_msg":"Incorrect amount"}`))
		return true
	}
	o.Refunded += amount
	o.Refunds = append(o.Refunds, amount)
	if s.loseRefundAnswers > 0 {
		s.loseRefundAnswers--
		w.WriteHeader(http.StatusBadGateway)
		return true
	}
	newStatus := StatusPartiallyRefunded
	if o.Refunded >= charged {
		newStatus = StatusRefunded
	}
	_, _ = fmt.Fprintf(w, `{"order_id":%q,"status":%d,"msg":"The amount will be refunded to the customer's card within 7 business days."}`, orderID, newStatus)
	return true
}

func (s *stubAllPay) addOrder(id string, o stubOrder) *stubOrder {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.Currency == "" {
		o.Currency = "ILS"
	}
	cp := o
	s.orders[id] = &cp
	return &cp
}

func (s *stubAllPay) refunds(id string) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.orders[id]; o != nil {
		return append([]int64(nil), o.Refunds...)
	}
	return nil
}

func (s *stubAllPay) count(show string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Show == show {
			n++
		}
	}
	return n
}

func refundReq(order string, amount, payment int64, key string) payments.RefundRequest {
	return payments.RefundRequest{
		ProviderChargeRef:  order,
		AmountMinor:        amount,
		PaymentAmountMinor: payment,
		Currency:           "ILS",
		IdempotencyKey:     key,
	}
}

func declined(t *testing.T, err error) *payments.RefundDeclinedError {
	t.Helper()
	var d *payments.RefundDeclinedError
	if !errors.As(err, &d) {
		t.Fatalf("err = %v; want *payments.RefundDeclinedError", err)
	}
	return d
}

func TestRefund_FullRefundIsSentInMajorUnits(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.addOrder("o-full", stubOrder{Status: StatusPaid, Amount: 12350, Currency: "ILS"})
	a := newTestAdapter(stub, Config{})
	res, err := a.Refund(t.Context(), refundReq("o-full", 12350, 12350, "r-1"))
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if res.Status != payments.RefundSucceeded || res.ProviderRefundID != "o-full/r-1" {
		t.Errorf("result = %+v; want succeeded, o-full/r-1", res)
	}
	if got := stub.refunds("o-full"); len(got) != 1 || got[0] != 12350 {
		t.Errorf("AllPay refunded %v; want [12350]", got)
	}
	if body := stub.last(t).Body; fmt.Sprint(body["amount"]) != "123.5" || body["order_id"] != "o-full" {
		t.Errorf("refund body = %v; want amount 123.5 for o-full", body)
	}
}

// The case PAY-14b exists for: the answer of a refund is lost, the engine
// calls again with the same refund, and the money must go back only once.
func TestRefund_LostAnswerIsNotRefundedTwice(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.addOrder("o-lost", stubOrder{Status: StatusPaid, Amount: 10000})
	stub.loseRefundAnswers = 1
	a := newTestAdapter(stub, Config{})
	req := refundReq("o-lost", 4000, 10000, "r-lost")
	if _, err := a.Refund(t.Context(), req); err == nil {
		t.Fatal("a lost answer was reported as an answer")
	} else if _, _, ok := payments.RefundDeclined(err); ok {
		t.Fatalf("a lost answer was reported as a refusal: %v; the outcome is unknown", err)
	}
	res, err := a.Refund(t.Context(), req)
	if err != nil {
		t.Fatalf("second Refund: %v", err)
	}
	if res.Status != payments.RefundSucceeded || res.ProviderRefundID != "o-lost/r-lost" {
		t.Errorf("second result = %+v", res)
	}
	if got := stub.refunds("o-lost"); len(got) != 1 {
		t.Errorf("AllPay refunded %v; want exactly one refund", got)
	}
}

func TestRefund_SecondTicketOfTheOrderIsSent(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.addOrder("o-two", stubOrder{Status: StatusPaid, Amount: 10000})
	a := newTestAdapter(stub, Config{})
	if _, err := a.Refund(t.Context(), refundReq("o-two", 4000, 10000, "r-a")); err != nil {
		t.Fatalf("first: %v", err)
	}
	req := refundReq("o-two", 6000, 10000, "r-b")
	req.OtherRefundedMinor = 4000
	if _, err := a.Refund(t.Context(), req); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := stub.refunds("o-two"); len(got) != 2 || got[1] != 6000 {
		t.Errorf("AllPay refunded %v; want [4000 6000]", got)
	}
}

func TestRefund_UnexplainedDifferenceStopsAndSendsNothing(t *testing.T) {
	cases := []struct {
		name             string
		refunded         int64
		known, uncertain int64
		amount           int64
		wantCode         string
	}{
		// A dashboard refund arena does not know of, of another size.
		{name: "dashboard refund", refunded: 2500, amount: 4000, wantCode: RefundCodeUnexplained},
		// Another refund's call ended without an answer: AllPay's total
		// cannot tell which refund it belongs to.
		{name: "uncertain other refund", refunded: 4000, uncertain: 4000, amount: 4000, wantCode: RefundCodeUnexplained},
		// AllPay knows less than arena does.
		{name: "allpay knows less", refunded: 1000, known: 4000, amount: 3000, wantCode: RefundCodeKnowsLess},
		// More than is left on the order.
		{name: "over remaining", refunded: 7000, known: 7000, amount: 4000, wantCode: RefundCodeOverRemaining},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubAllPay(t, "pp1", "key")
			stub.addOrder("o-x", stubOrder{Status: StatusPaid, Amount: 10000, Refunded: tc.refunded})
			a := newTestAdapter(stub, Config{})
			req := refundReq("o-x", tc.amount, 10000, "r-x")
			req.OtherRefundedMinor, req.OtherUncertainMinor = tc.known, tc.uncertain
			_, err := a.Refund(t.Context(), req)
			d := declined(t, err)
			if d.Code != tc.wantCode || !d.NeedsReview {
				t.Errorf("declined = %+v; want %s needing review", d, tc.wantCode)
			}
			if n := stub.count(methodRefund); n != 0 {
				t.Errorf("the stub received %d refund call(s); want none", n)
			}
		})
	}
}

func TestRefund_UncertainOtherRefundThatDidNotLandIsSent(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.addOrder("o-u", stubOrder{Status: StatusPaid, Amount: 10000})
	a := newTestAdapter(stub, Config{})
	req := refundReq("o-u", 3000, 10000, "r-u")
	req.OtherUncertainMinor = 4000 // its call ended without an answer, but AllPay refunded nothing
	if _, err := a.Refund(t.Context(), req); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if got := stub.refunds("o-u"); len(got) != 1 || got[0] != 3000 {
		t.Errorf("AllPay refunded %v; want [3000]", got)
	}
}

func TestRefund_OrderStatesThatNeedAHuman(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.addOrder("o-unpaid", stubOrder{Status: 0, Amount: 10000, Currency: "ILS"})
	stub.addOrder("o-usd", stubOrder{Status: StatusPaid, Amount: 10000, Currency: "USD"})
	a := newTestAdapter(stub, Config{})
	for order, code := range map[string]string{
		"o-unpaid":  RefundCodeNotPaid,
		"o-usd":     RefundCodeCurrency,
		"o-missing": RefundCodeOrderNotFound,
	} {
		_, err := a.Refund(t.Context(), refundReq(order, 1000, 10000, "r-"+order))
		if d := declined(t, err); d.Code != code || !d.NeedsReview {
			t.Errorf("%s: declined = %+v; want %s needing review", order, d, code)
		}
	}
	if n := stub.count(methodRefund); n != 0 {
		t.Errorf("the stub received %d refund call(s); want none", n)
	}
}

func TestRefund_DiscountIsWhatTheBuyerDidNotPay(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.addOrder("o-disc", stubOrder{Status: StatusPaid, Amount: 10000, Discount: 1000, Refunded: 9000})
	a := newTestAdapter(stub, Config{})
	// Fully refunded: the charged 90.00 is back. AllPay reports status 3.
	req := refundReq("o-disc", 1000, 9000, "r-d")
	req.OtherRefundedMinor = 9000
	_, err := a.Refund(t.Context(), req)
	if d := declined(t, err); d.Code != RefundCodeOverRemaining {
		t.Errorf("declined = %+v; want %s", d, RefundCodeOverRemaining)
	}
}

func TestRefund_AllPayRefusals(t *testing.T) {
	cases := map[string]struct {
		answer     string
		wantCode   string
		wantReview bool
	}{
		"processor refused": {`{"error_code":17,"error_msg":"Refund error"}`, RefundCodeProcessorRefused, false},
		"wrong status":      {`{"error_code":16,"error_msg":"Wrong status"}`, RefundCodeAPIRefused, true},
		"bad parameter":     {`{"error_code":2,"error_msg":"Incorrect amount"}`, RefundCodeAPIRefused, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stub := newStubAllPay(t, "pp1", "key")
			stub.addOrder("o-r", stubOrder{Status: StatusPaid, Amount: 10000})
			stub.answer[methodRefund] = tc.answer
			a := newTestAdapter(stub, Config{})
			_, err := a.Refund(t.Context(), refundReq("o-r", 1000, 10000, "r-r"))
			if d := declined(t, err); d.Code != tc.wantCode || d.NeedsReview != tc.wantReview {
				t.Errorf("declined = %+v; want %s review=%v", d, tc.wantCode, tc.wantReview)
			}
		})
	}
}

func TestRefund_StatusUnreadableIsUnknown(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.answer[methodPaymentStatus] = `not json`
	a := newTestAdapter(stub, Config{})
	_, err := a.Refund(t.Context(), refundReq("o-any", 1000, 10000, "r"))
	if err == nil {
		t.Fatal("an unreadable status was taken as an answer")
	}
	if _, _, ok := payments.RefundDeclined(err); ok {
		t.Errorf("an unreadable status was reported as a refusal: %v", err)
	}
	if n := stub.count(methodRefund); n != 0 {
		t.Errorf("the stub received %d refund call(s); want none", n)
	}
}

// Review M2: status 4 must name a part of the order. A missing (0) or full
// refunded total is not "nothing refunded yet".
func TestRefund_PartialStatusWithoutAPartIsReviewed(t *testing.T) {
	for name, answer := range map[string]string{
		"refunded missing": `{"order_id":"o-p","status":4,"amount":100,"currency":"ILS"}`,
		"refunded all":     `{"order_id":"o-p","status":4,"amount":100,"refunded":100,"currency":"ILS"}`,
	} {
		t.Run(name, func(t *testing.T) {
			stub := newStubAllPay(t, "pp1", "key")
			stub.answer[methodPaymentStatus] = answer
			a := newTestAdapter(stub, Config{})
			_, err := a.Refund(t.Context(), refundReq("o-p", 1000, 10000, "r-p"))
			if d := declined(t, err); d.Code != RefundCodePartialUnclear || !d.NeedsReview {
				t.Errorf("declined = %+v; want %s needing review", d, RefundCodePartialUnclear)
			}
			if n := stub.count(methodRefund); n != 0 {
				t.Errorf("the stub received %d refund call(s); want none", n)
			}
		})
	}
}

// Review M1 for refunds: an answer without amount or currency sends nothing
// and is an unknown outcome, not a refusal.
func TestRefund_StatusWithoutAmountOrCurrencySendsNothing(t *testing.T) {
	for name, answer := range map[string]string{
		"no amount":   `{"order_id":"o-n","status":1,"currency":"ILS"}`,
		"no currency": `{"order_id":"o-n","status":1,"amount":100}`,
	} {
		t.Run(name, func(t *testing.T) {
			stub := newStubAllPay(t, "pp1", "key")
			stub.answer[methodPaymentStatus] = answer
			a := newTestAdapter(stub, Config{})
			_, err := a.Refund(t.Context(), refundReq("o-n", 1000, 10000, "r-n"))
			if err == nil {
				t.Fatal("an answer without amount or currency was trusted")
			}
			if _, _, ok := payments.RefundDeclined(err); ok {
				t.Errorf("reported as a refusal: %v; want an unknown outcome", err)
			}
			if n := stub.count(methodRefund); n != 0 {
				t.Errorf("the stub received %d refund call(s); want none", n)
			}
		})
	}
}

// Review L2: a repeated call of a refund whose money AllPay does not show yet
// waits SettleWait from the first call before it sends again.
func TestRefund_RepeatWaitsForTheEarlierCallToSettle(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.addOrder("o-w", stubOrder{Status: StatusPaid, Amount: 10000})
	a := newTestAdapter(stub, Config{})
	req := refundReq("o-w", 4000, 10000, "r-w")
	req.Attempt = 2
	req.FirstAttemptAt = a.now().Add(-time.Minute)
	_, err := a.Refund(t.Context(), req)
	if err == nil {
		t.Fatal("a repeat one minute after the first call was sent")
	}
	if _, _, ok := payments.RefundDeclined(err); ok {
		t.Errorf("reported as a refusal: %v; want an unknown outcome", err)
	}
	if n := stub.count(methodRefund); n != 0 {
		t.Fatalf("the stub received %d refund call(s) inside the wait", n)
	}
	req.FirstAttemptAt = a.now().Add(-SettleWait - time.Second)
	if _, err := a.Refund(t.Context(), req); err != nil {
		t.Fatalf("repeat after the wait: %v", err)
	}
	if got := stub.refunds("o-w"); len(got) != 1 {
		t.Errorf("AllPay refunded %v; want one refund after the wait", got)
	}
	// A first call never waits.
	stub.addOrder("o-w2", stubOrder{Status: StatusPaid, Amount: 10000})
	first := refundReq("o-w2", 1000, 10000, "r-w2")
	first.Attempt, first.FirstAttemptAt = 1, a.now()
	if _, err := a.Refund(t.Context(), first); err != nil {
		t.Errorf("first call: %v", err)
	}
}

// Review L4: an answer about another order is not trusted.
func TestPaymentStatus_AnswerForAnotherOrderIsAnError(t *testing.T) {
	stub := newStubAllPay(t, "pp1", "key")
	stub.answer[methodPaymentStatus] = `{"order_id":"someone-else","status":1,"amount":100,"currency":"ILS"}`
	a := newTestAdapter(stub, Config{})
	if _, err := a.PaymentStatus(t.Context(), "o-mine"); err == nil {
		t.Fatal("a status for another order was accepted")
	}
}
