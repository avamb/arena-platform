package main

// Live Bil24 venue import.  This deliberately lives in the operator-only
// binary: no Bil24 credential or network dependency is introduced to the API.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultBil24URL = "https://api.bil24.pro/json"

type liveVenueImportOptions struct {
	OrgID, DBURL, APIURL, FID, Token, Locale string
	DryRun                                   bool
}
type bil24Country struct{ ID, Name, ISO2, ISO3 string }
type bil24City struct{ ID, Name, CountryID string }
type bil24Venue struct {
	ID, Name, Address, CityID string
	Latitude, Longitude       *float64
}
type liveVenueStats struct{ Countries, Cities, Imported, Updated, Skipped int }

func runLiveVenueImport(opts liveVenueImportOptions) error {
	if opts.APIURL == "" {
		opts.APIURL = envOr("BIL24_API_URL", defaultBil24URL)
	}
	if opts.FID == "" {
		opts.FID = os.Getenv("BIL24_FID")
	}
	if opts.Token == "" {
		opts.Token = os.Getenv("BIL24_TOKEN")
	}
	if opts.FID == "" || opts.Token == "" {
		return fmt.Errorf("--venues requires Bil24 credentials: pass --bil24-fid/--bil24-token or set BIL24_FID/BIL24_TOKEN")
	}
	client := bil24RPCClient{url: opts.APIURL, fid: opts.FID, token: opts.Token, locale: opts.Locale, http: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	countries, err := client.countries(ctx)
	if err != nil {
		return err
	}
	cities, err := client.cities(ctx)
	if err != nil {
		return err
	}
	venues, err := client.venues(ctx)
	if err != nil {
		return err
	}
	if opts.DryRun {
		fmt.Printf("arena-bil24-import venues: DRY RUN\n  countries: %d\n  cities: %d\n  venues: %d\n", len(countries), len(cities), len(venues))
		return nil
	}
	dsn := opts.DBURL
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable or --db-url flag is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	stats, err := importLiveVenues(ctx, pool, opts.OrgID, countries, cities, venues)
	fmt.Printf("arena-bil24-import venues summary\n  countries: %d\n  cities: %d\n  imported: %d\n  updated: %d\n  skipped (unchanged): %d\n", stats.Countries, stats.Cities, stats.Imported, stats.Updated, stats.Skipped)
	return err
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

type bil24RPCClient struct {
	url, fid, token, locale string
	http                    *http.Client
}

func (c bil24RPCClient) call(ctx context.Context, command string) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{"command": command, "fid": c.fid, "token": c.token, "locale": c.locale})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Bil24 %s request: %w", command, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Bil24 %s: %w", command, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Bil24 %s returned HTTP %s", command, resp.Status)
	}
	limited := io.LimitReader(resp.Body, 10<<20)
	var decoded map[string]any
	if err := json.NewDecoder(limited).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode Bil24 %s response: %w", command, err)
	}
	if code, ok := number(decoded["resultCode"]); ok && code != 0 {
		return nil, fmt.Errorf("Bil24 %s failed (resultCode=%d): %s", command, int(code), text(decoded["description"]))
	}
	return decoded, nil
}

func (c bil24RPCClient) countries(ctx context.Context) ([]bil24Country, error) {
	raw, err := c.call(ctx, "GET_COUNTRIES")
	if err != nil {
		return nil, err
	}
	rows := responseList(raw, "countryList", "countries")
	out := make([]bil24Country, 0, len(rows))
	for _, r := range rows {
		out = append(out, bil24Country{ID: field(r, "countryId", "id"), Name: field(r, "countryName", "name"), ISO2: strings.ToUpper(field(r, "iso2", "countryCode", "countryIso2")), ISO3: strings.ToUpper(field(r, "iso3", "countryIso3"))})
	}
	return out, nil
}
func (c bil24RPCClient) cities(ctx context.Context) ([]bil24City, error) {
	raw, err := c.call(ctx, "GET_CITIES")
	if err != nil {
		return nil, err
	}
	rows := responseList(raw, "cityList", "cities")
	out := make([]bil24City, 0, len(rows))
	for _, r := range rows {
		out = append(out, bil24City{ID: field(r, "cityId", "id"), Name: field(r, "cityName", "name"), CountryID: field(r, "countryId")})
	}
	return out, nil
}
func (c bil24RPCClient) venues(ctx context.Context) ([]bil24Venue, error) {
	raw, err := c.call(ctx, "GET_VENUES")
	if err != nil {
		return nil, err
	}
	rows := responseList(raw, "venueList", "venues")
	out := make([]bil24Venue, 0, len(rows))
	for _, r := range rows {
		out = append(out, bil24Venue{ID: field(r, "venueId", "id"), Name: field(r, "venueName", "name"), Address: field(r, "address"), CityID: field(r, "cityId"), Latitude: floatField(r, "latitude", "lat"), Longitude: floatField(r, "longitude", "lng", "lon")})
	}
	return out, nil
}

