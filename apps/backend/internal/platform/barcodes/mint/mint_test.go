package mint

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// fakeInserter is an in-memory Inserter: external_ref -> won, keyed
// globally (not per-authority) to mirror the cross-authority uniqueness
// InsertBarcodeIfUnique enforces in production.
type fakeInserter struct {
	won      map[string]bool
	err      error
	tryCalls int
}

func newFakeInserter(preClaimed ...string) *fakeInserter {
	f := &fakeInserter{won: map[string]bool{}}
	for _, ref := range preClaimed {
		f.won[ref] = true
	}
	return f
}

func (f *fakeInserter) InsertBarcodeIfUnique(_ context.Context, _ uuid.UUID, externalRef string, _ *uuid.UUID) (bool, error) {
	f.tryCalls++
	if f.err != nil {
		return false, f.err
	}
	if f.won[externalRef] {
		return false, nil
	}
	f.won[externalRef] = true
	return true, nil
}

// TestEAN13_FirstDrawWins proves the common case: a single draw that does
// not collide is claimed on the first attempt.
func TestEAN13_FirstDrawWins(t *testing.T) {
	ins := newFakeInserter()
	ticketID := uuid.New()
	authorityID := uuid.New()

	code, err := EAN13(context.Background(), ins, authorityID, &ticketID, nil)
	if err != nil {
		t.Fatalf("EAN13: %v", err)
	}
	if len(code) != 13 {
		t.Fatalf("EAN13 returned %q, want a 13-digit code", code)
	}
	if ins.tryCalls != 1 {
		t.Fatalf("tryCalls = %d, want 1", ins.tryCalls)
	}
}

// TestEAN13_RedrawsOnCollisionUnderAnyAuthority is the collision case the
// task calls out explicitly: a pre-existing row with the same external_ref
// under a DIFFERENT authority (simulated here by fakeInserter's won map,
// which is NOT scoped by authorityID — mirroring InsertBarcodeIfUnique's
// cross-authority WHERE NOT EXISTS) must force a re-draw, not an error.
func TestEAN13_RedrawsOnCollisionUnderAnyAuthority(t *testing.T) {
	const colliding = "2101234567895" // fed as the first candidate below
	const fresh = "2109876543218"

	ins := newFakeInserter(colliding) // pretend legacy_bil24 already owns this ref
	ticketID := uuid.New()
	authorityID := uuid.New()

	draws := []string{colliding, fresh}
	callIdx := 0
	gen := func() (string, error) {
		if callIdx >= len(draws) {
			t.Fatalf("gen called more times than expected (%d)", callIdx+1)
		}
		v := draws[callIdx]
		callIdx++
		return v, nil
	}

	code, err := EAN13(context.Background(), ins, authorityID, &ticketID, gen)
	if err != nil {
		t.Fatalf("EAN13: %v", err)
	}
	if code != fresh {
		t.Fatalf("EAN13 = %q, want the redrawn candidate %q (the collision must force a retry)", code, fresh)
	}
	if ins.tryCalls != 2 {
		t.Fatalf("tryCalls = %d, want 2 (one losing attempt, one winning attempt)", ins.tryCalls)
	}
}

// TestEAN13_ExhaustsAttemptsAndReturnsClearError proves a generator that
// always collides is bounded, not an infinite loop, and reports a
// diagnosable error rather than a raw DB error.
func TestEAN13_ExhaustsAttemptsAndReturnsClearError(t *testing.T) {
	const stuck = "2100000000006"
	ins := newFakeInserter(stuck)
	ticketID := uuid.New()
	authorityID := uuid.New()

	gen := func() (string, error) { return stuck, nil }

	_, err := EAN13(context.Background(), ins, authorityID, &ticketID, gen)
	if err == nil {
		t.Fatal("EAN13: want an error when every attempt collides, got nil")
	}
	if ins.tryCalls != MaxAttempts {
		t.Fatalf("tryCalls = %d, want %d (MaxAttempts)", ins.tryCalls, MaxAttempts)
	}
}

// TestEAN13_PropagatesInserterError proves a genuine failure (as opposed to
// an ordinary collision) is NOT retried into a misleading "exhausted
// attempts" message — it surfaces immediately.
func TestEAN13_PropagatesInserterError(t *testing.T) {
	ins := newFakeInserter()
	ins.err = errors.New("boom: connection reset")
	ticketID := uuid.New()
	authorityID := uuid.New()

	_, err := EAN13(context.Background(), ins, authorityID, &ticketID, nil)
	if err == nil {
		t.Fatal("EAN13: want an error, got nil")
	}
	if ins.tryCalls != 1 {
		t.Fatalf("tryCalls = %d, want 1 (a real error must not be retried like a collision)", ins.tryCalls)
	}
}

// TestEAN13_PropagatesGeneratorError proves a failing candidate draw (e.g.
// crypto/rand exhausted) surfaces without ever calling the Inserter.
func TestEAN13_PropagatesGeneratorError(t *testing.T) {
	ins := newFakeInserter()
	ticketID := uuid.New()
	authorityID := uuid.New()
	gen := func() (string, error) { return "", errors.New("rand: no entropy") }

	_, err := EAN13(context.Background(), ins, authorityID, &ticketID, gen)
	if err == nil {
		t.Fatal("EAN13: want an error, got nil")
	}
	if ins.tryCalls != 0 {
		t.Fatalf("tryCalls = %d, want 0 (a generator error must not reach the Inserter)", ins.tryCalls)
	}
}
