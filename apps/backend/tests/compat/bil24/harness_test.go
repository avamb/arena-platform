//go:build integration

// harness_test.go — feature #450 (W1-0) contract harness skeleton.
//
// This file boots the real arena_new HTTP server against a live PostgreSQL
// database and runs the 10 wire-contract scenarios defined in
// 08_architecture/18_bil24_compat_wave1_specification_ru.md §15.3.
//
// STATUS TODAY: every scenario calls t.Skip with the feature id that will
// implement the corresponding command. As those features land they replace
// the Skip line with the real assertion. The harness itself is green until
// then, which is the "done" bar for feature #450 (see backlog entry).
//
// Golden-response comparison rules (spec §15.2):
//   - STRICT key-set: a missing OR extra key fails the test.
//   - Placeholders in golden files ({{actionEventId}}, {{seatId:Parter-3-12}},
//     {{orderId}}, {{now+ttl}}, {{sessionId}}, {{token}}) are resolved by the
//     harness from seeded state before comparison.
//   - Numbers are compared as JSON numbers (float64), so integer/float drift
//     inside the JSON encoder is a defect.
//   - Never edit a golden file to make a test go green — extend the spec or
//     stop and ask.
//
// Prerequisites:
//   - DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//   - JWT_SIGNING_SECRET=<anything> (config loader demands it even in tests)
//   - Migrations applied (Integration CI job does this automatically).
package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// harness state
// ─────────────────────────────────────────────────────────────────────────────

// harnessState is the seeded fixture shared by every scenario. The concrete
// seed helpers land alongside the features that consume them (#451 seeds
// org/channel, #453 seeds venue, #454 seeds event, etc.). Until then the
// struct exists to document the target shape.
type harnessState struct {
	OrgID          string
	ChannelFID     int64  // = sales_channels.display_number
	ChannelToken   string // raw token; bcrypt is stored server-side
	VenueID        string
	VenueTimezone  string // e.g. "Europe/Prague"
	EventID        string
	AssignedSessID string
	GAsessID       string
	SeatIDs        map[string]string // "Parter-3-12" → system_seat_id
}

// setupHarness reads DATABASE_URL, opens a pgxpool, seeds the fixture via
// seedHarness (seed_test.go, feature #470) and returns the resulting state.
// Individual scenarios that un-skip are expected to boot the real
// httpserver.Server on top of the seeded state; that server bootstrap lands
// with the scenario that first needs it (each t.Skip line names the id).
func setupHarness(t *testing.T) *harnessState {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("harness requires DATABASE_URL; see harness_test.go docs")
	}
	if os.Getenv("JWT_SIGNING_SECRET") == "" {
		t.Skip("harness requires JWT_SIGNING_SECRET; see harness_test.go docs")
	}
	// seedHarness lives in seed_test.go (feature #470). It creates the
	// full fixture (org + channel + venue + published event + one
	// assigned-seats session bound to Palác Akropolis + one GA session +
	// tiers + promo code) and registers cleanup via t.Cleanup.
	return seedHarness(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// fixture loader (request + golden)
// ─────────────────────────────────────────────────────────────────────────────

// loadWPFixture returns the request and matching golden payload for a
// (command, case) pair, e.g. ("RESERVATION", "reserve_by_seat"). Both files
// are read from testdata/wp/{requests,golden}/<COMMAND>/<case>.json.
func loadWPFixture(t *testing.T, command, caseName string) (request, golden map[string]interface{}) {
	t.Helper()
	reqPath := filepath.Join("testdata", "wp", "requests", command, caseName+".json")
	gldPath := filepath.Join("testdata", "wp", "golden", command, caseName+".json")
	request = mustReadJSON(t, reqPath)
	golden = mustReadJSON(t, gldPath)
	return
}

func mustReadJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// resolveGolden replaces the documented placeholders in a golden object with
// values from the harness state. Used by scenarios once they un-skip.
func resolveGolden(g map[string]interface{}, st *harnessState) map[string]interface{} {
	return walk(g, func(v string) string {
		v = strings.ReplaceAll(v, "{{actionEventId}}", st.EventID)
		v = strings.ReplaceAll(v, "{{sessionId}}", st.AssignedSessID)
		v = strings.ReplaceAll(v, "{{token}}", st.ChannelToken)
		for label, seat := range st.SeatIDs {
			v = strings.ReplaceAll(v, "{{seatId:"+label+"}}", seat)
		}
		return v
	}).(map[string]interface{})
}

func walk(v interface{}, fn func(string) string) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, sub := range x {
			out[k] = walk(sub, fn)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, sub := range x {
			out[i] = walk(sub, fn)
		}
		return out
	case string:
		return fn(x)
	default:
		return v
	}
}

// assertGoldenKeySet performs the STRICT key-set comparison required by spec
// §15.2 between an actual response and a (placeholder-resolved) golden. It
// reports missing and extra keys per level.
func assertGoldenKeySet(t *testing.T, actual, expected map[string]interface{}) {
	t.Helper()
	compareKeys(t, "", actual, expected)
}

