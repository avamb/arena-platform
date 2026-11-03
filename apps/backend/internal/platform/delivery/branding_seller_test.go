package delivery

import (
	"log/slog"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// Spec 51 section 2: without a legal name the footer names the organization,
// never the platform, as the seller.
func TestResolveBranding_SellerFallsBackToTheOrganizationNotThePlatform(t *testing.T) {
	b := resolveBranding(t.Context(), nil, Payload{OrgName: "Teatro Kolybel"}, slog.Default()).Branding
	if b.LegalName != "Teatro Kolybel" {
		t.Fatalf("LegalName = %q; want the organization name", b.LegalName)
	}
	b = resolveBranding(t.Context(), nil, Payload{OrgName: "Teatro Kolybel", OrgLegalName: "Kolybel S.L."}, slog.Default()).Branding
	if b.LegalName != "Kolybel S.L." {
		t.Fatalf("LegalName = %q; want the legal name", b.LegalName)
	}
	if b := resolveBranding(t.Context(), nil, Payload{}, slog.Default()).Branding; b.LegalName != templates.PlatformLegalName {
		t.Fatalf("an organization without any name: LegalName = %q", b.LegalName)
	}
}

func TestProviderTitle_NamesRegisteredProvidersOnly(t *testing.T) {
	for name, want := range map[string]string{"stripe": "Stripe", "payplus": "PayPlus", "manual": "", "none": "", "": "", "mock": ""} {
		if got := providerTitle(name); got != want {
			t.Errorf("providerTitle(%q) = %q; want %q", name, got, want)
		}
	}
}
