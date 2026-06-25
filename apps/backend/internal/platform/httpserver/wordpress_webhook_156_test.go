// wordpress_webhook_156_test.go — unit tests for feature #156
// (WordPress webhook receiver: WP REST endpoint + platform subscriber registration).
//
// Test coverage:
//
//	Step 1:  WP webhook class file exists
//	Step 2:  WP webhook class registers REST API namespace arena-events/v1
//	Step 3:  WP webhook class registers route /webhook via register_rest_route
//	Step 4:  WP webhook class verifies X-Arena-Signature header
//	Step 5:  Signature verification uses hash_hmac with sha256
//	Step 6:  Signature verification uses hash_equals (constant-time comparison)
//	Step 7:  Webhook handler reads raw body for signature verification
//	Step 8:  Webhook handler dispatches order_paid event type
//	Step 9:  Webhook handler dispatches ticket_issued event type
//	Step 10: Webhook handler dispatches refund_succeeded event type
//	Step 11: order_paid handler updates _arena_order_paid post meta
//	Step 12: ticket_issued handler updates _arena_ticket_id post meta
//	Step 13: refund_succeeded handler updates _arena_refund_id post meta
//	Step 14: Settings class has arena_webhook_secret option
//	Step 15: Settings class has get_webhook_secret() helper
//	Step 16: Settings class has arena_email_notifications option
//	Step 17: Settings class has is_email_notifications_enabled() helper
//	Step 18: Webhook handler sends customer email when notifications enabled
//	Step 19: Main plugin file loads class-webhook.php
//	Step 20: Main plugin file calls Arena_Events_Webhook::init()
//	Step 21: Webhook handler returns 401 on invalid/missing signature
//	Step 22: Webhook handler returns 422 on missing arena_event_id
//	Step 23: WP plugin directory now contains class-webhook.php
//	Step 24: Migration file for webhook_subscribers table exists
//	Step 25: Migration creates webhook_subscribers table
//	Step 26: Migration creates signing_secret column
//	Step 27: Migration creates event_types column (TEXT[])
//	Step 28: Migration creates active column with default TRUE
//	Step 29: SQL query file for webhook_subscribers exists
//	Step 30: SQL query CreateWebhookSubscriber exists
//	Step 31: SQL query ListActiveWebhookSubscribers exists
//	Step 32: SQL query DeactivateWebhookSubscriber exists
//	Step 33: Generated Go file webhook_subscribers.sql.go exists
//	Step 34: WebhookSubscriberRow struct in generated Go code
//	Step 35: Querier interface includes CreateWebhookSubscriber
//	Step 36: Querier interface includes ListActiveWebhookSubscribers
//	Step 37: Platform handler file wp_webhooks.go exists
//	Step 38: Platform handler has handleRegisterWebhookSubscriber
//	Step 39: Platform handler has handleListWebhookSubscribers
//	Step 40: Platform handler generates crypto/rand secret (32 bytes)
//	Step 41: Platform handler response includes signing_secret field
//	Step 42: Platform handler GET response does not expose signing_secret
//	Step 43: server.go routes /v1/webhooks/subscribers
//	Step 44: server.go declares webhookSubQueries field
//	Step 45: Settings class registers webhook_secret settings field
//
// All tests are pure file/content checks — no live server or WordPress required.
package httpserver

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// wpWebhookRepoRoot returns the absolute path to the repository root, used by
// multiple helper functions in this test file.
func wpWebhookRepoRoot(t *testing.T) string {
	t.Helper()

	// Strategy 1: compile-time absolute path via runtime.Caller.
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		dir := filepath.Dir(thisFile)
		root := dir
		for i := 0; i < 5; i++ {
			root = filepath.Dir(root)
		}
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
	}

	// Strategy 2: walk upward from CWD.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("wpWebhookRepoRoot: getwd: %v", err)
	}
	dir := cwd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("wpWebhookRepoRoot: cannot locate repo root; cwd=%s", cwd)
	return ""
}

