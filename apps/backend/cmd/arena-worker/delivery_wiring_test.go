package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// stubResolver stands in for the real media resolver; the wiring tests only
// care whether one reached the handler options.
type stubResolver struct{}

func (stubResolver) ResolveLogo(context.Context, string) ([]byte, string, error) {
	return nil, "", nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestBuildDeliveryHandlerOptions_CarriesMediaResolver is the regression
// guard for the defect this seam exists for: arena-worker is the only
// process that renders live ticket e-mails, and it built the delivery
// handler with HandlerOptions.Media nil, so resolveBranding always fell back
// to the platform logo and resolvePoster always returned nil — no organizer
// logo and no poster ever reached a real ticket.
func TestBuildDeliveryHandlerOptions_CarriesMediaResolver(t *testing.T) {
	cfg := &config.Config{}
	opts := buildDeliveryHandlerOptions(cfg, nil, stubResolver{}, testLogger())

	if opts.Media == nil {
		t.Fatal("HandlerOptions.Media is nil: the worker would render every ticket without the organizer logo and without the poster")
	}
	if opts.Sender == nil {
		t.Error("HandlerOptions.Sender is nil")
	}
	if opts.FromAddress == "" {
		t.Error("HandlerOptions.FromAddress is empty")
	}
}

// Media storage is optional. With no resolver the options must carry a nil
// INTERFACE (not a typed nil, which would panic on the first method call)
// and the handler must still be constructible — delivery keeps working
// exactly as it does today when MEDIA_BACKEND is unset.
func TestBuildDeliveryHandlerOptions_WithoutMediaStillBuildsAHandler(t *testing.T) {
	cfg := &config.Config{}
	opts := buildDeliveryHandlerOptions(cfg, nil, buildDeliveryMediaResolver(nil, cfg, testLogger()), testLogger())

	if opts.Media != nil {
		t.Fatalf("HandlerOptions.Media = %#v, want nil when media storage is not configured", opts.Media)
	}
	if h := delivery.NewHandler(opts); h == nil {
		t.Fatal("delivery.NewHandler returned nil")
	}
}

// buildMediaRepo is the shared construction path: no MEDIA_BACKEND means no
// repo and no hard failure — the worker must still start.
func TestBuildMediaRepo_UnconfiguredBackendIsNotAStartupFailure(t *testing.T) {
	if repo := buildMediaRepo(nil, &config.Config{}, testLogger()); repo != nil {
		t.Fatalf("buildMediaRepo = %#v, want nil when MEDIA_BACKEND is unset", repo)
	}
}

// TestDeliveryRegistration_GoesThroughTheOptionsBuilder pins the call site
// itself: the options builder above can only guarantee the wiring while
// registerBuiltinHandlers actually uses it. A future edit that inlines a
// fresh delivery.HandlerOptions literal there would silently reintroduce the
// nil-Media defect.
func TestDeliveryRegistration_GoesThroughTheOptionsBuilder(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(src), "delivery.NewHandler(buildDeliveryHandlerOptions(") {
		t.Error("arena-worker must register ticket.deliver through buildDeliveryHandlerOptions, " +
			"which is what guarantees the handler gets a MediaResolver")
	}
}
