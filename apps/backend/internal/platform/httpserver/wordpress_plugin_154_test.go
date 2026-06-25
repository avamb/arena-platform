// wordpress_plugin_154_test.go — unit tests for feature #154
// (WordPress plugin core: custom post type + feed config).
//
// Test coverage:
//
//	Step 1: Plugin main file exists with correct WordPress plugin header
//	Step 2: Plugin declares arena_event custom post type
//	Step 3: Plugin includes class-post-type.php with register_post_type call
//	Step 4: Settings page class exists with arena_feed_token option
//	Step 5: Settings page class has arena_api_base_url option
//	Step 6: Settings page uses WordPress Settings API (register_setting, add_settings_section)
//	Step 7: Settings page renders a feed_token input field
//	Step 8: Settings page renders an api_base_url input field
//	Step 9: Sync class exists with WP-Cron hook registration
//	Step 10: Sync class implements fetch_events_page with correct API path
//	Step 11: Sync class calls /v1/public/feeds/{feed_token}/events endpoint
//	Step 12: Sync class implements upsert_event (insert + update logic)
//	Step 13: Sync class stores _arena_event_id meta key
//	Step 14: Sync class stores _arena_event_sessions meta key
//	Step 15: Plugin activation registers the arena_events_sync cron hook
//	Step 16: Plugin deactivation unschedules the cron hook
//	Step 17: Main plugin file loads all three include classes
//	Step 18: Plugin uses plugins_loaded hook to initialise components
//	Step 19: Settings class provides get_feed_token() helper
//	Step 20: Settings class provides get_api_base_url() helper with fallback
//	Step 21: Sync class run() returns synced+errors summary
//	Step 22: Post type registered with supports title, editor, thumbnail, custom-fields
//	Step 23: Post type uses dashicons-tickets-alt icon
//	Step 24: Post type is public and show_in_rest = true
//	Step 25: Plugin directory exists at apps/wp-plugin/arena-events/
//
// All tests are pure file/content checks — no live WordPress required.
package httpserver

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helper: locate the WP plugin directory relative to the repo root.
// ─────────────────────────────────────────────────────────────────────────────

// wpPluginRoot returns the absolute path to apps/wp-plugin/arena-events/.
func wpPluginRoot(t *testing.T) string {
	t.Helper()

	// Strategy 1: runtime.Caller (absolute path in normal test runs).
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		// thisFile = .../apps/backend/internal/platform/httpserver/wordpress_plugin_154_test.go
		// Navigate up 5 levels to reach the repo root.
		dir := filepath.Dir(thisFile)
		repoRoot := dir
		for i := 0; i < 5; i++ {
			repoRoot = filepath.Dir(repoRoot)
		}
		candidate := filepath.Join(repoRoot, "apps", "wp-plugin", "arena-events")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Strategy 2: walk upward from CWD looking for go.mod (Docker / -trimpath).
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("wpPluginRoot: cannot determine working directory: %v", err)
	}
	dir := cwd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			candidate := filepath.Join(dir, "apps", "wp-plugin", "arena-events")
			if _, err2 := os.Stat(candidate); err2 == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("wpPluginRoot: cannot locate apps/wp-plugin/arena-events; cwd=%s", cwd)
	return ""
}

// readWPFile reads a file inside the WP plugin directory and returns its content.
func readWPFile(t *testing.T, relPath string) string {
	t.Helper()
	root := wpPluginRoot(t)
	full := filepath.Join(root, relPath)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("readWPFile(%q): %v", relPath, err)
	}
	return string(data)
}

