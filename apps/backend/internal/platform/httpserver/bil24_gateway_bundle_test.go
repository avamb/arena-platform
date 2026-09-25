package httpserver

// Regression (2026-09-25): production arena-api never passed an i18n bundle,
// so every Bil24 wire description came back English whatever locale a site
// sent — Lampyris showed "promo code was not found" on its Russian page.
// Options.GatewayBundle localizes the gateway alone, without switching on the
// REST locale middleware that Options.Bundle brings.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func buildBil24ServerWithGatewayBundle(t *testing.T, bundle *i18n.Bundle) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:             config.EnvDevelopment,
		RequestTimeout:     5 * time.Second,
		BodyLimitBytes:     1 << 20,
		Bil24CompatEnabled: true,
		DefaultLocale:      "en",
		ActiveLocales:      []string{"en", "ru"},
	}
	return New(Options{
		Config:             cfg,
		Bil24CompatEnabled: true,
		GatewayBundle:      bundle,
		EventQueries:       gen.New(nil),
		TierQueries:        gen.New(nil),
		CheckoutQueries:    gen.New(nil),
		TicketQueries:      gen.New(nil),
		BarcodeQueries:     gen.New(nil),
	})
}

func bil24Description(t *testing.T, s *Server, body string) string {
	t.Helper()
	rr := postBil24(s, body)
	var env struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode gateway envelope: %v (body=%q)", err, rr.Body.String())
	}
	return env.Description
}

func TestBil24Gateway_GatewayBundleAnswersInRequestLocale(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	body := `{"command":"NO_SUCH_COMMAND","locale":"ru"}`

	got := bil24Description(t, buildBil24ServerWithGatewayBundle(t, bundle), body)
	if !strings.Contains(got, "неизвестная команда") {
		t.Fatalf("with GatewayBundle a ru request must get Russian, got %q", got)
	}

	if bare := bil24Description(t, buildBil24ServerWithGatewayBundle(t, nil), body); !strings.Contains(bare, "unknown command") {
		t.Fatalf("without a bundle the gateway stays English, got %q", bare)
	}
}
