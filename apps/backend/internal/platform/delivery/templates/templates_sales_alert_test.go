package templates

import (
	"strings"
	"testing"
)

// The three letters of the sales alert render in every locale with their own
// facts and no stray template value.
func TestSalesAlert_RendersEveryVariantInEveryLocale(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	base := Data{EventName: "Gala", Branding: Branding{OrgName: "Org"}}
	variants := map[string]SalesAlertData{
		"auto":   {Kind: "sold_out", When: "20.11.2026 19:00", Venue: "Hall", Total: 120},
		"manual": {Kind: "sold_out", Manual: true, Actor: "Ana", When: "20.11.2026 19:00", Free: 7},
		"stale":  {Kind: "site_stale", State: "soon", When: "20.11.2026 19:00"},
	}
	for _, loc := range SupportedLocales {
		subjects := map[string]bool{}
		for name, v := range variants {
			d := base
			d.SalesAlert = v
			out, err := r.Render(TemplateKindSalesAlert, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", loc, name, err)
			}
			all := out.Subject + "\n" + out.HTMLBody + "\n" + out.TextBody
			for _, want := range []string{"Gala", "20.11.2026 19:00"} {
				if !strings.Contains(out.Subject, want) {
					t.Errorf("%s/%s: subject %q lacks %q", loc, name, out.Subject, want)
				}
			}
			if strings.Contains(all, "<no value>") || strings.Contains(all, "{{") {
				t.Errorf("%s/%s: unrendered value:\n%s", loc, name, all)
			}
			switch name {
			case "auto":
				if !strings.Contains(out.TextBody, "120") || !strings.Contains(out.TextBody, "Hall") {
					t.Errorf("%s/auto: places or venue missing:\n%s", loc, out.TextBody)
				}
			case "manual":
				if !strings.Contains(out.TextBody, "Ana") || !strings.Contains(out.TextBody, "7") {
					t.Errorf("%s/manual: actor or free places missing:\n%s", loc, out.TextBody)
				}
			case "stale":
				if !strings.Contains(out.TextBody, "15") {
					t.Errorf("%s/stale: the 15 minutes are not said:\n%s", loc, out.TextBody)
				}
			}
			subjects[out.Subject] = true
		}
		if len(subjects) != len(variants) {
			t.Errorf("%s: the three variants must have three subjects, got %v", loc, subjects)
		}
	}
}
