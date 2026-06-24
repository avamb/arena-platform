<?php
/**
 * class-settings.php — admin settings page for the Arena Events plugin.
 *
 * Provides an admin settings page where administrators configure:
 *   - feed_token:    The public API feed token issued by the Arena platform
 *   - api_base_url:  The Arena API base URL (default: https://api.arena.abhteam.com)
 *
 * Options are stored using the WordPress Options API:
 *   - arena_feed_token     → wp_options.option_value
 *   - arena_api_base_url   → wp_options.option_value
 *
 * @package Arena_Events
 */

defined( 'ABSPATH' ) || exit;

/**
 * Arena_Events_Settings
 *
 * Registers the plugin settings page and settings fields.
 */
class Arena_Events_Settings {

	/** Option names. */
	const OPTION_FEED_TOKEN    = 'arena_feed_token';
	const OPTION_API_BASE_URL  = 'arena_api_base_url';

	/** Default API base URL. */
	const DEFAULT_API_BASE_URL = 'https://api.arena.abhteam.com';

	/** Settings group name (used in settings_fields()). */
	const SETTINGS_GROUP = 'arena_events_settings';

	/** Settings page slug. */
	const PAGE_SLUG = 'arena-events-settings';

	/**
	 * Hook into WordPress admin to register the settings page.
	 */
	public static function init(): void {
		add_action( 'admin_menu', [ __CLASS__, 'add_settings_page' ] );
		add_action( 'admin_init', [ __CLASS__, 'register_settings' ] );
	}

	/**
	 * Register the settings page under Settings menu.
	 */
	public static function add_settings_page(): void {
		add_options_page(
			__( 'Arena Events Settings', 'arena-events' ),
			__( 'Arena Events', 'arena-events' ),
			'manage_options',
			self::PAGE_SLUG,
			[ __CLASS__, 'render_settings_page' ]
		);
	}

	/**
	 * Register settings, sections, and fields with the Settings API.
	 */
	public static function register_settings(): void {
		// Register the two options.
		register_setting(
			self::SETTINGS_GROUP,
			self::OPTION_FEED_TOKEN,
			[
				'type'              => 'string',
				'sanitize_callback' => 'sanitize_text_field',
				'default'           => '',
			]
		);

		register_setting(
			self::SETTINGS_GROUP,
			self::OPTION_API_BASE_URL,
			[
				'type'              => 'string',
				'sanitize_callback' => 'esc_url_raw',
				'default'           => self::DEFAULT_API_BASE_URL,
			]
		);

		// Add a settings section.
		add_settings_section(
			'arena_events_api_section',
			__( 'API Connection', 'arena-events' ),
			[ __CLASS__, 'render_api_section_description' ],
			self::PAGE_SLUG
		);

		// Feed token field.
		add_settings_field(
			self::OPTION_FEED_TOKEN,
			__( 'Feed Token', 'arena-events' ),
			[ __CLASS__, 'render_feed_token_field' ],
			self::PAGE_SLUG,
			'arena_events_api_section'
		);

		// API base URL field.
		add_settings_field(
			self::OPTION_API_BASE_URL,
			__( 'API Base URL', 'arena-events' ),
			[ __CLASS__, 'render_api_base_url_field' ],
			self::PAGE_SLUG,
			'arena_events_api_section'
		);
	}

	/**
	 * Render the API section description.
	 */
	public static function render_api_section_description(): void {
		echo '<p>' . esc_html__(
			'Enter the credentials provided by the Arena ticketing platform to connect this site to your event feed.',
			'arena-events'
		) . '</p>';
	}

	/**
	 * Render the feed_token input field.
	 */
	public static function render_feed_token_field(): void {
		$value = get_option( self::OPTION_FEED_TOKEN, '' );
		?>
		<input
			type="text"
			id="<?php echo esc_attr( self::OPTION_FEED_TOKEN ); ?>"
			name="<?php echo esc_attr( self::OPTION_FEED_TOKEN ); ?>"
			value="<?php echo esc_attr( $value ); ?>"
			class="regular-text"
			placeholder="<?php esc_attr_e( 'e.g. ft_xxxxxxxxxxxxxxxx', 'arena-events' ); ?>"
			autocomplete="off"
		/>
		<p class="description">
			<?php esc_html_e( 'The public feed token issued by the Arena platform for this site. Required for event sync.', 'arena-events' ); ?>
		</p>
		<?php
	}

	/**
	 * Render the api_base_url input field.
	 */
	public static function render_api_base_url_field(): void {
		$value = get_option( self::OPTION_API_BASE_URL, self::DEFAULT_API_BASE_URL );
		?>
		<input
			type="url"
			id="<?php echo esc_attr( self::OPTION_API_BASE_URL ); ?>"
			name="<?php echo esc_attr( self::OPTION_API_BASE_URL ); ?>"
			value="<?php echo esc_attr( $value ); ?>"
			class="regular-text"
			placeholder="<?php echo esc_attr( self::DEFAULT_API_BASE_URL ); ?>"
		/>
		<p class="description">
			<?php esc_html_e( 'The Arena API base URL. Leave as default unless you are using a custom deployment.', 'arena-events' ); ?>
		</p>
		<?php
	}

	/**
	 * Render the full settings page HTML.
	 */
	public static function render_settings_page(): void {
		if ( ! current_user_can( 'manage_options' ) ) {
			return;
		}
		?>
		<div class="wrap">
			<h1><?php esc_html_e( 'Arena Events Settings', 'arena-events' ); ?></h1>
			<form method="post" action="options.php">
				<?php
				settings_fields( self::SETTINGS_GROUP );
				do_settings_sections( self::PAGE_SLUG );
				submit_button( __( 'Save Settings', 'arena-events' ) );
				?>
			</form>
		</div>
		<?php
	}

	/**
	 * Helper: retrieve the configured feed token.
	 *
	 * @return string Feed token or empty string if not configured.
	 */
	public static function get_feed_token(): string {
		return (string) get_option( self::OPTION_FEED_TOKEN, '' );
	}

	/**
	 * Helper: retrieve the configured API base URL.
	 *
	 * @return string API base URL (never empty — falls back to DEFAULT_API_BASE_URL).
	 */
	public static function get_api_base_url(): string {
		$url = (string) get_option( self::OPTION_API_BASE_URL, '' );
		return $url !== '' ? rtrim( $url, '/' ) : rtrim( self::DEFAULT_API_BASE_URL, '/' );
	}
}
