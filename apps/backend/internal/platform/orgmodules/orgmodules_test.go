package orgmodules

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func d(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }

func ptr[T any](v T) *T { return &v }

func TestConnectInputValidate(t *testing.T) {
	org := uuid.New()
	good := ConnectInput{OrgID: org, Module: ModuleShop, StartsOn: d(2026, 11, 5), Reason: " pilot "}
	cases := []struct {
		name string
		mod  func(*ConnectInput)
		ok   bool
	}{
		{"minimal", func(*ConnectInput) {}, true},
		{"free price", func(in *ConnectInput) { in.MonthlyPriceMinor, in.Currency = ptr(int64(0)), ptr("eur") }, true},
		{"open end day before start", func(in *ConnectInput) { in.EndsOn = ptr(d(2026, 11, 4)) }, true},
		{"end before day before start", func(in *ConnectInput) { in.EndsOn = ptr(d(2026, 11, 3)) }, false},
		{"no org", func(in *ConnectInput) { in.OrgID = uuid.Nil }, false},
		{"unknown module", func(in *ConnectInput) { in.Module = "white_label" }, false},
		{"empty module", func(in *ConnectInput) { in.Module = "" }, false},
		{"blank reason", func(in *ConnectInput) { in.Reason = "  \t" }, false},
		{"no start", func(in *ConnectInput) { in.StartsOn = time.Time{} }, false},
		{"price without currency", func(in *ConnectInput) { in.MonthlyPriceMinor = ptr(int64(5)) }, false},
		{"currency without price", func(in *ConnectInput) { in.Currency = ptr("EUR") }, false},
		{"negative price", func(in *ConnectInput) { in.MonthlyPriceMinor, in.Currency = ptr(int64(-1)), ptr("EUR") }, false},
		{"huge price", func(in *ConnectInput) { in.MonthlyPriceMinor, in.Currency = ptr(int64(2_000_000_000)), ptr("EUR") }, false},
		{"bad currency", func(in *ConnectInput) { in.MonthlyPriceMinor, in.Currency = ptr(int64(5)), ptr("EURO") }, false},
	}
	for _, c := range cases {
		in := good
		c.mod(&in)
		got, err := in.validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
		if c.ok && got.Reason != "pilot" && c.name == "minimal" {
			t.Errorf("reason not trimmed: %q", got.Reason)
		}
	}
	in := good
	in.StartsOn = time.Date(2026, 11, 5, 23, 59, 0, 0, time.FixedZone("x", -5*3600)) // 2026-11-06 04:59 UTC
	in.MonthlyPriceMinor, in.Currency = ptr(int64(100)), ptr(" czk ")
	got, err := in.validate()
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartsOn.Equal(d(2026, 11, 6)) || *got.Currency != "CZK" {
		t.Errorf("not normalized: %v %v", got.StartsOn, *got.Currency)
	}
}

func TestPeriodCoversUTCDays(t *testing.T) {
	p := Period{StartsOn: d(2026, 11, 5), EndsOn: ptr(d(2026, 11, 7))}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{
		{d(2026, 11, 4), false},
		{time.Date(2026, 11, 4, 23, 59, 59, 0, time.UTC), false},
		{d(2026, 11, 5), true},
		{time.Date(2026, 11, 7, 23, 59, 59, 0, time.UTC), true},
		{d(2026, 11, 8), false},
		// 2026-11-07 22:00 at UTC-5 is 2026-11-08 03:00 UTC: past the end.
		{time.Date(2026, 11, 7, 22, 0, 0, 0, time.FixedZone("x", -5*3600)), false},
	} {
		if got := p.Covers(c.t); got != c.want {
			t.Errorf("Covers(%v) = %v, want %v", c.t, got, c.want)
		}
	}
	if !(Period{StartsOn: d(2026, 1, 1)}).Covers(d(2099, 1, 1)) {
		t.Error("an open period covers the far future")
	}
	// An empty period (ends the day before it starts) covers nothing.
	empty := Period{StartsOn: d(2026, 11, 5), EndsOn: ptr(d(2026, 11, 4))}
	if empty.Covers(d(2026, 11, 4)) || empty.Covers(d(2026, 11, 5)) {
		t.Error("an empty period covers a day")
	}
}

