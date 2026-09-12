//go:build integration

// city_name_537_integration_test.go — feature #537 (W1-S1c), spec
// 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.3.
//
// arena's `cities` table stores a slug and nothing else; the display name lives
// in i18n_text (namespace `geo.cities`, key = slug) and every reader falls back
// to the slug when there is no translation. The import created cities with a
// slug only, so a site that sent `cityName: "Praha"` got `praha` back out of
// GET_ALL_ACTIONS — which is what the live stand showed (spec §1 item 4).
//
// These tests go through the real import routes and read the name back off the
// real GET_ALL_ACTIONS wire, for BOTH sources (arena event bundle and
// bil24-session), because the translation is written in the shared
// resolveGeography path and a regression in either route is equally invisible.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	JWT_SIGNING_SECRET=<anything>
package compat_bil24_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestCompatBil24_537_BundleCityNameKeepsItsCase is the spec §2.3 check on the
// arena-native bundle: the name the site sent comes back verbatim on the wire,
// a repeat writes no duplicates, and an operator's correction outranks the
// import.
func TestCompatBil24_537_BundleCityNameKeepsItsCase(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)

	// The city name is randomized per run because `cities.slug` is GLOBAL, not
	// org-scoped (AGENTS.md): a fixed "Praha" would reuse whatever row — and
	// whatever translation — a previous run or the seed left on the shared dev
	// stand, and the assertion would stop proving anything. The shape is the
	// spec's: mixed case, so the lowercase slug can never pass for the name.
	suffix := uuid.New().String()[:8]
	cityName := "Praha Harness " + suffix
	citySlug := "praha-harness-" + suffix
	city537RegisterCleanup(t, st, citySlug)

	poster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nharness-537"))
	}))
	defer poster.Close()

	body := bundle527Fixture(t, poster.URL+"/poster.png")
	venue, _ := body["venue"].(map[string]any)
	if venue == nil {
		t.Fatalf("bundle fixture has no venue object: %v", body)
	}
	venue["cityName"] = cityName
	venueName, _ := venue["venueName"].(string)

	status, resp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil, body)
	if status != 200 {
		t.Fatalf("POST imports/event-bundle status = %d, want 200 (body %v)", status, resp)
	}
	eventID := sc8UUIDField(t, resp, "event_id")
	sessionID := sc8UUIDField(t, resp, "session_id")
	bundle527RegisterCleanup(t, st, eventID, sessionID)

	// ── the wire: GET_ALL_ACTIONS.cityList[].cityName ───────────────────────
	req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	catalog := postBil24(t, base, req)
	if code := numberField(t, catalog, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, catalog["description"])
	}
	gotName := city537NameOfVenue(t, catalog, venueName)
	if gotName != cityName {
		t.Errorf("GET_ALL_ACTIONS cityName = %q, want the name the bundle sent, %q "+
			"(a lowercase value means the slug fallback fired and no translation was written)",
			gotName, cityName)
	}

	// ── the rows behind it: one per locale, en always present ───────────────
	translations := city537Translations(t, st, citySlug)
	if translations["en"] != cityName {
		t.Errorf("i18n_text[geo.cities/%s/en] = %q, want %q — en is the second link of every "+
			"reader's fallback chain and must always be written", citySlug, translations["en"], cityName)
	}
	orgLocale := city537OrgLocale(t, st, orgID)
	if orgLocale != "en" && translations[orgLocale] != cityName {
		t.Errorf("i18n_text[geo.cities/%s/%s] = %q, want %q for the organization's own locale",
			citySlug, orgLocale, translations[orgLocale], cityName)
	}
	before := len(translations)

	// ── repeat: no duplicates, and no second row per locale ─────────────────
	status2, resp2 := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil, body)
	if status2 != 200 {
		t.Fatalf("repeated import status = %d, want 200 (body %v)", status2, resp2)
	}
	if got := city537Count(t, st, citySlug); got != before {
		t.Errorf("i18n_text rows for %s = %d after the repeat, want %d — the import must not stack translations",
			citySlug, got, before)
	}

	// ── an operator's correction is never clobbered ─────────────────────────
	corrected := "Praha (operator) " + suffix
	if _, err := st.Pool.Exec(ctx,
		`UPDATE i18n_text SET value = $2 WHERE namespace = 'geo.cities' AND key = $1 AND locale = 'en'`,
		citySlug, corrected,
	); err != nil {
		t.Fatalf("simulate an operator correction: %v", err)
	}
	status3, resp3 := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil, body)
	if status3 != 200 {
		t.Fatalf("third import status = %d, want 200 (body %v)", status3, resp3)
	}
	if got := city537Translations(t, st, citySlug)["en"]; got != corrected {
		t.Errorf("i18n_text[geo.cities/%s/en] = %q after a re-import, want the operator's %q — "+
			"the import writes only when a translation is absent", citySlug, got, corrected)
	}
}