// assertContains fails the test if src does not contain substr.
func assertContains(t *testing.T, src, substr, label string) {
	t.Helper()
	if !strings.Contains(src, substr) {
		t.Errorf("%s: expected to contain %q but it did not", label, substr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Plugin main file exists with correct WordPress plugin header
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step1_MainFileExists(t *testing.T) {
	content := readWPFile(t, "arena-events.php")
	assertContains(t, content, "Plugin Name:", "arena-events.php")
	assertContains(t, content, "Arena Events", "arena-events.php plugin name")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Plugin declares arena_event custom post type
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step2_ArenaEventPostTypeDeclared(t *testing.T) {
	content := readWPFile(t, "includes/class-post-type.php")
	assertContains(t, content, "arena_event", "class-post-type.php")
	assertContains(t, content, "register_post_type", "class-post-type.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Plugin includes class-post-type.php with register_post_type call
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step3_PostTypeFileIncluded(t *testing.T) {
	main := readWPFile(t, "arena-events.php")
	assertContains(t, main, "class-post-type.php", "arena-events.php should require class-post-type.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Settings page class exists with arena_feed_token option
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step4_SettingsClassHasFeedToken(t *testing.T) {
	content := readWPFile(t, "includes/class-settings.php")
	assertContains(t, content, "arena_feed_token", "class-settings.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Settings page class has arena_api_base_url option
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step5_SettingsClassHasApiBaseUrl(t *testing.T) {
	content := readWPFile(t, "includes/class-settings.php")
	assertContains(t, content, "arena_api_base_url", "class-settings.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Settings page uses WordPress Settings API
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step6_SettingsAPIUsed(t *testing.T) {
	content := readWPFile(t, "includes/class-settings.php")
	assertContains(t, content, "register_setting", "class-settings.php")
	assertContains(t, content, "add_settings_section", "class-settings.php")
	assertContains(t, content, "add_settings_field", "class-settings.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: Settings page renders a feed_token input field
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step7_FeedTokenInputRendered(t *testing.T) {
	content := readWPFile(t, "includes/class-settings.php")
	assertContains(t, content, "render_feed_token_field", "class-settings.php should have a render_feed_token_field method")
	assertContains(t, content, `type="text"`, "class-settings.php feed_token field should be a text input")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: Settings page renders an api_base_url input field
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step8_ApiBaseUrlInputRendered(t *testing.T) {
	content := readWPFile(t, "includes/class-settings.php")
	assertContains(t, content, "render_api_base_url_field", "class-settings.php should have a render_api_base_url_field method")
	assertContains(t, content, `type="url"`, "class-settings.php api_base_url field should be a url input")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Sync class exists with WP-Cron hook registration
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step9_SyncClassExists(t *testing.T) {
	content := readWPFile(t, "includes/class-sync.php")
	assertContains(t, content, "Arena_Events_Sync", "class-sync.php should declare Arena_Events_Sync class")
	assertContains(t, content, "arena_events_sync", "class-sync.php should reference the cron hook name")
	assertContains(t, content, "add_action", "class-sync.php should register via add_action")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Sync class implements fetch_events_page with correct API path
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step10_FetchEventsPageExists(t *testing.T) {
	content := readWPFile(t, "includes/class-sync.php")
	assertContains(t, content, "fetch_events_page", "class-sync.php should have fetch_events_page method")
	assertContains(t, content, "wp_remote_get", "class-sync.php should use wp_remote_get")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: Sync class calls /v1/public/feeds/{feed_token}/events endpoint
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step11_PublicFeedAPIEndpointUsed(t *testing.T) {
	content := readWPFile(t, "includes/class-sync.php")
	assertContains(t, content, "v1/public/feeds/", "class-sync.php should call the public feed API path")
	assertContains(t, content, "/events", "class-sync.php should target the /events endpoint")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: Sync class implements upsert_event (insert + update logic)
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step12_UpsertEventLogic(t *testing.T) {
	content := readWPFile(t, "includes/class-sync.php")
	assertContains(t, content, "upsert_event", "class-sync.php should have upsert_event method")
	assertContains(t, content, "wp_insert_post", "class-sync.php should call wp_insert_post")
	assertContains(t, content, "wp_update_post", "class-sync.php should call wp_update_post")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 13: Sync class stores _arena_event_id meta key
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step13_ArenaEventIdMetaKey(t *testing.T) {
	content := readWPFile(t, "includes/class-sync.php")
	assertContains(t, content, "_arena_event_id", "class-sync.php should store _arena_event_id post meta")
	assertContains(t, content, "update_post_meta", "class-sync.php should call update_post_meta")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 14: Sync class stores _arena_event_sessions meta key
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step14_ArenaEventSessionsMeta(t *testing.T) {
	content := readWPFile(t, "includes/class-sync.php")
	assertContains(t, content, "_arena_event_sessions", "class-sync.php should store _arena_event_sessions meta")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 15: Plugin activation registers the arena_events_sync cron hook
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step15_ActivationSchedulesCron(t *testing.T) {
	main := readWPFile(t, "arena-events.php")
	assertContains(t, main, "register_activation_hook", "arena-events.php should register activation hook")
	assertContains(t, main, "wp_schedule_event", "activation should schedule the cron job")
	assertContains(t, main, "arena_events_sync", "activation should use the arena_events_sync hook name")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 16: Plugin deactivation unschedules the cron hook
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step16_DeactivationClearsCron(t *testing.T) {
	main := readWPFile(t, "arena-events.php")
	assertContains(t, main, "register_deactivation_hook", "arena-events.php should register deactivation hook")
	assertContains(t, main, "wp_unschedule_event", "deactivation should unschedule the cron job")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 17: Main plugin file loads all three include classes
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step17_AllIncludesLoaded(t *testing.T) {
	main := readWPFile(t, "arena-events.php")
	assertContains(t, main, "class-post-type.php", "arena-events.php should require class-post-type.php")
	assertContains(t, main, "class-settings.php", "arena-events.php should require class-settings.php")
	assertContains(t, main, "class-sync.php", "arena-events.php should require class-sync.php")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 18: Plugin uses plugins_loaded hook to initialise components
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step18_PluginsLoadedHook(t *testing.T) {
	main := readWPFile(t, "arena-events.php")
	assertContains(t, main, "plugins_loaded", "arena-events.php should hook into plugins_loaded")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 19: Settings class provides get_feed_token() helper
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step19_GetFeedTokenHelper(t *testing.T) {
	content := readWPFile(t, "includes/class-settings.php")
	assertContains(t, content, "get_feed_token", "class-settings.php should expose get_feed_token() helper")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 20: Settings class provides get_api_base_url() helper with fallback
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step20_GetApiBaseUrlHelper(t *testing.T) {
	content := readWPFile(t, "includes/class-settings.php")
	assertContains(t, content, "get_api_base_url", "class-settings.php should expose get_api_base_url() helper")
	assertContains(t, content, "DEFAULT_API_BASE_URL", "class-settings.php should define a default API URL constant")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 21: Sync class run() returns synced+errors summary
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step21_SyncRunReturnsSummary(t *testing.T) {
	content := readWPFile(t, "includes/class-sync.php")
	assertContains(t, content, "synced", "class-sync.php run() should track synced count")
	assertContains(t, content, "errors", "class-sync.php run() should track errors count")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 22: Post type registered with required supports
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step22_PostTypeSupports(t *testing.T) {
	content := readWPFile(t, "includes/class-post-type.php")
	assertContains(t, content, "'title'", "arena_event CPT should support title")
	assertContains(t, content, "'editor'", "arena_event CPT should support editor")
	assertContains(t, content, "'thumbnail'", "arena_event CPT should support thumbnail")
	assertContains(t, content, "'custom-fields'", "arena_event CPT should support custom-fields")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 23: Post type uses dashicons-tickets-alt icon
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step23_PostTypeMenuIcon(t *testing.T) {
	content := readWPFile(t, "includes/class-post-type.php")
	assertContains(t, content, "dashicons-tickets-alt", "arena_event CPT should use tickets icon")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 24: Post type is public and show_in_rest = true
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step24_PostTypePublicRest(t *testing.T) {
	content := readWPFile(t, "includes/class-post-type.php")
	assertContains(t, content, "'public'", "arena_event CPT should declare public")
	assertContains(t, content, "show_in_rest", "arena_event CPT should be available in REST API")
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 25: Plugin directory structure is valid
// ─────────────────────────────────────────────────────────────────────────────

func TestWordPressPlugin154_Step25_DirectoryStructure(t *testing.T) {
	root := wpPluginRoot(t)

	requiredFiles := []string{
		"arena-events.php",
		filepath.Join("includes", "class-post-type.php"),
		filepath.Join("includes", "class-settings.php"),
		filepath.Join("includes", "class-sync.php"),
	}

	for _, rel := range requiredFiles {
		full := filepath.Join(root, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("Step25: required plugin file missing: %s (err: %v)", rel, err)
		}
	}
}
