<?php
/**
 * Plugin Name:       Arena Events
 * Plugin URI:        https://github.com/abhteam/arena_new
 * Description:       Syncs events from the Arena ticketing platform public feed API into WordPress as a custom post type.
 * Version:           0.1.0
 * Requires at least: 6.0
 * Requires PHP:      8.0
 * Author:            ABH Team
 * License:           GPL v2 or later
 * Text Domain:       arena-events
 */

defined( 'ABSPATH' ) || exit;

define( 'ARENA_EVENTS_VERSION', '0.1.0' );
define( 'ARENA_EVENTS_PLUGIN_DIR', plugin_dir_path( __FILE__ ) );
define( 'ARENA_EVENTS_PLUGIN_URL', plugin_dir_url( __FILE__ ) );

// Load sub-components.
require_once ARENA_EVENTS_PLUGIN_DIR . 'includes/class-post-type.php';
require_once ARENA_EVENTS_PLUGIN_DIR . 'includes/class-settings.php';
require_once ARENA_EVENTS_PLUGIN_DIR . 'includes/class-sync.php';
require_once ARENA_EVENTS_PLUGIN_DIR . 'includes/class-webhook.php';
require_once ARENA_EVENTS_PLUGIN_DIR . 'includes/class-checkout.php';

/**
 * Bootstrap the plugin.
 */
function arena_events_init(): void {
	Arena_Events_Post_Type::register();
	Arena_Events_Settings::init();
	Arena_Events_Sync::init();
	Arena_Events_Webhook::init();
	Arena_Events_Checkout::init();
}
add_action( 'plugins_loaded', 'arena_events_init' );

/**
 * Plugin activation: schedule the WP-Cron sync job.
 */
function arena_events_activate(): void {
	if ( ! wp_next_scheduled( 'arena_events_sync' ) ) {
		wp_schedule_event( time(), 'hourly', 'arena_events_sync' );
	}
}
register_activation_hook( __FILE__, 'arena_events_activate' );

/**
 * Plugin deactivation: remove the scheduled sync job.
 */
function arena_events_deactivate(): void {
	$timestamp = wp_next_scheduled( 'arena_events_sync' );
	if ( $timestamp ) {
		wp_unschedule_event( $timestamp, 'arena_events_sync' );
	}
}
register_deactivation_hook( __FILE__, 'arena_events_deactivate' );
