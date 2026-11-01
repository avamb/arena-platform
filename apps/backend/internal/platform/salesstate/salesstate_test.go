package salesstate

import (
	"testing"
	"time"
)

func TestEffective(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	past := now.Add(-time.Hour)
	selling := Facts{Status: "scheduled", SalesEnd: future, TotalPlaces: 100, FreePlaces: 66, Selling: true}

	cases := []struct {
		name   string
		manual string
		facts  Facts
		want   string
	}{
		{"selling", "", selling, Open},
		{"manual sold out with places left", SoldOut, selling, SoldOut},
		{"manual soon", Soon, selling, Soon},
		{"cancelled beats manual", SoldOut, Facts{Status: "cancelled", SalesEnd: future, TotalPlaces: 10, FreePlaces: 10, Selling: true}, Cancelled},
		{"manual beats ended", SoldOut, Facts{Status: "scheduled", SalesEnd: past, TotalPlaces: 10, FreePlaces: 10}, SoldOut},
		{"sales end passed", "", Facts{Status: "scheduled", SalesEnd: past, TotalPlaces: 10, FreePlaces: 10, Selling: true}, Ended},
		{"no categories is never sold out", "", Facts{Status: "scheduled", SalesEnd: future}, Soon},
		{"ledger-only categories stay open", "", Facts{Status: "scheduled", SalesEnd: future, Categories: 2}, Open},
		{"last place taken", "", Facts{Status: "scheduled", SalesEnd: future, Categories: 1, TotalPlaces: 10}, SoldOut},
		{"window not open yet", "", Facts{Status: "scheduled", SalesEnd: future, TotalPlaces: 10, FreePlaces: 10, OpensLater: true}, Soon},
		{"windows over", "", Facts{Status: "scheduled", SalesEnd: future, TotalPlaces: 10, FreePlaces: 10}, Ended},
		{"unknown manual ignored", "open", selling, Open},
	}
	for _, c := range cases {
		if got := Effective(c.manual, c.facts, now); got != c.want {
			t.Errorf("%s: Effective = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWire(t *testing.T) {
	for in, want := range map[string]string{Open: "open", Soon: "soon", SoldOut: "soldout", Ended: "ended", Cancelled: "ended"} {
		if got := Wire(in); got != want {
			t.Errorf("Wire(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidRequested(t *testing.T) {
	for _, s := range []string{Open, Soon, SoldOut} {
		if !ValidRequested(s) {
			t.Errorf("%q should be accepted", s)
		}
	}
	for _, s := range []string{"", "soldout", "ended", "cancelled", "OPEN"} {
		if ValidRequested(s) {
			t.Errorf("%q should be refused", s)
		}
	}
}
