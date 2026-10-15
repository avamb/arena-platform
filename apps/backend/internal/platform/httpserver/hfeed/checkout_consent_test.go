package hfeed

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func strp(s string) *string { return &s }

func completeFlags() gen.FeedTokenBuyerFlagsRow {
	return gen.FeedTokenBuyerFlagsRow{
		ConsentRequired:   true,
		OrgName:           "Teatro Demo",
		LegalName:         strp("Teatro Demo S.L."),
		TaxID:             strp("B12345678"),
		AddressLine1:      strp("Calle Mayor 1"),
		AddressCity:       strp("Madrid"),
		AddressPostalCode: strp("28001"),
		PublicContact:     []byte(`{"email":"tickets@teatro.example"}`),
	}
}

func TestConsentFromFlags(t *testing.T) {
	t.Parallel()

	req, name := consentFromFlags(completeFlags())
	if !req || name != "Teatro Demo S.L." {
		t.Fatalf("complete profile + switch on: got (%v, %q), want (true, legal name)", req, name)
	}

	off := completeFlags()
	off.ConsentRequired = false
	if req, name := consentFromFlags(off); req || name != "" {
		t.Fatalf("switch off: got (%v, %q), want (false, \"\")", req, name)
	}

	// The switch stays stored, but a profile emptied afterwards turns the
	// question off: no seller block, no consent (plan 8.9).
	noTax := completeFlags()
	noTax.TaxID = nil
	if req, _ := consentFromFlags(noTax); req {
		t.Fatal("incomplete profile (no tax id) must not require consent")
	}
	noEmail := completeFlags()
	noEmail.PublicContact = nil
	if req, _ := consentFromFlags(noEmail); req {
		t.Fatal("a channel without a public e-mail must not require consent")
	}
}

func TestConsentRefused_PreFlight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		required bool
		consent  *PublicCheckoutConsent
		want     bool
	}{
		{"required, no consent member", true, nil, true},
		{"required, not ticked", true, &PublicCheckoutConsent{Accepted: false}, true},
		{"required, ticked", true, &PublicCheckoutConsent{Accepted: true, Locale: "es"}, false},
		{"not required, nothing sent", false, nil, false},
		{"not required, ticked anyway", false, &PublicCheckoutConsent{Accepted: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := consentRefused(c.required, c.consent); got != c.want {
				t.Fatalf("consentRefused = %v, want %v", got, c.want)
			}
		})
	}
}

func TestConsentBlock_NamesTheSellerOnlyWhenRequired(t *testing.T) {
	t.Parallel()
	h := (&Handler{}).WithCheckoutConsent(" https://arenasoldout.com/legal/terms ", "https://arenasoldout.com/legal/privacy")
	on := h.consentBlock(true, "Teatro Demo S.L.")
	if !on.Required || on.SellerLegalName != "Teatro Demo S.L." || on.TermsURL != "https://arenasoldout.com/legal/terms" || on.PrivacyURL == "" {
		t.Fatalf("required block: %+v", on)
	}
	off := h.consentBlock(false, "Teatro Demo S.L.")
	if off.Required || off.SellerLegalName != "" {
		t.Fatalf("not required block must not name the seller: %+v", off)
	}
}

