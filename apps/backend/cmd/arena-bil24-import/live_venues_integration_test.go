//go:build integration

package main

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLiveVenueImportPersistence proves the AB-21 (#405) live importer against
// a real migrated PostgreSQL: idempotent re-runs and persisted address +
// external id. Lives behind the integration tag because it needs the goose
// schema and a seeded organization; the plain Unit job exposes a DATABASE_URL
// without either (that combination failed CI run 30510302440).
func TestLiveVenueImportPersistence(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is required for the PostgreSQL import proof")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var orgID string
	if err := pool.QueryRow(ctx, `SELECT id FROM organizations ORDER BY created_at LIMIT 1`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	countries := []bil24Country{{ID: "country-" + suffix, Name: "Estonia", ISO2: "EE", ISO3: "EST"}}
	cities := []bil24City{{ID: "city-" + suffix, Name: "TEST_405 City " + suffix, CountryID: countries[0].ID}}
	venues := []bil24Venue{{ID: "TEST_405_" + suffix, Name: "TEST_405 Venue " + suffix, Address: "1 Test Street", CityID: cities[0].ID}}
	stats, err := importLiveVenues(ctx, pool, orgID, countries, cities, venues)
	if err != nil || stats.Imported != 1 {
		t.Fatalf("first import: stats=%+v err=%v", stats, err)
	}
	stats, err = importLiveVenues(ctx, pool, orgID, countries, cities, venues)
	if err != nil || stats.Skipped != 1 {
		t.Fatalf("rerun: stats=%+v err=%v", stats, err)
	}
	var externalID, address string
	if err := pool.QueryRow(ctx, `SELECT external_bil24_id, address FROM venues WHERE external_bil24_id=$1`, venues[0].ID).Scan(&externalID, &address); err != nil {
		t.Fatal(err)
	}
	if externalID != venues[0].ID || address != venues[0].Address {
		t.Fatalf("persisted venue mismatch: %q %q", externalID, address)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM venues WHERE external_bil24_id=$1`, venues[0].ID); err != nil {
		t.Fatal(err)
	}
}
