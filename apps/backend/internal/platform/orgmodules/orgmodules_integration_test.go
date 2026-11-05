//go:build integration

package orgmodules_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func ptr[T any](v T) *T { return &v }

type fixture struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &fixture{pool: pool, q: gen.New(pool)}
}

// org creates an organization with a random slug (slugs are globally unique)
// and removes it, with everything this test put under it, when the test ends.
func (f *fixture) org(t *testing.T) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	nonce := uuid.NewString()
	var id uuid.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id`,
		"orgmodules "+nonce, "orgmodules-"+nonce).Scan(&id); err != nil {
		t.Fatalf("org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.pool.Exec(bg, `DELETE FROM shop_orders WHERE org_id = $1`, id)
		_, _ = f.pool.Exec(bg, `DELETE FROM org_module_periods WHERE org_id = $1`, id)
		_, _ = f.pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, id)
	})
	return id
}

// shopOrder inserts a shop order (a random token: the index on it is global).
func (f *fixture) shopOrder(t *testing.T, org uuid.UUID, status, delivery string, shipped bool) {
	t.Helper()
	token := fmt.Sprintf("%032x%032x", time.Now().UnixNano(), uuid.New().ID())
	var shippedAt *time.Time
	if shipped {
		shippedAt = ptr(time.Now())
	}
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO shop_orders (org_id, order_token, status, delivery_kind, currency, subtotal, total, shipped_at)
		VALUES ($1, $2, $3, $4, 'EUR', 100, 100, $5)`, org, token[:64], status, delivery, shippedAt); err != nil {
		t.Fatalf("shop order %s/%s: %v", status, delivery, err)
	}
}

func (f *fixture) clearOrders(t *testing.T, org uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM shop_orders WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
}

