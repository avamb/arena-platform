package stripe

import "testing"

// Spec 63, L-4: the languages of the Lampyris site against Stripe Checkout.
// Stripe answers 400 for a language it does not list, so uk (which it does not
// list) must be omitted and Stripe then picks the buyer's own language (auto).
func TestCheckoutLocaleForSiteLanguages(t *testing.T) {
	cases := map[string]string{
		"en": "en", "cs": "cs", "pl": "pl", "ru": "ru", "de": "de",
		"et": "et", "fi": "fi", "lv": "lv", "lt": "lt", "hu": "hu",
		"uk": "", "uk-UA": "", "": "", "he": "",
		"PL-pl": "pl", "fi_FI": "fi",
	}
	for in, want := range cases {
		if got := checkoutLocale(in); got != want {
			t.Errorf("checkoutLocale(%q) = %q, want %q", in, got, want)
		}
	}
}
