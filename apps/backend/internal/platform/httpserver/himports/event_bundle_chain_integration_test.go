//go:build integration

// event_bundle_chain_integration_test.go — the event-center additions to an
// arena-native bundle (import_chain.go): a chain of categories that hand
// their free places on when a sale window closes, per-category sale windows
// and a price schedule per category.
package himports

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/tierchain"
)

func chainPayload(f *bundle525Fixture, earlyEnd time.Time, headAvailability int32) bil24compat.ImportSessionRequest {
	p := f.payload()
	one, two := 1, 2
	p.CategoryList = []bil24compat.ImportSessionCategory{
		{CategoryPriceName: "Early", Price: 99, Availability: headAvailability,
			SellEndTime: earlyEnd.Format(time.RFC3339), NextCategoryIndex: &one},
		{CategoryPriceName: "Friends", Price: 149, Availability: 0,
			SellEndTime: earlyEnd.Add(24 * time.Hour).Format(time.RFC3339), NextCategoryIndex: &two},
		{CategoryPriceName: "Last minute", Price: 249, Availability: 0},
	}
	return p
}

type tierState struct {
	places int64
	open   bool
	end    *time.Time
}

func readTier(t *testing.T, ctx context.Context, f *bundle525Fixture, sessionID, tierID uuid.UUID) tierState {
	t.Helper()
	var s tierState
	if err := f.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM session_seats WHERE session_id=$2 AND tier_id=$1),
		        is_open, sale_window_end
		   FROM ticket_tiers WHERE id=$1`, tierID, sessionID).Scan(&s.places, &s.open, &s.end); err != nil {
		t.Fatalf("read tier %s: %v", tierID, err)
	}
	return s
}

func TestEventBundleChain_CreateHandOverAndReSave(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	earlyEnd := time.Now().Add(time.Hour).Truncate(time.Second)
	rec, out := f.call(h, chainPayload(f, earlyEnd, 50))
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d; body=%s", rec.Code, rec.Body.String())
	}
	if hasWarning(out.Warnings, WarnCategorySoldOut) {
		t.Errorf("a chain successor declared with 0 must not raise %s", WarnCategorySoldOut)
	}
	ids := out.CompatIDs.CategoryPriceIDs
	early := out.TierIDs[externalIDString(ids[0])]
	friends := out.TierIDs[externalIDString(ids[1])]
	last := out.TierIDs[externalIDString(ids[2])]

	e := readTier(t, ctx, f, out.SessionID, early)
	if e.places != 50 || !e.open || e.end == nil || !e.end.Equal(earlyEnd) {
		t.Errorf("Early after create = %+v, want 50 places, open, own window end %s", e, earlyEnd)
	}
	for name, id := range map[string]uuid.UUID{"Friends": friends, "Last minute": last} {
		if s := readTier(t, ctx, f, out.SessionID, id); s.places != 0 || s.open {
			t.Errorf("%s after create = %+v, want no places and closed", name, s)
		}
	}
	var links int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ticket_tier_chain WHERE tier_id = ANY($1)`,
		[]uuid.UUID{early, friends, last}).Scan(&links); err != nil || links != 2 {
		t.Fatalf("chain links = %d (err %v), want 2", links, err)
	}

	// Sell 5 Early places, then close Early's window and run the real sweep.
	if _, err := pool.Exec(ctx,
		`UPDATE session_seats SET status='sold' WHERE id IN (
		   SELECT id FROM session_seats WHERE session_id=$1 AND tier_id=$2 ORDER BY seat_key LIMIT 5)`,
		out.SessionID, early); err != nil {
		t.Fatalf("sell Early places: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE ticket_tiers SET sale_window_end = now() - interval '1 minute' WHERE id=$1`, early); err != nil {
		t.Fatalf("close Early window: %v", err)
	}
	sweep := tierchain.NewHandler(tierchain.Options{Store: tierchain.NewPGStore(pool)})
	if err := sweep(ctx, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if s := readTier(t, ctx, f, out.SessionID, friends); s.places != 45 || !s.open {
		t.Errorf("Friends after hand-over = %+v, want 45 places, open", s)
	}
	if s := readTier(t, ctx, f, out.SessionID, early); s.places != 5 || s.open {
		t.Errorf("Early after hand-over = %+v, want its 5 sold places, closed", s)
	}

	// The operator re-saves the event with 60 places in the hall: the 10 extra
	// go to Friends (selling now); Early is not re-minted.
	resave := chainPayload(f, earlyEnd, 60)
	for i := range resave.CategoryList {
		resave.CategoryList[i].CategoryPriceID = ids[i]
	}
	resave.Action.ActionID = out.CompatIDs.ActionID
	resave.ActionEvent.ActionEventID = out.CompatIDs.ActionEventID
	resave.Venue.VenueID = out.CompatIDs.VenueID
	// The site re-sends Early's original window; the stored (closed) one was
	// moved into the past only by this test, so skip that field here.
	resave.CategoryList[0].SellEndTime = time.Now().Add(-time.Minute).Format(time.RFC3339)
	rec2, _ := f.call(h, resave)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-save: status %d; body=%s", rec2.Code, rec2.Body.String())
	}
	if s := readTier(t, ctx, f, out.SessionID, early); s.places != 5 {
		t.Errorf("Early after re-save owns %d places, want 5 (never re-minted)", s.places)
	}
	if s := readTier(t, ctx, f, out.SessionID, friends); s.places != 55 || !s.open {
		t.Errorf("Friends after re-save = %+v, want 55 places (60 in the hall - 5 sold), open", s)
	}
	var hall int
	if err := pool.QueryRow(ctx, `SELECT capacity_total FROM sessions WHERE id=$1`, out.SessionID).Scan(&hall); err != nil || hall != 60 {
		t.Errorf("session capacity = %d (err %v), want 60", hall, err)
	}
}

