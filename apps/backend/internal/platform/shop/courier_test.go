package shop

import (
	"reflect"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestCourierFields_OrderAndFallbacks(t *testing.T) {
	o := gen.ShopOrderRow{
		BuyerName: "Anna K", BuyerPhone: " +972500000000 ", ShipCity: "Haifa",
		ShipAddress1: "Herzl 1", ShipAddress2: "apt 4", ShipPostal: "3100000", ShipCountry: "il",
	}
	c := CourierFieldsOf(o)
	want := []string{"Anna K", "+972500000000", "Herzl 1", "apt 4", "Haifa", "3100000", "Israel"}
	if got := c.Lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("Lines = %q, want %q", got, want)
	}
	wantJoined := []string{"Anna K", "+972500000000", "Herzl 1, apt 4", "Haifa", "3100000", "Israel"}
	if got := c.JoinedLines(); !reflect.DeepEqual(got, wantJoined) {
		t.Errorf("JoinedLines = %q, want %q", got, wantJoined)
	}

	o.ShipName = "Dan L"
	o.ShipAddress2 = ""
	if got := CourierFieldsOf(o).Lines(); got[0] != "Dan L" || len(got) != 6 {
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

func TestCourierFields_MapLinksAreBuiltFromTheAddressWithoutLine2(t *testing.T) {
	c := CourierFieldsOf(gen.ShopOrderRow{
		ShipAddress1: "Herzl 1", ShipAddress2: "entrance B, floor 3", ShipCity: "Haifa", ShipPostal: "3100000", ShipCountry: "IL",
	})
	if !c.HasAddress() {
		t.Fatal("HasAddress = false for a street and a city")
	}
	q := c.MapQuery()
	if q != "Herzl 1, Haifa, 3100000, Israel" {
		t.Errorf("MapQuery = %q: entrance and floor must not reach a geocoder", q)
	}
	if got, want := GoogleMapsURL(q), "https://www.google.com/maps/search/?api=1&query=Herzl+1%2C+Haifa%2C+3100000%2C+Israel"; got != want {
		t.Errorf("GoogleMapsURL = %q, want %q", got, want)
	}
	if got, want := WazeURL(q), "https://waze.com/ul?q=Herzl+1%2C+Haifa%2C+3100000%2C+Israel&navigate=yes"; got != want {
		t.Errorf("WazeURL = %q, want %q", got, want)
	}
	cyr := WazeURL("ул. Тестовая 1, Хайфа")
	if strings.ContainsAny(strings.TrimPrefix(cyr, "https://waze.com/ul?q="), " Тест") {
		t.Errorf("a Cyrillic address must be percent-encoded: %q", cyr)
	}
}

func TestCountryName(t *testing.T) {
	for in, want := range map[string]string{"IL": "Israel", "il": "Israel", "ES": "Spain", "": "", "??": "??"} {
		if got := CountryName(in); got != want {
			t.Errorf("CountryName(%q) = %q, want %q", in, got, want)
		}
	}
}
