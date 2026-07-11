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

	const WIDGET_VERSION = '1';
	const CDN_BASE_DEFAULT = 'https://cdn.jsdelivr.net/gh/avamb/arena-platform@master/apps/widget/dist/v1';

	/**
	 * Register the shortcode and Gutenberg block.
	 */
	public static function init(): void {
		add_shortcode( 'arena_tickets', [ __CLASS__, 'render_shortcode' ] );
		add_action( 'init', [ __CLASS__, 'register_block' ] );
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
		}
		wp_enqueue_script( $handle );
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