func TestDecideShopMode(t *testing.T) {
	for _, c := range []struct {
		active, open bool // open: an open or an ever-paid order
		want         Mode
	}{
		{true, false, ModeFull},
		{true, true, ModeFull},
		{false, true, ModeOrdersOnly},
		{false, false, ModeNone},
	} {
		if got := decideShopMode(c.active, c.open); got != c.want {
			t.Errorf("decideShopMode(%v,%v) = %q, want %q", c.active, c.open, got, c.want)
		}
	}
}

func TestCheckEnd(t *testing.T) {
	open := Period{StartsOn: d(2026, 11, 5)}
	if err := checkEnd(open, d(2026, 11, 4)); err != nil {
		t.Errorf("end the day before start: %v", err)
	}
	if err := checkEnd(open, d(2026, 11, 3)); !errors.Is(err, ErrInvalid) {
		t.Errorf("end two days before start: %v", err)
	}
	closed := Period{StartsOn: d(2026, 11, 5), EndsOn: ptr(d(2026, 12, 1))}
	if err := checkEnd(closed, d(2026, 12, 1)); !errors.Is(err, ErrAlreadyEnded) {
		t.Errorf("same end: %v", err)
	}
	if err := checkEnd(closed, d(2026, 12, 2)); !errors.Is(err, ErrAlreadyEnded) {
		t.Errorf("later end: %v", err)
	}
	if err := checkEnd(closed, d(2026, 11, 30)); err != nil {
		t.Errorf("earlier end: %v", err)
	}
}

// fakeQ answers OrgModuleStates from a table and records what it was asked.
type fakeQ struct {
	states map[uuid.UUID]gen.OrgModuleStateRow
	calls  int
	day    time.Time
}

func (f *fakeQ) OrgModuleActive(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	return false, nil
}
func (f *fakeQ) ActiveOrgModules(context.Context, uuid.UUID, time.Time) ([]string, error) {
	return nil, nil
}
func (f *fakeQ) ListOrgModulePeriods(context.Context, uuid.UUID, *string) ([]gen.OrgModulePeriodRow, error) {
	return nil, nil
}
func (f *fakeQ) OrgModuleStates(_ context.Context, ids []uuid.UUID, day time.Time) ([]gen.OrgModuleStateRow, error) {
	f.calls++
	f.day = day
	var out []gen.OrgModuleStateRow
	for _, id := range ids {
		if r, ok := f.states[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestStatesBatchesAndDecidesMode(t *testing.T) {
	full, only, none, missing := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{states: map[uuid.UUID]gen.OrgModuleStateRow{
		full: {OrgID: full, Modules: []string{ModuleShop}, OpenShopOrders: true},
		only: {OrgID: only, OpenShopOrders: true},
		none: {OrgID: none},
	}}
	now := time.Date(2026, 11, 5, 23, 30, 0, 0, time.FixedZone("x", 2*3600)) // 21:30 UTC
	got, err := States(context.Background(), f, []uuid.UUID{full, only, none, missing}, now)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Errorf("queries = %d, want 1 for the whole batch", f.calls)
	}
	if !f.day.Equal(d(2026, 11, 5)) {
		t.Errorf("day = %v, want the UTC day", f.day)
	}
	for id, want := range map[uuid.UUID]Mode{full: ModeFull, only: ModeOrdersOnly, none: ModeNone, missing: ModeNone} {
		if got[id].ShopMode != want {
			t.Errorf("%v: mode %q, want %q", id, got[id].ShopMode, want)
		}
	}
	if !got[full].Has(ModuleShop) || got[only].Has(ModuleShop) {
		t.Error("Has is wrong")
	}
	if m, err := ShopMode(context.Background(), f, only, now); err != nil || m != ModeOrdersOnly {
		t.Errorf("ShopMode = %q, %v", m, err)
	}
	empty, err := States(context.Background(), f, nil, now)
	if err != nil || len(empty) != 0 || f.calls != 2 {
		t.Errorf("empty batch: %v %v calls=%d", empty, err, f.calls)
	}
}
