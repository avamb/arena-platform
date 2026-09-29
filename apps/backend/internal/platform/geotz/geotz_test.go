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
