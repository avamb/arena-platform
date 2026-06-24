// wordpress_checkout_155_test.go — unit tests for feature #155
// (WordPress checkout integration: tier display shortcode + checkout proxy).
//
// Test coverage:
//   Step 1:  class-checkout.php file exists
//   Step 2:  Arena_Events_Checkout class is declared
//   Step 3:  [arena_event_tiers] shortcode registered via add_shortcode
//   Step 4:  REST namespace 'arena-events/v1' used
//   Step 5:  /checkout/start REST route registered
//   Step 6:  /checkout/redirect/ REST route registered
//   Step 7:  handle_checkout_start method exists
//   Step 8:  handle_checkout_redirect method exists
//   Step 9:  Checkout start proxies to /v1/public/feeds/ platform endpoint
//   Step 10: Uses wp_remote_post for checkout API call
//   Step 11: Uses wp_remote_get for tier availability fetch
//   Step 12: Uses Arena_Events_Settings::get_feed_token()
//   Step 13: Uses Arena_Events_Settings::get_api_base_url()
//   Step 14: render_tiers_shortcode method exists
//   Step 15: render_tiers_html method exists
//   Step 16: fetch_tier_availability method exists
//   Step 17: Checkout form renders tier_id + session_id hidden fields
//   Step 18: Checkout form renders qty + holder_email inputs
//   Step 19: Checkout form has checkout button (.arena-checkout-btn)
//   Step 20: Success response includes redirect_url
//   Step 21: Success response includes local_redirect_url
//   Step 22: enqueue_scripts method scoped to arena_event post type
//   Step 23: JS fetch call handles redirect_url on success
//   Step 24: Error handling when feed token not configured → 503 WP_Error
//   Step 25: permission_callback '__return_true' (public endpoint)
//   Step 26: Main plugin file includes class-checkout.php
//   Step 27: Main plugin calls Arena_Events_Checkout::init()
//   Step 28: wp_redirect called in redirect handler
//   Step 29: /v1/public/feeds/.../checkout/start is the proxied endpoint
//   Step 30: Sold-out tiers render sold-out label (no checkout form)
//
// All tests are pure file/content checks — no live WordPress required.
package httpserver

