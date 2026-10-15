//go:build integration

package himports

// event_bundle_read_review_integration_test.go — reviewer's adversarial round
// trip for task 3-2: two dates where only the SECOND carries a chain, a closed
// category on the first, and a category whose window simply ends with the
// session's sales end. The read is re-imported TWICE; the place counters of
// every category and the open flags must not move, and reads after the first
// and the second re-import must be equal.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

type reviewTier struct {
	places int64
	open   bool
	end    *time.Time
}

func reviewTiers(ctx context.Context, t *testing.T, f *bundle525Fixture, eventID uuid.UUID) map[string]reviewTier {
	t.Helper()
	rows, err := f.pool.Query(ctx,
		`SELECT s.id::text || '/' || tt.name, tt.is_open, tt.sale_window_end,
		        (SELECT count(*) FROM session_seats ss WHERE ss.session_id = s.id AND ss.tier_id = tt.id)
		   FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id
		  WHERE s.event_id = $1 AND tt.deleted_at IS NULL`, eventID)
	if err != nil {
		t.Fatalf("read tiers: %v", err)
	}
	defer rows.Close()
	out := map[string]reviewTier{}
	for rows.Next() {
		var k string
		var rt reviewTier
		if err := rows.Scan(&k, &rt.open, &rt.end, &rt.places); err != nil {
			t.Fatal(err)
		}
		out[k] = rt
	}
	return out
}

func TestEventBundleRead_ReviewTwoDatesChainOnSecondTwiceReimported(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	rec, one := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create first date: %d %s", rec.Code, rec.Body.String())
	}
	earlyEnd := time.Now().Add(time.Hour).Truncate(time.Second)
	chain := chainPayload(f, earlyEnd, 50)
	limit := int32(20)
	chain.CategoryList[1].SellLimit = &limit
	chain.ExternalRef = f.externalRef + ":chain"
	chain.Action.ArenaEventID = one.EventID.String()
	chain.ActionEvent.Day = "27.04.2026"
	if rec, _ = f.call(h, chain); rec.Code != http.StatusOK {
		t.Fatalf("create chain date: %d %s", rec.Code, rec.Body.String())
	}

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// A closed category, and a category whose window just ends with the sale.
	mustExec(`UPDATE ticket_tiers SET is_open = false WHERE session_id = $1 AND name = 'Balcony'`, one.SessionID)
	mustExec(`UPDATE ticket_tiers SET sale_window_end = (SELECT sales_end_at FROM sessions WHERE id = $1)
	           WHERE session_id = $1 AND name = 'Parter'`, one.SessionID)

	tiers0 := reviewTiers(ctx, t, f, one.EventID)
	first, firstRaw := f.readBundles(h, one.EventID)
	if len(first.Bundles) != 2 {
		t.Fatalf("read: %s", firstRaw)
	}
	f.reimport(h, first)
	tiers1 := reviewTiers(ctx, t, f, one.EventID)
	second, secondRaw := f.readBundles(h, one.EventID)
	f.reimport(h, second)
	tiers2 := reviewTiers(ctx, t, f, one.EventID)
	_, thirdRaw := f.readBundles(h, one.EventID)

	for k, before := range tiers0 {
		for label, after := range map[string]reviewTier{"1st": tiers1[k], "2nd": tiers2[k]} {
			if after.places != before.places {
				t.Errorf("%s re-import moved the places of %s: %d -> %d", label, k, before.places, after.places)
			}
			if after.open != before.open {
				t.Errorf("%s re-import changed the open flag of %s: %v -> %v", label, k, before.open, after.open)
			}
		}
		if (before.end == nil) != (tiers1[k].end == nil) || (before.end != nil && !before.end.Equal(*tiers1[k].end)) {
			t.Errorf("re-import changed sale_window_end of %s: %v -> %v", k, before.end, tiers1[k].end)
		}
	}
	if secondRaw != firstRaw {
		t.Errorf("read after the first re-import differs from the first read:\nfirst  %s\nsecond %s", firstRaw, secondRaw)
	}
	if thirdRaw != secondRaw {
		t.Errorf("read after the second re-import differs:\nsecond %s\nthird  %s", secondRaw, thirdRaw)
	}
}