func TestConnectOverlapEndAndList(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	org := f.org(t)

	connect := func(in orgmodules.ConnectInput) (orgmodules.Period, error) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		p, err := orgmodules.Connect(ctx, tx, in)
		if err != nil {
			return p, err
		}
		return p, tx.Commit(ctx)
	}
	actor := uuid.New()
	first, err := connect(orgmodules.ConnectInput{OrgID: org, Module: orgmodules.ModuleShop,
		StartsOn: day(2026, 11, 1), EndsOn: ptr(day(2026, 11, 30)), Reason: "pilot", ActorID: &actor})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if first.MonthlyPriceMinor != nil || first.Currency != nil || first.CreatedBy == nil || *first.CreatedBy != actor {
		t.Errorf("period = %+v", first)
	}

	// Overlaps: inside, straddling the end, containing, open-ended.
	for name, in := range map[string]orgmodules.ConnectInput{
		"inside":    {StartsOn: day(2026, 11, 10), EndsOn: ptr(day(2026, 11, 12))},
		"straddle":  {StartsOn: day(2026, 11, 30), EndsOn: ptr(day(2026, 12, 5))},
		"contains":  {StartsOn: day(2026, 10, 1), EndsOn: ptr(day(2026, 12, 31))},
		"open-ends": {StartsOn: day(2026, 10, 1)},
	} {
		in.OrgID, in.Module, in.Reason = org, orgmodules.ModuleShop, "x"
		if _, err := connect(in); !errors.Is(err, orgmodules.ErrOverlap) {
			t.Errorf("%s: err = %v, want ErrOverlap", name, err)
		}
	}
	// Touching without sharing a day is fine, before and after.
	if _, err := connect(orgmodules.ConnectInput{OrgID: org, Module: orgmodules.ModuleShop,
		StartsOn: day(2026, 10, 1), EndsOn: ptr(day(2026, 10, 31)), Reason: "before"}); err != nil {
		t.Errorf("adjacent before: %v", err)
	}
	if _, err := connect(orgmodules.ConnectInput{OrgID: org, Module: orgmodules.ModuleShop,
		StartsOn: day(2026, 12, 1), MonthlyPriceMinor: ptr(int64(1500)), Currency: ptr("eur"), Reason: "after"}); err != nil {
		t.Errorf("adjacent after: %v", err)
	}
	// Another organization is not affected.
	if _, err := connect(orgmodules.ConnectInput{OrgID: f.org(t), Module: orgmodules.ModuleShop,
		StartsOn: day(2026, 11, 10), Reason: "other org"}); err != nil {
		t.Errorf("other org: %v", err)
	}
	// Invalid input.
	if _, err := connect(orgmodules.ConnectInput{OrgID: org, Module: "wine", StartsOn: day(2027, 1, 1), Reason: "x"}); !errors.Is(err, orgmodules.ErrInvalid) {
		t.Errorf("unknown module: %v", err)
	}

	// End: shorten the open period, then refuse to extend or repeat.
	listed, err := orgmodules.List(ctx, f.q, org)
	if err != nil || len(listed) != 3 {
		t.Fatalf("list = %d periods (%v)", len(listed), err)
	}
	open := listed[2]
	if open.EndsOn != nil || open.Currency == nil || *open.Currency != "EUR" {
		t.Fatalf("open period = %+v", open)
	}
	end := func(id uuid.UUID, on time.Time) (orgmodules.Period, error) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		p, err := orgmodules.End(ctx, tx, id, on, "wrap up", &actor)
		if err != nil {
			return p, err
		}
		return p, tx.Commit(ctx)
	}
	ended, err := end(open.ID, day(2026, 12, 31))
	if err != nil || ended.EndsOn == nil || !ended.EndsOn.Equal(day(2026, 12, 31)) || ended.EndedBy == nil || ended.EndedAt == nil {
		t.Fatalf("end: %+v %v", ended, err)
	}
	if _, err := end(open.ID, day(2027, 6, 1)); !errors.Is(err, orgmodules.ErrAlreadyEnded) {
		t.Errorf("extend: %v", err)
	}
	if _, err := end(open.ID, day(2026, 12, 31)); !errors.Is(err, orgmodules.ErrAlreadyEnded) {
		t.Errorf("repeat: %v", err)
	}
	if _, err := end(open.ID, day(2026, 11, 29)); !errors.Is(err, orgmodules.ErrInvalid) {
		t.Errorf("before start: %v", err)
	}
	if _, err := end(uuid.New(), day(2026, 12, 1)); !errors.Is(err, orgmodules.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	// An end the day before the start makes the period empty: its days are
	// free to connect again.
	if _, err := end(open.ID, day(2026, 11, 30)); err != nil {
		t.Errorf("end to the day before start: %v", err)
	}
	if _, err := connect(orgmodules.ConnectInput{OrgID: org, Module: orgmodules.ModuleShop,
		StartsOn: day(2026, 12, 1), Reason: "again"}); err != nil {
		t.Errorf("reconnect after an emptied period: %v", err)
	}
}

