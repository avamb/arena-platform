package geotz

import (
	"math"
	"testing"
)

func TestDistanceKm_KnownPairs(t *testing.T) {
	tlv, ok1 := CityPoint("IL", "Tel Aviv")
	jer, ok2 := CityPoint("IL", "Jerusalem")
	if !ok1 || !ok2 {
		t.Fatal("Tel Aviv or Jerusalem is not in the data")
	}
	if d := DistanceKm(tlv.Lat, tlv.Lon, jer.Lat, jer.Lon); math.Abs(d-54) > 3 {
		t.Errorf("Tel Aviv - Jerusalem = %.1f km, want about 54", d)
	}
	if d := DistanceKm(10, 20, 10, 20); d != 0 {
		t.Errorf("same point = %v", d)
	}
}

func TestCityPoint_ScriptsAndSizes(t *testing.T) {
	for _, name := range []string{"Tel Aviv", "tel-aviv", "תל אביב"} {
		if _, ok := CityPoint("il", name); !ok {
			t.Errorf("%q not found", name)
		}
	}
	if _, ok := CityPoint("IL", "Nowhereville"); ok {
		t.Error("an unknown city was found")
	}
	if _, ok := CityPoint("IL", ""); ok {
		t.Error("a blank name was found")
	}
}

func TestThreshold_ScalesWithSizeAndIsBounded(t *testing.T) {
	small := Place{Population: 20000}
	tlv, _ := CityPoint("IL", "Tel Aviv")
	moscow := Place{Population: 12000000}
	if small.ThresholdKm() != 15 {
		t.Errorf("small town = %v, want the 15 km floor", small.ThresholdKm())
	}
	if th := tlv.ThresholdKm(); th < 22 || th > 26 {
		t.Errorf("Tel Aviv = %.1f km, want about 24", th)
	}
	if moscow.ThresholdKm() != 60 {
		t.Errorf("megacity = %v, want the 60 km cap", moscow.ThresholdKm())
	}
}

// Netanya is the intended city, the point is in the middle of Ramat HaSharon:
// far from Netanya AND nearer to another city, which is named.
func TestCheckPoint_Israel_WrongNeighbourCity(t *testing.T) {
	rh, ok := CityPoint("IL", "Ramat HaSharon")
	if !ok {
		t.Fatal("Ramat HaSharon is not in the data")
	}
	c, ok := CheckPoint("IL", "Netanya", rh.Lat, rh.Lon)
	if !ok {
		t.Fatal("Netanya is not in the data")
	}
	if !c.Far {
		t.Errorf("%.1f km from Netanya (limit %.1f) should be far", c.DistanceKm, c.ThresholdKm)
	}
	if c.Nearer == nil || c.Nearer.Name != "Ramat HaSharon" {
		t.Errorf("nearer city = %+v, want Ramat HaSharon", c.Nearer)
	}
	if !c.Suspicious() {
		t.Error("should warn")
	}
}

// A point in Hod HaSharon is not "far" from Tel Aviv (the limit is about 24 km)
// but it is outside Tel Aviv's own radius and Hod HaSharon is nearer.
func TestCheckPoint_Israel_NotFarButNearerAnotherCity(t *testing.T) {
	hod, _ := CityPoint("IL", "Hod HaSharon")
	c, ok := CheckPoint("IL", "Tel Aviv", hod.Lat, hod.Lon)
	if !ok {
		t.Fatal("no verdict")
	}
	if c.Far {
		t.Errorf("%.1f km of %.1f should not be far", c.DistanceKm, c.ThresholdKm)
	}
	if c.Nearer == nil || c.Nearer.Name != "Hod HaSharon" {
		t.Errorf("nearer = %+v", c.Nearer)
	}
}

func TestCheckPoint_InsideTheCityIsQuiet(t *testing.T) {
	tlv, _ := CityPoint("IL", "Tel Aviv")
	c, ok := CheckPoint("IL", "Tel Aviv", tlv.Lat+0.01, tlv.Lon+0.01)
	if !ok || c.Suspicious() {
		t.Errorf("a point in the centre warned: %+v", c)
	}
	if _, ok := CheckPoint("IL", "Nowhereville", 32, 34); ok {
		t.Error("an unknown city gave a verdict")
	}
}