import (
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers (wpPluginRoot / readWPFile / assertContains are in
// wordpress_plugin_154_test.go in the same package).
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: class-checkout.php file exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step1_CheckoutFileExists(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	if len(content) == 0 {
		t.Error("Step1: includes/class-checkout.php exists but is empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Arena_Events_Checkout class is declared
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step2_CheckoutClassDeclared(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "class Arena_Events_Checkout", "class-checkout.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: [arena_event_tiers] shortcode registered via add_shortcode
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step3_ShortcodeRegistered(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "add_shortcode", "class-checkout.php should call add_shortcode")
	assertContains(t, content, "arena_event_tiers", "class-checkout.php should register [arena_event_tiers] shortcode")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: REST namespace 'arena-events/v1' used
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step4_RestNamespace(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "REST_NAMESPACE", "class-checkout.php should define REST_NAMESPACE")
	assertContains(t, content, "arena-events/v1", "class-checkout.php should use arena-events/v1 namespace")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: /checkout/start REST route registered
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step5_CheckoutStartRoute(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "register_rest_route", "class-checkout.php should register REST routes")
	assertContains(t, content, "/checkout/start", "class-checkout.php should register /checkout/start route")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: /checkout/redirect/ REST route registered
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step6_CheckoutRedirectRoute(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "/checkout/redirect/", "class-checkout.php should register /checkout/redirect/ route")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: handle_checkout_start method exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step7_HandleCheckoutStartExists(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "handle_checkout_start", "class-checkout.php should have handle_checkout_start method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: handle_checkout_redirect method exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step8_HandleCheckoutRedirectExists(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "handle_checkout_redirect", "class-checkout.php should have handle_checkout_redirect method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Checkout start proxies to /v1/public/feeds/ platform endpoint
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step9_ProxiesToPublicFeedsEndpoint(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "v1/public/feeds/", "class-checkout.php should proxy to v1/public/feeds/ endpoint")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Uses wp_remote_post for checkout API call
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step10_UsesWpRemotePost(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "wp_remote_post", "class-checkout.php should use wp_remote_post for checkout start")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: Uses wp_remote_get for tier availability fetch
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step11_UsesWpRemoteGet(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "wp_remote_get", "class-checkout.php should use wp_remote_get for availability fetch")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: Uses Arena_Events_Settings::get_feed_token()
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step12_UsesFeedToken(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "get_feed_token", "class-checkout.php should call Arena_Events_Settings::get_feed_token()")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 13: Uses Arena_Events_Settings::get_api_base_url()
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step13_UsesApiBaseUrl(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "get_api_base_url", "class-checkout.php should call Arena_Events_Settings::get_api_base_url()")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 14: render_tiers_shortcode method exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step14_RenderTiersShortcodeExists(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "render_tiers_shortcode", "class-checkout.php should have render_tiers_shortcode method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 15: render_tiers_html method exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step15_RenderTiersHtmlExists(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "render_tiers_html", "class-checkout.php should have render_tiers_html method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 16: fetch_tier_availability method exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step16_FetchTierAvailabilityExists(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "fetch_tier_availability", "class-checkout.php should have fetch_tier_availability method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 17: Checkout form renders tier_id + session_id hidden fields
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step17_FormHiddenFields(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, `name="tier_id"`, "class-checkout.php form should include tier_id hidden field")
	assertContains(t, content, `name="session_id"`, "class-checkout.php form should include session_id hidden field")
	assertContains(t, content, `type="hidden"`, "class-checkout.php form should use hidden input type")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 18: Checkout form renders qty + holder_email inputs
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step18_FormInputFields(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, `name="qty"`, "class-checkout.php form should include qty input")
	assertContains(t, content, `name="holder_email"`, "class-checkout.php form should include holder_email input")
	assertContains(t, content, `type="email"`, "class-checkout.php holder_email should be email type")
	assertContains(t, content, `type="number"`, "class-checkout.php qty should be number type")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 19: Checkout form has checkout button (.arena-checkout-btn)
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step19_CheckoutButton(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "arena-checkout-btn", "class-checkout.php should render .arena-checkout-btn button")
	assertContains(t, content, "arena-checkout-form", "class-checkout.php should render .arena-checkout-form")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 20: Success response includes redirect_url
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step20_ResponseIncludesRedirectUrl(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "redirect_url", "class-checkout.php response should include redirect_url")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 21: Success response includes local_redirect_url
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step21_ResponseIncludesLocalRedirectUrl(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "local_redirect_url", "class-checkout.php response should include local_redirect_url")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 22: enqueue_scripts scoped to arena_event post type
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step22_EnqueueScriptsScopedToArenaEvent(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "enqueue_scripts", "class-checkout.php should have enqueue_scripts method")
	assertContains(t, content, "is_singular", "class-checkout.php should check is_singular for arena_event")
	assertContains(t, content, "arena_event", "class-checkout.php enqueue_scripts should check for arena_event post type")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 23: JS fetch call handles redirect_url on success
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step23_JSHandlesRedirectUrl(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "redirect_url", "class-checkout.php inline JS should use redirect_url")
	assertContains(t, content, "window.location.href", "class-checkout.php inline JS should redirect via window.location.href")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 24: Error handling when feed token not configured → 503 WP_Error
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step24_ErrorHandlingNoFeedToken(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "no_feed_token", "class-checkout.php should define no_feed_token error code")
	assertContains(t, content, "WP_Error", "class-checkout.php should return WP_Error on missing feed token")
	assertContains(t, content, "503", "class-checkout.php should return 503 when feed token is missing")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 25: permission_callback '__return_true' (public endpoint)
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step25_PublicPermissionCallback(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "__return_true", "class-checkout.php checkout endpoints should be public (permission_callback => __return_true)")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 26: Main plugin file includes class-checkout.php
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step26_MainFileIncludesCheckout(t *testing.T) {
	main := readWPFile(t, "arena-events.php")
	assertContains(t, main, "class-checkout.php", "arena-events.php should require_once class-checkout.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 27: Main plugin calls Arena_Events_Checkout::init()
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step27_MainFileInitsCheckout(t *testing.T) {
	main := readWPFile(t, "arena-events.php")
	assertContains(t, main, "Arena_Events_Checkout::init()", "arena-events.php should call Arena_Events_Checkout::init()")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 28: wp_redirect called in redirect handler
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step28_WpRedirectCalled(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "wp_redirect", "class-checkout.php redirect handler should call wp_redirect")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 29: /v1/public/feeds/.../checkout/start is proxied endpoint path
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step29_CheckoutStartEndpointPath(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "/checkout/start", "class-checkout.php should proxy to /checkout/start on Arena API")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 30: Sold-out tiers render sold-out label
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressCheckout155_Step30_SoldOutTierRendering(t *testing.T) {
	content := readWPFile(t, "includes/class-checkout.php")
	assertContains(t, content, "sold-out", "class-checkout.php should render sold-out CSS class for unavailable tiers")
	assertContains(t, content, "Sold Out", "class-checkout.php should display 'Sold Out' text")
}
