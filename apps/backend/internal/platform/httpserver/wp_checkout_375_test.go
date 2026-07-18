// wp_checkout_375_test.go — unit tests for feature #375
// (PR2-19 MAJOR: Fix WordPress plugin session/tier fetch and error parsing).
//
// Root causes fixed:
//   - Plugin called GET /v1/public/feeds/{token}/sessions (non-existent, returned 404
//     swallowed silently → tiers always render empty in production).
//   - Checkout proxy read $data['error'] (an object/array) instead of
//     $data['error']['message'], so error messages were never surfaced to the user.
//   - Fetch failures returned an empty array, indistinguishable from "no tiers".
//
// Verified fixes:
//
//	Step 1:  fetch_tier_availability uses /events/{event_id} not /sessions
//	Step 2:  URL contains rawurlencode($arena_event_id) — event ID in path
//	Step 3:  Response is parsed as $data['event']['sessions'] (nested shape)
//	Step 4:  price_amount is normalised to 'price' key for rendering
//	Step 5:  capacity is normalised to 'capacity_available' key for sold-out check
//	Step 6:  Return type is ?array (nullable) to distinguish failure from empty
//	Step 7:  is_wp_error returns null (not [])
//	Step 8:  Non-200 HTTP status returns null (not [])
//	Step 9:  Unexpected response shape returns null (not [])
//	Step 10: Error extraction reads $data['error']['message'] not $data['error']
//	Step 11: render_tiers_shortcode handles null with a visible error message
//	Step 12: Error message uses arena-fetch-error CSS class for targeting
//
// All tests are pure file/content checks — no live WordPress required.
package httpserver

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helper
// ─────────────────────────────────────────────────────────────────────────────

