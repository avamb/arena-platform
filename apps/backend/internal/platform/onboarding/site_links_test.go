package onboarding

import "testing"

// The consent screen links to the legal pages the applicant accepts. They live
// under /legal/ on the new site; explicit URLs win while that site is still on
// a sub-domain.
func TestSiteLinks(t *testing.T) {
	cases := []struct {
		name                string
		opts                Options
		wantTerms, wantPriv string
	}{
		{"follows the site origin", Options{SiteURL: "https://arenasoldout.com/"},
			"https://arenasoldout.com/legal/terms", "https://arenasoldout.com/legal/privacy"},
		{"explicit URLs win", Options{SiteURL: "https://arenasoldout.com",
			TermsURL: " https://new.arenasoldout.com/legal/terms ", PrivacyURL: "https://new.arenasoldout.com/legal/privacy"},
			"https://new.arenasoldout.com/legal/terms", "https://new.arenasoldout.com/legal/privacy"},
		{"one override, one default", Options{SiteURL: "https://arenasoldout.com", TermsURL: "https://x.test/t"},
			"https://x.test/t", "https://arenasoldout.com/legal/privacy"},
		{"nothing configured", Options{}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			terms, priv := New(c.opts).SiteLinks()
			if terms != c.wantTerms || priv != c.wantPriv {
				t.Fatalf("SiteLinks() = (%q, %q), want (%q, %q)", terms, priv, c.wantTerms, c.wantPriv)
			}
		})
	}
}
