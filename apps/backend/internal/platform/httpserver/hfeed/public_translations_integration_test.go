//go:build integration

// public_translations_integration_test.go - spec 63 §16.3/§16.6: the public
// feed serves an organizer translation for ?locale=xx.
package hfeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/contenttranslate"
)

type translatedEvent struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ShortDescription *string  `json:"short_description"`
	DescriptionHTML  *string  `json:"description_html"`
	DescriptionText  *string  `json:"description_text"`
	ContentLocale    string   `json:"content_locale"`
	AvailableLocales []string `json:"available_locales"`
	TranslationStale bool     `json:"translation_stale"`
	Materials        struct {
		ReportTextHTML *string `json:"report_text_html"`
	} `json:"materials"`
}

func (f *publicPageFixture) feedEventLocale(t *testing.T, query string) translatedEvent {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.token+"/events/"+f.eventID.String()+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("feed_token", f.token)
	rctx.URLParams.Add("event_id", f.eventID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	publicPageHandler(f.pool).HandlePublicFeedEvent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("feed event status = %d (%s)", w.Code, w.Body.String())
	}
	var env struct {
		Event translatedEvent `json:"event"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Event
}

func (f *publicPageFixture) feedListLocale(t *testing.T, query string) translatedEvent {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.token+"/events"+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("feed_token", f.token)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	publicPageHandler(f.pool).HandlePublicFeedEvents(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("feed list status = %d (%s)", w.Code, w.Body.String())
	}
	var env struct {
		Events []translatedEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, e := range env.Events {
		if e.ID == f.eventID.String() {
			return e
		}
	}
	t.Fatalf("event %s not in list %s", f.eventID, w.Body.String())
	return translatedEvent{}
}

func putTranslation(t *testing.T, ctx context.Context, f *publicPageFixture, locale string, fields contenttranslate.Fields) {
	t.Helper()
	orig, err := contenttranslate.LoadEventOriginal(ctx, f.pool, f.orgID, f.eventID)
	if err != nil {
		t.Fatalf("load original: %v", err)
	}
	if _, err := contenttranslate.PutManual(ctx, f.pool, contenttranslate.Translation{
		OwnerType: contenttranslate.OwnerEvent, OwnerID: f.eventID, OrgID: f.orgID,
		Locale: locale, Fields: fields, SourceHash: orig.Hash(),
	}); err != nil {
		t.Fatalf("put translation: %v", err)
	}
}

func TestPublicFeedTranslations_Integration(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newPublicPageFixture(t, ctx, pool, true)
	f.setDescription(t, ctx, "<p>Original text</p>", "art")
	if _, err := pool.Exec(ctx, `UPDATE events SET short_description = 'short en',
 report_text_html = '<p>report en</p>', last_session_at = now() - interval '1 day' WHERE id = $1`, f.eventID); err != nil {
		t.Fatalf("prepare event: %v", err)
	}
	s := func(v string) *string { return &v }
	putTranslation(t, ctx, f, "cs", contenttranslate.Fields{
		Name: s("Noc"), ShortDescription: s("kratce"), DescriptionHTML: s("<p>Cesky</p>"), ReportTextHTML: s("<p>report cs</p>"),
	})
	putTranslation(t, ctx, f, "de", contenttranslate.Fields{ShortDescription: s("kurz")})
	// A script that reached the table by a back door must still never leave it.
	if _, err := pool.Exec(ctx, `UPDATE content_translations SET description_html = '<p>ok</p><script>alert(1)</script>'
 WHERE owner_id = $1 AND locale = 'de'`, f.eventID); err != nil {
		t.Fatalf("poison: %v", err)
	}

	for where, get := range map[string]func(string) translatedEvent{
		"detail": func(q string) translatedEvent { return f.feedEventLocale(t, q) },
		"list":   func(q string) translatedEvent { return f.feedListLocale(t, q) },
	} {
		cs := get("?locale=cs-CZ")
		if cs.ContentLocale != "cs" || cs.Name != "Noc" || cs.ShortDescription == nil || *cs.ShortDescription != "kratce" {
			t.Errorf("%s cs: %+v", where, cs)
		}
		if cs.DescriptionHTML == nil || *cs.DescriptionHTML != "<p>Cesky</p>" || cs.DescriptionText == nil || *cs.DescriptionText != "Cesky" {
			t.Errorf("%s cs description: %v / %v", where, cs.DescriptionHTML, cs.DescriptionText)
		}
		if cs.TranslationStale {
			t.Errorf("%s cs must not be stale", where)
		}
		if want := []string{"en", "cs", "de"}; !reflect.DeepEqual(cs.AvailableLocales, want) {
			t.Errorf("%s available = %v, want %v", where, cs.AvailableLocales, want)
		}

		hu := get("?locale=hu")
		if hu.ContentLocale != "en" || hu.Name == "Noc" || hu.DescriptionHTML == nil || *hu.DescriptionHTML != "<p>Original text</p>" {
			t.Errorf("%s hu must be the original: %+v", where, hu)
		}
		for _, q := range []string{"", "?locale=xx"} {
			o := get(q)
			if o.ContentLocale != "en" || o.Name == "Noc" || o.TranslationStale {
				t.Errorf("%s %q: %+v", where, q, o)
			}
		}

		de := get("?locale=de")
		if de.DescriptionHTML != nil && strings.Contains(*de.DescriptionHTML, "script") {
			t.Errorf("%s: script reached the feed: %v", where, *de.DescriptionHTML)
		}
		if de.DescriptionText != nil && strings.Contains(*de.DescriptionText, "alert") {
			t.Errorf("%s: script text reached the feed: %v", where, *de.DescriptionText)
		}
	}

	d := f.feedEventLocale(t, "?locale=cs")
	if d.Materials.ReportTextHTML == nil || *d.Materials.ReportTextHTML != "<p>report cs</p>" {
		t.Errorf("translated report = %v", d.Materials.ReportTextHTML)
	}
	if o := f.feedEventLocale(t, ""); o.Materials.ReportTextHTML == nil || *o.Materials.ReportTextHTML != "<p>report en</p>" {
		t.Errorf("original report = %v", o.Materials.ReportTextHTML)
	}

	// The original changes: the translation is still served, marked stale.
	f.setDescription(t, ctx, "<p>Changed text</p>", "art")
	if st := f.feedEventLocale(t, "?locale=cs"); !st.TranslationStale || st.Name != "Noc" {
		t.Errorf("stale = %v, name = %q", st.TranslationStale, st.Name)
	}
	if st := f.feedListLocale(t, "?locale=cs"); !st.TranslationStale {
		t.Error("list: stale not reported")
	}
}