func TestRepriceSplitsThePeriod(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	org := f.org(t)
	actor := uuid.New()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := orgmodules.Connect(ctx, tx, orgmodules.ConnectInput{OrgID: org, Module: orgmodules.ModuleShop,
		StartsOn: day(2026, 11, 1), EndsOn: ptr(day(2027, 3, 31)), MonthlyPriceMinor: ptr(int64(0)), Currency: ptr("EUR"), Reason: "free"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	reprice := func(id uuid.UUID, from time.Time, price *int64, cur *string) (orgmodules.Period, error) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		p, err := orgmodules.Reprice(ctx, tx, id, from, price, cur, "new deal", &actor)
		if err != nil {
			return p, err
		}
		return p, tx.Commit(ctx)
	}
	// Invalid price pair changes nothing.
	if _, err := reprice(orig.ID, day(2026, 12, 1), ptr(int64(900)), nil); !errors.Is(err, orgmodules.ErrInvalid) {
		t.Errorf("price without currency: %v", err)
	}
	if _, err := reprice(orig.ID, day(2026, 10, 1), ptr(int64(900)), ptr("EUR")); !errors.Is(err, orgmodules.ErrInvalid) {
		t.Errorf("from before start: %v", err)
	}
	if _, err := reprice(orig.ID, day(2027, 4, 1), ptr(int64(900)), ptr("EUR")); !errors.Is(err, orgmodules.ErrAlreadyEnded) {
		t.Errorf("from after end: %v", err)
	}
	if _, err := reprice(uuid.New(), day(2026, 12, 1), nil, nil); !errors.Is(err, orgmodules.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	periods, _ := orgmodules.List(ctx, f.q, org)
	if len(periods) != 1 || periods[0].EndsOn == nil || !periods[0].EndsOn.Equal(day(2027, 3, 31)) {
		t.Fatalf("a refused reprice changed something: %+v", periods)
	}

	np, err := reprice(orig.ID, day(2026, 12, 1), ptr(int64(900)), ptr("eur"))
	if err != nil {
		t.Fatalf("reprice: %v", err)
	}
	if np.ID == orig.ID || !np.StartsOn.Equal(day(2026, 12, 1)) || np.EndsOn == nil || !np.EndsOn.Equal(day(2027, 3, 31)) ||
		np.MonthlyPriceMinor == nil || *np.MonthlyPriceMinor != 900 || *np.Currency != "EUR" || np.Reason != "new deal" {
		t.Errorf("new period = %+v", np)
	}
	periods, _ = orgmodules.List(ctx, f.q, org)
	if len(periods) != 2 || periods[0].ID != orig.ID || periods[0].EndsOn == nil || !periods[0].EndsOn.Equal(day(2026, 11, 30)) ||
		periods[0].EndedBy == nil {
		t.Fatalf("old period after reprice: %+v", periods)
	}
	// Exactly one period covers every day, no gap at the seam.
	for _, c := range []struct {
		d    time.Time
		want int64
	}{{day(2026, 11, 30), 0}, {day(2026, 12, 1), 900}, {day(2027, 3, 31), 900}} {
		n := 0
		var price int64 = -1
		for _, p := range periods {
			if p.Covers(c.d) {
				n++
				price = *p.MonthlyPriceMinor
			}
		}
		if n != 1 || price != c.want {
			t.Errorf("%v: %d periods, price %d, want 1 at %d", c.d, n, price, c.want)
		}
	}

	// Reprice an open-ended period from its own first day: the old one
	// becomes empty and the new one takes over.
	org2 := f.org(t)
	tx, _ = f.pool.Begin(ctx)
	p2, err := orgmodules.Connect(ctx, tx, orgmodules.ConnectInput{OrgID: org2, Module: orgmodules.ModuleShop, StartsOn: day(2026, 11, 1), Reason: "list price"})
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(ctx)
	np2, err := reprice(p2.ID, day(2026, 11, 1), nil, nil)
	if err != nil || np2.EndsOn != nil || np2.MonthlyPriceMinor != nil {
		t.Fatalf("reprice from first day: %+v %v", np2, err)
	}
	act, _ := orgmodules.Active(ctx, f.q, org2, orgmodules.ModuleShop, day(2026, 11, 1))
	if !act {
		t.Error("module not active after repricing from the first day")
	}
}

func TestActiveAndShopMode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := day(2026, 11, 15).Add(13 * time.Hour)

	connect := func(org uuid.UUID, from time.Time, to *time.Time) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := orgmodules.Connect(ctx, tx, orgmodules.ConnectInput{OrgID: org, Module: orgmodules.ModuleShop, StartsOn: from, EndsOn: to, Reason: "t"}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mode := func(org uuid.UUID) orgmodules.Mode {
		m, err := orgmodules.ShopMode(ctx, f.q, org, now)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	// No module, no orders: none.
	none := f.org(t)
	if m := mode(none); m != orgmodules.ModeNone {
		t.Errorf("no module: %q", m)
	}
	if mods, err := orgmodules.ActiveModules(ctx, f.q, none, now); err != nil || len(mods) != 0 {
		t.Errorf("modules = %v %v", mods, err)
	}

	// Connected today: full, with or without orders.
	full := f.org(t)
	connect(full, day(2026, 11, 15), nil)
	f.shopOrder(t, full, "paid", "shipping", false)
	if m := mode(full); m != orgmodules.ModeFull {
		t.Errorf("connected: %q", m)
	}
	if mods, _ := orgmodules.ActiveModules(ctx, f.q, full, now); len(mods) != 1 || mods[0] != orgmodules.ModuleShop {
		t.Errorf("modules = %v", mods)
	}
	// Scheduled for tomorrow: not active today.
	soon := f.org(t)
	connect(soon, day(2026, 11, 16), nil)
	if act, _ := orgmodules.Active(ctx, f.q, soon, orgmodules.ModuleShop, now); act {
		t.Error("a period starting tomorrow is active today")
	}
	if act, _ := orgmodules.Active(ctx, f.q, soon, orgmodules.ModuleShop, now.AddDate(0, 0, 1)); !act {
		t.Error("a period is not active on its first day")
	}

	// Ended yesterday: orders-only exactly while an order is open.
	off := f.org(t)
	connect(off, day(2026, 10, 1), ptr(day(2026, 11, 14)))
	if m := mode(off); m != orgmodules.ModeNone {
		t.Errorf("ended, no orders: %q", m)
	}
	closed := []struct {
		status, delivery string
		shipped          bool
	}{
		{"shipped", "shipping", true},
		{"paid", "none", false}, // digital: nothing to ship
		{"partially_refunded", "none", false},
		{"paid", "shipping", true}, // shipped_at set
		{"cancelled", "shipping", false},
		{"expired", "shipping", false},
		{"refunded", "shipping", false},
	}
	for _, c := range closed {
		f.shopOrder(t, off, c.status, c.delivery, c.shipped)
	}
	if m := mode(off); m != orgmodules.ModeNone {
		t.Errorf("only closed orders: %q", m)
	}
	if n, err := f.q.CountOpenShopOrders(ctx, off); err != nil || n != 0 {
		t.Errorf("open count = %d %v", n, err)
	}
	open := []struct{ status, delivery string }{
		{"pending_payment", "shipping"}, {"manual_review", "shipping"}, {"paid", "shipping"},
		{"paid", "pickup"}, {"partially_refunded", "shipping"}, {"pending_payment", "none"}, {"manual_review", "none"},
	}
	for i, c := range open {
		f.clearOrders(t, off)
		f.shopOrder(t, off, c.status, c.delivery, false)
		if m := mode(off); m != orgmodules.ModeOrdersOnly {
			t.Errorf("open order #%d (%s/%s): %q", i, c.status, c.delivery, m)
		}
		if n, _ := f.q.CountOpenShopOrders(ctx, off); n != 1 {
			t.Errorf("open count #%d = %d", i, n)
		}
	}
	// The last open order closes: the section is gone.
	f.clearOrders(t, off)
	f.shopOrder(t, off, "paid", "shipping", false)
	if _, err := f.pool.Exec(ctx, `UPDATE shop_orders SET status = 'shipped', shipped_at = now() WHERE org_id = $1`, off); err != nil {
		t.Fatal(err)
	}
	if m := mode(off); m != orgmodules.ModeNone {
		t.Errorf("after shipping the last order: %q", m)
	}

	// The batch answers for everyone at once, including an unknown id.
	f.shopOrder(t, none, "pending_payment", "shipping", false)
	ghost := uuid.New()
	states, err := orgmodules.States(ctx, f.q, []uuid.UUID{none, full, soon, off, ghost}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uuid.UUID]orgmodules.Mode{none: orgmodules.ModeOrdersOnly, full: orgmodules.ModeFull, soon: orgmodules.ModeNone, off: orgmodules.ModeNone, ghost: orgmodules.ModeNone}
	if len(states) != len(want) {
		t.Errorf("states = %d entries", len(states))
	}
	for id, m := range want {
		if states[id].ShopMode != m {
			t.Errorf("%v: batch mode %q, want %q", id, states[id].ShopMode, m)
		}
	}
	if !states[full].Has(orgmodules.ModuleShop) || states[none].Has(orgmodules.ModuleShop) || !states[none].OpenShopOrders {
		t.Errorf("states = %+v", states)
	}
}
