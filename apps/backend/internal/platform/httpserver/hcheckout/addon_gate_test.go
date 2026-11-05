package hcheckout

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeAddonQuerier map[uuid.UUID]bool

func (f fakeAddonQuerier) ListAddonTierIDs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if f[id] {
			out[id] = true
		}
	}
	return out, nil
}

func TestCheckAddonCompanions(t *testing.T) {
	main, extra, extra2 := uuid.New(), uuid.New(), uuid.New()
	q := fakeAddonQuerier{extra: true, extra2: true}
	for _, c := range []struct {
		name  string
		cart  []uuid.UUID
		error bool
	}{
		{"empty cart", nil, false},
		{"main only", []uuid.UUID{main}, false},
		{"main with an extra", []uuid.UUID{main, extra}, false},
		{"two extras and a main", []uuid.UUID{extra, extra2, main}, false},
		{"one extra alone", []uuid.UUID{extra}, true},
		{"two extras alone", []uuid.UUID{extra, extra2, extra}, true},
	} {
		err := CheckAddonCompanions(context.Background(), q, c.cart)
		if c.error != errors.Is(err, ErrAddonWithoutMain) {
			t.Errorf("%s: err=%v, want refusal %v", c.name, err, c.error)
		}
	}
	if code := CategoryGateErrorCode(ErrAddonWithoutMain); code != "tier.addon_requires_main" {
		t.Errorf("code = %q", code)
	}
	if CategoryGateMessage(ErrAddonWithoutMain) == "" {
		t.Error("the refusal has no message")
	}
}
