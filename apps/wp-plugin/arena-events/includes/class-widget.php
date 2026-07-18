<?php
/**
 * Arena Tickets Widget
 *
 * Provides the [arena_tickets] shortcode and a Gutenberg block
 * (arena-events/arena-tickets) for embedding the Arena Tickets widget
 * on WordPress sites.
 *
 * The widget JS is loaded from a CDN (default: jsDelivr pointing at the
 * GH repo) or from a custom CDN base URL configured in plugin settings.
 *
 * @package Arena_Events
 */

defined( 'ABSPATH' ) || exit;

class Arena_Events_Widget {

	/**
	 * Pinned widget release tag (PR2-22).
	 *
	 * Update this constant whenever a new widget build is released.
	 * The CDN URL is constructed as:
	 *   https://cdn.jsdelivr.net/gh/{repo}@{WIDGET_GIT_TAG}/apps/widget/dist/v1
	 *
	 * IMPORTANT: Always update WIDGET_DIST_SRI alongside this constant so the
	 * browser can verify asset integrity.  Compute the new hash with:
	 *   curl -s <url> | openssl dgst -sha384 -binary | openssl base64 -A
	 * then prefix with "sha384-".
	 */
	const WIDGET_VERSION     = '0.1.0';
	const WIDGET_GIT_TAG     = 'v0.1.0';

	/**
	 * SRI hash for the pinned widget JS (sha384).
	 *
	 * Empty string disables integrity enforcement (development / self-hosted).
	 * Set this to the computed sha384 hash when deploying to production with
	 * the jsDelivr CDN.  The plugin settings page allows administrators to
	 * override this value for staged rollouts.
	 */
	const WIDGET_DIST_SRI    = '';

	const CDN_BASE_DEFAULT   = 'https://cdn.jsdelivr.net/gh/avamb/arena-platform@v0.1.0/apps/widget/dist/v1';

	/**
	 * Register the shortcode, Gutenberg block, and SRI filter.
	 */
	public static function init(): void {
		add_shortcode( 'arena_tickets', [ __CLASS__, 'render_shortcode' ] );
		add_action( 'init', [ __CLASS__, 'register_block' ] );
		// Inject integrity + crossorigin attributes when an SRI hash is configured.
		add_filter( 'script_loader_tag', [ __CLASS__, 'add_sri_attributes' ], 10, 3 );
	}

	/**
	 * Render the [arena_tickets] shortcode.
	 *
	 * @param array  $atts    Shortcode attributes.
	 * @param string $content Enclosed content (unused).
	 * @return string HTML output.
	 */
	public static function render_shortcode( $atts, $content = '' ): string {
		$a = shortcode_atts(
			[
				'feed_token' => '',
				'session_id' => '',
				'locale'     => 'en',
				'cdn_base'   => '',
			],
			$atts,
			'arena_tickets'
		);

		$cdn = trim( $a['cdn_base'] ) !== '' ? esc_url( trim( $a['cdn_base'] ) ) : self::cdn_base();
		self::enqueue_widget_script( $cdn );

		$html = '<arena-tickets';
		if ( $a['feed_token'] ) {
			$html .= ' feed-token="' . esc_attr( $a['feed_token'] ) . '"';
		}
		if ( $a['session_id'] ) {
			$html .= ' session-id="' . esc_attr( $a['session_id'] ) . '"';
		}
		$html .= ' locale="' . esc_attr( $a['locale'] ) . '"';
		$html .= '></arena-tickets>';

		return $html;
	}

	/**
	 * Return the effective CDN base URL (from settings or default).
	 */
	private static function cdn_base(): string {
		$opt  = get_option( 'arena_events_settings', [] );
		$base = $opt['widget_cdn_base'] ?? '';
		return $base !== '' ? esc_url( $base ) : self::CDN_BASE_DEFAULT;
	}

	/**
	 * Enqueue the widget JS from CDN (once per page).
	 */
	private static function enqueue_widget_script( string $cdn ): void {
		$handle = 'arena-tickets-widget';
		if ( ! wp_script_is( $handle, 'registered' ) ) {
			$url = rtrim( $cdn, '/' ) . '/arena-tickets.js';
			wp_register_script(
				$handle,
				$url,
				[],
				self::WIDGET_VERSION,
				[
					'strategy'  => 'defer',
					'in_footer' => true,
				]
			);
			// Mark as ES module so browsers load it correctly.
			wp_script_add_data( $handle, 'type', 'module' );

			// Store the resolved SRI hash for use in the script_loader_tag filter.
			$sri = self::resolved_sri();
			if ( $sri !== '' ) {
				wp_script_add_data( $handle, 'arena_sri', $sri );
			}
		}
		wp_enqueue_script( $handle );
	}

	/**
	 * Add integrity and crossorigin attributes to the widget script tag (PR2-22).
	 *
	 * Hooked to `script_loader_tag`.  Only modifies the arena-tickets-widget handle.
	 *
	 * @param string $tag    The <script> HTML tag.
	 * @param string $handle The registered script handle.
	 * @param string $src    The script src URL (unused — tag already contains it).
	 * @return string Modified (or unchanged) tag.
	 */
	public static function add_sri_attributes( string $tag, string $handle, string $src ): string {
		if ( $handle !== 'arena-tickets-widget' ) {
			return $tag;
		}
		$sri = self::resolved_sri();
		if ( $sri === '' ) {
			return $tag;
		}
		// Insert integrity and crossorigin before the closing > of the opening tag.
		// WordPress 6.x renders <script ... src="..."></script>; we inject before src=.
		$tag = str_replace(
			' src=',
			' integrity="' . esc_attr( $sri ) . '" crossorigin="anonymous" src=',
			$tag
		);
		return $tag;
	}

	/**
	 * Return the effective SRI hash: settings override → compile-time constant.
	 *
	 * An empty string means integrity checking is disabled (dev / self-hosted).
	 */
	private static function resolved_sri(): string {
		$opt = get_option( 'arena_events_settings', [] );
		$override = $opt['widget_dist_sri'] ?? '';
		if ( $override !== '' ) {
			return $override;
		}
		return self::WIDGET_DIST_SRI;
	}

	/**
	 * Register the arena-events/arena-tickets Gutenberg block.
	 *
	 * The block definition lives in blocks/arena-tickets/block.json.
	 * Server-side rendering delegates to render_shortcode().
	 */
	public static function register_block(): void {
		if ( ! function_exists( 'register_block_type' ) ) {
			return;
		}
		$block_dir = ARENA_EVENTS_PLUGIN_DIR . 'blocks/arena-tickets';
		if ( file_exists( $block_dir . '/block.json' ) ) {
			register_block_type(
				$block_dir,
				[
					'render_callback' => [ __CLASS__, 'render_shortcode' ],
				]
			);
		}
	}
}