// A head that sold nothing hands EVERY place over and owns none. A re-save
// must not mint it a fresh set: the next sweep would hand those on too and
// the hall would grow by its own size on every save (staging 2026-09-28).
func TestEventBundleChain_ReSaveAfterHeadHandedOverEverything(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	earlyEnd := time.Now().Add(time.Hour).Truncate(time.Second)
	rec, out := f.call(h, chainPayload(f, earlyEnd, 30))
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d; body=%s", rec.Code, rec.Body.String())
	}
	ids := out.CompatIDs.CategoryPriceIDs
	early := out.TierIDs[externalIDString(ids[0])]
	friends := out.TierIDs[externalIDString(ids[1])]

	if _, err := pool.Exec(ctx,
		`UPDATE ticket_tiers SET sale_window_end = now() - interval '1 minute' WHERE id=$1`, early); err != nil {
		t.Fatalf("close Early window: %v", err)
	}
	sweep := tierchain.NewHandler(tierchain.Options{Store: tierchain.NewPGStore(pool)})
	if err := sweep(ctx, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if s := readTier(t, ctx, f, out.SessionID, early); s.places != 0 {
		t.Fatalf("Early after hand-over owns %d places, want 0 (nothing sold)", s.places)
	}

	resave := func(hall int32) {
		t.Helper()
		p := chainPayload(f, earlyEnd, hall)
		for i := range p.CategoryList {
			p.CategoryList[i].CategoryPriceID = ids[i]
		}
		p.Action.ActionID = out.CompatIDs.ActionID
		p.ActionEvent.ActionEventID = out.CompatIDs.ActionEventID
		p.Venue.VenueID = out.CompatIDs.VenueID
		p.CategoryList[0].SellEndTime = time.Now().Add(-time.Minute).Format(time.RFC3339)
		if rec, _ := f.call(h, p); rec.Code != http.StatusOK {
			t.Fatalf("re-save %d: status %d; body=%s", hall, rec.Code, rec.Body.String())
		}
		if err := sweep(ctx, nil); err != nil {
			t.Fatalf("sweep: %v", err)
		}
	}

	resave(30)
	if s := readTier(t, ctx, f, out.SessionID, early); s.places != 0 {
		t.Errorf("Early after an unchanged re-save owns %d places, want 0 (never re-minted)", s.places)
	}
	if s := readTier(t, ctx, f, out.SessionID, friends); s.places != 30 {
		t.Errorf("Friends after an unchanged re-save = %d places, want 30 (the hall did not grow)", s.places)
	}

	resave(40)
	if s := readTier(t, ctx, f, out.SessionID, friends); s.places != 40 {
		t.Errorf("Friends after a re-save with 40 = %d places, want 40", s.places)
	}
	if s := readTier(t, ctx, f, out.SessionID, early); s.places != 0 {
		t.Errorf("Early after a re-save with 40 owns %d places, want 0", s.places)
	}
}