func TestNewPendingConsent(t *testing.T) {
	t.Parallel()
	org := uuid.New()
	r := httptest.NewRequest("POST", "/v1/public/feeds/x/checkout/start", nil)
	r.RemoteAddr = "203.0.113.7:51234"
	r.Header.Set("User-Agent", "Mozilla/5.0 test\x00browser")
	// X-Forwarded-For is never trusted here: the router has already rewritten
	// RemoteAddr when a proxy hop count is configured.
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("x", 7200))

	r.Header.Set("Origin", "https://Shop.Example:8443")
	shown := publicConsentBlock{Required: true, SellerLegalName: "Teatro Demo S.L.",
		TermsURL: "https://arenasoldout.com/legal/terms", PrivacyURL: "https://arenasoldout.com/legal/privacy"}
	p := newPendingConsent(r, org, "ES-es", "ru", true, now, shown)
	if p.originHost == nil || *p.originHost != "https://shop.example:8443" {
		t.Fatalf("origin host = %v", p.originHost)
	}
	if p.sellerName == nil || *p.sellerName != "Teatro Demo S.L." || p.termsURL == nil || p.privacyURL == nil ||
		*p.termsURL != shown.TermsURL || *p.privacyURL != shown.PrivacyURL {
		t.Fatalf("shown values = %v %v %v", p.sellerName, p.termsURL, p.privacyURL)
	}
	if p.clientIP == nil || *p.clientIP != "203.0.113.7" {
		t.Fatalf("client ip = %v, want 203.0.113.7", p.clientIP)
	}
	if p.userAgent == nil || *p.userAgent != "Mozilla/5.0 testbrowser" {
		t.Fatalf("user agent = %v", p.userAgent)
	}
	if p.locale == nil || *p.locale != "es" {
		t.Fatalf("locale = %v, want es (the consent's own language first)", p.locale)
	}
	if !p.withdrawal {
		t.Fatal("withdrawal flag lost")
	}
	if !p.acceptedAt.Equal(now) || p.acceptedAt.Location() != time.UTC {
		t.Fatalf("accepted_at = %v, want %v in UTC", p.acceptedAt, now)
	}
	want := "platform-terms:" + PlatformTermsVersion + ";seller:" + org.String()
	if p.termsVersion != want {
		t.Fatalf("terms version = %q, want %q", p.termsVersion, want)
	}
	params := p.params(org, uuid.New())
	if params.Kind != ConsentKindSaleTerms || params.OrgID != org || params.OriginHost != p.originHost || params.TermsURL != p.termsURL {
		t.Fatalf("params = %+v", params)
	}

	// An unknown consent language falls back to the buyer's; none at all is NULL.
	if p := newPendingConsent(r, org, "xx", "ru", false, now, shown); p.locale == nil || *p.locale != "ru" {
		t.Fatalf("fallback locale = %v, want ru", p.locale)
	}
	if p := newPendingConsent(r, org, "", "", false, now, publicConsentBlock{}); p.locale != nil || p.sellerName != nil || p.termsURL != nil {
		t.Fatalf("no locale must be NULL, got %v", *p.locale)
	}
}

func TestConsentRecorded_OnlyWhenTheChannelAsks(t *testing.T) {
	t.Parallel()
	ticked := &PublicCheckoutConsent{Accepted: true}
	if !consentRecorded(true, ticked) {
		t.Fatal("a ticked consent of a channel that asks for it is recorded")
	}
	if consentRecorded(false, ticked) {
		t.Fatal("a tick sent to a channel that does not ask for it must store nothing")
	}
	if consentRecorded(true, &PublicCheckoutConsent{}) || consentRecorded(true, nil) {
		t.Fatal("an unticked consent is never recorded")
	}
}

func TestConsentOriginHost(t *testing.T) {
	t.Parallel()
	cases := []struct{ origin, referer, want string }{
		{"https://shop.example", "", "https://shop.example"},
		{"", "https://www.Site.example/events/x?utm=1#frag", "https://www.site.example"},
		{"null", "https://ref.example/path", "https://ref.example"},
		{"https://user:pw@shop.example/p?q=1", "", "https://shop.example"},
		{"ftp://shop.example", "javascript:alert(1)", ""},
		{"", "", ""},
		{"not a url", "/relative/path", ""},
		{"https://" + strings.Repeat("a", 300) + ".example", "", ""},
	}
	for _, c := range cases {
		got := consentOriginHost(c.origin, c.referer)
		if c.want == "" {
			if got != nil {
				t.Errorf("(%q, %q) = %q, want nil", c.origin, c.referer, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("(%q, %q) = %v, want %q", c.origin, c.referer, got, c.want)
		}
	}
}

func TestConsentClientIP(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"2001:db8::1":          "2001:db8::1",
		"::ffff:192.0.2.1":     "192.0.2.1",
		"fe80::1%eth0":         "fe80::1",
		" 203.0.113.8 ":        "203.0.113.8",
		"not-an-address":       "",
		"":                     "",
		"203.0.113.7:51234bad": "",
	}
	for in, want := range cases {
		got := consentClientIP(in)
		if want == "" {
			if got != nil {
				t.Errorf("%q: got %q, want nil", in, *got)
			}
			continue
		}
		if got == nil || *got != want {
			t.Errorf("%q: got %v, want %q", in, got, want)
		}
	}
}

func TestCapUserAgent(t *testing.T) {
	t.Parallel()
	if capUserAgent("   ") != nil {
		t.Fatal("blank user agent must be NULL")
	}
	long := strings.Repeat("я", 600)
	got := capUserAgent(long)
	if got == nil || utf8.RuneCountInString(*got) != maxUserAgentRunes {
		t.Fatalf("long user agent not capped at %d characters", maxUserAgentRunes)
	}
	bad := capUserAgent("ok\xffstill ok")
	if bad == nil || !utf8.ValidString(*bad) {
		t.Fatal("invalid UTF-8 must be cleaned, not stored")
	}
}
