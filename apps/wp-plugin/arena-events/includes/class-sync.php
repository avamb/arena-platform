<?php
/**
 * class-sync.php — WP-Cron sync from the Arena public feed API.
 *
 * Fetches events from GET /v1/public/feeds/{feed_token}/events and
 * upserts them as arena_event posts in the local WordPress database.
 *
 * Sync strategy:
 *   - Paginated fetch (per_page=100) until all pages consumed.
 *   - Each event is identified by its `id` (UUIDv7) stored in post meta
 *     as `_arena_event_id`. On repeat runs an existing post is updated
 *     (UPDATE) rather than creating a duplicate (INSERT).
 *   - Event sessions and tier data are stored as serialised post meta
 *     (`_arena_event_sessions`).
 *   - Events with status != "published" are set to draft in WordPress.
 *
 * WP-Cron hook:  arena_events_sync
 * Frequency:     hourly (registered at plugin activation)
 * Manual trigger: wp_schedule_single_event() or WP-CLI
 *
 * @package Arena_Events
 */

defined( 'ABSPATH' ) || exit;

/**
 * Arena_Events_Sync
 *
 * Handles WP-Cron registration and execution of the event feed sync.
 */
class Arena_Events_Sync {

	/** WP-Cron action hook name. */
	const CRON_HOOK = 'arena_events_sync';

	/** Events per page when paginating the API. */
	const PER_PAGE = 100;

	/** Post meta key that stores the Arena event UUID. */
	const META_ARENA_ID = '_arena_event_id';

	/** Post meta key that stores serialised session data. */
	const META_SESSIONS = '_arena_event_sessions';

	/** Post meta key that stores the raw event start date. */
	const META_START_DATE = '_arena_event_start_date';

	/** Post meta key that stores the event city identifier. */
	const META_CITY_ID = '_arena_event_city_id';

	/**
	 * Register the WP-Cron action callback.
	 * Called via plugins_loaded → arena_events_init().
	 */
	public static function init(): void {
		add_action( self::CRON_HOOK, [ __CLASS__, 'run' ] );
	}

	/**
	 * Run the full sync.
	 *
	 * Fetches all published events from the Arena feed API (paginating as
	 * needed) and upserts them as arena_event posts.
	 *
	 * @return array{synced: int, errors: int} Summary counts.
	 */
	public static function run(): array {
		$feed_token   = Arena_Events_Settings::get_feed_token();
		$api_base_url = Arena_Events_Settings::get_api_base_url();

		if ( $feed_token === '' ) {
			error_log( '[Arena Events] Sync skipped: feed_token not configured.' );
			return [ 'synced' => 0, 'errors' => 0 ];
		}

		$synced = 0;
		$errors = 0;
		$page   = 1;

		do {
			$response = self::fetch_events_page( $api_base_url, $feed_token, $page );

			if ( is_wp_error( $response ) ) {
				error_log( '[Arena Events] API error on page ' . $page . ': ' . $response->get_error_message() );
				$errors++;
				break;
			}

			$body = wp_remote_retrieve_body( $response );
			$data = json_decode( $body, true );

			if ( ! isset( $data['events'] ) || ! is_array( $data['events'] ) ) {
				error_log( '[Arena Events] Unexpected API response on page ' . $page . ': ' . $body );
				$errors++;
				break;
			}

			foreach ( $data['events'] as $event ) {
				$result = self::upsert_event( $event );
				if ( $result ) {
					$synced++;
				} else {
					$errors++;
				}
			}

			$total_pages = (int) ( $data['total_pages'] ?? 1 );
			$page++;

		} while ( $page <= $total_pages );

		error_log( sprintf( '[Arena Events] Sync complete: %d synced, %d errors.', $synced, $errors ) );

		return [ 'synced' => $synced, 'errors' => $errors ];
	}

