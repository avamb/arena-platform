package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBil24LiveVenueClientRecordedFixture(t *testing.T) {
	requests := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			t.Fatal(err)
		}
		command := body["command"].(string)
		requests[command] = true
		w.Header().Set("Content-Type", "application/json")
		switch command {
		case "GET_COUNTRIES":
			_, _ = w.Write([]byte(`{"resultCode":0,"countryList":[{"countryId":233,"countryName":"Estonia","iso2":"EE","iso3":"EST"}]}`))
		case "GET_CITIES":
			_, _ = w.Write([]byte(`{"resultCode":0,"cityList":[{"cityId":1,"cityName":"Tallinn","countryId":233}]}`))
		case "GET_VENUES":
			_, _ = w.Write([]byte(`{"resultCode":0,"venueList":[{"venueId":10549,"venueName":"Palac Akropolis","cityId":1,"address":"Narva mnt 7, Tallinn","latitude":59.436962,"longitude":24.753574}]}`))
		default:
			t.Fatalf("unexpected command %q", command)
		}
	}))
	defer server.Close()
	client := bil24RPCClient{url: server.URL, fid: "1271", token: "secret", locale: "ru-RU", http: server.Client()}
	countries, err := client.countries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cities, err := client.cities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	venues, err := client.venues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(countries) != 1 || countries[0].ISO2 != "EE" || len(cities) != 1 || cities[0].CountryID != "233" {
		t.Fatalf("unexpected geo data: %#v %#v", countries, cities)
	}
	if len(venues) != 1 || venues[0].ID != "10549" || venues[0].Latitude == nil || *venues[0].Latitude != 59.436962 {
		t.Fatalf("unexpected venue data: %#v", venues)
	}
	for _, command := range []string{"GET_COUNTRIES", "GET_CITIES", "GET_VENUES"} {
		if !requests[command] {
			t.Errorf("%s was not requested", command)
		}
	}
}

func TestBil24LiveVenueClientRejectsRPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resultCode":1,"description":"bad token"}`))
	}))
	defer server.Close()
	_, err := (bil24RPCClient{url: server.URL, fid: "1", token: "bad", http: server.Client()}).countries(context.Background())
	if err == nil {
		t.Fatal("expected RPC error")
	}
}

func TestCountryCodes(t *testing.T) {
	iso2, iso3 := countryCodes(bil24Country{Name: "Estonia"})
	if iso2 != "EE" || iso3 != "EST" {
		t.Fatalf("got %s/%s", iso2, iso3)
	}
	if got := slugFor("Дворец культуры", "bil24-city-77"); got != "bil24-city-77" {
		t.Fatalf("Cyrillic city fallback = %q", got)
	}
}

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

func readJSON(r *http.Request, into any) error { return json.NewDecoder(r.Body).Decode(into) }