func compareKeys(t *testing.T, path string, got, want map[string]interface{}) {
	t.Helper()
	extra, missing := diffKeys(got, toSet(mapKeysOf(want)))
	if len(extra) > 0 {
		t.Errorf("%s: extra keys %v", pathOr(path, "<root>"), extra)
	}
	if len(missing) > 0 {
		t.Errorf("%s: missing keys %v", pathOr(path, "<root>"), missing)
	}
	for k, sub := range want {
		gv, ok := got[k]
		if !ok {
			continue
		}
		if wm, isMap := sub.(map[string]interface{}); isMap {
			if gm, isGotMap := gv.(map[string]interface{}); isGotMap {
				compareKeys(t, path+"."+k, gm, wm)
			} else {
				t.Errorf("%s.%s: expected object, got %T", pathOr(path, ""), k, gv)
			}
		}
	}
}

func mapKeysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pathOr(p, fallback string) string {
	if p == "" {
		return fallback
	}
	return p
}

// ─────────────────────────────────────────────────────────────────────────────
// spec §15.3 — 10 scenarios (each t.Skip'd with the implementing feature id)
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_450_Harness_Scenarios runs the 10 wire-contract scenarios
// as sub-tests. Each sub-test's first line is a t.Skip that names the feature
// id which will implement it. Removing the Skip line is how a subsequent
// feature adopts the scenario.
func TestCompatBil24_450_Harness_Scenarios(t *testing.T) {
	st := setupHarness(t)

	// Scenario skip ids retargeted by feature #470 (W1-A1a) against the
	// post-W1-split backlog in 09_autoforge/wp_bil24_compat_backlog.md.
	// The mapping is: 1→#497, 2→#495, 3→#484, 4→#509, 5→#494, 6→#492,
	// 7→#501, 8→#518, 9→#514, 10→#520. Removing the Skip line is how the
	// implementing feature adopts the scenario.
	t.Run("01_catalog_get_all_actions", func(t *testing.T) {
		t.Skip("feature #497: GET_ALL_ACTIONS with org isolation + venue TZ")
		req, gld := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
		_ = req
		_ = resolveGolden(gld, st)
	})

	t.Run("02_ga_purchase_flow", func(t *testing.T) {
		t.Skip("feature #495: CREATE_USER→RESERVE×2→GET_CART→ADD_PROMO_CODES→CREATE_ORDER_EXT→PAY_ORDER→GET_TICKETS_BY_ORDER end-to-end")
	})

	t.Run("03_seated_seats_conflict_and_expiry", func(t *testing.T) {
		t.Skip("feature #484: assigned seats: reserve → parallel conflict resultCode 101 → UN_RESERVE_ALL → expired session resultCode 1")
	})

	t.Run("04_refund_dedup", func(t *testing.T) {
		t.Skip("feature #509: REFUND_TICKET fires ticket.refunded to wpstub AND MACS-stub; repeat → dedup; orders.status updates")
	})

	t.Run("05_expired_hold_on_pay_order", func(t *testing.T) {
		t.Skip("feature #494: PAY_ORDER on expired hold → ReacquireHold success path AND failure path (manual_review + operator alert)")
	})

	t.Run("06_one_open_order_rule", func(t *testing.T) {
		t.Skip("feature #492: two sequential CREATE_ORDER_EXT for the same session return the same orderId (one-open-order invariant)")
	})

	t.Run("07_svg_image_and_etag", func(t *testing.T) {
		t.Skip("feature #501: GET /compat/bil24/image sbt/1.0 shape §8 + ETag 304 caching + sbt:cat matches <metadata>")
		// Sanity: the SVG skeleton is checked in.
		if _, err := os.Stat(filepath.Join("testdata", "wp", "svg", "palac_akropolis.sbt.svg")); err != nil {
			t.Fatalf("SVG skeleton missing: %v", err)
		}
	})

	t.Run("08_bil24_session_import_idempotent", func(t *testing.T) {
		t.Skip("feature #518: POST /v1/organizations/{org}/imports/bil24-session twice with same payload → created:false on 2nd; GET_SEAT_LIST preserves Bil24 seatIds")
	})

	t.Run("09_api_keys_service_scope", func(t *testing.T) {
		t.Skip("feature #514: service API keys with scope limits: cross-org → 403; revoked → 401")
	})

	t.Run("10_customer_import_c7_dry_run_then_apply", func(t *testing.T) {
		t.Skip("feature #520: dry-run on bil24_orders_pseudonymized.json → report; apply → idempotent")
	})
}

// TestCompatBil24_450_Harness_ScenarioCoverage guards against future PRs
// silently dropping a scenario. Every §15.3 scenario id must remain named
// in this file.
func TestCompatBil24_450_Harness_ScenarioCoverage(t *testing.T) {
	want := []string{
		"01_catalog_get_all_actions",
		"02_ga_purchase_flow",
		"03_seated_seats_conflict_and_expiry",
		"04_refund_dedup",
		"05_expired_hold_on_pay_order",
		"06_one_open_order_rule",
		"07_svg_image_and_etag",
		"08_bil24_session_import_idempotent",
		"09_api_keys_service_scope",
		"10_customer_import_c7_dry_run_then_apply",
	}
	src, err := os.ReadFile("harness_test.go")
	if err != nil {
		t.Fatalf("read self: %v", err)
	}
	body := string(src)
	for _, name := range want {
		if !strings.Contains(body, `t.Run("`+name+`"`) {
			t.Errorf("scenario %q missing from harness_test.go — spec §15.3 requires all 10", name)
		}
	}
}
