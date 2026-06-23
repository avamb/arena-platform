// geo_test.go — unit tests for feature #123 (Country & city reference data).
//
// Test coverage:
//   Step 1: Migration file 0006_geo.sql exists with countries/cities schema + IL/EE seeds
//   Step 2: GET /v1/geo/countries, GET /v1/geo/cities routes mounted and responding
//   Step 3: Admin POST/PATCH routes require JWT auth (401 when unauthenticated)
//   Step 4: i18n_text linkage — migration seeds geo.countries / geo.cities namespaces;
//           geo.sql queries use i18n_text LEFT JOINs with COALESCE locale fallback
//
// All tests are unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test server factory for geo admin-route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildGeoAdminServer builds a Server with stub auth enabled and geo routes
// fully mounted. The pool is a dbDownPool so any real DB operation returns a
// connection error — but auth middleware fires before the handler reaches the
// pool, so unauthenticated requests get 401 (not 503).
//
// GeoQueries is injected via Options.GeoQueries so the geo routes are mounted
// during New() without requiring a real *pgxpool.Pool.
func buildGeoAdminServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildGeoAdminServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// Pool is non-nil so the admin route guard passes. dbDownPool makes
		// any real DB call fail, but auth middleware fires first → 401.
		Pool: &dbDownPool{},
		// GeoQueries is non-nil (gen.New(nil)) so the geo route conditionals
		// in mountV1Routes() are satisfied at server construction time.
		GeoQueries: gen.New(nil),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration file exists with correct schema + seeds
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0006_geo.sql")
	if content == "" {
		t.Fatal("0006_geo.sql is empty")
	}
}

func TestGeo123_MigrationHasCountriesTable(t *testing.T) {
	sql := findFileByName(t, "0006_geo.sql")
	for _, check := range []string{"CREATE TABLE countries", "iso2", "iso3", "slug"} {
		if !strings.Contains(sql, check) {
			t.Errorf("0006_geo.sql missing: %q", check)
		}
	}
}

func TestGeo123_MigrationHasCitiesTable(t *testing.T) {
	sql := findFileByName(t, "0006_geo.sql")
	for _, check := range []string{"CREATE TABLE cities", "country_id", "REFERENCES countries"} {
		if !strings.Contains(sql, check) {
			t.Errorf("0006_geo.sql missing: %q", check)
		}
	}
}

func TestGeo123_MigrationHasILSeed(t *testing.T) {
	sql := findFileByName(t, "0006_geo.sql")
	if !strings.Contains(sql, "'IL'") {
		t.Error("migration missing Israel (IL) country seed")
	}
	if !strings.Contains(sql, "'ISR'") {
		t.Error("migration missing ISR iso3 seed")
	}
	if !strings.Contains(sql, "tel-aviv") {
		t.Error("migration missing Tel Aviv city seed")
	}
}

func TestGeo123_MigrationHasEESeed(t *testing.T) {
	sql := findFileByName(t, "0006_geo.sql")
	if !strings.Contains(sql, "'EE'") {
		t.Error("migration missing Estonia (EE) country seed")
	}
	if !strings.Contains(sql, "'EST'") {
		t.Error("migration missing EST iso3 seed")
	}
	if !strings.Contains(sql, "tallinn") {
		t.Error("migration missing Tallinn city seed")
	}
}

func TestGeo123_MigrationHasI18nSeeds(t *testing.T) {
	sql := findFileByName(t, "0006_geo.sql")
	for _, check := range []string{
		"geo.countries", "geo.cities",
		"Israel", "Израиль",
		"Estonia", "Эстония",
		"Tel Aviv", "Тель-Авив",
		"Tallinn", "Таллин",
	} {
		if !strings.Contains(sql, check) {
			t.Errorf("migration missing i18n seed: %q", check)
		}
	}
}

func TestGeo123_MigrationHasSlugUniqueness(t *testing.T) {
	sql := findFileByName(t, "0006_geo.sql")
	for _, check := range []string{"cities_slug_uniq", "countries_slug_uniq", "countries_iso2_uniq"} {
		if !strings.Contains(sql, check) {
			t.Errorf("migration missing constraint: %q", check)
		}
	}
}

