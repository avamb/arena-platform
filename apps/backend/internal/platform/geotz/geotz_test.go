package geotz

import (
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// Every zone in the table must be one the Go runtime can load — a typo here
// would make the bot and the import write a venue the API would then refuse
// to PATCH.
func TestCountryZones_AreKnownIANAZones(t *testing.T) {
	for iso2, zone := range countryZones {
		if _, err := time.LoadLocation(zone); err != nil {
			t.Errorf("%s: %q is not a loadable IANA zone: %v", iso2, zone, err)
		}
	}
	if got := ForCountry(" cz "); got != "Europe/Prague" {
		t.Errorf("ForCountry folds case and space: got %q", got)
	}
	if got := ForCountry("ES"); got != "" {
		t.Errorf("Spain has two zones and must be absent, got %q", got)
	}
}

var migrationPairPattern = regexp.MustCompile(`\('([A-Z]{2})',\s*'([A-Za-z_/]+)'\)`)

// Migration 0118 backfills legacy venues from a copy of this table written
// in SQL. The two must list exactly the same pairs.
func TestCountryZones_MatchMigration0118(t *testing.T) {
	sql, err := migrations.FS.ReadFile("sql/0118_venues_timezone_required.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	fromSQL := map[string]string{}
	for _, m := range migrationPairPattern.FindAllStringSubmatch(string(sql), -1) {
		fromSQL[m[1]] = m[2]
	}
	if len(fromSQL) == 0 {
		t.Fatal("no (iso2, zone) pairs found in migration 0118")
	}
	keys := Countries()
	sort.Strings(keys)
	for _, iso2 := range keys {
		if fromSQL[iso2] != countryZones[iso2] {
			t.Errorf("%s: Go says %q, migration 0118 says %q", iso2, countryZones[iso2], fromSQL[iso2])
		}
	}
	for iso2, zone := range fromSQL {
		if _, ok := countryZones[iso2]; !ok {
			t.Errorf("%s (%s) is in migration 0118 but not in geotz", iso2, zone)
		}
	}
}

// The city data resolves the cases the country table refuses to guess, in the
// scripts people actually type.
func TestForCity_ResolvesManyZoneCountries(t *testing.T) {
	cases := []struct{ iso2, city, want string }{
		{"US", "New York", "America/New_York"},
		{"us", "  los angeles ", "America/Los_Angeles"},
		{"US", "Chicago", "America/Chicago"},
		{"ES", "Las Palmas de Gran Canaria", "Atlantic/Canary"},
		{"ES", "Madrid", "Europe/Madrid"},
		{"ES", "Málaga", "Europe/Madrid"}, // accents are folded
		{"PT", "Ponta Delgada", "Atlantic/Azores"},
		{"RU", "Москва", "Europe/Moscow"},
		{"RU", "Владивосток", "Asia/Vladivostok"},
		{"CA", "Vancouver", "America/Vancouver"},
		{"AU", "Perth", "Australia/Perth"},
		{"ES", "Atlantis", ""},
		{"ES", "", ""},
		{"CZ", "Praha", ""}, // a single-zone country is answered by ForCountry
	}
	for _, c := range cases {
		if got := ForCity(c.iso2, c.city); got != c.want {
			t.Errorf("ForCity(%q, %q) = %q, want %q", c.iso2, c.city, got, c.want)
		}
	}
}

func TestZonesFor_ListsTheCountrysZonesBiggestFirst(t *testing.T) {
	us := ZonesFor("US")
	if len(us) < 4 || us[0].ID != "America/New_York" {
		t.Fatalf("US zones = %+v", us)
	}
	es := ZonesFor("es")
	if len(es) < 2 || es[0].ID != "Europe/Madrid" || es[1].ID != "Atlantic/Canary" {
		t.Fatalf("ES zones = %+v", es)
	}
	if in := ZonesFor("IN"); len(in) != 1 || in[0].ID != "Asia/Kolkata" {
		t.Fatalf("IN zones = %+v", in)
	}
	if got := ZonesFor("CZ"); got != nil {
		t.Fatalf("a country of the single-zone table has no entry, got %+v", got)
	}
	if CityOf("US", "America/New_York") == "" {
		t.Error("CityOf returned nothing for a known zone")
	}
}

// Every zone the data names must be one the runtime can load.
func TestCityData_ZonesAreLoadable(t *testing.T) {
	loadOnce.Do(load)
	if len(zonesBy) < 100 || len(cityZone) < 10 {
		t.Fatalf("data looks empty: %d countries, %d with cities", len(zonesBy), len(cityZone))
	}
	for cc, zs := range zonesBy {
		for _, z := range zs {
			if _, err := time.LoadLocation(z.ID); err != nil {
				t.Errorf("%s: %q is not a loadable zone: %v", cc, z.ID, err)
			}
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"  São Paulo ": "sao paulo", "St. John's": "st john s", "Нью-Йорк": "нью иорк", "": "", "---": "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopCities_CoverTheOfferedRegions(t *testing.T) {
	for _, cc := range []string{"ES", "CZ", "DE", "FR", "US", "MX", "BR", "AR", "IL", "GB", "PL"} {
		top := TopCities(cc)
		if len(top) < 10 {
			t.Errorf("%s: only %d cities", cc, len(top))
		}
	}
	if es := TopCities("es"); len(es) == 0 || es[0] != "Madrid" {
		t.Errorf("ES must start with Madrid: %v", es)
	}
	if us := TopCities("US"); len(us) == 0 || us[0] != "New York City" {
		t.Errorf("US must start with New York City: %v", us)
	}
	if got := TopCities("JP"); got != nil {
		t.Errorf("Japan is not offered, got %v", got)
	}
}
