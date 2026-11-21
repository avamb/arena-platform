package templates

import (
	"strings"
	"testing"
)

// The letters of the sales alert render in every locale with their own
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
		"entry": {Kind: "reissue_old_code", When: "20.11.2026 19:00", OrderNumber: 1000123,
			OldNumber: 774, NewNumber: 1203, At: "20.11.2026 19:05"},
		"attempt": {Kind: "reissue_old_code", When: "20.11.2026 19:00", Attempt: true,
			OldNumber: 775, NewNumber: 1204, At: "20.11.2026 19:40", Source: "macs", Gate: "North", Admitted: true},
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
			case "entry":
				for _, want := range []string{"1000123", "774", "1203", "19:05"} {
					if !strings.Contains(out.TextBody, want) || !strings.Contains(out.HTMLBody, want) {
						t.Errorf("%s/entry: %q missing: %s", loc, want, out.TextBody)
					}
				}
			case "attempt":
				for _, want := range []string{"775", "1204", "19:40", "MACS", "North"} {
					if !strings.Contains(out.TextBody, want) {
						t.Errorf("%s/attempt: %q missing: %s", loc, want, out.TextBody)
					}
				}
				if strings.Contains(out.TextBody, "1000123") {
					t.Errorf("%s/attempt: an invitation without an order printed an order number", loc)
				}
			case "stale":
				if !strings.Contains(out.TextBody, "15") {
					t.Errorf("%s/stale: the 15 minutes are not said:\n%s", loc, out.TextBody)
				}
			}
			subjects[out.Subject] = true
		}
		if len(subjects) != len(variants) {
			t.Errorf("%s: every variant must have its own subject, got %v", loc, subjects)
		}
	}
}
