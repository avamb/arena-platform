package stripe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

var _ payments.BalanceReader = (*Adapter)(nil)

// The referral payout reads the organizer's balance before a cashback refund
// (spec 52 section 6.2): only "available" counts, in the order's currency.
func TestAvailableBalance_SumsAvailableInTheCurrency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/balance" {
			t.Errorf("request %s %s, want GET /v1/balance", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"object":"balance",
			"available":[{"amount":1200,"currency":"eur"},{"amount":300,"currency":"eur"},{"amount":9000,"currency":"usd"}],
			"pending":[{"amount":50000,"currency":"eur"}]}`))
	}))
	defer srv.Close()
	a := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL + "/v1"})
	for cur, want := range map[string]int64{"EUR": 1500, "usd": 9000, "CZK": 0} {
		got, err := a.AvailableBalance(context.Background(), cur)
		if err != nil {
			t.Fatalf("%s: %v", cur, err)
		}
		if got != want {
			t.Errorf("%s: available = %d, want %d", cur, got, want)
		}
	}
}

func TestAvailableBalance_AFailedReadIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL + "/v1"})
	if _, err := a.AvailableBalance(context.Background(), "EUR"); err == nil {
		t.Fatal("a 500 was read as a balance")
	}
}
