package credits

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

func eur(amount, unseated, surcharge int64, seated bool) ChargeInput {
	return ChargeInput{Currency: "EUR", AmountMinor: amount, UnseatedFactMinor: unseated,
		SurchargeFactMinor: surcharge, Seated: seated, TierPriced: true, BonusAllowed: true}
}

func packLot(rem, rate, surcharge int64) Lot {
	return Lot{ID: uuid.New(), Currency: "EUR", RemainingMinor: rem, UnseatedRateMinor: rate, SeatedSurchargeMinor: surcharge}
}

func bonusLot(rem int64) Lot { return Lot{ID: uuid.New(), Currency: "EUR", RemainingMinor: rem} }

var policy30 = Policy{Currency: "EUR", BonusShareCapBP: 3000}

func mustPlan(t *testing.T, c ChargeInput, pol Policy, p, b []Lot) Plan {
	t.Helper()
	got, err := MakePlan(c, pol, p, b)
	if err != nil {
		t.Fatalf("MakePlan: %v", err)
	}
	if s := got.BonusMinor() + got.PurchasedMinor() + got.DiscountMinor + got.UncoveredMinor; s != c.AmountMinor {
		t.Fatalf("plan adds up to %d, charge is %d: %+v", s, c.AmountMinor, got)
	}
	return got
}

func TestMakePlan_Cases(t *testing.T) {
	floor40 := int64(40)
	type want struct{ bonus, purchased, discount, uncovered int64 }
	cases := []struct {
		name string
		c    ChargeInput
		pol  Policy
		p, b []Lot
		want want
	}{
		{"no lots: all for the invoice", eur(59, 59, 0, false), policy30, nil, nil, want{0, 0, 0, 59}},
		{"tier 5 pack, unseated: 0.43 paid, 0.16 pack discount", eur(59, 59, 0, false), policy30,
			[]Lot{packLot(1000, 43, 35)}, nil, want{0, 43, 16, 0}},
		{"tier 5 pack, seated: 0.43 + 0.35 surcharge from the same balance", eur(109, 59, 50, true), policy30,
			[]Lot{packLot(1000, 43, 35)}, nil, want{0, 78, 31, 0}},
		{"bonus only: 30 % of 0.59 rounded down, rest invoiced", eur(59, 59, 0, false), policy30,
			nil, []Lot{bonusLot(1000)}, want{17, 0, 0, 42}},
		{"bonus and pack: 30 % of the tier price", eur(59, 59, 0, false), policy30,
			[]Lot{packLot(1000, 43, 35)}, []Lot{bonusLot(1000)}, want{12, 31, 16, 0}},
		{"seated: the bonus never pays the surcharge", eur(109, 59, 50, true), policy30,
			[]Lot{packLot(1000, 43, 35)}, []Lot{bonusLot(1000)}, want{12, 66, 31, 0}},
		{"seated and bonus covers surcharge", eur(109, 59, 50, true),
			Policy{Currency: "EUR", BonusShareCapBP: 3000, BonusCoversSurcharge: true},
			[]Lot{packLot(1000, 43, 35)}, []Lot{bonusLot(1000)}, want{23, 55, 31, 0}},
		{"revenue floor cuts the bonus share", eur(59, 59, 0, false),
			Policy{Currency: "EUR", BonusShareCapBP: 3000, FloorPerTicketMinor: &floor40},
			[]Lot{packLot(1000, 43, 35)}, []Lot{bonusLot(1000)}, want{3, 40, 16, 0}},
		{"bonus lots spend in order, a small one first", eur(59, 59, 0, false), policy30,
			nil, []Lot{bonusLot(5), bonusLot(1000)}, want{17, 0, 0, 42}},
		{"a lot runs out: the rest at the next tier in proportion", eur(59, 59, 0, false), policy30,
			[]Lot{packLot(20, 43, 35), packLot(1000, 55, 35)}, nil, want{0, 49, 10, 0}},
		{"a lot runs out and nothing follows: the rest by the invoice", eur(59, 59, 0, false), policy30,
			[]Lot{packLot(20, 43, 35)}, nil, want{0, 20, 8, 31}},
		{"individual rate: packs at face", ChargeInput{Currency: "EUR", AmountMinor: 100, UnseatedFactMinor: 100,
			TierPriced: true, BonusAllowed: true, NoTierDiscount: true}, policy30,
			[]Lot{packLot(1000, 43, 35)}, nil, want{0, 100, 0, 0}},
		{"low price: paid at its own amount, no bonus", ChargeInput{Currency: "EUR", AmountMinor: 25, UnseatedFactMinor: 25},
			policy30, []Lot{packLot(1000, 43, 35)}, []Lot{bonusLot(1000)}, want{0, 25, 0, 0}},
		{"a tier dearer than pay-as-you-go is capped at it", eur(59, 59, 0, false), policy30,
			[]Lot{packLot(1000, 80, 35)}, nil, want{0, 59, 0, 0}},
		{"empty lots are skipped", eur(59, 59, 0, false), policy30,
			[]Lot{packLot(0, 43, 35), packLot(1000, 52, 35)}, []Lot{bonusLot(0)}, want{0, 52, 7, 0}},
		{"zero charge needs nothing", eur(0, 0, 0, false), policy30,
			[]Lot{packLot(1000, 43, 35)}, []Lot{bonusLot(1000)}, want{0, 0, 0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustPlan(t, tc.c, tc.pol, tc.p, tc.b)
			g := want{got.BonusMinor(), got.PurchasedMinor(), got.DiscountMinor, got.UncoveredMinor}
			if g != tc.want {
				t.Errorf("got %+v, want %+v", g, tc.want)
			}
		})
	}
}