// readWPWebhookFile reads a file relative to apps/wp-plugin/arena-events/ and
// returns its content as a string.
func readWPWebhookFile(t *testing.T, relPath string) string {
	t.Helper()
	root := wpWebhookRepoRoot(t)
	full := filepath.Join(root, "apps", "wp-plugin", "arena-events", relPath)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("readWPWebhookFile(%q): %v", relPath, err)
	}
	return string(data)
}

// readBackendFile reads a file relative to apps/backend/ and returns its content.
//
// Special case: when relPath targets the trimmed
// internal/platform/httpserver/server.go (post-feature-#174 split), the helper
// returns the concatenated union of server.go + server_struct.go + wire.go +
// mount_*.go so existing structural tests that grep for symbols still pass.
func readBackendFile(t *testing.T, relPath string) string {
	t.Helper()
	root := wpWebhookRepoRoot(t)
	if relPath == "internal/platform/httpserver/server.go" {
		if combined := readServerGoLike(root, "server.go"); combined != "" {
			return combined
		}
	}
	full := filepath.Join(root, "apps", "backend", relPath)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("readBackendFile(%q): %v", relPath, err)
	}
	return string(data)
}

// assertWPContains fails the test if src does not contain substr.
func assertWPContains(t *testing.T, src, substr, label string) {
	t.Helper()
	if !strings.Contains(src, substr) {
		t.Errorf("%s: expected to contain %q but it did not", label, substr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: WP webhook class file exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step1_WebhookClassFileExists(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "Arena_Events_Webhook", "class-webhook.php should declare Arena_Events_Webhook")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: WP webhook class registers REST API namespace arena-events/v1
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step2_RESTNamespace(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "arena-events/v1", "class-webhook.php should define the REST namespace arena-events/v1")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: WP webhook class registers route /webhook via register_rest_route
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step3_RegisterRESTRoute(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "register_rest_route", "class-webhook.php should call register_rest_route")
	assertWPContains(t, content, "/webhook", "class-webhook.php should register /webhook route")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: WP webhook class verifies X-Arena-Signature header
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step4_SignatureHeaderVerification(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "X-Arena-Signature", "class-webhook.php should reference X-Arena-Signature header")
	assertWPContains(t, content, "verify_signature", "class-webhook.php should have a verify_signature method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Signature verification uses hash_hmac with sha256
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step5_HMACAlgorithm(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "hash_hmac", "class-webhook.php should use hash_hmac()")
	assertWPContains(t, content, "'sha256'", "class-webhook.php should use sha256 HMAC algorithm")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Signature verification uses hash_equals (constant-time comparison)
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step6_ConstantTimeComparison(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "hash_equals", "class-webhook.php should use hash_equals() for constant-time comparison")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: Webhook handler reads raw body for signature verification
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step7_RawBodySignatureVerification(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "get_body", "class-webhook.php should call get_body() on the request")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: Webhook handler dispatches order_paid event type
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step8_OrderPaidDispatch(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "order_paid", "class-webhook.php should handle order_paid event")
	assertWPContains(t, content, "handle_order_paid", "class-webhook.php should have handle_order_paid method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Webhook handler dispatches ticket_issued event type
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step9_TicketIssuedDispatch(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "ticket_issued", "class-webhook.php should handle ticket_issued event")
	assertWPContains(t, content, "handle_ticket_issued", "class-webhook.php should have handle_ticket_issued method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Webhook handler dispatches refund_succeeded event type
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step10_RefundSucceededDispatch(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "refund_succeeded", "class-webhook.php should handle refund_succeeded event")
	assertWPContains(t, content, "handle_refund_succeeded", "class-webhook.php should have handle_refund_succeeded method")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: order_paid handler updates _arena_order_paid post meta
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step11_OrderPaidMetaUpdate(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "_arena_order_paid", "class-webhook.php should update _arena_order_paid meta")
	assertWPContains(t, content, "update_post_meta", "class-webhook.php should call update_post_meta")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: ticket_issued handler updates _arena_ticket_id post meta
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step12_TicketIssuedMetaUpdate(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "_arena_ticket_id", "class-webhook.php should update _arena_ticket_id meta")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 13: refund_succeeded handler updates _arena_refund_id post meta
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step13_RefundSucceededMetaUpdate(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "_arena_refund_id", "class-webhook.php should update _arena_refund_id meta")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 14: Settings class has arena_webhook_secret option
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step14_SettingsWebhookSecret(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-settings.php")
	assertWPContains(t, content, "arena_webhook_secret", "class-settings.php should define arena_webhook_secret option")
	assertWPContains(t, content, "OPTION_WEBHOOK_SECRET", "class-settings.php should define OPTION_WEBHOOK_SECRET constant")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 15: Settings class has get_webhook_secret() helper
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step15_GetWebhookSecretHelper(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-settings.php")
	assertWPContains(t, content, "get_webhook_secret", "class-settings.php should expose get_webhook_secret() helper")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 16: Settings class has arena_email_notifications option
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step16_SettingsEmailNotifications(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-settings.php")
	assertWPContains(t, content, "arena_email_notifications", "class-settings.php should define arena_email_notifications option")
	assertWPContains(t, content, "OPTION_EMAIL_NOTIFICATIONS", "class-settings.php should define OPTION_EMAIL_NOTIFICATIONS constant")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 17: Settings class has is_email_notifications_enabled() helper
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step17_IsEmailNotificationsEnabled(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-settings.php")
	assertWPContains(t, content, "is_email_notifications_enabled", "class-settings.php should expose is_email_notifications_enabled() helper")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 18: Webhook handler sends customer email when notifications enabled
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step18_CustomerEmailNotification(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "send_notification_email", "class-webhook.php should have send_notification_email method")
	assertWPContains(t, content, "wp_mail", "class-webhook.php should call wp_mail() to send emails")
	assertWPContains(t, content, "is_email_notifications_enabled", "class-webhook.php should check is_email_notifications_enabled()")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 19: Main plugin file loads class-webhook.php
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step19_MainFileLoadsWebhookClass(t *testing.T) {
	content := readWPWebhookFile(t, "arena-events.php")
	assertWPContains(t, content, "class-webhook.php", "arena-events.php should require class-webhook.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 20: Main plugin file calls Arena_Events_Webhook::init()
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step20_MainFileCallsWebhookInit(t *testing.T) {
	content := readWPWebhookFile(t, "arena-events.php")
	assertWPContains(t, content, "Arena_Events_Webhook::init", "arena-events.php should call Arena_Events_Webhook::init()")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 21: Webhook handler returns 401 on invalid/missing signature
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step21_InvalidSignatureReturns401(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "401", "class-webhook.php should return 401 for invalid signatures")
	assertWPContains(t, content, "arena_webhook_signature_invalid", "class-webhook.php should return specific error code for invalid signatures")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 22: Webhook handler returns 422 on missing arena_event_id
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step22_MissingEventIDReturns422(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-webhook.php")
	assertWPContains(t, content, "422", "class-webhook.php should return 422 for missing arena_event_id")
	assertWPContains(t, content, "arena_event_id", "class-webhook.php should validate arena_event_id presence")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 23: WP plugin directory now contains class-webhook.php
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step23_DirectoryContainsWebhookClass(t *testing.T) {
	root := wpWebhookRepoRoot(t)
	webhookPath := filepath.Join(root, "apps", "wp-plugin", "arena-events", "includes", "class-webhook.php")
	if _, err := os.Stat(webhookPath); err != nil {
		t.Errorf("Step23: class-webhook.php missing at expected path: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 24: Migration file for webhook_subscribers table exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step24_MigrationFileExists(t *testing.T) {
	content := readBackendFile(t, "internal/migrations/sql/0040_webhook_subscribers.sql")
	assertWPContains(t, content, "webhook_subscribers", "0040_webhook_subscribers.sql should create webhook_subscribers table")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 25: Migration creates webhook_subscribers table
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step25_MigrationCreatesTable(t *testing.T) {
	content := readBackendFile(t, "internal/migrations/sql/0040_webhook_subscribers.sql")
	assertWPContains(t, content, "CREATE TABLE", "migration should create webhook_subscribers table")
	assertWPContains(t, content, "webhook_subscribers", "migration table must be named webhook_subscribers")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 26: Migration creates signing_secret column
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step26_MigrationSigningSecret(t *testing.T) {
	content := readBackendFile(t, "internal/migrations/sql/0040_webhook_subscribers.sql")
	assertWPContains(t, content, "signing_secret", "migration should include signing_secret column")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 27: Migration creates event_types column (TEXT[])
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step27_MigrationEventTypesArray(t *testing.T) {
	content := readBackendFile(t, "internal/migrations/sql/0040_webhook_subscribers.sql")
	assertWPContains(t, content, "event_types", "migration should include event_types column")
	assertWPContains(t, content, "TEXT[]", "migration event_types should be a TEXT array")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 28: Migration creates active column with default TRUE
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step28_MigrationActiveColumn(t *testing.T) {
	content := readBackendFile(t, "internal/migrations/sql/0040_webhook_subscribers.sql")
	assertWPContains(t, content, "active", "migration should include active column")
	assertWPContains(t, content, "DEFAULT TRUE", "migration active column should default to TRUE")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 29: SQL query file for webhook_subscribers exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step29_SQLQueryFileExists(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/queries/webhook_subscribers.sql")
	assertWPContains(t, content, "webhook_subscribers", "webhook_subscribers.sql should query webhook_subscribers table")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 30: SQL query CreateWebhookSubscriber exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step30_SQLCreateQuery(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/queries/webhook_subscribers.sql")
	assertWPContains(t, content, "CreateWebhookSubscriber", "webhook_subscribers.sql should have CreateWebhookSubscriber query")
	assertWPContains(t, content, "INSERT INTO webhook_subscribers", "webhook_subscribers.sql should INSERT into table")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 31: SQL query ListActiveWebhookSubscribers exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step31_SQLListQuery(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/queries/webhook_subscribers.sql")
	assertWPContains(t, content, "ListActiveWebhookSubscribers", "webhook_subscribers.sql should have ListActiveWebhookSubscribers query")
	assertWPContains(t, content, "active = TRUE", "ListActiveWebhookSubscribers should filter by active=TRUE")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 32: SQL query DeactivateWebhookSubscriber exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step32_SQLDeactivateQuery(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/queries/webhook_subscribers.sql")
	assertWPContains(t, content, "DeactivateWebhookSubscriber", "webhook_subscribers.sql should have DeactivateWebhookSubscriber query")
	assertWPContains(t, content, "active     = FALSE", "DeactivateWebhookSubscriber should set active=FALSE")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 33: Generated Go file webhook_subscribers.sql.go exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step33_GeneratedGoFileExists(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/gen/webhook_subscribers.sql.go")
	assertWPContains(t, content, "package gen", "webhook_subscribers.sql.go should be in gen package")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 34: WebhookSubscriberRow struct in generated Go code
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step34_WebhookSubscriberRowStruct(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/gen/webhook_subscribers.sql.go")
	assertWPContains(t, content, "WebhookSubscriberRow", "generated Go should define WebhookSubscriberRow struct")
	assertWPContains(t, content, "SigningSecret", "WebhookSubscriberRow should have SigningSecret field")
	assertWPContains(t, content, "EventTypes", "WebhookSubscriberRow should have EventTypes field")
	assertWPContains(t, content, "CallbackURL", "WebhookSubscriberRow should have CallbackURL field")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 35: Querier interface includes CreateWebhookSubscriber
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step35_QuerierCreateWebhookSubscriber(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/gen/querier.go")
	assertWPContains(t, content, "CreateWebhookSubscriber", "querier.go should include CreateWebhookSubscriber")
	assertWPContains(t, content, "WebhookSubscriberRow", "querier.go should reference WebhookSubscriberRow")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 36: Querier interface includes ListActiveWebhookSubscribers
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step36_QuerierListActiveWebhookSubscribers(t *testing.T) {
	content := readBackendFile(t, "internal/adapters/postgres/gen/querier.go")
	assertWPContains(t, content, "ListActiveWebhookSubscribers", "querier.go should include ListActiveWebhookSubscribers")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 37: Platform handler file wp_webhooks.go exists
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step37_PlatformHandlerFileExists(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/wp_webhooks.go")
	assertWPContains(t, content, "package httpserver", "wp_webhooks.go should be in httpserver package")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 38: Platform handler has handleRegisterWebhookSubscriber
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step38_HandlerRegisterSubscriber(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/wp_webhooks.go")
	assertWPContains(t, content, "handleRegisterWebhookSubscriber", "wp_webhooks.go should have handleRegisterWebhookSubscriber")
	assertWPContains(t, content, "handleListWebhookSubscribers", "wp_webhooks.go should have handleListWebhookSubscribers")
	assertWPContains(t, content, "handleDeactivateWebhookSubscriber", "wp_webhooks.go should have handleDeactivateWebhookSubscriber")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 39: Platform handler has handleListWebhookSubscribers
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step39_HandlerListSubscribers(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/wp_webhooks.go")
	assertWPContains(t, content, "handleListWebhookSubscribers", "wp_webhooks.go should have handleListWebhookSubscribers")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 40: Platform handler generates crypto/rand secret (32 bytes)
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step40_CryptoRandSecret(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/wp_webhooks.go")
	assertWPContains(t, content, "crypto/rand", "wp_webhooks.go should import crypto/rand")
	assertWPContains(t, content, "rand.Read", "wp_webhooks.go should use rand.Read for secret generation")
	assertWPContains(t, content, "make([]byte, 32)", "wp_webhooks.go should generate 32 bytes for the secret")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 41: Platform handler response includes signing_secret field
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step41_ResponseIncludesSigningSecret(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/wp_webhooks.go")
	assertWPContains(t, content, `"signing_secret"`, "registerSubscriberResponse should include signing_secret JSON field")
	assertWPContains(t, content, "SigningSecret", "response struct should have SigningSecret field")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 42: Platform handler GET response does not expose signing_secret
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step42_GETResponseNoSecret(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/wp_webhooks.go")
	// webhookSubscriberSummary (used by GET responses) should NOT have SigningSecret.
	assertWPContains(t, content, "webhookSubscriberSummary", "wp_webhooks.go should define a safe summary type (no secret)")
	// The summary type must NOT include a SigningSecret field.
	summaryIdx := strings.Index(content, "type webhookSubscriberSummary struct")
	if summaryIdx == -1 {
		t.Fatal("Step42: webhookSubscriberSummary struct not found")
	}
	// Find the closing brace of the struct.
	structContent := content[summaryIdx:]
	braceEnd := strings.Index(structContent, "}")
	if braceEnd == -1 {
		t.Fatal("Step42: could not find closing brace of webhookSubscriberSummary")
	}
	structDef := structContent[:braceEnd]
	if strings.Contains(structDef, "SigningSecret") {
		t.Error("Step42: webhookSubscriberSummary must NOT include SigningSecret field")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 43: server.go routes /v1/webhooks/subscribers
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step43_ServerRoutesMounted(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/server.go")
	assertWPContains(t, content, "/webhooks/subscribers", "server.go should mount /webhooks/subscribers routes")
	assertWPContains(t, content, "handleRegisterWebhookSubscriber", "server.go should register handleRegisterWebhookSubscriber")
	assertWPContains(t, content, "handleListWebhookSubscribers", "server.go should register handleListWebhookSubscribers")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 44: server.go declares webhookSubQueries field
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step44_ServerWebhookSubQueriesField(t *testing.T) {
	content := readBackendFile(t, "internal/platform/httpserver/server.go")
	assertWPContains(t, content, "webhookSubQueries", "server.go should declare webhookSubQueries field")
	assertWPContains(t, content, "WebhookSubQueries", "server.go Options should include WebhookSubQueries")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 45: Settings class registers webhook_secret settings field
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressWebhook156_Step45_SettingsWebhookSecretField(t *testing.T) {
	content := readWPWebhookFile(t, "includes/class-settings.php")
	assertWPContains(t, content, "render_webhook_secret_field", "class-settings.php should have render_webhook_secret_field method")
	assertWPContains(t, content, `type="password"`, "class-settings.php webhook_secret field should be a password input")
}
