package hseller

import "testing"

func TestConsentRefused(t *testing.T) {
	t.Parallel()
	full := &Contact{Email: "tickets@example.com"}
	missing := []string{"tax_id"}
	withPhone := Contact{Email: full.Email, Phone: "+34600000000"}
	cases := []struct {
		name           string
		consent        bool
		askedOn        bool
		contactChanged bool
		cur            *Contact
		next           Contact
		missing        []string
		want           bool
	}{
		{"switching on with a complete profile passes", true, true, false, full, *full, nil, false},
		{"switching on with data missing is refused", true, true, false, full, *full, missing, true},
		{"switching off with data missing passes", false, false, false, full, *full, missing, false},
		{"leaving it off and editing the phone passes", false, false, true, full, withPhone, missing, false},
		{"clearing the e-mail of a channel that keeps the consent on is refused", true, false, true, full, Contact{}, []string{"public_email"}, true},
		{"a phone edit on an older incomplete channel with consent on passes", true, false, true, full, withPhone, missing, false},
		{"consent on with nothing changed passes", true, false, false, full, *full, missing, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := consentRefused(c.consent, c.askedOn, c.contactChanged, c.cur, c.next, c.missing); got != c.want {
				t.Fatalf("consentRefused = %v, want %v", got, c.want)
			}
		})
	}
}
