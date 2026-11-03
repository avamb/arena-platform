package smartprice

import (
	"testing"
	"time"
)

func TestStepPrice_LinearRoundedUpAndCapped(t *testing.T) {
	cases := []struct {
		base     int64
		k        int
		step     int
		cap      int
		currency string
		want     int64
	}{
		{2000, 0, 10, 50, "EUR", 2000},
		{2000, 1, 10, 50, "EUR", 2200},
		{2000, 3, 10, 50, "EUR", 2600}, // linear: 20 + 3x2, not 20x1.1^3
		{2000, 9, 10, 50, "EUR", 3000}, // capped
		{1990, 1, 10, 50, "EUR", 2200}, // 21.89 rounds up to 22
		{1500, 1, 10, 50, "JPY", 1650}, // no minor units
		{1999, 0, 10, 50, "EUR", 1999}, // the starting price stays as typed
	}
	for _, c := range cases {
		if got := StepPrice(c.base, c.k, c.step, c.cap, c.currency); got != c.want {
			t.Errorf("StepPrice(%d,%d,%d,%d,%s) = %d, want %d", c.base, c.k, c.step, c.cap, c.currency, got, c.want)
		}
	}
}

func TestExpandBySales_SpecExample(t *testing.T) {
	// Spec 3.1: 300 places, 20 EUR, +10 % every 50 tickets, cap +50 %.
	steps, err := ExpandBySales(Params{Kind: KindBySales, StepPercent: 10, Every: 50, CapPercent: 50},
		SalesInput{Base: 2000, Currency: "EUR", Owned: 300})
	if err != nil {
		t.Fatal(err)
	}
	want := []SalesStep{
		{Limit: 50, Places: 50, Price: 2000},
		{Limit: 50, Places: 50, Price: 2200},
		{Limit: 50, Places: 50, Price: 2400},
		{Limit: 50, Places: 50, Price: 2600},
		{Limit: 50, Places: 50, Price: 2800},
		{Limit: 0, Places: 50, Price: 3000},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps = %+v", steps)
	}
	var places int32
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
		places += steps[i].Places
	}
	if places != 300 {
		t.Errorf("the chain sells %d places, the hall has 300", places)
	}
}

