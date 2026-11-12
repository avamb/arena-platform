package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeLegStore is an in-memory LegStateStore.
type fakeLegStore struct {
	delivered map[string]map[string]bool // event → leg → delivered
	attempts  map[string]map[string]int
	lastErr   map[string]map[string]string
	writes    int
	readErr   error
	writeErr  error
}

func newFakeLegStore() *fakeLegStore {
	return &fakeLegStore{
		delivered: map[string]map[string]bool{},
		attempts:  map[string]map[string]int{},
		lastErr:   map[string]map[string]string{},
	}
}

func (s *fakeLegStore) LegStates(_ context.Context, eventID string) (map[string]bool, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := map[string]bool{}
	for leg, d := range s.delivered[eventID] {
		out[leg] = d
	}
	return out, nil
}

func (s *fakeLegStore) touch(eventID, leg string) {
	if s.delivered[eventID] == nil {
		s.delivered[eventID] = map[string]bool{}
		s.attempts[eventID] = map[string]int{}
		s.lastErr[eventID] = map[string]string{}
	}
	if _, ok := s.delivered[eventID][leg]; !ok {
		s.delivered[eventID][leg] = false
	}
	s.attempts[eventID][leg]++
}

func (s *fakeLegStore) MarkLegDelivered(_ context.Context, eventID, leg string) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	s.touch(eventID, leg)
	s.delivered[eventID][leg] = true
	return nil
}

func (s *fakeLegStore) MarkLegFailed(_ context.Context, eventID, leg, lastErr string) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	s.touch(eventID, leg)
	s.lastErr[eventID][leg] = lastErr
	return nil
}

// countingLeg counts calls and answers err.
type countingLeg struct {
	calls int
	err   error
}