	/**
	 * Fetch a single page of events from the public feed API.
	 *
	 * Endpoint: GET {api_base_url}/v1/public/feeds/{feed_token}/events
	 *
	 * @param string $api_base_url  Base URL (no trailing slash).
	 * @param string $feed_token    Feed token.
	 * @param int    $page          1-based page number.
	 *
	 * @return array|WP_Error Response array or WP_Error on failure.
	 */
	public static function fetch_events_page( string $api_base_url, string $feed_token, int $page = 1 ) {
		$url = add_query_arg(
			[
				'page'     => $page,
				'per_page' => self::PER_PAGE,
			],
			trailingslashit( $api_base_url ) . 'v1/public/feeds/' . rawurlencode( $feed_token ) . '/events'
		);

		return wp_remote_get(
			$url,
			[
				'timeout' => 15,
				'headers' => [
					'Accept'     => 'application/json',
					'User-Agent' => 'Arena Events WP Plugin/' . ARENA_EVENTS_VERSION,
				],
			]
		);
	}

	/**
	 * Upsert a single event into WordPress.
	 *
	 * Searches for an existing arena_event post by `_arena_event_id` meta.
	 * If found: updates the existing post.
	 * If not found: inserts a new post.
	 *
	 * @param array $event  Event data from the API (associative array).
	 *
	 * @return int|false  Post ID on success, false on failure.
	 */
	public static function upsert_event( array $event ) {
		if ( empty( $event['id'] ) || empty( $event['name'] ) ) {
			error_log( '[Arena Events] Skipping event with missing id or name.' );
			return false;
		}

		$arena_id = sanitize_text_field( $event['id'] );
		$status   = isset( $event['status'] ) && $event['status'] === 'published' ? 'publish' : 'draft';

		// Find existing post by arena event UUID.
		$existing = self::find_post_by_arena_id( $arena_id );

		$post_data = [
			'post_type'    => 'arena_event',
			'post_title'   => sanitize_text_field( $event['name'] ),
			'post_content' => isset( $event['description'] ) ? wp_kses_post( $event['description'] ) : '',
			'post_excerpt' => isset( $event['short_description'] ) ? sanitize_text_field( $event['short_description'] ) : '',
			'post_status'  => $status,
		];

		if ( $existing ) {
			$post_data['ID'] = $existing;
			$post_id = wp_update_post( $post_data, true );
		} else {
			$post_id = wp_insert_post( $post_data, true );
		}

		if ( is_wp_error( $post_id ) ) {
			error_log( '[Arena Events] Failed to upsert event ' . $arena_id . ': ' . $post_id->get_error_message() );
			return false;
		}

		// Store meta.
		update_post_meta( $post_id, self::META_ARENA_ID, $arena_id );

		if ( ! empty( $event['sessions'] ) ) {
			update_post_meta( $post_id, self::META_SESSIONS, $event['sessions'] );
		}

		if ( ! empty( $event['start_date'] ) ) {
			update_post_meta( $post_id, self::META_START_DATE, sanitize_text_field( $event['start_date'] ) );
		}

		if ( ! empty( $event['city_id'] ) ) {
			update_post_meta( $post_id, self::META_CITY_ID, sanitize_text_field( $event['city_id'] ) );
		}

		return $post_id;
	}

	/**
	 * Find an existing arena_event post by the Arena platform event UUID.
	 *
	 * @param string $arena_id  Event UUID from the Arena platform.
	 *
	 * @return int|false  Post ID if found, false otherwise.
	 */
	public static function find_post_by_arena_id( string $arena_id ) {
		$query = new WP_Query( [
			'post_type'      => 'arena_event',
			'post_status'    => [ 'publish', 'draft', 'private' ],
			'meta_key'       => self::META_ARENA_ID,
			'meta_value'     => $arena_id,
			'posts_per_page' => 1,
			'fields'         => 'ids',
			'no_found_rows'  => true,
		] );

		if ( $query->have_posts() ) {
			return (int) $query->posts[0];
		}

		return false;
	}

	/**
	 * Manually trigger a sync run (useful from WP-CLI or admin tools).
	 *
	 * @return array{synced: int, errors: int}
	 */
	public static function trigger_manual_sync(): array {
		return self::run();
	}
}
