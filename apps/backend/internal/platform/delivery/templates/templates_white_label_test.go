package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// platformLine is the start of the line every buyer letter ends with. The
// bare words "Arena Platform" are not enough: a fixture may use them as the
// organization's name.
const platformLine = "Arena Platform —"

// Spec 08_architecture/52: a buyer letter of a brand with white label leaves
// out the line "Arena Platform — …" in both parts; every other letter keeps
// it. The organizer's own letters (alerts) are not white label and keep it
// whatever the flag says.
func TestRender_WhiteLabelDropsThePlatformLine(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for kind, d := range buyerLetterKinds() {
		for _, loc := range SupportedLocales {
			plain, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for _, body := range []string{plain.TextBody, plain.HTMLBody} {
				if !strings.Contains(body, platformLine) {
					t.Errorf("%s/%s: the platform line is missing without white label", kind, loc)
				}
			}
			d.WhiteLabel = true
			wl, err := r.Render(kind, loc, d)
			d.WhiteLabel = false
			if err != nil {
				t.Fatalf("%s/%s white label: %v", kind, loc, err)
			}
			for _, body := range []string{wl.TextBody, wl.HTMLBody} {
				if strings.Contains(body, platformLine) {
					t.Errorf("%s/%s: the platform line is printed under white label", kind, loc)
				}
			}
			if wl.Subject != plain.Subject {
				t.Errorf("%s/%s: white label changed the subject", kind, loc)
			}
		}
	}
}

// Every template file that prints the platform line either wraps it in the
// white label condition or is an organizer letter.
func TestTemplates_EveryBuyerPlatformLineFollowsWhiteLabel(t *testing.T) {
	files, err := filepath.Glob("*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	organizer := []string{"refund_blocked.", "refund_scan_exception.", "sales_alert."}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if !strings.Contains(s, "Arena Platform") {
			continue
		}
		isOrganizer := false
		for _, p := range organizer {
			if strings.HasPrefix(f, p) {
				isOrganizer = true
			}
		}
		if isOrganizer {
			continue
		}
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			if strings.Contains(line, "Arena Platform") && !strings.Contains(line, "{{if not .WhiteLabel}}Arena Platform") {
				t.Errorf("%s:%d: the platform line is not inside {{if not .WhiteLabel}}", f, i+1)
			}
		}
	}
}
