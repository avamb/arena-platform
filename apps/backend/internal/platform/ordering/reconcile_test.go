package ordering

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// recordingMutators captures the deltas ReconcileLines hands to the hold
// primitives, plus the order in which it calls them (shrink must precede
// extend so a category swap never needs capacity it already owns).
type recordingMutators struct {
	extended  []TierQuantity
	shrunk    []TierQuantity
	callOrder []string
	extendErr error
	shrinkErr error
}

func (r *recordingMutators) mutators() HoldMutators {
	return HoldMutators{
		Extend: func(_ context.Context, _ uuid.UUID, tiers []TierQuantity) error {
			r.callOrder = append(r.callOrder, "extend")
			if r.extendErr != nil {
				return r.extendErr
			}
			r.extended = append(r.extended, tiers...)
			return nil
		},
		Shrink: func(_ context.Context, _ uuid.UUID, tiers []TierQuantity) error {
			r.callOrder = append(r.callOrder, "shrink")
			if r.shrinkErr != nil {
				return r.shrinkErr
			}
			r.shrunk = append(r.shrunk, tiers...)
			return nil
		},
	}
}

func heldStore(reservationID uuid.UUID, held ...gen.ReservationGAItemRow) *fakeStore {
	f := newFakeStore()
	for i := range held {
		held[i].ReservationID = reservationID
	}
	f.gaItems = held
	return f
}

func findDelta(t *testing.T, deltas []LineDelta, tier uuid.UUID) LineDelta {
	t.Helper()
	for _, d := range deltas {
		if d.TierID == tier {
			return d
		}
	}
	t.Fatalf("no delta for tier %s in %+v", tier, deltas)
	return LineDelta{}
}

func TestReconcileLines_ExtendsWhenSiteWantsMore(t *testing.T) {
	resID := uuid.New()
	tier := uuid.New()
	f := heldStore(resID, gen.ReservationGAItemRow{TierID: tier, Quantity: 2, UnitPrice: 1000})
	m := &recordingMutators{}

	res, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: tier, Quantity: 5}},
	})
	if err != nil {
		t.Fatalf("ReconcileLines: %v", err)
	}
	if len(m.extended) != 1 || m.extended[0].TierID != tier || m.extended[0].Quantity != 3 {
		t.Fatalf("extended = %+v, want +3 of tier %s", m.extended, tier)
	}
	if len(m.shrunk) != 0 {
		t.Fatalf("shrunk = %+v, want nothing", m.shrunk)
	}
	d := findDelta(t, res.Deltas, tier)
	if d.Held != 2 || d.Requested != 5 || d.Added != 3 || d.Removed != 0 {
		t.Fatalf("delta = %+v, want held=2 requested=5 added=3", d)
	}
	if !res.Changed {
		t.Fatal("Changed = false after an extend")
	}
}

func TestReconcileLines_ShrinksWhenSiteWantsFewer(t *testing.T) {
	resID := uuid.New()
	tier := uuid.New()
	f := heldStore(resID, gen.ReservationGAItemRow{TierID: tier, Quantity: 4, UnitPrice: 1000})
	m := &recordingMutators{}

	res, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: tier, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("ReconcileLines: %v", err)
	}
	if len(m.shrunk) != 1 || m.shrunk[0].Quantity != 3 {
		t.Fatalf("shrunk = %+v, want -3", m.shrunk)
	}
	d := findDelta(t, res.Deltas, tier)
	if d.Removed != 3 || d.Added != 0 {
		t.Fatalf("delta = %+v, want removed=3", d)
	}
}

// A category the site no longer mentions is gone from the cart entirely
// (spec §7.7 step 3) — absence is a removal, not "leave it alone".
func TestReconcileLines_DropsCategoriesMissingFromRequest(t *testing.T) {
	resID := uuid.New()
	keep, drop := uuid.New(), uuid.New()
	f := heldStore(resID,
		gen.ReservationGAItemRow{TierID: keep, Quantity: 2, UnitPrice: 1000},
		gen.ReservationGAItemRow{TierID: drop, Quantity: 3, UnitPrice: 500},
	)
	m := &recordingMutators{}

	_, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: keep, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("ReconcileLines: %v", err)
	}
	if len(m.shrunk) != 1 || m.shrunk[0].TierID != drop || m.shrunk[0].Quantity != 3 {
		t.Fatalf("shrunk = %+v, want the whole dropped tier %s", m.shrunk, drop)
	}
}