func (c *countingLeg) Dispatch(context.Context, Event) error {
	c.calls++
	return c.err
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mustFanOut(t *testing.T, store LegStateStore, legs ...Leg) *LegFanOut {
	t.Helper()
	f, err := NewLegFanOut(store, quietLogger(), legs...)
	if err != nil {
		t.Fatalf("NewLegFanOut: %v", err)
	}
	return f
}

func TestLegFanOut_FailingLegNeitherBlocksNorRetriggersOthers(t *testing.T) {
	store := newFakeLegStore()
	notify, macs, wp, billing := &countingLeg{}, &countingLeg{err: errors.New("macs 500")}, &countingLeg{}, &countingLeg{}
	f := mustFanOut(t, store,
		Leg{Name: "sales_notify", Dispatcher: notify},
		Leg{Name: "macs", Dispatcher: macs},
		Leg{Name: "bil24_wp", Dispatcher: wp},
		Leg{Name: "billing_accel", Dispatcher: billing},
	)
	ev := Event{ID: "evt-1", EventType: "v1.order.paid"}

	for pass := 1; pass <= 3; pass++ {
		err := f.Dispatch(context.Background(), ev)
		if err == nil || !strings.Contains(err.Error(), "macs: macs 500") {
			t.Fatalf("pass %d: err = %v, want the macs failure named by its leg", pass, err)
		}
	}
	if notify.calls != 1 || wp.calls != 1 || billing.calls != 1 {
		t.Fatalf("calls notify=%d wp=%d billing=%d, want 1 each (delivered legs are never called again)",
			notify.calls, wp.calls, billing.calls)
	}
	if macs.calls != 3 {
		t.Fatalf("macs calls = %d, want 3", macs.calls)
	}
	if got := store.attempts["evt-1"]["macs"]; got != 3 {
		t.Errorf("macs attempts = %d, want 3", got)
	}
	if got := store.lastErr["evt-1"]["macs"]; got != "macs 500" {
		t.Errorf("macs last_error = %q", got)
	}
	if store.delivered["evt-1"]["macs"] {
		t.Error("macs marked delivered after failing")
	}

	// MACS recovers: only MACS is called, the event is done.
	macs.err = nil
	if err := f.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	if macs.calls != 4 || wp.calls != 1 || notify.calls != 1 || billing.calls != 1 {
		t.Fatalf("after recovery macs=%d wp=%d notify=%d billing=%d, want 4/1/1/1", macs.calls, wp.calls, notify.calls, billing.calls)
	}
	if !store.delivered["evt-1"]["macs"] {
		t.Error("macs not marked delivered after the recovery pass")
	}
}

func TestLegFanOut_AllDeliveredFirstPassWritesNothing(t *testing.T) {
	store := newFakeLegStore()
	a, b := &countingLeg{}, &countingLeg{}
	f := mustFanOut(t, store, Leg{Name: "a", Dispatcher: a}, Leg{Name: "b", Dispatcher: b})
	if err := f.Dispatch(context.Background(), Event{ID: "evt-2"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls a=%d b=%d, want 1 each", a.calls, b.calls)
	}
	if store.writes != 0 {
		t.Fatalf("state writes = %d, want 0 for a clean first pass", store.writes)
	}
}

func TestLegFanOut_JoinsEveryFailure(t *testing.T) {
	e1, e2 := errors.New("first down"), errors.New("second down")
	ok := &countingLeg{}
	f := mustFanOut(t, newFakeLegStore(),
		Leg{Name: "one", Dispatcher: &countingLeg{err: e1}},
		Leg{Name: "ok", Dispatcher: ok},
		Leg{Name: "two", Dispatcher: &countingLeg{err: e2}},
	)
	err := f.Dispatch(context.Background(), Event{ID: "evt-3"})
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Fatalf("err = %v, want both failures joined", err)
	}
	if ok.calls != 1 {
		t.Fatalf("the leg between two failures was called %d time(s), want 1", ok.calls)
	}
}

func TestLegFanOut_NoEventIDIsAPlainFanOut(t *testing.T) {
	store := newFakeLegStore()
	a, bad := &countingLeg{}, &countingLeg{err: errors.New("down")}
	f := mustFanOut(t, store, Leg{Name: "bad", Dispatcher: bad}, Leg{Name: "a", Dispatcher: a})
	for i := 0; i < 2; i++ {
		if err := f.Dispatch(context.Background(), Event{}); err == nil {
			t.Fatal("want the failure returned")
		}
	}
	if a.calls != 2 || bad.calls != 2 {
		t.Fatalf("calls a=%d bad=%d, want 2 each without state", a.calls, bad.calls)
	}
	if store.writes != 0 {
		t.Fatalf("state writes = %d, want 0 without an event id", store.writes)
	}
}

func TestLegFanOut_NilStoreCallsEveryLeg(t *testing.T) {
	a, bad := &countingLeg{}, &countingLeg{err: errors.New("down")}
	f := mustFanOut(t, nil, Leg{Name: "bad", Dispatcher: bad}, Leg{Name: "a", Dispatcher: a})
	_ = f.Dispatch(context.Background(), Event{ID: "evt-4"})
	_ = f.Dispatch(context.Background(), Event{ID: "evt-4"})
	if a.calls != 2 {
		t.Fatalf("a calls = %d, want 2 with no store", a.calls)
	}
}

func TestLegFanOut_StateReadFailureCallsNoLeg(t *testing.T) {
	store := newFakeLegStore()
	store.readErr = errors.New("db down")
	a := &countingLeg{}
	f := mustFanOut(t, store, Leg{Name: "a", Dispatcher: a})
	if err := f.Dispatch(context.Background(), Event{ID: "evt-5"}); !errors.Is(err, store.readErr) {
		t.Fatalf("err = %v, want the read error", err)
	}
	if a.calls != 0 {
		t.Fatalf("a called %d time(s) without its state, want 0", a.calls)
	}
}

func TestLegFanOut_StateWriteFailureIsOnlyLogged(t *testing.T) {
	store := newFakeLegStore()
	store.writeErr = errors.New("db down")
	a, bad := &countingLeg{}, &countingLeg{err: errors.New("leg down")}
	f := mustFanOut(t, store, Leg{Name: "a", Dispatcher: a}, Leg{Name: "bad", Dispatcher: bad})
	err := f.Dispatch(context.Background(), Event{ID: "evt-6"})
	if !errors.Is(err, bad.err) || errors.Is(err, store.writeErr) {
		t.Fatalf("err = %v, want only the leg failure", err)
	}
	// Nothing was recorded, so the next attempt calls a again (at-least-once).
	_ = f.Dispatch(context.Background(), Event{ID: "evt-6"})
	if a.calls != 2 {
		t.Fatalf("a calls = %d, want 2 when its success could not be recorded", a.calls)
	}
}

func TestLegFanOut_DisabledLegRecordsNoAttempt(t *testing.T) {
	store := newFakeLegStore()
	f := mustFanOut(t, store,
		Leg{Name: "off", Dispatcher: DisabledDispatcher{}},
		Leg{Name: "bad", Dispatcher: &countingLeg{err: errors.New("down")}},
	)
	err := f.Dispatch(context.Background(), Event{ID: "evt-7"})
	if !errors.Is(err, ErrDispatchDisabled) {
		t.Fatalf("err = %v, want ErrDispatchDisabled kept in the join", err)
	}
	if _, ok := store.attempts["evt-7"]["off"]; ok {
		t.Error("a disabled leg recorded an attempt")
	}
	if store.attempts["evt-7"]["bad"] != 1 {
		t.Errorf("bad attempts = %d, want 1", store.attempts["evt-7"]["bad"])
	}
}

func TestNewLegFanOut_RejectsBadLegs(t *testing.T) {
	d := &countingLeg{}
	cases := map[string][]Leg{
		"empty name":     {{Name: " ", Dispatcher: d}},
		"nil dispatcher": {{Name: "a"}},
		"duplicate name": {{Name: "a", Dispatcher: d}, {Name: "a", Dispatcher: d}},
	}
	for name, legs := range cases {
		if _, err := NewLegFanOut(nil, nil, legs...); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	f := mustFanOut(t, nil, Leg{Name: "x", Dispatcher: d}, Leg{Name: "y", Dispatcher: d})
	if got := strings.Join(f.Legs(), ","); got != "x,y" {
		t.Errorf("Legs() = %q, want x,y", got)
	}
}

func TestTruncateLegError_KeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("я", legErrorMaxBytes) // 2 bytes each
	got := truncateLegError(s)
	if len(got) > legErrorMaxBytes {
		t.Fatalf("len = %d, want <= %d", len(got), legErrorMaxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated text is not valid UTF-8")
	}
	odd := "x" + s
	if got := truncateLegError(odd); !utf8.ValidString(got) {
		t.Fatal("truncation inside a character left invalid UTF-8")
	}
	if got := truncateLegError("short"); got != "short" {
		t.Fatalf("short text changed: %q", got)
	}
}