func responseList(raw map[string]any, keys ...string) []map[string]any {
	for _, container := range []map[string]any{raw, object(raw["data"]), object(raw["result"])} {
		for _, key := range keys {
			if items, ok := container[key].([]any); ok {
				out := make([]map[string]any, 0, len(items))
				for _, item := range items {
					if row := object(item); row != nil {
						out = append(out, row)
					}
				}
				return out
			}
		}
	}
	return nil
}
func object(value any) map[string]any { row, _ := value.(map[string]any); return row }
func field(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(text(row[key])); value != "" {
			return value
		}
	}
	return ""
}
func text(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
func number(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case json.Number:
		f, e := value.Float64()
		return f, e == nil
	case string:
		f, e := strconv.ParseFloat(value, 64)
		return f, e == nil
	default:
		return 0, false
	}
}
func floatField(row map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		if value, ok := number(row[key]); ok {
			return &value
		}
	}
	return nil
}

func importLiveVenues(ctx context.Context, pool *pgxpool.Pool, orgID string, countries []bil24Country, cities []bil24City, venues []bil24Venue) (liveVenueStats, error) {
	stats := liveVenueStats{}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return stats, fmt.Errorf("begin venue import transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	countryIDs := map[string]string{}
	for _, country := range countries {
		iso2, iso3 := countryCodes(country)
		if country.ID == "" || country.Name == "" || iso2 == "" || iso3 == "" {
			return stats, fmt.Errorf("Bil24 country %q lacks a supported ISO-3166 code", country.Name)
		}
		var id string
		// countries.currency is NOT NULL since migration 0081 (AB-38).
		// New rows get the ISO-4217 code for the country; existing rows
		// keep whatever currency they already carry (the ON CONFLICT
		// branch deliberately does not touch it).
		err = tx.QueryRow(ctx, `INSERT INTO countries (iso2, iso3, slug, currency) VALUES ($1,$2,$3,$4) ON CONFLICT (iso2) DO UPDATE SET iso3=EXCLUDED.iso3 RETURNING id`, iso2, iso3, slugFor(country.Name, iso2), currencyForISO2(iso2)).Scan(&id)
		if err != nil {
			return stats, fmt.Errorf("upsert country %q: %w", country.Name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO i18n_text (namespace,key,locale,value) VALUES ('geo.countries',$1,'en',$2) ON CONFLICT (namespace,key,locale) DO UPDATE SET value=EXCLUDED.value`, iso2, country.Name); err != nil {
			return stats, err
		}
		countryIDs[country.ID] = id
		stats.Countries++
	}
	cityIDs := map[string]string{}
	for _, city := range cities {
		countryID := countryIDs[city.CountryID]
		if city.ID == "" || city.Name == "" || countryID == "" {
			return stats, fmt.Errorf("Bil24 city %q has no imported country", city.Name)
		}
		slug := slugFor(city.Name, "bil24-city-"+city.ID)
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO cities (country_id,slug) VALUES ($1,$2) ON CONFLICT (slug) DO UPDATE SET country_id=EXCLUDED.country_id RETURNING id`, countryID, slug).Scan(&id)
		if err != nil {
			return stats, fmt.Errorf("upsert city %q: %w", city.Name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO i18n_text (namespace,key,locale,value) VALUES ('geo.cities',$1,'en',$2) ON CONFLICT (namespace,key,locale) DO UPDATE SET value=EXCLUDED.value`, slug, city.Name); err != nil {
			return stats, err
		}
		cityIDs[city.ID] = id
		stats.Cities++
	}
	for _, venue := range venues {
		cityID := cityIDs[venue.CityID]
		if venue.ID == "" || venue.Name == "" {
			return stats, fmt.Errorf("Bil24 venue has no id or name")
		}
		if venue.CityID != "" && cityID == "" {
			return stats, fmt.Errorf("Bil24 venue %q references unknown city %s", venue.Name, venue.CityID)
		}
		var existed bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM venues WHERE external_bil24_id=$1)`, venue.ID).Scan(&existed); err != nil {
			return stats, err
		}
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO venues (org_id,city_id,name,address,address_line1,geo_lat,geo_lng,country,external_bil24_id) VALUES ($1,NULLIF($2,'')::uuid,$3,NULLIF($4,''),NULLIF($4,''),$5,$6,(SELECT c.iso2 FROM cities ci JOIN countries c ON c.id=ci.country_id WHERE ci.id=NULLIF($2,'')::uuid),$7) ON CONFLICT (external_bil24_id) WHERE external_bil24_id IS NOT NULL DO UPDATE SET city_id=EXCLUDED.city_id,name=EXCLUDED.name,address=EXCLUDED.address,address_line1=EXCLUDED.address_line1,geo_lat=EXCLUDED.geo_lat,geo_lng=EXCLUDED.geo_lng,country=EXCLUDED.country,updated_at=now() WHERE (venues.city_id,venues.name,venues.address,venues.geo_lat,venues.geo_lng) IS DISTINCT FROM (EXCLUDED.city_id,EXCLUDED.name,EXCLUDED.address,EXCLUDED.geo_lat,EXCLUDED.geo_lng) RETURNING id`, orgID, cityID, venue.Name, venue.Address, venue.Latitude, venue.Longitude, venue.ID).Scan(&id)
		if err == pgx.ErrNoRows {
			stats.Skipped++
			continue
		}
		if err != nil {
			return stats, fmt.Errorf("upsert venue %q: %w", venue.Name, err)
		}
		if existed {
			stats.Updated++
		} else {
			stats.Imported++
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return stats, fmt.Errorf("commit venue import: %w", err)
	}
	return stats, nil
}

func countryCodes(country bil24Country) (string, string) {
	iso2, iso3 := country.ISO2, country.ISO3
	if len(iso2) == 3 && iso3 == "" {
		iso3, iso2 = iso2, ""
	}
	known := map[string][2]string{"estonia": {"EE", "EST"}, "russia": {"RU", "RUS"}, "israel": {"IL", "ISR"}, "latvia": {"LV", "LVA"}, "lithuania": {"LT", "LTU"}, "germany": {"DE", "DEU"}, "france": {"FR", "FRA"}, "finland": {"FI", "FIN"}, "sweden": {"SE", "SWE"}, "ukraine": {"UA", "UKR"}}
	if iso2 == "" {
		if code, ok := known[strings.ToLower(strings.TrimSpace(country.Name))]; ok {
			iso2, iso3 = code[0], code[1]
		}
	}
	if len(iso2) != 2 || len(iso3) != 3 {
		return "", ""
	}
	return iso2, iso3
}

// currencyForISO2 maps an ISO-3166 alpha-2 country code to its ISO-4217
// currency for the countries this importer can encounter (the countryCodes
// allowlist plus the 0006 seed set). Unknown codes fall back to USD — the
// same defensive default migration 0081 applies.
func currencyForISO2(iso2 string) string {
	known := map[string]string{
		"EE": "EUR", "RU": "RUB", "IL": "ILS", "LV": "EUR", "LT": "EUR",
		"DE": "EUR", "FR": "EUR", "FI": "EUR", "SE": "SEK", "UA": "UAH",
		"US": "USD", "GB": "GBP",
	}
	if cur, ok := known[iso2]; ok {
		return cur
	}
	return "USD"
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonSlug.ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
}

// Bil24 names can be Cyrillic.  The geo schema has a slug field, so retain a
// stable source-ID fallback while the localized display name remains exact.
func slugFor(value, fallback string) string {
	if slug := slugify(value); slug != "" {
		return slug
	}
	return slugify(fallback)
}