func TestMakePlan_RefusesBadInput(t *testing.T) {
	other := packLot(100, 43, 35)
	other.Currency = "USD"
	bad := []struct {
		name string
		c    ChargeInput
		pol  Policy
		p    []Lot
	}{
		{"lot in another currency", eur(59, 59, 0, false), policy30, []Lot{other}},
		{"negative charge", eur(-1, 0, 0, false), policy30, nil},
		{"parts above the charge", eur(59, 59, 10, true), policy30, nil},
		{"cap above 100 %", eur(59, 59, 0, false), Policy{Currency: "EUR", BonusShareCapBP: 10001}, nil},
		{"no currency", ChargeInput{AmountMinor: 59, UnseatedFactMinor: 59}, policy30, nil},
	}
	for _, tc := range bad {
		if _, err := MakePlan(tc.c, tc.pol, tc.p, nil); !errors.Is(err, ErrBadPlanInput) {
			t.Errorf("%s: err = %v, want ErrBadPlanInput", tc.name, err)
		}
	}
}

// TestMakePlan_Invariants checks on random inputs that a plan never spends
// more than a lot holds, never gives the bonus more than its share, and
// always adds up to the charge with a non-negative discount.
func TestMakePlan_Invariants(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 20000; i++ {
		u := rng.Int63n(500)
		s := int64(0)
		seated := rng.Intn(2) == 0
		if seated {
			s = rng.Int63n(300)
		}
		c := eur(u+s, u, s, seated)
		c.NoTierDiscount = rng.Intn(5) == 0
		c.TierPriced = rng.Intn(4) != 0
		c.BonusAllowed = rng.Intn(4) != 0
		pol := Policy{Currency: "EUR", BonusShareCapBP: rng.Int63n(10001), BonusCoversSurcharge: rng.Intn(2) == 0}
		if rng.Intn(3) == 0 {
			f := rng.Int63n(200)
			pol.FloorPerTicketMinor = &f
		}
		var p, b []Lot
		for j := rng.Intn(4); j > 0; j-- {
			p = append(p, packLot(rng.Int63n(200), rng.Int63n(120), rng.Int63n(80)))
		}
		for j := rng.Intn(3); j > 0; j-- {
			b = append(b, bonusLot(rng.Int63n(100)))
		}
		got := mustPlan(t, c, pol, p, b)
		if got.DiscountMinor < 0 || got.UncoveredMinor < 0 {
			t.Fatalf("negative part: %+v", got)
		}
		spent := map[uuid.UUID]int64{}
		for _, part := range append(append([]Part(nil), got.Bonus...), got.Purchased...) {
			if part.AmountMinor <= 0 {
				t.Fatalf("non-positive part %+v", part)
			}
			spent[part.LotID] += part.AmountMinor
		}
		for _, l := range append(append([]Lot(nil), p...), b...) {
			if spent[l.ID] > l.RemainingMinor {
				t.Fatalf("lot %s spent %d of %d", l.ID, spent[l.ID], l.RemainingMinor)
			}
		}
		if !c.BonusAllowed && got.BonusMinor() != 0 {
			t.Fatalf("bonus on a charge that allows none: %+v", got)
		}
		if got.BonusMinor()*10000 > c.AmountMinor*pol.BonusShareCapBP {
			t.Fatalf("bonus %d above %d bp of %d", got.BonusMinor(), pol.BonusShareCapBP, c.AmountMinor)
		}
		if len(p) == 0 && got.DiscountMinor != 0 {
			t.Fatalf("pack discount without a purchased lot: %+v", got)
		}
	}
}
