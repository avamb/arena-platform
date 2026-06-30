package delivery

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// stubMediaResolver lets the tests dictate what ResolveLogo returns.
type stubMediaResolver struct {
	bytes []byte
	url   string
	err   error
	calls int
}

func (s *stubMediaResolver) ResolveLogo(_ context.Context, _ string) ([]byte, string, error) {
	s.calls++
	return s.bytes, s.url, s.err
}

func TestResolveBranding_NoLogoMediaID_UsesPlatformLogoURL(t *testing.T) {
	r := &stubMediaResolver{} // never called because OrgLogoMediaID is empty
	got := resolveBranding(context.Background(), r, Payload{}, slog.Default())
	if got.Branding.LogoURL != templates.PlatformLogoURL {
		t.Errorf("LogoURL = %q, want PlatformLogoURL", got.Branding.LogoURL)
	}
	if len(got.LogoBytes) != 0 {
		t.Errorf("LogoBytes should be empty when no logo configured")
	}
	if got.Branding.OrgName != templates.PlatformOrgName {
		t.Errorf("OrgName fallback = %q, want %q", got.Branding.OrgName, templates.PlatformOrgName)
	}
	if got.Branding.LegalName != templates.PlatformLegalName {
		t.Errorf("LegalName fallback = %q, want %q", got.Branding.LegalName, templates.PlatformLegalName)
	}
	if got.Branding.ContactEmail != templates.PlatformContactEmail {
		t.Errorf("ContactEmail fallback = %q, want %q",
			got.Branding.ContactEmail, templates.PlatformContactEmail)
	}
	if r.calls != 0 {
		t.Errorf("MediaResolver.ResolveLogo should not be called when OrgLogoMediaID is empty (got %d calls)", r.calls)
	}
}

func TestResolveBranding_LogoResolved_BytesAndURLBothPlumbed(t *testing.T) {
	r := &stubMediaResolver{
		bytes: []byte("PNG bytes"),
		url:   "https://media.example.com/o/abc/logo.png?sig=xyz",
	}
	got := resolveBranding(context.Background(), r, Payload{
		OrgLogoMediaID:         "11111111-2222-3333-4444-555555555555",
		OrgName:                "Globe Theatre",
		OrgWebsiteURL:          "https://globe.example.com",
		OrgLegalName:           "Globe Theatre Ltd",
		OrgLegalAddressLine1:   "21 New Globe Walk",
		OrgLegalAddressPostal:  "SE1 9DT",
		OrgLegalAddressCity:    "London",
		OrgLegalAddressCountry: "GB",
		OrgContactEmail:        "hello@globe.example.com",
	}, slog.Default())

	if got.Branding.OrgName != "Globe Theatre" {
		t.Errorf("OrgName = %q", got.Branding.OrgName)
	}
	if got.Branding.WebsiteURL != "https://globe.example.com" {
		t.Errorf("WebsiteURL = %q", got.Branding.WebsiteURL)
	}
	if got.Branding.LogoURL != r.url {
		t.Errorf("LogoURL = %q, want %q", got.Branding.LogoURL, r.url)
	}
	if got.Branding.LogoAlt != "Globe Theatre" {
		t.Errorf("LogoAlt = %q, want OrgName", got.Branding.LogoAlt)
	}
	if string(got.LogoBytes) != "PNG bytes" {
		t.Errorf("LogoBytes = %q, want PNG bytes", got.LogoBytes)
	}
	if got.Branding.LegalAddressLine1 != "21 New Globe Walk" {
		t.Errorf("LegalAddressLine1 = %q", got.Branding.LegalAddressLine1)
	}
	if got.Branding.LegalAddressCountry != "GB" {
		t.Errorf("LegalAddressCountry = %q", got.Branding.LegalAddressCountry)
	}
}

func TestResolveBranding_LogoNotFound_FallsBackToPlatformLogo(t *testing.T) {
	r := &stubMediaResolver{err: ErrLogoNotFound}
	got := resolveBranding(context.Background(), r, Payload{
		OrgLogoMediaID: "11111111-2222-3333-4444-555555555555",
		OrgLegalName:   "Globe Theatre Ltd",
	}, slog.Default())
	if got.Branding.LogoURL != templates.PlatformLogoURL {
		t.Errorf("LogoURL = %q, want PlatformLogoURL fallback", got.Branding.LogoURL)
	}
	if len(got.LogoBytes) != 0 {
		t.Errorf("LogoBytes should be empty on not-found fallback")
	}
	// Org-supplied legal name should still take precedence over the
	// platform default — only the logo falls back.
	if got.Branding.LegalName != "Globe Theatre Ltd" {
		t.Errorf("LegalName = %q, want Globe Theatre Ltd", got.Branding.LegalName)
	}
}

func TestResolveBranding_MediaOutage_LogsAndFallsBack(t *testing.T) {
	r := &stubMediaResolver{err: errors.New("s3: connection refused")}
	got := resolveBranding(context.Background(), r, Payload{
		OrgLogoMediaID: "11111111-2222-3333-4444-555555555555",
	}, slog.Default())
	if got.Branding.LogoURL != templates.PlatformLogoURL {
		t.Errorf("LogoURL = %q, want PlatformLogoURL fallback on outage",
			got.Branding.LogoURL)
	}
	if len(got.LogoBytes) != 0 {
		t.Errorf("LogoBytes should be empty on outage fallback")
	}
}

func TestResolveBranding_NilMediaResolver_DoesNotPanic(t *testing.T) {
	got := resolveBranding(context.Background(), nil, Payload{
		OrgLogoMediaID: "11111111-2222-3333-4444-555555555555",
		OrgName:        "Globe Theatre",
	}, slog.Default())
	if got.Branding.LogoURL != templates.PlatformLogoURL {
		t.Errorf("LogoURL = %q, want PlatformLogoURL fallback when resolver nil",
			got.Branding.LogoURL)
	}
	if got.Branding.OrgName != "Globe Theatre" {
		t.Errorf("OrgName = %q, want Globe Theatre", got.Branding.OrgName)
	}
}

func TestResolveBranding_OrgEmptyDefaults_PlatformIdentificationPreserved(t *testing.T) {
	// EU "commercial communications" rule: every email must identify a
	// legal entity. When the org omits LegalName / ContactEmail we
	// substitute the platform values so the footer is never empty.
	got := resolveBranding(context.Background(), nil, Payload{}, slog.Default())
	if got.Branding.LegalName == "" {
		t.Error("LegalName must not be empty (EU min-identification rule)")
	}
	if got.Branding.ContactEmail == "" {
		t.Error("ContactEmail must not be empty (EU min-identification rule)")
	}
}
