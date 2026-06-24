// testfilehelpers_test.go provides shared file-finding utilities for
// httpserver package tests that need to read project-level files such as
// openapi/openapi.yaml and README.md at test time.
//
// The strategy mirrors the approach used in openapi_drift_test.go:
//  1. Use runtime.Caller(0) to get the absolute path of the test source file,
//     then walk upward to find the target file.
//  2. Fall back to os.Getwd() for -trimpath / Docker environments where
//     runtime.Caller returns a module-relative (non-absolute) path.
//
// Both strategies navigate from the httpserver package directory (6 levels
// below the repo root) to the repo root, then locate well-known files.
package httpserver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// findFileByName locates a named project file and returns its content as a
// string. Supported names:
//
//   - "openapi.yaml"  → apps/backend/openapi/openapi.yaml (relative to repo root)
//   - "README.md"     → README.md at the repo root
//
// The function uses two strategies (runtime.Caller then CWD) so it works both
// in normal `go test` runs and in Docker/CI builds that use -trimpath.
func findFileByName(t *testing.T, name string) string {
	t.Helper()

	// Strategy 1: compile-time absolute path via runtime.Caller.
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		// thisFile = .../apps/backend/internal/platform/httpserver/testfilehelpers_test.go
		// Navigate up to the repo root: dir(thisFile) → httpserver/ → platform/ →
		// internal/ → backend/ → apps/ → repo-root (5 steps).
		dir := filepath.Dir(thisFile)
		repoRoot := dir
		for i := 0; i < 5; i++ {
			repoRoot = filepath.Dir(repoRoot)
		}
		if candidate := resolveFileInRepo(repoRoot, name); candidate != "" {
			data, err := os.ReadFile(candidate)
			if err == nil {
				return string(data)
			}
		}
	}

	// Strategy 2: CWD-based fallback for -trimpath / Docker environments.
	// `go test` sets CWD to the package directory being tested.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("findFileByName: cannot determine working directory: %v", err)
	}

	// Walk upward from CWD looking for the repo root (signalled by the presence
	// of go.mod) and then resolve the target file.
	dir := cwd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			// Found repo root.
			if candidate := resolveFileInRepo(dir, name); candidate != "" {
				data, err := os.ReadFile(candidate)
				if err == nil {
					return string(data)
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("findFileByName: cannot locate %q; cwd=%s", name, cwd)
	return ""
}

// resolveFileInRepo maps a well-known filename to its canonical path within the
// repository rooted at repoRoot. Returns the path if the file exists, or "".
func resolveFileInRepo(repoRoot, name string) string {
	var candidates []string
	switch name {
	case "openapi.yaml":
		// The spec lives at apps/backend/openapi/openapi.yaml inside the repo.
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "openapi", "openapi.yaml"),
			// Also try a direct openapi/ at the repo root in case the layout changes.
			filepath.Join(repoRoot, "openapi", "openapi.yaml"),
		}
	case "README.md":
		candidates = []string{
			filepath.Join(repoRoot, "README.md"),
		}
	case "0004_scaffold_echo.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0004_scaffold_echo.sql"),
		}
	case "scaffold_echo.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "scaffold_echo.sql.go"),
		}
	// Geo reference data (feature #123)
	case "0006_geo.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0006_geo.sql"),
		}
	case "geo.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "geo.sql"),
		}
	case "geo.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "geo.sql.go"),
		}
	// Users (feature #114)
	case "0005_users.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0005_users.sql"),
		}
	case "users.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "users.sql.go"),
		}
	// Refresh tokens (feature #115)
	case "0007_refresh_tokens.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0007_refresh_tokens.sql"),
		}
	case "refresh_tokens.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "refresh_tokens.sql.go"),
		}
	// RBAC permission engine (feature #117)
	case "0008_rbac.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0008_rbac.sql"),
		}
	case "rbac.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "rbac.sql.go"),
		}
	// Organizations (feature #119)
	case "0009_organizations.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0009_organizations.sql"),
		}
	case "orgs.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "orgs.sql"),
		}
	case "orgs.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "orgs.sql.go"),
		}
	// Sales Channels (feature #121)
	case "0010_sales_channels.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0010_sales_channels.sql"),
		}
	case "channels.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "channels.sql"),
		}
	case "channels.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "channels.sql.go"),
		}
	// Memberships (feature #120)
	case "0011_memberships.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0011_memberships.sql"),
		}
	case "memberships.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "memberships.sql"),
		}
	case "memberships.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "memberships.sql.go"),
		}
	// Venues (feature #124)
	case "0012_venues.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0012_venues.sql"),
		}
	case "venues.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "venues.sql"),
		}
	case "venues.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "venues.sql.go"),
		}
	// Agent feed tokens (feature #122)
	case "0013_agent_feed_tokens.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0013_agent_feed_tokens.sql"),
		}
	case "feed_tokens.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "feed_tokens.sql"),
		}
	case "feed_tokens.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "feed_tokens.sql.go"),
		}
	// Events (feature #125)
	case "0014_events.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0014_events.sql"),
		}
	case "events.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "events.sql"),
		}
	case "events.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "events.sql.go"),
		}
	// Password reset tokens (feature #116)
	case "0015_password_reset_tokens.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0015_password_reset_tokens.sql"),
		}
	case "password_reset_tokens.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "password_reset_tokens.sql"),
		}
	case "password_reset_tokens.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "password_reset_tokens.sql.go"),
		}
	// Sessions (feature #126)
	case "0016_sessions.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0016_sessions.sql"),
		}
	case "sessions.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "sessions.sql"),
		}
	case "sessions.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "sessions.sql.go"),
		}
	// Event publications (feature #151)
	case "0017_event_publications.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0017_event_publications.sql"),
		}
	case "event_publications.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "event_publications.sql"),
		}
	case "event_publications.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "event_publications.sql.go"),
		}
	// GDPR data workflows (feature #164)
	case "0018_gdpr.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0018_gdpr.sql"),
		}
	case "gdpr.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "gdpr.sql"),
		}
	case "gdpr.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "gdpr.sql.go"),
		}
	// Ticket tiers (feature #127)
	case "0019_ticket_tiers.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0019_ticket_tiers.sql"),
		}
	case "ticket_tiers.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "ticket_tiers.sql"),
		}
	case "ticket_tiers.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "ticket_tiers.sql.go"),
		}
	// Inventory ledger (feature #130)
	case "0020_inventory_ledger.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0020_inventory_ledger.sql"),
		}
	case "inventory_ledger.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "inventory_ledger.sql"),
		}
	case "inventory_ledger.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "inventory_ledger.sql.go"),
		}
	// Reservations state machine (feature #131)
	case "0021_reservations.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0021_reservations.sql"),
		}
	case "reservations.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "reservations.sql"),
		}
	case "reservations.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "reservations.sql.go"),
		}
	// Shared gen/httpserver files referenced by multiple test files
	case "querier.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "querier.go"),
		}
	case "sessions.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "sessions.go"),
		}
	case "server.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "server.go"),
		}
	case "reservations.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "reservations.go"),
		}
	case "reservation_processor.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "reservation_processor.go"),
		}
	// Promo codes (feature #128)
	case "0022_promo_codes.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0022_promo_codes.sql"),
		}
	case "promo_codes.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "promo_codes.sql"),
		}
	case "promo_codes.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "promo_codes.sql.go"),
		}
	case "promo_codes.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "promo_codes.go"),
		}
	// Pricing calculator (feature #129)
	case "0023_pricing_calculator.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0023_pricing_calculator.sql"),
		}
	case "pricing_calculator.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "pricing_calculator.go"),
		}
	// Checkout sessions (feature #132)
	case "0024_checkout_sessions.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0024_checkout_sessions.sql"),
		}
	case "checkout_sessions.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "checkout_sessions.sql.go"),
		}
	case "checkout.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "checkout.go"),
		}
	// Price breakdown — all-in display endpoint (feature #163)
	case "price_breakdown.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "price_breakdown.go"),
		}
	// Payment intents — SCA-aware state machine (feature #137)
	case "0025_payment_intents.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0025_payment_intents.sql"),
		}
	case "payment_intents.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "payment_intents.sql"),
		}
	case "payment_intents.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "payment_intents.sql.go"),
		}
	case "payment_intents.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "payment_intents.go"),
		}
	// Tickets — issued entitlements (feature #139)
	case "0026_tickets.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0026_tickets.sql"),
		}
	case "tickets.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "tickets.sql"),
		}
	case "tickets.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "tickets.sql.go"),
		}
	case "tickets.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "tickets.go"),
		}
	// Ticket credentials — QR and PDF bearer artifacts (feature #140)
	case "0027_ticket_credentials.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0027_ticket_credentials.sql"),
		}
	case "ticket_credentials.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "ticket_credentials.sql"),
		}
	case "ticket_credentials.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "ticket_credentials.sql.go"),
		}
	case "credentials.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "credentials.go"),
		}
	default:
		// Generic fallback: try the file directly at the repo root.
		candidates = []string{
			filepath.Join(repoRoot, name),
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
