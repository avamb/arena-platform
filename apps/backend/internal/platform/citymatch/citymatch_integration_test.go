//go:build integration

package citymatch

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Everything runs in one transaction that is rolled back: the rows are
// throwaway countries and cities, nothing stays in the database.
func TestFindInCountry_Integration(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var home, other string
	for iso, dst := range map[string]*string{"ZX": &home, "ZY": &other} {
		if err := tx.QueryRow(ctx, `INSERT INTO countries (iso2, iso3, slug, currency) VALUES ($1, $2, $3, 'EUR') RETURNING id`, iso, iso+"Q", "citymatch-test-"+strings.ToLower(iso)).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	mkCity := func(country, slug string, names map[string]string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO cities (country_id, slug) VALUES ($1, $2) RETURNING id`, country, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for loc, v := range names {
			if _, err := tx.Exec(ctx, `INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, $2, $3)`, slug, loc, v); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	pragueID := mkCity(home, "citymatch-prague", map[string]string{"en": "Prague", "ru": "Прага"})
	otherID := mkCity(other, "citymatch-prague-other", map[string]string{"en": "Prague"})

	homeUUID := mustUUID(t, home)
	otherUUID := mustUUID(t, other)

	for _, name := range []string{"Praha", "Prague", "Прага"} {
		got, ok, err := FindInCountry(ctx, tx, homeUUID, "CZ", name)
		if err != nil || !ok || got.ID.String() != pragueID {
			t.Errorf("%q: got %v %v %v, want the Prague row %s", name, got, ok, err, pragueID)
		}
	}
	if got, ok, err := FindInCountry(ctx, tx, homeUUID, "CZ", "Ostrava"); err != nil || ok {
		t.Errorf("Ostrava must be new: %v %v %v", got, ok, err)
	}
	// A city of the same name in ANOTHER country is never matched: each
	// country finds only its own row.
	if got, ok, err := FindInCountry(ctx, tx, otherUUID, "CZ", "Praha"); err != nil || !ok || got.ID.String() != otherID {
		t.Errorf("other country must find its own row %s: %v %v %v", otherID, got, ok, err)
	}
	var third string
	if err := tx.QueryRow(ctx, `INSERT INTO countries (iso2, iso3, slug, currency) VALUES ('ZW', 'ZWQ', 'citymatch-test-zw', 'EUR') RETURNING id`).Scan(&third); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := FindInCountry(ctx, tx, mustUUID(t, third), "CZ", "Praha"); err != nil || ok {
		t.Errorf("a country with no cities must not match: %v %v %v", got, ok, err)
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
