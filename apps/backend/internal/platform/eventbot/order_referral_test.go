package eventbot

import (
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The "Refer a friend" cashback (spec 52) is money the organizer returned to
// a referrer: the money line names it next to the refunds, and the net the
// server sends already has it taken off. The stand's case: 252,50 + 454,50
// paid, one ticket refunded for 207,05, cashback 45,45, net 454,50.
func TestMoneyBlock_ShowsReferralCashback(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	m := openapi.SummaryMoneyItem{Currency: "EUR", PaidOrders: 2, Paid: 70700, Refunded: 20705,
		CashbackRefunds: 1, Cashback: 4545, Net: 45450}
	refunds := []openapi.SummaryRefundsItem{{Currency: "EUR", Refunds: 1, Amount: 20705, Settlement: "provider", State: "succeeded"}}

	got := b.moneyBlock("ru", m, refunds)
	for _, want := range []string{"Возвраты: 1 на 207,05 EUR", "кэшбэк «Приведи друга»: 1 на 45,45 EUR", "нетто 454,50 EUR"} {
		if !strings.Contains(got, want) {
			t.Errorf("ru money block lacks %q:\n%s", want, got)
		}
	}
	if got := b.moneyBlock("en", m, refunds); !strings.Contains(got, `cashback "Refer a friend": 1 for 45.45 EUR, net 454.50 EUR`) {
		t.Errorf("en money block:\n%s", got)
	}

	// Without cashback the line is what it always was.
	m.CashbackRefunds, m.Cashback, m.Net = 0, 0, 49995
	if got := b.moneyBlock("ru", m, refunds); strings.Contains(got, "кэшбэк") {
		t.Errorf("a money block without cashback must not name it:\n%s", got)
	}
}

func TestReferralLines_OrderCard(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	friend1, friend2, friend3, referrer := int64(1000000501), int64(1000000502), int64(1000000503), int64(1000000500)
	paidAt := time.Date(2026, 11, 2, 17, 30, 0, 0, time.UTC)
	r := &openapi.OrderReferral{Cashback: []openapi.OrderReferralCashback{
		{FriendOrderNumber: &friend1, State: "paid", Amount: 4545, Currency: "EUR", PaidAt: &paidAt},
		{FriendOrderNumber: &friend2, State: "sent", Amount: 1000, Currency: "EUR"},
		{FriendOrderNumber: &friend3, State: "not_possible", Amount: 0, Currency: "EUR"},
	}}
	got := b.referralLines("ru", r, "Europe/Madrid")
	for _, want := range []string{
		"Кэшбэк «Приведи друга»: 45,45 EUR выплачен 02.11.2026 18:30, заказ друга №1000000501",
		"Кэшбэк «Приведи друга»: 10 EUR в пути, заказ друга №1000000502",
		"Кэшбэк «Приведи друга» за заказ друга №1000000503 не выплачен",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("referrer's card lacks %q:\n%s", want, got)
		}
	}

	friend := &openapi.OrderReferral{Cashback: []openapi.OrderReferralCashback{},
		UsedCode: &openapi.OrderReferralCode{Code: "ANNA-7K2Q", ReferrerOrderNumber: &referrer}}
	if got := b.referralLines("ru", friend, "Europe/Madrid"); !strings.Contains(got, "Куплено по коду пригласившего <b>ANNA-7K2Q</b>, заказ пригласившего №1000000500") {
		t.Errorf("friend's card:\n%s", got)
	}
	if got := b.referralLines("es", friend, ""); !strings.Contains(got, "<b>ANNA-7K2Q</b>") {
		t.Errorf("es friend's card:\n%s", got)
	}
	if got := b.referralLines("en", nil, ""); got != "" {
		t.Errorf("an order with no part in the programme must add nothing, got %q", got)
	}

	// Many friends: the first ones and "and N more".
	many := &openapi.OrderReferral{}
	for i := 0; i < maxReferralLines+3; i++ {
		many.Cashback = append(many.Cashback, openapi.OrderReferralCashback{State: "due", Amount: 100, Currency: "EUR"})
	}
	if got := b.referralLines("en", many, ""); !strings.Contains(got, "and 3 more friends' orders") {
		t.Errorf("long list:\n%s", got)
	}
}
