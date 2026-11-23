package hfeed

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/contenttranslate"
)

func origResp() publicFeedEventResponse {
	return publicFeedEventResponse{
		Name:             "Night",
		ShortDescription: strp("short en"),
		DescriptionHTML:  strp("<p>en</p>"),
		DescriptionText:  strp("en"),
	}
}

func trFor(locale string, orig publicFeedEventResponse, report *string, f contenttranslate.Fields) contenttranslate.Translation {
	name := orig.Name
	h := contenttranslate.SourceHash(contenttranslate.Fields{
		Name: &name, ShortDescription: orig.ShortDescription, DescriptionHTML: orig.DescriptionHTML, ReportTextHTML: report,
	})
	return contenttranslate.Translation{Locale: locale, Fields: f, SourceHash: h}
}

func TestApplyTranslation_ServesTranslation(t *testing.T) {
	r := origResp()
	cs := trFor("cs", r, nil, contenttranslate.Fields{
		Name: strp("Noc"), ShortDescription: strp("kratce"), DescriptionHTML: strp("<p>cs</p><script>x()</script>"),
	})
	de := trFor("de", r, nil, contenttranslate.Fields{ShortDescription: strp("kurz")})
	rep := applyTranslation(&r, eventOriginal{ContentLocale: "en"}, []contenttranslate.Translation{de, cs}, "cs")
	if rep != nil {
		t.Errorf("report = %v, want nil", rep)
	}
	if r.ContentLocale != "cs" || r.Name != "Noc" || *r.ShortDescription != "kratce" {
		t.Errorf("not translated: %+v", r)
	}
	if r.DescriptionHTML == nil || *r.DescriptionHTML != "<p>cs</p>" || strings.Contains(*r.DescriptionText, "x()") {
		t.Errorf("description = %v / %v", r.DescriptionHTML, r.DescriptionText)
	}
	if r.TranslationStale {
		t.Error("fresh translation reported stale")
	}
	if want := []string{"en", "cs", "de"}; !reflect.DeepEqual(r.AvailableLocales, want) {
		t.Errorf("available = %v, want %v", r.AvailableLocales, want)
	}
}

func TestApplyTranslation_KeepsOriginalNameWhenTranslationHasNone(t *testing.T) {
	r := origResp()
	tr := trFor("cs", r, nil, contenttranslate.Fields{ShortDescription: strp("kratce")})
	applyTranslation(&r, eventOriginal{ContentLocale: "en"}, []contenttranslate.Translation{tr}, "cs")
	if r.Name != "Night" || r.ContentLocale != "cs" {
		t.Errorf("name = %q, locale = %q", r.Name, r.ContentLocale)
	}
}

func TestApplyTranslation_NoTranslationServesOriginal(t *testing.T) {
	r := origResp()
	cs := trFor("cs", r, nil, contenttranslate.Fields{Name: strp("Noc")})
	applyTranslation(&r, eventOriginal{ContentLocale: "en"}, []contenttranslate.Translation{cs}, "hu")
	if r.ContentLocale != "en" || r.Name != "Night" || r.TranslationStale {
		t.Errorf("got %+v", r)
	}
	if !reflect.DeepEqual(r.AvailableLocales, []string{"en", "cs"}) {
		t.Errorf("available = %v", r.AvailableLocales)
	}
}

func TestApplyTranslation_NoLocaleAndEmptyOriginal(t *testing.T) {
	r := origResp()
	applyTranslation(&r, eventOriginal{}, nil, "")
	if r.ContentLocale != "en" || !reflect.DeepEqual(r.AvailableLocales, []string{"en"}) {
		t.Errorf("got %q %v", r.ContentLocale, r.AvailableLocales)
	}
}

func TestApplyTranslation_StaleWhenOriginalChanged(t *testing.T) {
	r := origResp()
	cs := trFor("cs", r, nil, contenttranslate.Fields{Name: strp("Noc")})
	r.DescriptionHTML = strp("<p>changed</p>")
	applyTranslation(&r, eventOriginal{ContentLocale: "en"}, []contenttranslate.Translation{cs}, "cs")
	if !r.TranslationStale {
		t.Error("changed original must make the translation stale")
	}
}

func TestApplyTranslation_ReportText(t *testing.T) {
	orig := eventOriginal{ContentLocale: "en", ReportTextHTML: strp("<p>report en</p>"), ReportOpen: true}
	r := origResp()
	cs := trFor("cs", r, orig.ReportTextHTML, contenttranslate.Fields{ReportTextHTML: strp("<p>report cs</p>")})
	if rep := applyTranslation(&r, orig, []contenttranslate.Translation{cs}, "cs"); rep == nil || *rep != "<p>report cs</p>" {
		t.Errorf("translated report = %v", rep)
	}
	r = origResp()
	if rep := applyTranslation(&r, orig, nil, "cs"); rep == nil || *rep != "<p>report en</p>" {
		t.Errorf("original report = %v", rep)
	}
	orig.ReportOpen = false
	r = origResp()
	if rep := applyTranslation(&r, orig, nil, ""); rep != nil {
		t.Errorf("report before the event ended = %v", rep)
	}
}

func TestRequestedLocale(t *testing.T) {
	for raw, want := range map[string]string{"": "", "cs": "cs", "CS-cz": "cs", "xx": "", "!!": ""} {
		r := httptest.NewRequest("GET", "/x?locale="+raw, nil)
		if got := requestedLocale(r); got != want {
			t.Errorf("%q -> %q, want %q", raw, got, want)
		}
	}
}
