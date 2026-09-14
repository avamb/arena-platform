//go:build integration

// import_quota_47_integration_test.go — live-DB coverage for step 7 of plan
// 08_architecture/23 on the Bil24-format package importer (decision 7):
//
//   - the FIRST import of a category materializes its places from the
//     package's `availability`, so an imported session can actually sell
//     (before this it was created with no place at all — "sold out" from the
//     first minute);
//   - a REPEAT import never touches a quantity: `availability` on the wire is
//     the source system's REMAINDER, and re-applying it would subtract that
//     system's sales on top of arena's. Price and sort order do keep updating;
//   - a category that vanished from the package is CLOSED, never deleted;
//   - a category that is NEW in a repeat package is created with its places;
//   - `availability: 0` on a first import creates a CLOSED category with a
//     quantity of 1 (a GA category with 0 is illegal — decision 3) and raises
//     import.category_sold_out.
//
// Requires DATABASE_URL against a migrated database (see AGENTS.md).
package himports

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// quota47Category is one category's stored state after an import.
type quota47Category struct {
	Capacity *int32
	Price    int64
	IsOpen   bool
	Places   int64
}

func quota47Read(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierID uuid.UUID) quota47Category {
	t.Helper()
	var out quota47Category
	if err := pool.QueryRow(ctx, `
		SELECT tt.capacity, tt.price_amount, tt.is_open,
		       (SELECT count(*) FROM session_seats ss
		         WHERE ss.tier_id = tt.id AND ss.kind = 'ga_unit')
		FROM   ticket_tiers tt
		WHERE  tt.id = $1`, tierID).
		Scan(&out.Capacity, &out.Price, &out.IsOpen, &out.Places); err != nil {
		t.Fatalf("read category %s: %v", tierID, err)
	}
	return out
}

func quota47SessionCapacity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) (int32, int32) {
	t.Helper()
	var sessionCap, ledgerCap int32
	if err := pool.QueryRow(ctx, `
		SELECT s.capacity_total,
		       COALESCE((SELECT il.capacity_total FROM inventory_ledger il
		                  WHERE il.session_id = s.id AND il.tier_id IS NULL), 0)
		FROM   sessions s WHERE s.id = $1`, sessionID).Scan(&sessionCap, &ledgerCap); err != nil {
		t.Fatalf("read session capacity: %v", err)
	}
	return sessionCap, ledgerCap
}

// TestGA47_ImportFirstMintsPlaces_RepeatKeepsQuantities is the core decision-7
// guarantee across two imports of the same action event.
func TestGA47_ImportFirstMintsPlaces_RepeatKeepsQuantities(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newImport517Fixture(t, ctx, pool)
	defer f.cleanup()

	h := New(gen.New(pool), pool, nil, nil).WithMembershipQueries(gen.New(pool))

	rec, first := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("first import: status = %d; body = %s", rec.Code, rec.Body.String())
	}
	tierA := first.TierIDs[externalIDString(f.categoryA)]
	tierB := first.TierIDs[externalIDString(f.categoryB)]
	if tierA == uuid.Nil || tierB == uuid.Nil {
		t.Fatalf("first import: tier_ids = %+v", first.TierIDs)
	}

	a := quota47Read(t, ctx, pool, tierA)
	b := quota47Read(t, ctx, pool, tierB)
	if a.Places != 100 || b.Places != 40 {
		t.Fatalf("first import places = %d / %d, want 100 / 40 — an imported "+
			"session that owns no place cannot sell a ticket", a.Places, b.Places)
	}
	if a.Capacity == nil || *a.Capacity != 100 || b.Capacity == nil || *b.Capacity != 40 {
		t.Fatalf("first import quantities = %v / %v, want 100 / 40", a.Capacity, b.Capacity)
	}
	if sessionCap, ledgerCap := quota47SessionCapacity(t, ctx, pool, first.SessionID); sessionCap != 140 || ledgerCap != 140 {
		t.Fatalf("first import capacity = %d (ledger %d), want 140 (100 + 40)", sessionCap, ledgerCap)
	}

	// ── repeat import: a smaller remainder and a new price ────────────────
	repeat := f.payload()
	repeat.CategoryList[0].Availability = 17 // the source system has sold 83
	repeat.CategoryList[0].Price = 30
	repeat.CategoryList[1].Availability = 0 // sold out upstream
	rec, second := f.call(h, repeat)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat import: status = %d; body = %s", rec.Code, rec.Body.String())
	}
	if second.Created {
		t.Errorf("repeat import: created = true, want false")
	}

	a = quota47Read(t, ctx, pool, tierA)
	b = quota47Read(t, ctx, pool, tierB)
	if a.Capacity == nil || *a.Capacity != 100 || a.Places != 100 {
		t.Errorf("repeat import changed a quantity: capacity = %v, places = %d, want 100 / 100",
			a.Capacity, a.Places)
	}
	if b.Capacity == nil || *b.Capacity != 40 || b.Places != 40 {
		t.Errorf("repeat import changed a quantity: capacity = %v, places = %d, want 40 / 40",
			b.Capacity, b.Places)
	}
	if a.Price != 3000 {
		t.Errorf("repeat import price = %d, want 3000 (price DOES keep updating)", a.Price)
	}
	if sessionCap, _ := quota47SessionCapacity(t, ctx, pool, first.SessionID); sessionCap != 140 {
		t.Errorf("repeat import capacity = %d, want 140 unchanged", sessionCap)
	}
}