func TestEventBundleChain_PriceSchedule(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	p := f.payload()
	schedule := []bil24compat.ImportPriceWindow{
		{ValidFrom: "2026-09-01T00:00:00+03:00", ValidTo: "2026-10-01T00:00:00+03:00", Price: 199},
		{ValidFrom: "2026-10-01T00:00:00+03:00", Price: 249},
	}
	p.CategoryList[0].PriceSchedule = &schedule
	rec, out := f.call(h, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d; body=%s", rec.Code, rec.Body.String())
	}
	tier := out.TierIDs[externalIDString(out.CompatIDs.CategoryPriceIDs[0])]
	windows := func() (n int, lastPrice int64) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT count(*), coalesce(max(price_amount) FILTER (WHERE valid_to IS NULL), 0)
			   FROM ticket_tier_prices WHERE tier_id=$1`, tier).Scan(&n, &lastPrice); err != nil {
			t.Fatalf("read schedule: %v", err)
		}
		return n, lastPrice
	}
	if n, last := windows(); n != 2 || last != 24900 {
		t.Errorf("schedule after create = %d windows / open price %d, want 2 / 24900", n, last)
	}

	keep := f.payload()
	keep.CategoryList[0].CategoryPriceID = out.CompatIDs.CategoryPriceIDs[0]
	keep.CategoryList[1].CategoryPriceID = out.CompatIDs.CategoryPriceIDs[1]
	if rec, _ := f.call(h, keep); rec.Code != http.StatusOK {
		t.Fatalf("re-save without schedule: status %d; body=%s", rec.Code, rec.Body.String())
	}
	if n, _ := windows(); n != 2 {
		t.Errorf("a bundle without priceSchedule must keep it: %d windows", n)
	}

	empty := []bil24compat.ImportPriceWindow{}
	keep.CategoryList[0].PriceSchedule = &empty
	if rec, _ := f.call(h, keep); rec.Code != http.StatusOK {
		t.Fatalf("clear schedule: status %d; body=%s", rec.Code, rec.Body.String())
	}
	if n, _ := windows(); n != 0 {
		t.Errorf("an empty priceSchedule must clear it: %d windows", n)
	}

	overlap := []bil24compat.ImportPriceWindow{
		{ValidFrom: "2026-09-01T00:00:00Z", Price: 10},
		{ValidFrom: "2026-10-01T00:00:00Z", Price: 20},
	}
	keep.CategoryList[0].PriceSchedule = &overlap
	rec, _ = f.call(h, keep)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("overlapping schedule: status %d, want 422", rec.Code)
	}
	assertErrorCode(t, rec, "import.invalid_price_schedule")
}

func TestEventBundleChain_RejectsBadLinks(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	self := f.payload()
	zero := 0
	self.CategoryList[0].NextCategoryIndex = &zero
	rec, _ := f.call(h, self)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self link: status %d, want 422", rec.Code)
	}
	assertErrorCode(t, rec, "import.invalid_next_category")

	cycle := f.payload()
	one := 1
	cycle.CategoryList[0].NextCategoryIndex = &one
	cycle.CategoryList[1].NextCategoryIndex = &zero
	rec, _ = f.call(h, cycle)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cycle: status %d, want 422", rec.Code)
	}
	assertErrorCode(t, rec, "import.category_chain_cycle")
}

// A quantity step (sellLimit, migration 0114) owns exactly its limit; the rest
// of the hall waits in the next, closed category. When the step has no free
// place left the sweep opens the next one even though its date has not come,
// and a middle step then keeps its own limit and passes the rest on.
func TestEventBundleChain_QuantitySteps(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	earlyEnd := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	three, four := int32(3), int32(4)
	p := chainPayload(f, earlyEnd, 10)
	p.CategoryList[0].SellLimit = &three
	p.CategoryList[1].SellLimit = &four
	// Planned to take over at Early's date; it opens before that when Early
	// sells out first.
	p.CategoryList[1].SellStartTime = earlyEnd.Format(time.RFC3339)
	rec, out := f.call(h, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d; body=%s", rec.Code, rec.Body.String())
	}
	ids := out.CompatIDs.CategoryPriceIDs
	early := out.TierIDs[externalIDString(ids[0])]
	friends := out.TierIDs[externalIDString(ids[1])]
	last := out.TierIDs[externalIDString(ids[2])]
	want := func(stage, name string, id uuid.UUID, places int64, open bool) {
		t.Helper()
		if s := readTier(t, ctx, f, out.SessionID, id); s.places != places || s.open != open {
			t.Errorf("%s: %s = %d places open=%v, want %d open=%v", stage, name, s.places, s.open, places, open)
		}
	}
	want("create", "Early", early, 3, true)
	want("create", "Friends", friends, 7, false)
	want("create", "Last minute", last, 0, false)

	resave := func(stage string, hall int32, mutate func(*bil24compat.ImportSessionRequest)) {
		t.Helper()
		p := chainPayload(f, earlyEnd, hall)
		p.CategoryList[0].SellLimit = &three
		p.CategoryList[1].SellLimit = &four
		for i := range p.CategoryList {
			p.CategoryList[i].CategoryPriceID = ids[i]
		}
		p.Action.ActionID = out.CompatIDs.ActionID
		p.ActionEvent.ActionEventID = out.CompatIDs.ActionEventID
		p.Venue.VenueID = out.CompatIDs.VenueID
		if mutate != nil {
			mutate(&p)
		}
		if rec, _ := f.call(h, p); rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d; body=%s", stage, rec.Code, rec.Body.String())
		}
	}
	resave("unchanged re-save", 10, nil)
	want("unchanged re-save", "Early", early, 3, true)
	want("unchanged re-save", "Friends", friends, 7, false)

	resave("hall 12", 12, nil)
	want("hall 12", "Early", early, 3, true)
	want("hall 12", "Friends", friends, 9, false)

	sweep := tierchain.NewHandler(tierchain.Options{Store: tierchain.NewPGStore(pool)})
	// Two sold and one held: the step is exhausted before its date.
	if _, err := pool.Exec(ctx,
		`UPDATE session_seats SET status = CASE WHEN rn <= 2 THEN 'sold' ELSE 'held' END
		   FROM (SELECT id, row_number() OVER (ORDER BY seat_key) rn
		           FROM session_seats WHERE session_id=$1 AND tier_id=$2) x
		  WHERE session_seats.id = x.id`, out.SessionID, early); err != nil {
		t.Fatalf("sell Early: %v", err)
	}
	if err := sweep(ctx, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	want("exhausted", "Early", early, 3, false)
	want("exhausted", "Friends", friends, 4, true)
	want("exhausted", "Last minute", last, 5, false)
	var started bool
	if err := pool.QueryRow(ctx, `SELECT sale_window_start <= now() FROM ticket_tiers WHERE id=$1`, friends).Scan(&started); err != nil || !started {
		t.Errorf("Friends opened early must start selling now: started=%v err=%v", started, err)
	}

	// Removing the middle step's limit gives it the rest of the hall back.
	resave("limit removed", 12, func(p *bil24compat.ImportSessionRequest) {
		zero := int32(0)
		p.CategoryList[1].SellLimit = &zero
		p.CategoryList[0].SellEndTime = earlyEnd.Format(time.RFC3339)
	})
	want("limit removed", "Friends", friends, 9, true)
	want("limit removed", "Last minute", last, 0, false)
	var limit *int32
	if err := pool.QueryRow(ctx, `SELECT sell_limit FROM ticket_tier_chain WHERE tier_id=$1`, friends).Scan(&limit); err != nil || limit != nil {
		t.Errorf("Friends sell_limit after 0 = %v (err %v), want NULL", limit, err)
	}
	var hall int
	if err := pool.QueryRow(ctx, `SELECT capacity_total FROM sessions WHERE id=$1`, out.SessionID).Scan(&hall); err != nil || hall != 12 {
		t.Errorf("session capacity = %d (err %v), want 12", hall, err)
	}

	bad := f.payload()
	bad.CategoryList[0].SellLimit = &three
	rec, _ = f.call(h, bad)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit without a next category: status %d, want 422", rec.Code)
	}
	assertErrorCode(t, rec, "import.invalid_sell_limit")
}

// A hold that takes the last free places of a quantity step may take the
// rest from the next, still closed category in the same cart; the cheaper
// places can never be skipped (hcheckout.CheckGALinesSellable).
func TestEventBundleChain_SpillIntoNextStep(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	three := int32(3)
	p := chainPayload(f, time.Now().Add(24*time.Hour).Truncate(time.Second), 10)
	p.CategoryList[0].SellLimit = &three
	// The gate asks "can it be bought NOW", and since migration 0128 a
	// session stops selling at its own sales end (the start by default): the
	// fixture's April date has passed, so this session plays next month.
	p.ActionEvent.Day = time.Now().AddDate(0, 1, 0).Format("02.01.2006")
	rec, out := f.call(h, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d; body=%s", rec.Code, rec.Body.String())
	}
	ids := out.CompatIDs.CategoryPriceIDs
	early := out.TierIDs[externalIDString(ids[0])]
	friends := out.TierIDs[externalIDString(ids[1])]
	last := out.TierIDs[externalIDString(ids[2])]
	q := gen.New(pool)
	now := time.Now().UTC()
	check := func(lines ...hcheckout.GALine) error {
		t.Helper()
		return hcheckout.CheckGALinesSellable(ctx, q, out.SessionID, lines, now)
	}
	if err := check(hcheckout.GALine{TierID: early, Quantity: 3}, hcheckout.GALine{TierID: friends, Quantity: 2}); err != nil {
		t.Errorf("3 Early + 2 Friends must spill into the waiting step: %v", err)
	}
	if err := check(hcheckout.GALine{TierID: early, Quantity: 2}, hcheckout.GALine{TierID: friends, Quantity: 1}); !errors.Is(err, hcheckout.ErrCategoryClosed) {
		t.Errorf("a cart that leaves an Early place free must not reach Friends: %v", err)
	}
	if err := check(hcheckout.GALine{TierID: friends, Quantity: 1}); !errors.Is(err, hcheckout.ErrCategoryClosed) {
		t.Errorf("Friends alone must stay closed: %v", err)
	}
	if err := check(hcheckout.GALine{TierID: early, Quantity: 3}, hcheckout.GALine{TierID: last, Quantity: 1}); err == nil {
		t.Error("the spill must not jump over Friends to Last minute")
	}

	// A cart that already holds Early's places grows into Friends.
	if _, err := pool.Exec(ctx,
		`UPDATE session_seats SET status='held' WHERE session_id=$1 AND tier_id=$2`, out.SessionID, early); err != nil {
		t.Fatalf("hold Early: %v", err)
	}
	if err := check(hcheckout.GALine{TierID: friends, Quantity: 2}); err != nil {
		t.Errorf("with Early fully held Friends sells before the sweep opens it: %v", err)
	}
}