// TestCompatBil24_537_Bil24SessionImportNamesItsCity pins the same behaviour on
// the source=bil24 route, which reaches resolveGeography through a different
// caller (himports/import_exec.go rather than import_arena.go).
func TestCompatBil24_537_Bil24SessionImportNamesItsCity(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)

	suffix := uuid.New().String()[:8]
	cityName := "Brno Harness " + suffix
	citySlug := "brno-harness-" + suffix
	city537RegisterCleanup(t, st, citySlug)

	f := sc8NewFixture()
	payload := sc8Payload(f, st)
	venue, _ := payload["venue"].(map[string]any)
	if venue == nil {
		t.Fatalf("scenario-08 payload has no venue object: %v", payload)
	}
	venue["cityName"] = cityName

	status, resp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/bil24-session", rawKey, nil, payload)
	if status != 200 {
		t.Fatalf("POST imports/bil24-session status = %d, want 200 (body %v)", status, resp)
	}
	sc8RegisterCleanup(t, st, sc8UUIDField(t, resp, "event_id"), sc8UUIDField(t, resp, "session_id"))

	if got := city537Translations(t, st, citySlug)["en"]; got != cityName {
		t.Errorf("i18n_text[geo.cities/%s/en] = %q, want %q — the bil24 import must name its city too",
			citySlug, got, cityName)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// city537NameOfVenue returns the cityName of the cityList entry that hosts the
// named venue — the one the import just created, as opposed to whatever else
// the shared stand's organization happens to own.
func city537NameOfVenue(t *testing.T, catalog map[string]interface{}, venueName string) string {
	t.Helper()
	cities, ok := catalog["cityList"].([]interface{})
	if !ok {
		t.Fatalf("GET_ALL_ACTIONS has no cityList array: %v", catalog["cityList"])
	}
	for _, raw := range cities {
		city, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		venues, _ := city["venueList"].([]interface{})
		for _, rawVenue := range venues {
			venue, ok := rawVenue.(map[string]interface{})
			if !ok {
				continue
			}
			if name, _ := venue["venueName"].(string); name == venueName {
				got, _ := city["cityName"].(string)
				return got
			}
		}
	}
	t.Fatalf("no cityList entry hosts venue %q: %v", venueName, cities)
	return ""
}

// city537Translations reads every geo.cities translation of one slug.
func city537Translations(t *testing.T, st *harnessState, slug string) map[string]string {
	t.Helper()
	rows, err := st.Pool.Query(context.Background(),
		`SELECT locale, value FROM i18n_text WHERE namespace = 'geo.cities' AND key = $1`, slug)
	if err != nil {
		t.Fatalf("read city translations for %s: %v", slug, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var locale, value string
		if err := rows.Scan(&locale, &value); err != nil {
			t.Fatalf("scan city translation: %v", err)
		}
		out[locale] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate city translations: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("city %s has no geo.cities translation at all — the import must write one", slug)
	}
	return out
}

func city537Count(t *testing.T, st *harnessState, slug string) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM i18n_text WHERE namespace = 'geo.cities' AND key = $1`, slug,
	).Scan(&n); err != nil {
		t.Fatalf("count city translations for %s: %v", slug, err)
	}
	return n
}

func city537OrgLocale(t *testing.T, st *harnessState, orgID uuid.UUID) string {
	t.Helper()
	var locale string
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT default_locale FROM organizations WHERE id = $1`, orgID).Scan(&locale); err != nil {
		t.Fatalf("read organization default_locale: %v", err)
	}
	return strings.ToLower(strings.TrimSpace(locale))
}

// city537RegisterCleanup sweeps the translation rows and the city itself. The
// venue that references the city is dropped by the import's own cleanup, which
// t.Cleanup runs LIFO — so this registration must come FIRST, before the
// import cleanups, to run LAST.
func city537RegisterCleanup(t *testing.T, st *harnessState, slug string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := st.Pool.Exec(ctx,
			`DELETE FROM i18n_text WHERE namespace = 'geo.cities' AND key = $1`, slug); err != nil {
			t.Logf("cleanup city translations %s: %v", slug, err)
		}
		if _, err := st.Pool.Exec(ctx, `DELETE FROM cities WHERE slug = $1`, slug); err != nil {
			t.Logf("cleanup city %s: %v", slug, err)
		}
	})
}
