package templates

import (
	"strings"
	"testing"
)

// downloadLabels is the link text of each shipped language (spec 51, R-13).
var downloadLabels = map[string]string{
	"en": "Download PDF",
	"ru": "Скачать PDF",
	"cs": "Stáhnout PDF",
	"es": "Descargar PDF",
	"de": "PDF herunterladen",
	"fr": "Télécharger le PDF",
	"he": "הורדת PDF",
}

// Every ticket line of the letter links its own PDF, in the HTML and in the
// text part, in every shipped language; the link carries ?src=email so the
// download is logged as coming from the letter.
func TestRender_Ticket_DownloadLinkPerTicket_AllLocales(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	const (
		url1 = "https://api.example.com/v1/public/checkout/abc/tickets/11111111-1111-1111-1111-111111111111/pdf?src=email"
		url2 = "https://api.example.com/v1/public/checkout/abc/tickets/22222222-2222-2222-2222-222222222222/pdf?src=email"
	)
	for _, loc := range SupportedLocales {
		label, ok := downloadLabels[loc]
		if !ok {
			t.Fatalf("no download label known for locale %q: add it to the templates and to this test", loc)
		}
		d := orderData()
		d.Tickets[0].DownloadURL = url1
		d.Tickets[1].DownloadURL = url2
		out, err := r.Render(TemplateKindTicket, loc, d)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		for _, want := range []string{`href="` + url1 + `"`, `href="` + url2 + `"`, label} {
			if !strings.Contains(out.HTMLBody, want) {
				t.Errorf("%s: html missing %q", loc, want)
			}
		}
		if n := strings.Count(out.HTMLBody, label); n != 2 {
			t.Errorf("%s: html has %d download links, want 2", loc, n)
		}
		for _, want := range []string{label + ": " + url1, label + ": " + url2} {
			if !strings.Contains(out.TextBody, want) {
				t.Errorf("%s: text missing %q", loc, want)
			}
		}
		if strings.Contains(out.HTMLBody, "ZgotmplZ") {
			t.Errorf("%s: html/template refused a value (ZgotmplZ)", loc)
		}
	}
}

// Without a URL (no API address configured, a ticket without a checkout) the
// letter prints no link at all.
func TestRender_Ticket_NoDownloadURL_NoLink(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindTicket, loc, orderData())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		label := downloadLabels[loc]
		if strings.Contains(out.HTMLBody, label) || strings.Contains(out.TextBody, label) {
			t.Errorf("%s: a letter without download URLs prints the link text", loc)
		}
		if strings.Contains(out.HTMLBody, "/pdf?src=email") {
			t.Errorf("%s: a letter without download URLs prints a link", loc)
		}
	}
}
