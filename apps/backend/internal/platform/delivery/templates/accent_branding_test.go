package templates

import (
	"strings"
	"testing"
)

func TestRender_AccentFromBranding(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render(TemplateKindTicket, "en", Data{TicketID: "1", EventName: "E", Branding: Branding{OrgName: "Vino", Accent: "#822121"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.HTMLBody, "#822121") {
		t.Fatal("the organization's accent is not in the letter")
	}
	out, err = r.Render(TemplateKindTicket, "en", Data{TicketID: "1", EventName: "E", Branding: Branding{OrgName: "X"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.HTMLBody, DefaultAccentColor) {
		t.Fatal("without a branding accent the platform accent is used")
	}
}
