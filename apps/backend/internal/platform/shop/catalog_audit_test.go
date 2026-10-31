package shop

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCheckVariantSet(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	current := map[uuid.UUID]int32{a: 1, b: 2}

	if err := checkVariantSet(current, []uuid.UUID{b, a}); err != nil {
		t.Fatalf("the same set in another order = %v; want nil", err)
	}
	for name, expected := range map[string][]uuid.UUID{
		"a variant was added meanwhile":   {a},
		"a variant was removed meanwhile": {a, b, c},
		"another set":                     {a, c},
		"an empty list":                   {},
	} {
		err := checkVariantSet(current, expected)
		e, ok := AsError(err)
		if !ok || e.Code != "shop.variants_changed" || e.Status != 409 {
			t.Errorf("%s: %v; want 409 shop.variants_changed", name, err)
			continue
		}
		ids, _ := e.Details["variant_ids"].([]string)
		if len(ids) != 2 {
			t.Errorf("%s: details %v; want the two live variant ids", name, e.Details)
		}
	}
}

func TestSameInstantIgnoresSubMicrosecondDrift(t *testing.T) {
	base := time.Date(2026, 10, 30, 12, 0, 0, 123456000, time.UTC)
	if !sameInstant(base, base.Add(789*time.Nanosecond)) {
		t.Error("nanosecond drift must not count as a change")
	}
	if sameInstant(base, base.Add(time.Microsecond)) {
		t.Error("a microsecond apart is a different save")
	}
	loc := time.FixedZone("x", 3*3600)
	if !sameInstant(base, base.In(loc)) {
		t.Error("the same instant in another zone must match")
	}
}
