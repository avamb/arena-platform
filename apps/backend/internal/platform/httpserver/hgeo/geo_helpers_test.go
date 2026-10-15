package hgeo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// helperRouter mounts the two path helpers the way mount_iam.go does.
func helperRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Get("/v1/geo/countries/{iso2}/timezones", h.HandleCountryTimezones)
	r.Get("/v1/geo/countries/{iso2}/currency", h.HandleCountryCurrency)
	return r
}

func getJSON(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body %q is not JSON: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body
}

func zoneIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	var ids []string
	for _, z := range body["timezones"].([]any) {
		ids = append(ids, z.(map[string]any)["id"].(string))
	}
	return ids
}

func contains(ids []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, id := range ids {
			if id == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestCountryTimezones(t *testing.T) {
	rt := helperRouter(New(nil, nil, nil))

	code, body := getJSON(t, rt, "/v1/geo/countries/ES/timezones")
	if code != http.StatusOK || body["single"] != false {
		t.Fatalf("ES: code %d single %v, want 200 and false", code, body["single"])
	}
	if ids := zoneIDs(t, body); !contains(ids, "Europe/Madrid", "Atlantic/Canary") {
		t.Errorf("ES zones %v lack Europe/Madrid or Atlantic/Canary", ids)
	}

	// The code folds case and the answer is the same.
	code, body = getJSON(t, rt, "/v1/geo/countries/pt/timezones")
	if code != http.StatusOK || body["single"] != false {
		t.Fatalf("PT: code %d single %v", code, body["single"])
	}
	if ids := zoneIDs(t, body); !contains(ids, "Europe/Lisbon", "Atlantic/Azores", "Atlantic/Madeira") {
		t.Errorf("PT zones %v lack Lisbon, Azores or Madeira", ids)
	}

	code, body = getJSON(t, rt, "/v1/geo/countries/CZ/timezones")
	if code != http.StatusOK || body["single"] != true {
		t.Fatalf("CZ: code %d single %v, want 200 and true", code, body["single"])
	}
	zs := body["timezones"].([]any)
	if len(zs) != 1 || zs[0].(map[string]any)["id"] != "Europe/Prague" || zs[0].(map[string]any)["label"] != "Prague" {
		t.Errorf("CZ zones = %v, want one Europe/Prague labelled Prague", zs)
	}
}

func TestCountryTimezones_UnknownAndMalformed(t *testing.T) {
	rt := helperRouter(New(nil, nil, nil))
	code, body := getJSON(t, rt, "/v1/geo/countries/ZZ/timezones")
	if code != http.StatusNotFound || body["error"].(map[string]any)["code"] != "geo.country_not_found" {
		t.Errorf("ZZ: %d %v, want 404 geo.country_not_found", code, body)
	}
	code, body = getJSON(t, rt, "/v1/geo/countries/ESP/timezones")
	if code != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "geo.invalid_iso2" {
		t.Errorf("ESP: %d %v, want 400 geo.invalid_iso2", code, body)
	}
}

func TestCountryCurrency(t *testing.T) {
	h := New(nil, nil, nil)
	table := map[string]string{"ES": "EUR", "CZ": "CZK", "IL": "ILS"}
	h.currencyLookup = func(_ context.Context, iso2 string) (string, error) {
		if cur, ok := table[iso2]; ok {
			return cur + " ", nil // the column is char(3): padding must not leak
		}
		return "", pgx.ErrNoRows
	}
	rt := helperRouter(h)
	for iso2, want := range table {
		code, body := getJSON(t, rt, "/v1/geo/countries/"+iso2+"/currency")
		if code != http.StatusOK || body["currency"] != want {
			t.Errorf("%s: %d %v, want 200 %s", iso2, code, body, want)
		}
	}
	code, body := getJSON(t, rt, "/v1/geo/countries/ZZ/currency")
	if code != http.StatusNotFound || body["error"].(map[string]any)["code"] != "geo.country_not_found" {
		t.Errorf("ZZ: %d %v, want 404 geo.country_not_found", code, body)
	}
}

func TestCountryCurrency_NoDatabase(t *testing.T) {
	code, _ := getJSON(t, helperRouter(New(nil, nil, nil)), "/v1/geo/countries/ES/currency")
	if code != http.StatusServiceUnavailable {
		t.Errorf("no queries and no lookup: %d, want 503", code)
	}
}

func TestAssembleCities_SearchSpainByPrefixAndCyrillic(t *testing.T) {
	cities, got := assembleCities(nil, "ES", false, "barc")
	if len(cities) != 0 {
		t.Errorf("no platform rows, yet cities = %+v", cities)
	}
	if len(got) == 0 || got[0].Name != "Barcelona" || got[0].Timezone != "Europe/Madrid" || got[0].Source != "geonames" || got[0].CountryIso2 != "ES" {
		t.Fatalf("barc: %+v, want Barcelona/Europe/Madrid/geonames first", got)
	}
	_, got = assembleCities(nil, "ES", false, "мадр")
	if len(got) == 0 || got[0].Name != "Madrid" {
		t.Fatalf("Cyrillic query: %+v, want Madrid first", got)
	}
	if _, many := assembleCities(nil, "ES", false, "a"); len(many) > 20 {
		t.Error("a search must return at most 20 suggestions")
	}
	if _, none := assembleCities(nil, "", false, "mad"); none == nil || len(none) != 0 {
		t.Errorf("no country: suggestions = %v, want an empty array", none)
	}
}

func TestAssembleCities_PlatformCityWinsOverSuggestion(t *testing.T) {
	id, cid := uuid.New(), uuid.New()
	rows := []gen.ListCityRow{
		{ID: id, CountryID: cid, Slug: "madrid", CountryIso2: "ES", Name: "Мадрид"},
		{ID: uuid.New(), CountryID: uuid.New(), Slug: "prague", CountryIso2: "CZ", Name: "Prague"},
	}
	got, sugg := assembleCities(rows, "ES", false, "мадр")
	if len(got) != 1 || got[0].ID != id.String() || got[0].Source != "platform" {
		t.Fatalf("got %+v, want exactly the platform Madrid (Prague is another country)", got)
	}
	for _, c := range sugg {
		if c.Name == "Madrid" {
			t.Errorf("Madrid is already a platform city (slug madrid) but is suggested again: %+v", c)
		}
	}
	// No q: the plain list, filtered by country only, no suggestions.
	plain, none := assembleCities(rows, "CZ", false, "")
	if len(plain) != 1 || plain[0].Slug != "prague" || plain[0].Timezone != "Europe/Prague" || none != nil {
		t.Errorf("plain CZ list = %+v, suggestions %v", plain, none)
	}
}

func TestCityQuery_Validation(t *testing.T) {
	for _, tc := range []struct {
		url  string
		code int
	}{
		{"/x?country=ESP", http.StatusBadRequest},
		{"/x?country=E1", http.StatusBadRequest},
		{"/x?q=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", http.StatusBadRequest},
		{"/x?country=es&q=mad", http.StatusOK},
		{"/x", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		_, _, ok := cityQuery(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if (tc.code == http.StatusOK) != ok || (!ok && rec.Code != tc.code) {
			t.Errorf("%s: ok=%v status=%d, want %d", tc.url, ok, rec.Code, tc.code)
		}
	}
}
