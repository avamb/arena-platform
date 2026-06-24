<?php
/**
 * class-post-type.php — registers the arena_event custom post type.
 *
 * The arena_event post type stores events synced from the Arena ticketing
 * platform. Each post maps to one event from the public feed API.
 *
 * @package Arena_Events
 */

defined( 'ABSPATH' ) || exit;

/**
 * Arena_Events_Post_Type
 *
 * Handles registration of the arena_event custom post type.
 */
class Arena_Events_Post_Type {

	/**
	 * Register the arena_event custom post type.
	 * Called via plugins_loaded → arena_events_init().
	 */
	public static function register(): void {
		add_action( 'init', [ __CLASS__, 'register_post_type' ] );
	}

	/**
	 * Registers the arena_event CPT with WordPress.
	 */
	public static function register_post_type(): void {
		$labels = [
			'name'                  => __( 'Arena Events', 'arena-events' ),
			'singular_name'         => __( 'Arena Event', 'arena-events' ),
			'add_new'               => __( 'Add New', 'arena-events' ),
			'add_new_item'          => __( 'Add New Arena Event', 'arena-events' ),
			'edit_item'             => __( 'Edit Arena Event', 'arena-events' ),
			'new_item'              => __( 'New Arena Event', 'arena-events' ),
			'view_item'             => __( 'View Arena Event', 'arena-events' ),
			'view_items'            => __( 'View Arena Events', 'arena-events' ),
			'search_items'          => __( 'Search Arena Events', 'arena-events' ),
			'not_found'             => __( 'No arena events found.', 'arena-events' ),
			'not_found_in_trash'    => __( 'No arena events found in Trash.', 'arena-events' ),
			'all_items'             => __( 'All Arena Events', 'arena-events' ),
			'menu_name'             => __( 'Arena Events', 'arena-events' ),
		];

		$args = [
			'labels'             => $labels,
			'public'             => true,
			'show_ui'            => true,
			'show_in_menu'       => true,
			'show_in_rest'       => true,
			'menu_position'      => 20,
			'menu_icon'          => 'dashicons-tickets-alt',
			'capability_type'    => 'post',
			'hierarchical'       => false,
			'supports'           => [ 'title', 'editor', 'thumbnail', 'custom-fields', 'excerpt' ],
			'has_archive'        => true,
			'rewrite'            => [ 'slug' => 'arena-events', 'with_front' => false ],
			'query_var'          => true,
			'map_meta_cap'       => true,
		];

		register_post_type( 'arena_event', $args );
	}
}
