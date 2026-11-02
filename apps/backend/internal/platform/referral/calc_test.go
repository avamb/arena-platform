package referral

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestCashbackAmount(t *testing.T) {
	for _, tc := range []struct {
		paid int64
		pct  int
		want int64
	}{
		{5000, 10, 500},
		{1999, 10, 199}, // rounded down
		{1, 10, 0},
		{0, 10, 0},
		{-5, 10, 0},
		{5000, 0, 0},
		{5000, 100, 5000},
		{99, 33, 32},
	} {
		if got := CashbackAmount(tc.paid, tc.pct); got != tc.want {
			t.Errorf("CashbackAmount(%d, %d) = %d; want %d", tc.paid, tc.pct, got, tc.want)
		}
	}
}

func TestApplyCap(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		amount, paid, cashback, refunded, cap int64
		wantPay                               int64
		wantCapped                            bool
	}{
		{"room left", 500, 5000, 0, 0, 100, 500, false},
		{"cap 20% of 5000 is 1000, nothing used", 500, 5000, 0, 0, 20, 500, false},
		{"partly capped by earlier cashback", 500, 5000, 800, 0, 20, 200, true},
		{"cap reached", 500, 5000, 1000, 0, 20, 0, true},
		{"other refunds eat the room", 500, 5000, 0, 4800, 100, 200, true},
		{"over-refunded order never goes negative", 500, 5000, 0, 6000, 100, 0, true},
		{"exact fit is not capped", 500, 5000, 500, 0, 20, 500, false},
		{"zero amount", 0, 5000, 0, 0, 100, 0, false},
		{"cap rounds down", 100, 999, 0, 0, 10, 99, true},
	} {
		pay, capped := ApplyCap(tc.amount, tc.paid, tc.cashback, tc.refunded, int(tc.cap))
		if pay != tc.wantPay || capped != tc.wantCapped {
			t.Errorf("%s: ApplyCap = (%d, %v); want (%d, %v)", tc.name, pay, capped, tc.wantPay, tc.wantCapped)
		}
	}
}

func TestSplitOverTickets(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	budgets := []TicketBudget{{a, 2500}, {b, 4000}, {c, 1000}}
	for _, tc := range []struct {
		name   string
		amount int64
		in     []TicketBudget
		want   []Item
	}{
		{"fits the dearest", 3000, budgets, []Item{{b, 3000}}},
		{"spills to the next dearest", 5000, budgets, []Item{{b, 4000}, {a, 1000}}},
		{"exactly the budgets together", 7500, budgets, []Item{{b, 4000}, {a, 2500}, {c, 1000}}},
		{"more than the budgets: items sum to the budgets", 9000, budgets, []Item{{b, 4000}, {a, 2500}, {c, 1000}}},
		{"ties keep the given order", 100, []TicketBudget{{a, 500}, {b, 500}}, []Item{{a, 100}}},
		{"empty budgets are skipped", 100, []TicketBudget{{a, 0}, {b, -5}, {c, 100}}, []Item{{c, 100}}},
		{"zero amount", 0, budgets, nil},
		{"no tickets", 100, nil, nil},
	} {
		got := SplitOverTickets(tc.amount, tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: SplitOverTickets = %v; want %v", tc.name, got, tc.want)
		}
		if sum := Sum(got); sum > tc.amount {
			t.Errorf("%s: items sum %d exceeds the amount %d", tc.name, sum, tc.amount)
		}
	}
}

func TestGenerateCode_Format(t *testing.T) {
	// A fixed byte stream makes the code deterministic: 0,1,2,3 -> A,B,C,D.
	rnd := func() *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{0, 1, 2, 3}, 8)) }
	for _, tc := range []struct{ name, want string }{
		{"Anna", "ANNA-ABCDAB"},
		{"Анна Петрова", "ANNA-ABCDAB"},          // Cyrillic is transliterated, first word only
		{"Aleksandra Romanova", "ALEKSA-ABCDAB"}, // cut to six letters
		{"  ", "ABCDABCDAB"},                     // no name: ten characters
		{"", "ABCDABCDAB"},
		{"12345", "ABCDABCDAB"}, // no letters survive
	} {
		got, err := generateCode(tc.name, rnd())
		if err != nil {
			t.Fatalf("%q: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("generateCode(%q) = %q; want %q", tc.name, got, tc.want)
		}
	}
}

func TestRandomChars_StaysInTheAlphabetAndRejectsTheBiasedTail(t *testing.T) {
	// 255 and 248 lie in the rejected tail (>= 248), so only 0 and 1 count.
	src := append(bytes.Repeat([]byte{255, 248}, 8), bytes.Repeat([]byte{0, 1}, 8)...)
	got, err := randomChars(2, bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if got != "AB" {
		t.Errorf("randomChars skipped the tail wrongly: %q", got)
	}
	for _, r := range got {
		if !strings.ContainsRune(codeAlphabet, r) {
			t.Errorf("%q is outside the alphabet", r)
		}
	}
	if _, err := randomChars(4, bytes.NewReader(nil)); err == nil {
		t.Error("an exhausted random source must be an error")
	}
	if strings.ContainsAny(codeAlphabet, "01ILO") {
		t.Errorf("the alphabet %q holds an ambiguous character", codeAlphabet)
	}
	if len(codeAlphabet) != 31 {
		t.Errorf("the alphabet has %d characters; spec 43 has 31", len(codeAlphabet))
	}
}

func TestOrderEligible(t *testing.T) {
	facts := func(mod func(o *gen.ReferralOrderFacts)) gen.ReferralOrderFacts {
		o := gen.ReferralOrderFacts{Status: "paid", Source: "public_feed", Total: 5000}
		mod(&o)
		return o
	}
	if !orderEligible(facts(func(*gen.ReferralOrderFacts) {})) {
		t.Error("a paid public order must be eligible")
	}
	if !orderEligible(facts(func(o *gen.ReferralOrderFacts) { o.Status = "partially_refunded" })) {
		t.Error("a partly refunded order keeps its code")
	}
	for name, mod := range map[string]func(*gen.ReferralOrderFacts){
		"pending":       func(o *gen.ReferralOrderFacts) { o.Status = "pending_payment" },
		"refunded":      func(o *gen.ReferralOrderFacts) { o.Status = "refunded" },
		"free":          func(o *gen.ReferralOrderFacts) { o.Total = 0 },
		"complimentary": func(o *gen.ReferralOrderFacts) { o.Source = "complimentary" },
		"site":          func(o *gen.ReferralOrderFacts) { o.Source = "bil24_gateway" },
	} {
		if orderEligible(facts(mod)) {
			t.Errorf("%s order must not be eligible", name)
		}
	}
}
