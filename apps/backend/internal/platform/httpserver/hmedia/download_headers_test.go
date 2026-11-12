package hmedia

import (
	"net/http"
	"testing"
)

func TestSetDownloadHeaders(t *testing.T) {
	const csp = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox"
	cases := []struct {
		name, contentType, owner string
		wantType                 string
		attachment               bool
	}{
		{"svg", "image/svg+xml", "event_poster", "image/svg+xml", false},
		{"png", "image/png", "event_poster", "image/png", false},
		{"jpeg with params", "IMAGE/JPEG; charset=binary", "event_poster", "IMAGE/JPEG; charset=binary", false},
		{"html", "text/html; charset=utf-8", "event_poster", "text/html; charset=utf-8", true},
		{"pdf", "application/pdf", "event_poster", "application/pdf", true},
		{"zip", "application/zip", "event_poster", "application/zip", true},
		{"empty", "", "event_poster", "application/octet-stream", true},
		{"receipt html", "text/html", "payment_receipt", "text/html", true},
		{"receipt png", "image/png", "payment_receipt", "image/png", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			setDownloadHeaders(h, c.contentType, c.owner)
			if got := h.Get("Content-Type"); got != c.wantType {
				t.Errorf("Content-Type = %q; want %q", got, c.wantType)
			}
			if got := h.Get("Content-Security-Policy"); got != csp {
				t.Errorf("CSP = %q", got)
			}
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("nosniff = %q", got)
			}
			if got := h.Get("Content-Disposition") == "attachment"; got != c.attachment {
				t.Errorf("attachment = %v; want %v", got, c.attachment)
			}
		})
	}
}
