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
		if combined := readServerGoLike(repoRoot, name); combined != "" {
			return combined
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
			if combined := readServerGoLike(dir, name); combined != "" {
				return combined
			}
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
	// scaffold_echo cleanup migration (feature #171)
	case "0031_remove_scaffold_echo.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0031_remove_scaffold_echo.sql"),
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
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcatalog", "sessions.go"),
		}
	case "server.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "server.go"),
		}
	case "reservations.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "reservations.go"),
		}
	case "reservation_processor.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "reservation_processor.go"),
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
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "promo_codes.go"),
		}
	// Pricing calculator (feature #129)
	case "0023_pricing_calculator.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0023_pricing_calculator.sql"),
		}
	case "pricing_calculator.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "pricing_calculator.go"),
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
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "checkout.go"),
		}
	// Price breakdown — all-in display endpoint (feature #163)
	case "price_breakdown.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "price_breakdown.go"),
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
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "payment_intents.go"),
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
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "htickets", "tickets.go"),
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
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "htickets", "credentials.go"),
		}
	// Refund state machine (feature #138)
	case "0028_refunds.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0028_refunds.sql"),
		}
	case "refunds.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "refunds.sql"),
		}
	case "refunds.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "refunds.sql.go"),
		}
	case "refunds.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hcheckout", "refunds.go"),
		}
	// Barcode authority federation (feature #142)
	case "0029_barcode_authorities.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0029_barcode_authorities.sql"),
		}
	case "barcodes.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "barcodes.sql"),
		}
	case "barcodes.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "barcodes.sql.go"),
		}
	case "barcodes.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "barcodes.go"),
		}
	// Scanner webhook events — Bil24-compatible (feature #143)
	case "scanner_events.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "scanner_events.go"),
		}
	// Offline scanner snapshot + online validate (feature #144)
	case "scanner_snapshot.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "scanner_snapshot.go"),
		}
	// Admin ticket scan-events read view (feature #295, S-4)
	case "admin_ticket_scans.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "htickets", "admin_ticket_scans.go"),
		}
	// Admin ticket delivery resend (feature #291, T-4)
	case "admin_ticket_delivery.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "htickets", "admin_ticket_delivery.go"),
		}
	case "mount_admin.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "mount_admin.go"),
		}
	case "scan_events.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "scan_events.sql"),
		}
	case "scan_events.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "scan_events.sql.go"),
		}
	// External scanner callback ingest (feature #293, S-2)
	case "scanner_callback.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "scanner_callback.go"),
		}
	case "0055_scan_events.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0055_scan_events.sql"),
		}
	case "mount_scanning.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "mount_scanning.go"),
		}
	case "mount_v1.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "mount_v1.go"),
		}
	// Bil24 command gateway (feature #157) — moved into hbil24/ (phase 1n)
	case "bil24_compat.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hbil24", "bil24_compat.go"),
		}
	// Platform config file (referenced by feature #157 tests)
	case "config.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "config", "config.go"),
		}
	// Ticket delivery via email (feature #141)
	case "0030_delivery_jobs.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0030_delivery_jobs.sql"),
		}
	case "delivery_jobs.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "delivery_jobs.sql"),
		}
	case "delivery_jobs.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "delivery_jobs.sql.go"),
		}
	case "sender.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "email", "sender.go"),
		}
	case "delivery_handler.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "delivery", "handler.go"),
		}
	case "delivery_enqueue.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "htickets", "delivery_enqueue.go"),
		}
	// Public feed events API (feature #152)
	case "public_feed.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "public_feed.sql"),
		}
	case "public_feed.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "public_feed.sql.go"),
		}
	// Widget funnel events telemetry sink (feature #322 WID-0e)
	case "widget_funnel_events.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "widget_funnel_events.sql"),
		}
	case "widget_funnel_events.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "widget_funnel_events.sql.go"),
		}
	case "0062_widget_funnel_events.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0062_widget_funnel_events.sql"),
		}
	case "public_feed.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hfeed", "public_feed.go"),
		}
	// Public feed checkout start (feature #153)
	case "public_feed_checkout.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hfeed", "public_feed_checkout.go"),
		}
	// Post-event report generation (feature #159)
	case "0032_event_reports.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0032_event_reports.sql"),
		}
	case "event_reports.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "event_reports.sql"),
		}
	case "event_reports.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "event_reports.sql.go"),
		}
	case "event_reports.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hreports", "event_reports.go"),
		}
	case "reporting_handler.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "reporting", "handler.go"),
		}
	// Service billing ledger (feature #161)
	case "0033_billing_ledger.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0033_billing_ledger.sql"),
		}
	case "billing_ledger.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "billing_ledger.sql"),
		}
	case "billing_ledger.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "billing_ledger.sql.go"),
		}
	case "billing_ledger.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hbilling", "billing_ledger.go"),
		}
	// Platform superadmin console (feature #166)
	case "0034_superadmin.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0034_superadmin.sql"),
		}
	case "superadmin.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "superadmin.sql"),
		}
	case "superadmin.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "superadmin.sql.go"),
		}
	case "superadmin.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hiam", "superadmin.go"),
		}
	// External allocation quota model (feature #145)
	case "0035_external_allocations.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0035_external_allocations.sql"),
		}
	case "external_allocations.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "external_allocations.sql"),
		}
	case "external_allocations.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "external_allocations.sql.go"),
		}
	case "external_allocations.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hinventory", "external_allocations.go"),
		}
	// Complimentary ticket issuance flow (feature #148)
	case "0036_complimentary_issuances.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0036_complimentary_issuances.sql"),
		}
	case "complimentary_issuances.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "complimentary_issuances.sql"),
		}
	case "complimentary_issuances.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "complimentary_issuances.sql.go"),
		}
	case "complimentary.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "htickets", "complimentary.go"),
		}
	// Complimentary revocation flow (feature #150)
	case "0038_complimentary_revocation.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0038_complimentary_revocation.sql"),
		}
	// Report delivery + recipient deduplication (feature #160)
	case "reportdelivery_handler.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "reportdelivery", "handler.go"),
		}
	case "report_delivery_enqueue.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hreports", "report_delivery_enqueue.go"),
		}
	// Admin impersonation (feature #167)
	case "impersonation.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hiam", "impersonation.go"),
		}
	// Stripe Billing adapter for SaaS invoices (feature #162)
	case "0037_stripe_billing.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0037_stripe_billing.sql"),
		}
	case "stripe_billing.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "stripe_billing.sql"),
		}
	case "stripe_billing.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "stripe_billing.sql.go"),
		}
	case "stripe_billing.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hbilling", "stripe_billing.go"),
		}
	case "stripebilling_adapter.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "stripebilling", "adapter.go"),
		}
	// WordPress plugin core (feature #154)
	case "arena-events.php":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "wp-plugin", "arena-events", "arena-events.php"),
		}
	case "class-post-type.php":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "wp-plugin", "arena-events", "includes", "class-post-type.php"),
		}
	case "class-settings.php":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "wp-plugin", "arena-events", "includes", "class-settings.php"),
		}
	case "class-sync.php":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "wp-plugin", "arena-events", "includes", "class-sync.php"),
		}
	// WordPress webhook receiver (feature #156)
	case "class-webhook.php":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "wp-plugin", "arena-events", "includes", "class-webhook.php"),
		}
	// WordPress checkout integration (feature #155)
	case "class-checkout.php":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "wp-plugin", "arena-events", "includes", "class-checkout.php"),
		}
	// WCAG 2.2 AA accessibility audit (feature #165)
	case "wcag-checklist.md":
		candidates = []string{
			filepath.Join(repoRoot, "ops", "accessibility", "wcag-checklist.md"),
		}
	case "wp-demo-test-plan.md":
		candidates = []string{
			filepath.Join(repoRoot, "ops", "accessibility", "wp-demo-test-plan.md"),
		}
	case "remediation-backlog.md":
		candidates = []string{
			filepath.Join(repoRoot, "ops", "accessibility", "remediation-backlog.md"),
		}
	case "generate-snapshots.js":
		candidates = []string{
			filepath.Join(repoRoot, "ops", "accessibility", "generate-snapshots.js"),
		}
	case "accessibility.yml":
		candidates = []string{
			filepath.Join(repoRoot, ".github", "workflows", "accessibility.yml"),
		}
	case "0040_webhook_subscribers.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0040_webhook_subscribers.sql"),
		}
	case "webhook_subscribers.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "webhook_subscribers.sql"),
		}
	case "webhook_subscribers.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "webhook_subscribers.sql.go"),
		}
	// WordPress webhook subscriber registry (feature #156) — moved into
	// hwordpress/ (phase 1n)
	case "wp_webhooks.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hwordpress", "wp_webhooks.go"),
		}
	// External barcode batch import (feature #146)
	case "0039_barcode_batches.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0039_barcode_batches.sql"),
		}
	case "barcode_batches.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "barcode_batches.sql"),
		}
	case "barcode_batches.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "barcode_batches.sql.go"),
		}
	case "barcode_batches.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "barcode_batches.go"),
		}
	// network_operator role seed (feature #203)
	case "0042_network_operator_role.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0042_network_operator_role.sql"),
		}
	// Operator Network persistence (feature #204)
	case "0043_operator_networks.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0043_operator_networks.sql"),
		}
	// network.* permissions (feature #206)
	case "0044_network_permissions.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0044_network_permissions.sql"),
		}
	// Sales channel settings + credential masking (feature #236)
	case "0045_channel_settings.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0045_channel_settings.sql"),
		}
	// Payment provider configs (feature #237)
	case "0046_payment_provider_configs.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0046_payment_provider_configs.sql"),
		}
	// Superadmin user provisioning
	case "0047_admin_user_provisioning.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0047_admin_user_provisioning.sql"),
		}
	case "payment_provider_configs.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "payment_provider_configs.sql"),
		}
	case "payment_provider_configs.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "payment_provider_configs.sql.go"),
		}
	case "payment_configs.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hpayments", "payment_configs.go"),
		}
	// Organization bank accounts (feature #255; table from #254 wave)
	case "0048_organization_bank_accounts.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0048_organization_bank_accounts.sql"),
		}
	case "0056_organization_bank_accounts_country.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0056_organization_bank_accounts_country.sql"),
		}
	case "bank_accounts.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "bank_accounts.sql"),
		}
	case "bank_accounts.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "bank_accounts.sql.go"),
		}
	// External reconciliation (feature #147)
	// WID-0c recovery endpoint (feature #320) structural files
	case "mount_catalog.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "mount_catalog.go"),
		}
	case "feed_shims.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "feed_shims.go"),
		}
	case "types_gen.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "http", "openapi", "types_gen.go"),
		}
	case "0061_buyer_field_flags.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0061_buyer_field_flags.sql"),
		}
	case "0041_reconciliation_reports.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0041_reconciliation_reports.sql"),
		}
	case "reconciliation.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "queries", "reconciliation.sql"),
		}
	case "reconciliation.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "reconciliation.sql.go"),
		}
	case "reconciliation.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver", "hreconciliation", "reconciliation.go"),
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

