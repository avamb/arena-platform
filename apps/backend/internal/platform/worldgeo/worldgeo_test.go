package worldgeo

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

func TestTable_SizeCodesAndRegions(t *testing.T) {
	if parseErr != nil {
		t.Fatalf("the embedded table does not parse: %v", parseErr)
	}
	all := All()
	if len(all) < 245 || len(all) > 252 {
		t.Fatalf("the table has %d countries, want the ISO 3166-1 list (249)", len(all))
	}
	alpha2 := regexp.MustCompile(`^[A-Z]{2}$`)
	alpha3 := regexp.MustCompile(`^[A-Z]{3}$`)
	seen2, seen3 := map[string]bool{}, map[string]bool{}
	for _, c := range all {
		if !alpha2.MatchString(c.Iso2) || !alpha3.MatchString(c.Iso3) {
			t.Errorf("%s/%s: bad code", c.Iso2, c.Iso3)
		}
		if !alpha3.MatchString(c.Currency) {
			t.Errorf("%s: currency %q is not three letters", c.Iso2, c.Currency)
		}
		if !ValidRegion(c.Region) {
			t.Errorf("%s: region %q is not one of the seven", c.Iso2, c.Region)
		}
		if strings.TrimSpace(c.NameEn) == "" || strings.TrimSpace(c.NameRu) == "" {
			t.Errorf("%s: a name is missing", c.Iso2)
		}
		if seen2[c.Iso2] {
			t.Errorf("duplicate iso2 %s", c.Iso2)
		}
		if seen3[c.Iso3] {
			t.Errorf("duplicate iso3 %s", c.Iso3)
		}
		seen2[c.Iso2], seen3[c.Iso3] = true, true
	}
}

func TestTable_OwnerRegionChoices(t *testing.T) {
	want := map[string]string{
		"AM": RegionEurope, "GE": RegionEurope, "CY": RegionEurope, "RU": RegionEurope,
		"KZ": RegionAsia, "TR": RegionMiddleEast, "IL": RegionMiddleEast,
	}
	for iso2, region := range want {
		c, ok := Lookup(strings.ToLower(iso2))
		if !ok || c.Region != region {
			t.Errorf("%s: region %q (found %v), want %q", iso2, c.Region, ok, region)
		}
	}
	if _, ok := Lookup("ZZ"); ok {
		t.Error("ZZ is not a country")
	}
	if len(Regions()) != 7 {
		t.Errorf("regions = %v", Regions())
	}
}

// TestEveryCountryHasAZone: the timezone route answers for every country of
// the table, from the city data or from the fallback list.
func TestEveryCountryHasAZone(t *testing.T) {
	for _, c := range All() {
		fb := FallbackZones(c.Iso2)
		inData := geotz.ForCountry(c.Iso2) != "" || len(geotz.ZonesFor(c.Iso2)) > 0
		if !inData && len(fb) == 0 {
			t.Errorf("%s has no time zone", c.Iso2)
		}
		if inData && len(fb) > 0 {
			t.Errorf("%s is in the city data now, drop its fallback", c.Iso2)
		}
		for _, z := range fb {
			if _, err := time.LoadLocation(z); err != nil {
				t.Errorf("%s: zone %s: %v", c.Iso2, z, err)
			}
		}
	}
}

func TestCountry_Name(t *testing.T) {
	c, _ := Lookup("MY")
	if c.Name("ru") != "Малайзия" || c.Name("en") != "Malaysia" || c.Name("es") != "Malaysia" {
		t.Errorf("names: %q %q %q", c.Name("ru"), c.Name("en"), c.Name("es"))
	}
}

// migration0127Row matches one ('XX', 'XXX', 'slug', 'CUR', 'region') tuple.
var migration0127Row = regexp.MustCompile(`\('([A-Z]{2})', '([A-Z]{3})', '[a-z-]+', '([A-Z]{3})', '([a-z_]+)'\)`)

// TestTable_MatchesMigration0127 keeps the table and the countries migration
// 0127 seeded in agreement: a country created later from the table must look
// like one the migration created.
func TestTable_MatchesMigration0127(t *testing.T) {
	entries, err := migrations.FS.ReadDir(migrations.Dir)
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	name := ""
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "0127_") {
			name = e.Name()
		}
	}
	if name == "" {
		t.Fatal("migration 0127 is not embedded")
	}
	raw, err := migrations.FS.ReadFile(migrations.Dir + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	rows := migration0127Row.FindAllStringSubmatch(up, -1)
	if len(rows) < 90 {
		t.Fatalf("parsed %d country rows from %s, the pattern is stale", len(rows), name)
	}
	for _, m := range rows {
		c, ok := Lookup(m[1])
		if !ok {
			t.Errorf("%s: seeded by %s but missing from the table", m[1], name)
			continue
		}
		if c.Iso3 != m[2] || c.Currency != m[3] || c.Region != m[4] {
			t.Errorf("%s: table has %s/%s/%s, %s has %s/%s/%s", m[1], c.Iso3, c.Currency, c.Region, name, m[2], m[3], m[4])
		}
	}
}
