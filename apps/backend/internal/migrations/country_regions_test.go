package migrations_test

import (
	"regexp"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// Migration 0127 seeds the world-wide country list. A country that lacks a
// name in one of the bot's languages shows as a bare ISO code there, and a
// region outside the CHECK list would fail the whole migration, so both are
// checked on the file itself, without a database.
func TestMigration0127_CountriesAreCompleteAndConsistent(t *testing.T) {
	raw, err := migrations.FS.ReadFile("sql/0127_country_regions.sql")
	if err != nil {
		t.Fatalf("read 0127: %v", err)
	}
	sql := string(raw)

	check := regexp.MustCompile(`CHECK \(region IN \(([^)]*)\)\)`).FindStringSubmatch(sql)
	if check == nil {
		t.Fatal("0127 has no region CHECK constraint")
	}
	allowed := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(check[1], -1) {
		allowed[m[1]] = true
	}

	countries := map[string]string{} // iso2 -> region
	slugs := map[string]bool{}
	for _, m := range regexp.MustCompile(`\('([A-Z]{2})', '([A-Z]{3})', '([a-z-]+)', '([A-Z]{3})', '([a-z_]+)'\)`).FindAllStringSubmatch(sql, -1) {
		if _, dup := countries[m[1]]; dup {
			t.Errorf("country %s is listed twice", m[1])
		}
		if slugs[m[3]] {
			t.Errorf("slug %s is used twice", m[3])
		}
		slugs[m[3]] = true
		if !allowed[m[5]] {
			t.Errorf("country %s has region %q outside the CHECK list", m[1], m[5])
		}
		countries[m[1]] = m[5]
	}
	if len(countries) < 90 {
		t.Fatalf("only %d countries parsed from 0127", len(countries))
	}
	// Countries the platform already served, and the ones the owner asked for.
	for iso2, region := range map[string]string{"IT": "europe", "CH": "europe", "PL": "europe", "BR": "latin_america", "AR": "latin_america", "MX": "latin_america", "US": "north_america", "IL": "middle_east"} {
		if countries[iso2] != region {
			t.Errorf("%s: region %q, want %q", iso2, countries[iso2], region)
		}
	}

	names := map[string]map[string]bool{}
	for _, m := range regexp.MustCompile(`\('geo\.countries', '([A-Z]{2})', '(en|ru|es)', '(?:[^']|'')+'\)`).FindAllStringSubmatch(sql, -1) {
		if names[m[1]] == nil {
			names[m[1]] = map[string]bool{}
		}
		names[m[1]][m[2]] = true
	}
	for iso2 := range countries {
		for _, loc := range []string{"en", "ru", "es"} {
			if !names[iso2][loc] {
				t.Errorf("country %s has no %s name", iso2, loc)
			}
		}
	}
	for iso2 := range names {
		if _, ok := countries[iso2]; !ok {
			t.Errorf("a name is seeded for %s, which is not in the country list", iso2)
		}
	}
}