func TestExpandBySales_CountsAlreadySoldAndStopsAtPlaces(t *testing.T) {
	steps, err := ExpandBySales(Params{Kind: KindBySales, StepPercent: 10, Every: 50, CapPercent: 100},
		SalesInput{Base: 2000, Currency: "EUR", Owned: 130, Used: 30})
	if err != nil {
		t.Fatal(err)
	}
	// 100 free: the first step sells 50 MORE (limit 30 + 50), the second the rest.
	if len(steps) != 2 || steps[0].Limit != 80 || steps[0].Places != 50 || steps[1].Limit != 0 || steps[1].Places != 50 || steps[1].Price != 2200 {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestExpandBySales_Refusals(t *testing.T) {
	p := Params{Kind: KindBySales, StepPercent: 10, Every: 50, CapPercent: 50}
	if _, err := ExpandBySales(p, SalesInput{Base: 2000, Currency: "EUR", Owned: 50}); err != ErrNotEnoughPlaces {
		t.Errorf("50 places for a 50-ticket step: %v", err)
	}
	if _, err := ExpandBySales(p, SalesInput{Base: 0, Currency: "EUR", Owned: 500}); err != ErrFreeCategory {
		t.Errorf("free category: %v", err)
	}
	bad := []Params{
		{Kind: "x", StepPercent: 10, Every: 5, CapPercent: 50},
		{Kind: KindBySales, StepPercent: 0, Every: 5, CapPercent: 50},
		{Kind: KindBySales, StepPercent: 10, Every: 0, CapPercent: 50},
		{Kind: KindBySales, StepPercent: 10, Every: 5, CapPercent: 5},
		{Kind: KindBySales, StepPercent: 10, Every: 5, CapPercent: 50, StartDate: "2026-11-05"},
	}
	for _, b := range bad {
		if err := b.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil", b)
		}
	}
}

func TestExpandBySales_StepsBounded(t *testing.T) {
	steps, err := ExpandBySales(Params{Kind: KindBySales, StepPercent: 1, Every: 1, CapPercent: 500},
		SalesInput{Base: 100000, Currency: "EUR", Owned: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != MaxSteps || steps[len(steps)-1].Limit != 0 {
		t.Fatalf("%d steps, last %+v", len(steps), steps[len(steps)-1])
	}
}

func TestExpandByDays_SpecExample(t *testing.T) {
	madrid, _ := time.LoadLocation("Europe/Madrid")
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, madrid)
	salesEnd := time.Date(2026, 11, 30, 20, 0, 0, 0, madrid)
	ws, err := ExpandByDays(Params{Kind: KindByDays, StepPercent: 10, Every: 7, CapPercent: 50, StartDate: "2026-11-03"},
		DaysInput{Base: 2000, Currency: "EUR", Loc: madrid, SessionStart: salesEnd, SalesEnd: salesEnd, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := []string{"2026-11-10", "2026-11-17", "2026-11-24"}
	wantPrice := []int64{2200, 2400, 2600}
	if len(ws) != 3 {
		t.Fatalf("windows = %+v", ws)
	}
	for i := range ws {
		if got := ws[i].From.In(madrid).Format("2006-01-02 15:04"); got != wantFrom[i]+" 00:00" { // allow:timeformat: test
			t.Errorf("window %d from %s", i, got)
		}
		if ws[i].Price != wantPrice[i] {
			t.Errorf("window %d price %d", i, ws[i].Price)
		}
	}
	if ws[0].To == nil || !ws[0].To.Equal(ws[1].From) || ws[2].To != nil {
		t.Errorf("windows are not contiguous: %+v", ws)
	}
}

func TestExpandByDays_StopsAtCapAndRefusesPast(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	end := now.AddDate(1, 0, 0)
	ws, err := ExpandByDays(Params{Kind: KindByDays, StepPercent: 25, Every: 1, CapPercent: 50},
		DaysInput{Base: 1000, Currency: "EUR", SessionStart: end, SalesEnd: end, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[1].Price != 1500 || ws[1].To != nil {
		t.Fatalf("windows = %+v", ws)
	}
	if _, err := ExpandByDays(Params{Kind: KindByDays, StepPercent: 10, Every: 1, CapPercent: 50, StartDate: "2026-11-01"},
		DaysInput{Base: 1000, Currency: "EUR", SessionStart: end, SalesEnd: end, Now: now}); err != ErrTooLate {
		t.Errorf("start in the past: %v", err)
	}
	if _, err := ExpandByDays(Params{Kind: KindByDays, StepPercent: 10, Every: 30, CapPercent: 50},
		DaysInput{Base: 1000, Currency: "EUR", SessionStart: now.AddDate(0, 0, 3), SalesEnd: now.AddDate(0, 0, 3), Now: now}); err != ErrNoRoom {
		t.Errorf("no room: %v", err)
	}
}

func TestExpandLastDays(t *testing.T) {
	madrid, _ := time.LoadLocation("Europe/Madrid")
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, madrid)
	start := time.Date(2026, 11, 20, 20, 0, 0, 0, madrid)
	ws, err := ExpandByDays(Params{Kind: KindLastDays, StepPercent: 20, Every: 3},
		DaysInput{Base: 2000, Currency: "EUR", Loc: madrid, SessionStart: start, SalesEnd: start, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Price != 2400 || ws[0].To != nil || !ws[0].From.Equal(time.Date(2026, 11, 17, 0, 0, 0, 0, madrid)) {
		t.Fatalf("windows = %+v", ws)
	}
	if _, err := ExpandByDays(Params{Kind: KindLastDays, StepPercent: 20, Every: 30},
		DaysInput{Base: 2000, Currency: "EUR", Loc: madrid, SessionStart: start, SalesEnd: start, Now: now}); err != ErrTooLate {
		t.Errorf("30 days before a show in 18 days: %v", err)
	}
}
