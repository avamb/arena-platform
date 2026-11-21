package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// platformLine is the line every buyer letter ends with (spec 62 P-22, owner
// decision R-19): "Ticket processing by arenasoldout.com", linked to
// https://arenasoldout.com in the HTML part. It replaces the old "Arena
// Platform — automated ..." line in every locale.
const platformLine = "Ticket processing by arenasoldout.com"

// legacyPlatformLine is what the shop and the "Refer a friend" letters still
// end with: they must not speak of tickets, so the ticket line does not fit.
const legacyPlatformLine = "ArenaSoldOut.com —"

// platformLineFor is the platform line of a buyer letter kind.
func platformLineFor(kind string) string {
	if strings.HasPrefix(kind, "shop_") || strings.HasPrefix(kind, "referral_") {
		return legacyPlatformLine
	}
	return platformLine
}

// platformLink is the address the HTML part links the line to.
const platformLink = `href="https://arenasoldout.com"`

// Spec 08_architecture/53: a buyer letter of a brand with white label leaves
// out the line "Ticket processing by arenasoldout.com" in both parts; every other letter keeps
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
				if !strings.Contains(body, platformLineFor(kind)) {
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
				if strings.Contains(body, platformLineFor(kind)) {
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
	organizer := []string{"refund_blocked.", "refund_scan_exception.", "sales_alert.", "referral_reversal_organizer.", "referral_payout_organizer."}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if !strings.Contains(s, platformLine) && !strings.Contains(s, legacyPlatformLine) {
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
			if (strings.Contains(line, platformLine) || strings.Contains(line, legacyPlatformLine)) && !strings.Contains(line, "{{if not .WhiteLabel}}") {
				t.Errorf("%s:%d: the platform line is not inside {{if not .WhiteLabel}}", f, i+1)
			}
		}
	}
}

// P-22: every buyer letter in every locale links the line to arenasoldout.com
// in the HTML part, and no letter keeps the old "Arena Platform -" line.
func TestRender_PlatformLineLinksToArenaSoldOut(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for kind, d := range buyerLetterKinds() {
		if platformLineFor(kind) != platformLine {
			continue
		}
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if !strings.Contains(out.HTMLBody, platformLink) {
				t.Errorf("%s/%s: the HTML part does not link the platform line to arenasoldout.com", kind, loc)
			}
			if !strings.Contains(out.TextBody, "https://arenasoldout.com") {
				t.Errorf("%s/%s: the text part does not carry the address", kind, loc)
			}
			for _, body := range []string{out.TextBody, out.HTMLBody} {
				if strings.Contains(body, "Arena Platform —") {
					t.Errorf("%s/%s: the old platform line is still printed", kind, loc)
				}
			}
		}
	}
}
