//go:build allpaylive

// live_test.go — the manual check against AllPay's TEST zone (spec
// 08_architecture/37 §11). It talks to the real allpay.to, so it never runs
// in CI: it needs the build tag `allpaylive` AND a TEST integration's
// credentials in the environment. The credentials are read from the
// environment only and never printed.
//
//	ALLPAY_TEST_LOGIN=... ALLPAY_TEST_KEY=... \
//	  go test -tags allpaylive -run TestAllPayLive -v ./apps/backend/internal/adapters/allpay/
//
// Optional: ALLPAY_TEST_ORDER_ID=<an order id paid by hand> reads that order's
// status back (it must be 1 after paying with test card 4557430402053431).
//
// Optional refund of a TEST order paid by hand (spec 37 §11 C):
// ALLPAY_TEST_REFUND_ORDER_ID=<order id>, ALLPAY_TEST_REFUND_MINOR=<amount in
// agorot>, ALLPAY_TEST_REFUND_KNOWN_MINOR=<what earlier refunds of this
// order returned, default 0>. The module reads the order first, exactly as
// the refund engine would use it.
package allpay

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func liveAdapter(t *testing.T) *Adapter {
	t.Helper()
	login := strings.TrimSpace(os.Getenv("ALLPAY_TEST_LOGIN"))
	key := strings.TrimSpace(os.Getenv("ALLPAY_TEST_KEY"))
	if login == "" || key == "" {
		t.Skip("ALLPAY_TEST_LOGIN and ALLPAY_TEST_KEY are not set")
	}
	// TestMode forces test_mode=1: no real card can be charged, whatever the
	// integration's own setting says.
	return New(Config{Login: login, APIKey: key, TestMode: true})
}

func TestAllPayLive_CheckKeys(t *testing.T) {
	a := liveAdapter(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := a.VerifyCredentials(ctx); err != nil {
		t.Fatalf("checkkeys: %v", err)
	}
	// A wrong key must be refused, not "unreachable".
	wrong := New(Config{Login: a.cfg.Login, APIKey: a.cfg.APIKey + "x"})
	if err := wrong.VerifyCredentials(ctx); err == nil {
		t.Fatal("a wrong key was accepted")
	} else {
		t.Logf("wrong key answer: %v", err)
	}
}

func TestAllPayLive_CreatePageAndReadStatus(t *testing.T) {
	a := liveAdapter(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	orderID := uuid.NewString()
	resp, err := a.CreateCheckoutSession(ctx, payments.CreateHostedCheckoutRequest{
		Amount:            1000, // 10.00 ILS: above AllPay's 5 ILS minimum
		Currency:          "ILS",
		ProductName:       "Arena test — AllPay integration",
		CustomerEmail:     "arena-allpay-test@example.com",
		CustomerName:      "Arena Test",
		CustomerPhone:     "+34 600 111 222", // foreign: the Israeli ID field is hidden
		ClientReferenceID: orderID,
		SuccessURL:        "https://example.com/arena-allpay-test/return",
		CancelURL:         "https://example.com/arena-allpay-test/return",
		ExpiresAtUnix:     time.Now().Add(30 * time.Minute).Unix(),
		Locale:            "en",
	})
	if err != nil {
		t.Fatalf("getpayment: %v", err)
	}
	t.Logf("order_id %s", orderID)
	t.Logf("payment page %s", resp.URL)

	// AllPay asks to read a status at least 2 seconds after an attempt.
	time.Sleep(3 * time.Second)
	st, err := a.PaymentStatus(ctx, orderID)
	if err != nil {
		t.Fatalf("paymentstatus of a fresh order: %v", err)
	}
	if st.Status != StatusUnpaid {
		t.Errorf("a fresh order has status %d; want 0", st.Status)
	}

	if paid := strings.TrimSpace(os.Getenv("ALLPAY_TEST_ORDER_ID")); paid != "" {
		st, err := a.PaymentStatus(ctx, paid)
		if err != nil {
			t.Fatalf("paymentstatus of %s: %v", paid, err)
		}
		t.Logf("order %s: status %d, amount %d %s", paid, st.Status, st.AmountMinor, st.Currency)
	}
}

func TestAllPayLive_Refund(t *testing.T) {
	a := liveAdapter(t)
	order := strings.TrimSpace(os.Getenv("ALLPAY_TEST_REFUND_ORDER_ID"))
	amount, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("ALLPAY_TEST_REFUND_MINOR")), 10, 64)
	known, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("ALLPAY_TEST_REFUND_KNOWN_MINOR")), 10, 64)
	if order == "" || amount <= 0 {
		t.Skip("ALLPAY_TEST_REFUND_ORDER_ID and ALLPAY_TEST_REFUND_MINOR are not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	res, err := a.Refund(ctx, payments.RefundRequest{
		ProviderChargeRef:  order,
		AmountMinor:        amount,
		Currency:           "ILS",
		IdempotencyKey:     uuid.NewString(),
		OtherRefundedMinor: known,
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	t.Logf("refund %s: %s", res.ProviderRefundID, res.Status)
	time.Sleep(3 * time.Second)
	st, err := a.PaymentStatus(ctx, order)
	if err != nil {
		t.Fatalf("paymentstatus after the refund: %v", err)
	}
	t.Logf("order %s: status %d, refunded %d of %d", order, st.Status, st.RefundedMinor, st.ChargedMinor())
}