// readServerGoLike returns the concatenated source of a logical handler
// "module" when its primary file has been split into siblings (and/or moved
// into a sub-package) by an httpserver refactor. Cases covered:
//
//   - name == "server.go": returns server.go + server_struct.go + wire.go +
//     every mount_*.go. Established by feature #174 when the original
//     2,396-line server.go was split into focused per-domain files; the
//     union lets existing structural tests that grep server.go for symbols
//     (struct fields, Options entries, route mounts) keep passing.
//
//   - name == "reconciliation.go": returns reconciliation.go +
//     reconciliation_submit.go + reconciliation_query.go +
//     reconciliation_review.go. Established by feature #175 when the
//     original 624-line reconciliation.go was split into one shared-types
//     file plus three handler files.
//
//   - name is a file moved into a domain sub-package (hcheckout/, hcatalog/,
//     hiam/, hreports/, hpayments/, …) — returns the sub-package file
//     concatenated with the corresponding shim file in httpserver/
//     (checkout_shims.go, catalog_shims.go, iam_shims.go, reports_shims.go,
//     payments_shims.go). The shim preserves the original
//     unexported handler/callback identifiers, so existing structural tests
//     that grep for symbols like handlePriceBreakdown or enqueueDeliveryJobs
//     keep matching even though those identifiers now live in the shim
//     while the handler bodies live in the sub-package.
//
// Returns "" for any other name so the caller falls through to the
// original resolveFileInRepo path.
func readServerGoLike(repoRoot, name string) string {
	httpserverDir := filepath.Join(repoRoot, "apps", "backend", "internal", "platform", "httpserver")

	switch name {
	case "server.go":
		entries, err := os.ReadDir(httpserverDir)
		if err != nil {
			return ""
		}
		var buf []byte
		wantedExact := map[string]bool{
			"server.go":        true,
			"server_struct.go": true,
			"wire.go":          true,
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			// Skip tests; include the canonical server files and every mount_*.go.
			if !wantedExact[n] && (len(n) <= len("mount_") || n[:len("mount_")] != "mount_" || filepath.Ext(n) != ".go" || endsWith(n, "_test.go")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(httpserverDir, n))
			if err != nil {
				continue
			}
			buf = append(buf, []byte("// === "+n+" ===\n")...)
			buf = append(buf, data...)
			buf = append(buf, '\n')
		}
		return string(buf)

	case "reconciliation.go":
		// The reconciliation domain now lives in hreconciliation/ (Phase 1i).
		// Aggregate the 4 sub-package files with the *Server-side shim so that
		// structural greps in reconciliation_147_test.go find both the moved
		// handler bodies (h-receiver) and the original *Server-receiver
		// witnesses preserved in reconciliation_shims.go.
		var buf []byte
		for _, n := range []string{
			"reconciliation.go",
			"reconciliation_submit.go",
			"reconciliation_query.go",
			"reconciliation_review.go",
		} {
			data, err := os.ReadFile(filepath.Join(httpserverDir, "hreconciliation", n))
			if err != nil {
				continue
			}
			buf = append(buf, []byte("// === hreconciliation/"+n+" ===\n")...)
			buf = append(buf, data...)
			buf = append(buf, '\n')
		}
		if data, err := os.ReadFile(filepath.Join(httpserverDir, "reconciliation_shims.go")); err == nil {
			buf = append(buf, []byte("// === reconciliation_shims.go ===\n")...)
			buf = append(buf, data...)
			buf = append(buf, '\n')
		}
		return string(buf)
	}

	// Domain-sub-package aggregation: when a file has been moved into hcheckout/,
	// hcatalog/, or hiam/, concatenate the sub-package file with its shim so that
	// the test sees both the moved handler bodies and the unexported identifiers
	// preserved in the shim layer.
	subPkg, shim := domainSubPackageFor(name)
	if subPkg == "" {
		return ""
	}
	var buf []byte
	subFile := filepath.Join(httpserverDir, subPkg, name)
	if data, err := os.ReadFile(subFile); err == nil {
		buf = append(buf, []byte("// === "+subPkg+"/"+name+" ===\n")...)
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	shimFile := filepath.Join(httpserverDir, shim)
	if data, err := os.ReadFile(shimFile); err == nil {
		buf = append(buf, []byte("// === "+shim+" ===\n")...)
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	return string(buf)
}

// domainSubPackageFor maps a filename to the (subPackage, shimFile) pair when
// the file has been moved into a domain sub-package by the httpserver refactor.
// Returns ("", "") when the name is not domain-mapped.
func domainSubPackageFor(name string) (string, string) {
	switch name {
	case "checkout.go", "reservations.go", "reservation_processor.go",
		"price_breakdown.go", "payment_intents.go", "refunds.go", "promo_codes.go":
		return "hcheckout", "checkout_shims.go"
	// pricing_calculator.go keeps its own top-level shim file (same name) so
	// TestPricing129_PricingCalculatorFileExists continues to stat it directly.
	case "pricing_calculator.go":
		return "hcheckout", "pricing_calculator.go"
	case "events.go", "channels.go", "publications.go", "ticket_tiers.go", "venues.go",
		"sessions.go":
		return "hcatalog", "catalog_shims.go"
	case "orgs.go", "memberships.go", "superadmin.go", "impersonation.go",
		"admin_memberships.go", "admin_orgs.go", "admin_users.go", "me.go":
		return "hiam", "iam_shims.go"
	case "tickets.go", "credentials.go", "complimentary.go", "delivery_enqueue.go",
		"admin_ticket_delivery.go", "admin_ticket_scans.go":
		return "htickets", "tickets_shims.go"
	case "barcodes.go", "barcode_batches.go":
		return "hbarcode", "barcode_shims.go"
	case "billing_ledger.go", "stripe_billing.go", "stripe_connect.go":
		return "hbilling", "billing_shims.go"
	case "scanner_callback.go", "scanner_events.go", "scanner_snapshot.go":
		return "hscanner", "scanner_shims.go"
	case "networks.go", "network_orgs.go", "network_users.go":
		return "hnetworks", "networks_shims.go"
	case "geo.go":
		return "hgeo", "geo_shims.go"
	case "gdpr.go", "gdpr_processor.go":
		return "hgdpr", "gdpr_shims.go"
	case "feed_tokens.go", "public_feed.go", "public_feed_checkout.go",
		"public_checkout_status.go", "public_checkout_recover.go",
		"public_funnel_events.go":
		return "hfeed", "feed_shims.go"
	case "inventory.go", "inventory_ledger.go", "external_allocations.go":
		return "hinventory", "inventory_shims.go"
	case "event_reports.go", "report_delivery_enqueue.go":
		return "hreports", "reports_shims.go"
	case "payment_configs.go", "payment_configs_write.go", "payment_configs_types.go":
		return "hpayments", "payments_shims.go"
	case "bank_accounts.go", "bank_accounts_write.go", "bank_accounts_types.go":
		return "hbankaccounts", "bank_accounts_shims.go"
	case "plans.go", "versions.go", "public_schema.go", "bind.go", "seats.go":
		return "hseating", "seating_shims.go"
	// NOTE: hauth is deliberately absent — the refactor renamed its files
	// (auth_login.go → hauth/login.go, …), so a same-name lookup cannot map
	// them. No structural test greps the old auth_*.go handler files.
	case "media.go":
		return "hmedia", "mount_media.go"
	case "wp_webhooks.go":
		return "hwordpress", "wordpress_shims.go"
	case "bil24_compat.go":
		return "hbil24", "bil24_shims.go"
	}
	return "", ""
}

// findFileByPattern locates a file at a path relative to the repo root
// (e.g. findFileByPattern(t, "apps/backend/openapi", "openapi.yaml"))
// and returns its content. Uses the same two-strategy approach as findFileByName.
func findFileByPattern(t *testing.T, relDir, filename string) string {
	t.Helper()

	locate := func(repoRoot string) string {
		candidate := filepath.Join(repoRoot, filepath.FromSlash(relDir), filename)
		data, err := os.ReadFile(candidate)
		if err == nil {
			return string(data)
		}
		return ""
	}

	// Strategy 1: compile-time absolute path via runtime.Caller.
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		dir := filepath.Dir(thisFile)
		repoRoot := dir
		for i := 0; i < 5; i++ {
			repoRoot = filepath.Dir(repoRoot)
		}
		if content := locate(repoRoot); content != "" {
			return content
		}
	}

	// Strategy 2: CWD-based fallback for -trimpath / Docker environments.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("findFileByPattern: cannot determine working directory: %v", err)
	}
	dir := cwd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			if content := locate(dir); content != "" {
				return content
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("findFileByPattern: cannot locate %q in %q; cwd=%s", filename, relDir, cwd)
	return ""
}

// endsWith is a tiny strings.HasSuffix-equivalent kept here so the helper
// stays self-contained (the test package may not import "strings").
func endsWith(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
