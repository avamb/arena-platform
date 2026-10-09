package templates

import (
	"strings"
	"testing"
)

// The doors-open time (migration 0128) is printed in the ticket letter and
// in the change letter, in every language, and only when there is one.
func TestRender_DoorsOpenLine(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{TemplateKindTicket, TemplateKindChange} {
		data := orderData()
		if kind == TemplateKindChange {
			data = changeTestData()
		}
		for _, loc := range SupportedLocales {
			data.DoorsOpen = "19:15"
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for part, s := range map[string]string{"html": out.HTMLBody, "text": out.TextBody} {
				if !strings.Contains(s, "19:15") {
					t.Errorf("%s/%s %s: no doors time", kind, loc, part)
				}
			}
			data.DoorsOpen = ""
			out, err = r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if strings.Contains(out.HTMLBody, "19:15") || strings.Contains(out.TextBody, "19:15") {
				t.Errorf("%s/%s: doors line printed without a doors time", kind, loc)
			}
		}
	}
}
