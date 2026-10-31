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

func TestStripApartment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ул. Тестовая 1, кв. 2", "ул. Тестовая 1"},
		{"ул. Тестовая 1 кв 12", "ул. Тестовая 1"},
		{"ул. Тестовая 1, квартира 7", "ул. Тестовая 1"},
		{"ул. Тестовая 1, кв.2", "ул. Тестовая 1"},
		{"Herzl 1, apt 4", "Herzl 1"},
		{"Herzl 1, Apt. 4b", "Herzl 1"},
		{"Herzl 1 Apartment 7", "Herzl 1"},
		{"הרצל 1, דירה 5", "הרצל 1"},
		{"ул. Квартальная 5", "ул. Квартальная 5"},
		{"ул. Квартальная", "ул. Квартальная"},
		{"Aptos Street 3", "Aptos Street 3"},
		{"кв 2", "кв 2"},
		{"Herzl 1", "Herzl 1"},
		{"", ""},
	}
	for _, c := range cases {
		if got := StripApartment(c.in); got != c.want {
			t.Errorf("StripApartment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCourierFields_MapQueryDropsAnApartmentTypedIntoLine1ButTheAddressKeepsIt(t *testing.T) {
	c := CourierFieldsOfIn(gen.ShopOrderRow{
		ShipAddress1: "ул. Тестовая 1, кв. 2", ShipCity: "Хайфа", ShipPostal: "3200000", ShipCountry: "IL",
	}, "ru")
	if q := c.MapQuery(); q != "ул. Тестовая 1, Хайфа, 3200000, Израиль" {
		t.Errorf("MapQuery = %q", q)
	}
	if got := c.Lines()[0]; got != "ул. Тестовая 1, кв. 2" {
		t.Errorf("printed address line = %q, the apartment must stay", got)
	}
}
