package templates

import (
	"html"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/directions"
)

// The directions line (owner decision 2026-11-07) is printed in the ticket,
// change and invitation letters in every language, with both links, and not
// at all without URLs. The cancel letter never carries it.
func TestRender_DirectionsLine(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{
		"en": "Directions", "ru": "Как добраться", "cs": "Jak se tam dostat",
		"de": "Anfahrt", "es": "Cómo llegar", "fr": "Itinéraire", "he": "הוראות הגעה",
	}
	links := directions.Build(nil, nil, "Dizengoff 5", "Tel Aviv")
	for _, kind := range []string{TemplateKindTicket, TemplateKindChange, TemplateKindInvitation, TemplateKindCancel} {
		for _, loc := range SupportedLocales {
			data := orderData()
			if kind != TemplateKindTicket {
				data = changeTestData()
			}
			data.VenueName = "Hall, Tel Aviv"
			data.Directions = links
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if kind == TemplateKindCancel {
				if strings.Contains(out.HTMLBody+out.TextBody, "waze.com") {
					t.Errorf("%s/%s: cancel letter carries directions", kind, loc)
				}
				continue
			}
			wantHref := `href="` + links.GoogleURL + `"`
			// html/template escapes the attribute (& and +); a browser decodes it back.
			if !strings.Contains(html.UnescapeString(out.HTMLBody), wantHref) || !strings.Contains(out.HTMLBody, "waze.com/ul?q=") {
				t.Errorf("%s/%s html: links missing", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, ">Google Maps</a> · <a") {
				t.Errorf("%s/%s html: Google must come first", kind, loc)
			}
			if !strings.Contains(out.TextBody, links.GoogleURL) || !strings.Contains(out.TextBody, links.WazeURL) {
				t.Errorf("%s/%s text: urls missing", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, labels[loc]) || !strings.Contains(out.TextBody, labels[loc]) {
				t.Errorf("%s/%s: label %q missing", kind, loc, labels[loc])
			}

			data.Directions = directions.Links{}
			out, err = r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for _, s := range []string{out.HTMLBody, out.TextBody} {
				if strings.Contains(s, "Google Maps") || strings.Contains(s, "waze.com") || strings.Contains(s, labels[loc]) {
					t.Errorf("%s/%s: directions printed without URLs", kind, loc)
				}
			}
		}
	}
}