func TestGeo123_MigrationHasGooseDownSection(t *testing.T) {
	sql := findFileByName(t, "0006_geo.sql")
	if !strings.Contains(sql, "-- +goose Down") {
		t.Error("migration missing +goose Down section")
	}
	if !strings.Contains(sql, "DROP TABLE IF EXISTS cities") {
		t.Error("migration Down missing DROP TABLE cities")
	}
	if !strings.Contains(sql, "DROP TABLE IF EXISTS countries") {
		t.Error("migration Down missing DROP TABLE countries")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — GET /v1/geo/countries and GET /v1/geo/cities route behaviour
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_GetCountriesReturns503WhenNoDB(t *testing.T) {
	// Server with nil geoQueries → 503 (dependency guard fires before any DB call).
	s := &Server{cfg: &config.Config{DefaultLocale: "en", ActiveLocales: []string{"en", "ru"}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/geo/countries", nil)
	rec := httptest.NewRecorder()
	s.handleListCountries(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when geoQueries=nil, got %d", rec.Code)
	}
}

func TestGeo123_GetCitiesReturns503WhenNoDB(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en", ActiveLocales: []string{"en", "ru"}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/geo/cities", nil)
	rec := httptest.NewRecorder()
	s.handleListCities(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when geoQueries=nil, got %d", rec.Code)
	}
}

func TestGeo123_GetCitiesInvalidCountryIDReturns400(t *testing.T) {
	// Wire a non-nil geoQueries so the handler gets past the nil guard.
	s := &Server{
		cfg:        &config.Config{DefaultLocale: "en", ActiveLocales: []string{"en", "ru"}},
		geoQueries: gen.New(nil),
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/geo/cities?country_id=not-a-uuid", nil)
	rec := httptest.NewRecorder()
	s.handleListCities(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid country_id, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("cannot decode response: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if code != "geo.invalid_country_id" {
		t.Errorf("expected error code 'geo.invalid_country_id', got %q", code)
	}
}

func TestGeo123_PublicGetCountriesRouteMounted(t *testing.T) {
	s := buildGeoAdminServer(t) // has geoQueries wired → public routes mounted

	req := httptest.NewRequest(http.MethodGet, "/v1/geo/countries", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Error("GET /v1/geo/countries returned 404 — route not mounted")
	}
}

func TestGeo123_PublicGetCitiesRouteMounted(t *testing.T) {
	s := buildGeoAdminServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/geo/cities", nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Error("GET /v1/geo/cities returned 404 — route not mounted")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Admin endpoints require authentication (401 when no JWT)
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_AdminCreateCountryRequiresAuth(t *testing.T) {
	s := buildGeoAdminServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/geo/countries",
		strings.NewReader(`{"iso2":"ZZ","iso3":"ZZZ","slug":"zz-land"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Error("POST /v1/admin/geo/countries returned 404 — route not mounted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d", rec.Code)
	}
}

func TestGeo123_AdminUpdateCountryRequiresAuth(t *testing.T) {
	s := buildGeoAdminServer(t)
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/geo/countries/IL",
		strings.NewReader(`{"iso3":"ISR"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Error("PATCH /v1/admin/geo/countries/{iso2} returned 404 — route not mounted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d", rec.Code)
	}
}

func TestGeo123_AdminCreateCityRequiresAuth(t *testing.T) {
	s := buildGeoAdminServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/geo/cities",
		strings.NewReader(`{"country_id":"00000000-0000-0000-0000-000000000001","slug":"new-city"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Error("POST /v1/admin/geo/cities returned 404 — route not mounted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d", rec.Code)
	}
}

func TestGeo123_AdminUpdateCityRequiresAuth(t *testing.T) {
	s := buildGeoAdminServer(t)
	cityID := uuid.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/geo/cities/"+cityID.String(),
		strings.NewReader(`{"slug":"updated-city"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Error("PATCH /v1/admin/geo/cities/{id} returned 404 — route not mounted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without JWT, got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — i18n linkage: geo.sql queries use i18n_text with locale fallback
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_SQLQueryFileHasLocaleFallback(t *testing.T) {
	sql := findFileByName(t, "geo.sql")
	for _, check := range []string{"COALESCE", "i18n_text", "geo.countries", "geo.cities"} {
		if !strings.Contains(sql, check) {
			t.Errorf("geo.sql missing: %q", check)
		}
	}
}

func TestGeo123_SQLQueryFileHasListCitiesByCountry(t *testing.T) {
	sql := findFileByName(t, "geo.sql")
	if !strings.Contains(sql, "country_id") {
		t.Error("geo.sql ListCities missing country_id filter")
	}
}

func TestGeo123_GeneratedGoFileHasAllMethods(t *testing.T) {
	src := findFileByName(t, "geo.sql.go")
	for _, check := range []string{
		"ListCountries", "ListCities",
		"InsertCountry", "UpdateCountry",
		"InsertCity", "UpdateCity",
		"ListCountryRow", "ListCityRow",
	} {
		if !strings.Contains(src, check) {
			t.Errorf("geo.sql.go missing: %q", check)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// geoLocale() — locale resolution from request
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_GeoLocaleDefaultsToEn(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en", ActiveLocales: []string{"en", "ru"}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/geo/countries", nil)
	if got := s.geoLocale(req); got != "en" {
		t.Errorf("geoLocale() = %q, want 'en'", got)
	}
}

func TestGeo123_GeoLocaleLangParam(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en", ActiveLocales: []string{"en", "ru"}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/geo/countries?lang=ru", nil)
	if got := s.geoLocale(req); got != "ru" {
		t.Errorf("geoLocale(?lang=ru) = %q, want 'ru'", got)
	}
}

func TestGeo123_GeoLocaleAcceptLanguage(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en", ActiveLocales: []string{"en", "ru"}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/geo/countries", nil)
	req.Header.Set("Accept-Language", "ru")
	if got := s.geoLocale(req); got != "ru" {
		t.Errorf("geoLocale(Accept-Language:ru) = %q, want 'ru'", got)
	}
}

func TestGeo123_GeoLocaleUnsupportedFallsToDefault(t *testing.T) {
	s := &Server{cfg: &config.Config{DefaultLocale: "en", ActiveLocales: []string{"en", "ru"}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/geo/countries?lang=ja", nil)
	if got := s.geoLocale(req); got != "en" {
		t.Errorf("geoLocale(?lang=ja) = %q, want fallback 'en'", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// i18n.NegotiateLocale — geo locale fallback chain unit tests
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_I18nLocaleNegotiation(t *testing.T) {
	tests := []struct {
		accept, lang, want string
	}{
		{"ru-RU", "", "ru"},
		{"", "ru", "ru"},
		{"", "", "en"},
		{"ja", "", "en"},
	}
	for _, tc := range tests {
		got := i18n.NegotiateLocale(tc.accept, tc.lang, "", "en", []string{"en", "ru"})
		if got != tc.want {
			t.Errorf("NegotiateLocale(%q, %q) = %q, want %q", tc.accept, tc.lang, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// geoFirstNonEmpty helper
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_GeoFirstNonEmpty(t *testing.T) {
	if got := geoFirstNonEmpty("", "b", "c"); got != "b" {
		t.Errorf("geoFirstNonEmpty('','b','c') = %q, want 'b'", got)
	}
	if got := geoFirstNonEmpty("a", "b"); got != "a" {
		t.Errorf("geoFirstNonEmpty('a','b') = %q, want 'a'", got)
	}
	if got := geoFirstNonEmpty("", ""); got != "" {
		t.Errorf("geoFirstNonEmpty('','') = %q, want ''", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Full verification (all 4 steps as subtests)
// ─────────────────────────────────────────────────────────────────────────────

func TestGeo123_FullVerification(t *testing.T) {
	t.Run("Step1_MigrationExists", func(t *testing.T) {
		sql := findFileByName(t, "0006_geo.sql")
		for _, want := range []string{"CREATE TABLE countries", "CREATE TABLE cities", "'IL'", "'EE'", "tel-aviv", "tallinn"} {
			if !strings.Contains(sql, want) {
				t.Errorf("migration missing: %q", want)
			}
		}
	})

	t.Run("Step2_PublicRoutesMount", func(t *testing.T) {
		s := buildGeoAdminServer(t)
		for _, path := range []string{"/v1/geo/countries", "/v1/geo/cities"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Errorf("public route %s not mounted (404)", path)
			}
		}
	})

	t.Run("Step3_AdminRoutesRequireAuth", func(t *testing.T) {
		s := buildGeoAdminServer(t)
		adminRoutes := []struct {
			method string
			path   string
			body   string
		}{
			{"POST", "/v1/admin/geo/countries", `{"iso2":"XX","iso3":"XXX","slug":"xx-test"}`},
			{"PATCH", "/v1/admin/geo/countries/IL", `{"iso3":"ISR"}`},
			{"POST", "/v1/admin/geo/cities", `{"country_id":"00000000-0000-0000-0000-000000000001","slug":"test-city"}`},
			{"PATCH", "/v1/admin/geo/cities/" + uuid.New().String(), `{"slug":"test-city"}`},
		}
		for _, ar := range adminRoutes {
			req := httptest.NewRequest(ar.method, ar.path, strings.NewReader(ar.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Errorf("admin route %s %s not mounted (404)", ar.method, ar.path)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("admin route %s %s: expected 401 without JWT, got %d", ar.method, ar.path, rec.Code)
			}
		}
	})

	t.Run("Step4_I18nLinkage", func(t *testing.T) {
		sql := findFileByName(t, "0006_geo.sql")
		for _, want := range []string{"geo.countries", "geo.cities", "Israel", "Израиль", "Estonia", "Таллин"} {
			if !strings.Contains(sql, want) {
				t.Errorf("migration i18n seeds missing: %q", want)
			}
		}
		qSQL := findFileByName(t, "geo.sql")
		if !strings.Contains(qSQL, "i18n_text") {
			t.Error("geo.sql missing i18n_text join")
		}
		if !strings.Contains(qSQL, "COALESCE") {
			t.Error("geo.sql missing COALESCE locale fallback")
		}
	})
}
