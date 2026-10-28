package shop

import (
	"reflect"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestCourierFields_OrderAndFallbacks(t *testing.T) {
	o := gen.ShopOrderRow{
		BuyerName: "Anna K", BuyerPhone: " +972500000000 ", ShipCity: "Haifa",
		ShipAddress1: "Herzl 1", ShipAddress2: "apt 4", ShipPostal: "3100000",
	}
	c := CourierFieldsOf(o)
	want := []string{"Anna K", "+972500000000", "Haifa", "Herzl 1", "apt 4", "3100000"}
	if got := c.Lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("Lines = %q, want %q", got, want)
	}
	wantJoined := []string{"Anna K", "+972500000000", "Haifa", "Herzl 1, apt 4", "3100000"}
	if got := c.JoinedLines(); !reflect.DeepEqual(got, wantJoined) {
		t.Errorf("JoinedLines = %q, want %q", got, wantJoined)
	}

	o.ShipName = "Dan L"
	o.ShipAddress2 = ""
	if got := CourierFieldsOf(o).Lines(); got[0] != "Dan L" || len(got) != 5 {
		t.Errorf("the shipping name wins and an empty line 2 is left out, got %q", got)
	}
}

func TestItemLabel(t *testing.T) {
	cases := []struct {
		want string
		it   gen.ShopOrderItemRow
	}{
		{"Rosé", gen.ShopOrderItemRow{ProductTitle: "Rosé", VariantTitle: "Default Title"}},
		{"Rosé (Magnum)", gen.ShopOrderItemRow{ProductTitle: "Rosé", VariantTitle: "Magnum"}},
		{"Rosé", gen.ShopOrderItemRow{ProductTitle: "Rosé"}},
	}
	for _, c := range cases {
		if got := ItemLabel(c.it); got != c.want {
			t.Errorf("ItemLabel(%+v) = %q, want %q", c.it, got, c.want)
		}
	}
}