// checkoutPhpContent reads class-checkout.php using the shared test helper and
// returns its content. All Step tests share this helper.
func checkoutPhpContent(t *testing.T) string {
	t.Helper()
	content := findFileByName(t, "class-checkout.php")
	if content == "" {
		t.Fatal("class-checkout.php not found or empty")
	}
	return content
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: fetch_tier_availability uses /events/{event_id} not /sessions
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step1_EventsEndpointNotSessions(t *testing.T) {
	content := checkoutPhpContent(t)

	if strings.Contains(content, "'/sessions'") || strings.Contains(content, `"/sessions"`) {
		t.Error("class-checkout.php still calls the non-existent /sessions endpoint; fix must switch to /events/{event_id}")
	}

	if !strings.Contains(content, "'/events/'") && !strings.Contains(content, `"/events/"`) {
		t.Error("class-checkout.php does not call the /events/ endpoint; fetch_tier_availability must use /v1/public/feeds/{token}/events/{event_id}")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: URL contains rawurlencode($arena_event_id) — event ID in path
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step2_EventIDInURL(t *testing.T) {
	content := checkoutPhpContent(t)

	if !strings.Contains(content, "rawurlencode( $arena_event_id )") &&
		!strings.Contains(content, "rawurlencode($arena_event_id)") {
		t.Error("class-checkout.php does not rawurlencode the arena_event_id in the URL; the event ID must be path-encoded")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Response parsed as $data['event']['sessions'] (nested shape)
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step3_ParseNestedEventSessions(t *testing.T) {
	content := checkoutPhpContent(t)

	// Must read from the nested event object returned by the detail endpoint.
	if !strings.Contains(content, `$data['event']['sessions']`) &&
		!strings.Contains(content, `$data["event"]["sessions"]`) {
		t.Error("class-checkout.php does not parse $data['event']['sessions']; the event-detail endpoint wraps sessions under the 'event' key")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: price_amount normalised to 'price' key for rendering
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step4_PriceAmountNormalised(t *testing.T) {
	content := checkoutPhpContent(t)

	// The API returns price_amount; rendering code expects 'price'.
	if !strings.Contains(content, "price_amount") {
		t.Error("class-checkout.php does not reference price_amount; must map API field price_amount to the 'price' key expected by render_tiers_html")
	}
	if !strings.Contains(content, `$tier['price'] = $tier['price_amount']`) &&
		!strings.Contains(content, `$tier["price"] = $tier["price_amount"]`) {
		t.Error("class-checkout.php does not assign $tier['price'] from $tier['price_amount']; field normalisation is required")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: capacity normalised to 'capacity_available' for sold-out check
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step5_CapacityAvailableNormalised(t *testing.T) {
	content := checkoutPhpContent(t)

	if !strings.Contains(content, `$tier['capacity_available'] = $tier['capacity']`) &&
		!strings.Contains(content, `$tier["capacity_available"] = $tier["capacity"]`) {
		t.Error("class-checkout.php does not normalise capacity to capacity_available; " +
			"render_tiers_html uses $tier['capacity_available'] to detect sold-out tiers")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Return type is ?array (nullable) to distinguish failure from empty
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step6_NullableReturnType(t *testing.T) {
	content := checkoutPhpContent(t)

	if !strings.Contains(content, "): ?array") {
		t.Error("fetch_tier_availability does not declare ?array return type; nullable return is required to distinguish fetch failure from empty tier list")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: is_wp_error returns null (not [])
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step7_WpErrorReturnsNull(t *testing.T) {
	content := checkoutPhpContent(t)

	// Find the fetch_tier_availability function block and verify null return
	// after is_wp_error — not an empty array.
	fetchIdx := strings.Index(content, "protected static function fetch_tier_availability")
	if fetchIdx < 0 {
		t.Fatal("fetch_tier_availability function not found in class-checkout.php")
	}
	funcBody := content[fetchIdx:]

	// Look for the is_wp_error guard returning null.
	if !strings.Contains(funcBody, "return null; // network error") &&
		!strings.Contains(funcBody, "return null;") {
		t.Error("fetch_tier_availability does not return null on is_wp_error; must return null (not []) to surface network failures")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: Non-200 HTTP status returns null (not [])
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step8_Non200StatusReturnsNull(t *testing.T) {
	content := checkoutPhpContent(t)

	fetchIdx := strings.Index(content, "protected static function fetch_tier_availability")
	if fetchIdx < 0 {
		t.Fatal("fetch_tier_availability function not found in class-checkout.php")
	}
	funcBody := content[fetchIdx:]

	// Must check HTTP status code and return null on non-200.
	if !strings.Contains(funcBody, "wp_remote_retrieve_response_code") {
		t.Error("fetch_tier_availability does not check HTTP status code; non-200 responses (e.g. 404) must return null")
	}
	if !strings.Contains(funcBody, "$http_code !== 200") && !strings.Contains(funcBody, "$http_code != 200") {
		t.Error("fetch_tier_availability does not check $http_code !== 200; must guard against 404 and other error statuses")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Unexpected response shape returns null (not [])
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step9_BadShapeReturnsNull(t *testing.T) {
	content := checkoutPhpContent(t)

	fetchIdx := strings.Index(content, "protected static function fetch_tier_availability")
	if fetchIdx < 0 {
		t.Fatal("fetch_tier_availability function not found in class-checkout.php")
	}
	funcBody := content[fetchIdx:]

	// Must validate that $data['event']['sessions'] exists.
	if !strings.Contains(funcBody, "isset( $data['event']['sessions'] )") &&
		!strings.Contains(funcBody, `isset($data['event']['sessions'])`) &&
		!strings.Contains(funcBody, `isset( $data["event"]["sessions"] )`) {
		t.Error("fetch_tier_availability does not validate $data['event']['sessions'] shape; unexpected responses must return null")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Error extraction reads $data['error']['message'] not $data['error']
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step10_ErrorMessageExtraction(t *testing.T) {
	content := checkoutPhpContent(t)

	// Must read the nested message from the error object.
	if !strings.Contains(content, `$error_obj['message']`) &&
		!strings.Contains(content, `$error_obj["message"]`) {
		t.Error("handle_checkout_start does not extract $error_obj['message']; the Arena error envelope is {\"error\":{\"code\":\"...\",\"message\":\"...\"}} — reading $data['error'] directly returns an array, not a string")
	}

	// Must NOT directly coerce $data['error'] to a string with ?? fallback anymore.
	// The old pattern: $data['error'] ?? $data['message'] ?? ...
	// The new pattern must use is_array($error_obj) and $error_obj['message'].
	if !strings.Contains(content, "is_array( $error_obj )") &&
		!strings.Contains(content, "is_array($error_obj)") {
		t.Error("handle_checkout_start does not check is_array($error_obj); must handle both nested-object and legacy-string error shapes")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: render_tiers_shortcode handles null with visible error message
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step11_NullHandledInShortcode(t *testing.T) {
	content := checkoutPhpContent(t)

	shortcodeIdx := strings.Index(content, "public static function render_tiers_shortcode")
	if shortcodeIdx < 0 {
		t.Fatal("render_tiers_shortcode function not found in class-checkout.php")
	}
	funcBody := content[shortcodeIdx:]

	// Must check for null explicitly.
	if !strings.Contains(funcBody, "$tiers === null") {
		t.Error("render_tiers_shortcode does not check '$tiers === null'; fetch failures must be surfaced as an error message, not as an empty tier list")
	}

	// Must return some error HTML.
	if !strings.Contains(funcBody, "could not be loaded") && !strings.Contains(funcBody, "fetch-error") {
		t.Error("render_tiers_shortcode does not output an error message when $tiers is null; fetch failures must be visible to the user")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: Error message uses arena-fetch-error CSS class for targeting
// ─────────────────────────────────────────────────────────────────────────────

func TestWPCheckout375_Step12_FetchErrorCSSClass(t *testing.T) {
	content := checkoutPhpContent(t)

	if !strings.Contains(content, "arena-fetch-error") {
		t.Error("class-checkout.php does not use the 'arena-fetch-error' CSS class on the fetch-failure message; add it for testability and future styling")
	}
}
