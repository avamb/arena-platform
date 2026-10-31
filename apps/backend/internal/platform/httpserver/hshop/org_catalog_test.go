package hshop

import (
	"testing"

	"github.com/google/uuid"
)

// An existing variant that names no expected_stock keeps its stored stock
// (SA-04); a new variant, or one that names the stock it saw, writes it.
func TestToVariantInputs_KeepStockOnlyWithoutExpectedStock(t *testing.T) {
	id := uuid.New()
	expected := int32(5)
	got := toVariantInputs([]variantBody{
		{ID: &id, Title: "kept", Stock: 9},
		{ID: &id, Title: "edited", Stock: 9, ExpectedStock: &expected},
		{Title: "new", Stock: 9},
	})
	want := []bool{true, false, false}
	for i, w := range want {
		if got[i].KeepStock != w {
			t.Errorf("row %d (%s): KeepStock = %v; want %v", i, got[i].Title, got[i].KeepStock, w)
		}
	}
}