// Shrinking before extending keeps the peak hold at max(old, new) instead of
// old+new, so swapping one category for another cannot self-deadlock on
// capacity the same cart is still holding.
func TestReconcileLines_ShrinksBeforeExtending(t *testing.T) {
	resID := uuid.New()
	out, in := uuid.New(), uuid.New()
	f := heldStore(resID, gen.ReservationGAItemRow{TierID: out, Quantity: 2, UnitPrice: 1000})
	m := &recordingMutators{}

	_, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: in, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("ReconcileLines: %v", err)
	}
	if len(m.callOrder) != 2 || m.callOrder[0] != "shrink" || m.callOrder[1] != "extend" {
		t.Fatalf("call order = %v, want [shrink extend]", m.callOrder)
	}
}

// The site listing the same category twice means the sum of both lines.
func TestReconcileLines_SumsDuplicateLines(t *testing.T) {
	resID := uuid.New()
	tier := uuid.New()
	f := heldStore(resID, gen.ReservationGAItemRow{TierID: tier, Quantity: 1, UnitPrice: 1000})
	m := &recordingMutators{}

	_, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: tier, Quantity: 2}, {TierID: tier, Quantity: 3}},
	})
	if err != nil {
		t.Fatalf("ReconcileLines: %v", err)
	}
	if len(m.extended) != 1 || m.extended[0].Quantity != 4 {
		t.Fatalf("extended = %+v, want +4 (1 held vs 2+3 requested)", m.extended)
	}
}

func TestReconcileLines_NoopWhenAlreadyMatching(t *testing.T) {
	resID := uuid.New()
	tier := uuid.New()
	f := heldStore(resID, gen.ReservationGAItemRow{TierID: tier, Quantity: 2, UnitPrice: 1000})
	m := &recordingMutators{}
	orderID := uuid.New()

	res, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: tier, Quantity: 2}},
		OrderID:       &orderID,
	})
	if err != nil {
		t.Fatalf("ReconcileLines: %v", err)
	}
	if res.Changed {
		t.Fatal("Changed = true for an identical cart")
	}
	if len(m.callOrder) != 0 {
		t.Fatalf("mutators called %v for an identical cart", m.callOrder)
	}
	if len(f.events) != 0 {
		t.Fatalf("wrote %d audit events for a no-op reconcile", len(f.events))
	}
}

// An unsatisfiable extend must be distinguishable, because the gateway turns
// it into result code 101 rather than a generic failure.
func TestReconcileLines_ExtendFailureIsCapacityUnavailable(t *testing.T) {
	resID := uuid.New()
	tier := uuid.New()
	f := heldStore(resID)
	sentinel := errors.New("only 1 left")
	m := &recordingMutators{extendErr: sentinel}

	_, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: tier, Quantity: 9}},
	})
	if !errors.Is(err, ErrCapacityUnavailable) {
		t.Fatalf("err = %v, want ErrCapacityUnavailable", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the underlying cause preserved", err)
	}
}

func TestReconcileLines_EmitsLinesReconciledEventWithDelta(t *testing.T) {
	resID := uuid.New()
	tier := uuid.New()
	f := heldStore(resID, gen.ReservationGAItemRow{TierID: tier, Quantity: 1, UnitPrice: 1000})
	m := &recordingMutators{}
	orderID := uuid.New()

	_, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: tier, Quantity: 4}},
		OrderID:       &orderID,
		Actor:         "gateway:7",
	})
	if err != nil {
		t.Fatalf("ReconcileLines: %v", err)
	}
	if len(f.events) != 1 {
		t.Fatalf("got %d events, want 1", len(f.events))
	}
	ev := f.events[0]
	if ev.Type != EventLinesReconciled || ev.OrderID != orderID || ev.Actor != "gateway:7" {
		t.Fatalf("event = %+v, want lines_reconciled on %s by gateway:7", ev, orderID)
	}
	if !strings.Contains(string(ev.Payload), `"added":3`) {
		t.Fatalf("payload = %s, want the +3 delta", ev.Payload)
	}
}

func TestReconcileLines_RequiresBothMutators(t *testing.T) {
	f := heldStore(uuid.New())
	_, err := ReconcileLines(context.Background(), f, HoldMutators{}, ReconcileInput{ReservationID: uuid.New()})
	if err == nil {
		t.Fatal("want an error when the mutators are not wired")
	}
}

func TestReconcileLines_RejectsNegativeQuantity(t *testing.T) {
	resID := uuid.New()
	f := heldStore(resID)
	m := &recordingMutators{}
	_, err := ReconcileLines(context.Background(), f, m.mutators(), ReconcileInput{
		ReservationID: resID,
		Lines:         []Line{{TierID: uuid.New(), Quantity: -1}},
	})
	if err == nil {
		t.Fatal("want an error for a negative quantity")
	}
}