// TestGA47_ImportClosesMissingCategoryAndMintsNewOne covers the other two
// halves of decision 7: a category the package stopped mentioning is closed
// rather than deleted, and one that appears for the first time in a repeat
// package gets its own places.
func TestGA47_ImportClosesMissingCategoryAndMintsNewOne(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newImport517Fixture(t, ctx, pool)
	defer f.cleanup()

	// A third Bil24 category id, outside the fixture's own cleanup list.
	categoryC := f.categoryB + 1
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM compatibility_id_map WHERE system_id = $1`, categoryC); err != nil {
			t.Logf("GA47 cleanup compat id: %v", err)
		}
	})

	h := New(gen.New(pool), pool, nil, nil).WithMembershipQueries(gen.New(pool))

	rec, first := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("first import: status = %d; body = %s", rec.Code, rec.Body.String())
	}
	tierA := first.TierIDs[externalIDString(f.categoryA)]
	tierB := first.TierIDs[externalIDString(f.categoryB)]

	// Second package: Parter stays, Balcony is gone, a brand-new category
	// arrives with 7 places.
	repeat := f.payload()
	repeat.CategoryList = []bil24compat.ImportSessionCategory{
		{CategoryPriceID: f.categoryA, CategoryPriceName: "Parter", Price: 25, Availability: 100},
		{CategoryPriceID: categoryC, CategoryPriceName: "Loge", Price: 40, Availability: 7},
	}
	rec, second := f.call(h, repeat)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat import: status = %d; body = %s", rec.Code, rec.Body.String())
	}
	tierC := second.TierIDs[externalIDString(categoryC)]
	if tierC == uuid.Nil {
		t.Fatalf("repeat import: tier_ids = %+v, want an entry for the new category", second.TierIDs)
	}

	if a := quota47Read(t, ctx, pool, tierA); !a.IsOpen || a.Places != 100 {
		t.Errorf("mentioned category: is_open = %v, places = %d, want open / 100", a.IsOpen, a.Places)
	}
	b := quota47Read(t, ctx, pool, tierB)
	if b.IsOpen {
		t.Errorf("a category missing from the package stayed open, want closed")
	}
	if b.Places != 40 {
		t.Errorf("a category missing from the package lost places: %d, want 40 kept", b.Places)
	}
	if c := quota47Read(t, ctx, pool, tierC); c.Places != 7 || c.Capacity == nil || *c.Capacity != 7 {
		t.Errorf("new category: places = %d, capacity = %v, want 7 / 7", c.Places, c.Capacity)
	}
	if sessionCap, ledgerCap := quota47SessionCapacity(t, ctx, pool, first.SessionID); sessionCap != 147 || ledgerCap != 147 {
		t.Errorf("capacity = %d (ledger %d), want 147 (100 + 40 + 7)", sessionCap, ledgerCap)
	}
}

// TestGA47_ImportZeroAvailabilityCreatesClosedCategory pins decision 7's
// sold-out branch: a quantity of 0 is illegal, so the category is created
// closed with a quantity of 1 and the operator is told.
func TestGA47_ImportZeroAvailabilityCreatesClosedCategory(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newImport517Fixture(t, ctx, pool)
	defer f.cleanup()

	h := New(gen.New(pool), pool, nil, nil).WithMembershipQueries(gen.New(pool))

	body := f.payload()
	body.CategoryList[1].Availability = 0
	rec, out := f.call(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: status = %d; body = %s", rec.Code, rec.Body.String())
	}

	sold := quota47Read(t, ctx, pool, out.TierIDs[externalIDString(f.categoryB)])
	if sold.IsOpen {
		t.Errorf("a category declared with availability 0 is open, want closed")
	}
	if sold.Capacity == nil || *sold.Capacity != 1 || sold.Places != 1 {
		t.Errorf("sold-out category: capacity = %v, places = %d, want 1 / 1",
			sold.Capacity, sold.Places)
	}
	found := false
	for _, warn := range out.Warnings {
		if warn.Code == WarnCategorySoldOut {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, want %s", out.Warnings, WarnCategorySoldOut)
	}
}
