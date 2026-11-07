package main

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// The growth letters (marketing confirmation, cart reminder, late payment)
// must carry the same media resolver as the ticket letters, or the
// organization's logo never reaches them.
func TestBuildGrowthLetterOptions_CarriesTheDeliveryMediaResolver(t *testing.T) {
	cfg := &config.Config{}
	d := buildDeliveryHandlerOptions(cfg, nil, stubResolver{}, testLogger())
	opts := buildGrowthLetterOptions(d, nil, cfg, testLogger())

	if opts.Media == nil {
		t.Fatal("GrowthLetterOptions.Media is nil: the marketing confirmation would leave without the organization's logo")
	}
	if opts.Sender == nil {
		t.Error("GrowthLetterOptions.Sender is nil")
	}
	if opts.Templates != d.Templates {
		t.Error("GrowthLetterOptions.Templates differs from the ticket letters' renderer")
	}
}

// Without media storage the options carry a nil INTERFACE, as the delivery
// options do, so branding falls back to the name without a panic.
func TestBuildGrowthLetterOptions_WithoutMediaIsANilInterface(t *testing.T) {
	cfg := &config.Config{}
	d := buildDeliveryHandlerOptions(cfg, nil, buildDeliveryMediaResolver(nil, cfg, testLogger()), testLogger())
	opts := buildGrowthLetterOptions(d, nil, cfg, testLogger())
	if opts.Media != nil {
		t.Fatalf("GrowthLetterOptions.Media = %#v, want nil", opts.Media)
	}
	if h := delivery.NewMarketingConfirmHandler(opts); h == nil {
		t.Fatal("NewMarketingConfirmHandler returned nil")
	}
}
