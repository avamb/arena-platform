package hmaterials

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

func TestMapPointWarnings_Israel_PointInTheNeighbourCity(t *testing.T) {
	rh, ok := geotz.CityPoint("IL", "Ramat HaSharon")
	if !ok {
		t.Fatal("Ramat HaSharon missing from the geo data")
	}
	got := MapPointWarnings([]gen.EventVenueCity{{Country: "IL", City: "netanya"}}, rh.Lat, rh.Lon)
	if !got.Checked || len(got.Warnings) != 1 {
		t.Fatalf("got %+v", got)
	}
	w := got.Warnings[0]
	if w.Code != WarnPointFarFromCity || w.City != "Netanya" {
		t.Errorf("warning = %+v", w)
	}
	if w.NearerCity == nil || *w.NearerCity != "Ramat HaSharon" {
		t.Errorf("nearer = %v", w.NearerCity)
	}
	if !strings.Contains(w.Message, "Ramat HaSharon") || !strings.Contains(w.Message, "Google Maps") {
		t.Errorf("message = %q", w.Message)
	}
}

func TestMapPointWarnings_NearerOnlyAndQuietCases(t *testing.T) {
	hod, _ := geotz.CityPoint("IL", "Hod HaSharon")
	got := MapPointWarnings([]gen.EventVenueCity{{Country: "IL", City: "Tel Aviv"}}, hod.Lat, hod.Lon)
	if len(got.Warnings) != 1 || got.Warnings[0].Code != WarnPointNearerOtherCity {
		t.Errorf("nearer-only = %+v", got)
	}

	tlv, _ := geotz.CityPoint("IL", "Tel Aviv")
	// Two venue cities: the point fits one of them, so there is no warning.
	got = MapPointWarnings([]gen.EventVenueCity{{Country: "IL", City: "Haifa"}, {Country: "IL", City: "tel-aviv"}}, tlv.Lat, tlv.Lon)
	if !got.Checked || len(got.Warnings) != 0 {
		t.Errorf("fitting city = %+v", got)
	}

	// A city the data does not know: nothing can be said.
	got = MapPointWarnings([]gen.EventVenueCity{{Country: "IL", City: "Nowhereville"}, {Country: "", City: ""}}, 32, 34)
	if got.Checked || len(got.Warnings) != 0 {
		t.Errorf("unknown city = %+v", got)
	}
}
