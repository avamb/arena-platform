<?php
/**
 * class-checkout.php — WordPress checkout integration for the Arena Events plugin.
 *
 * Provides:
 *   1. [arena_event_tiers] shortcode — renders event tier display with live
 *      availability and a checkout button for each available tier.
 *   2. WP REST API route: POST /wp-json/arena-events/v1/checkout/start
 *      — proxies to the Arena platform public checkout start endpoint
 *        POST /v1/public/feeds/{feed_token}/checkout/start.
 *   3. WP REST API route: GET /wp-json/arena-events/v1/checkout/redirect/{session_id}
 *      — performs wp_redirect to the hosted provider checkout URL.
 *   4. Inline JS + CSS (enqueued on arena_event singular pages) handles form
 *      submission and redirects the customer to the provider hosted checkout.
 *
 * @package Arena_Events
 */

defined( 'ABSPATH' ) || exit;

/**
 * Arena_Events_Checkout
 *
 * Handles frontend tier display, checkout proxying, and redirect to hosted checkout.
 */
class Arena_Events_Checkout {

	/** REST namespace — shared with Arena_Events_Webhook. */
	const REST_NAMESPACE = 'arena-events/v1';

	/**
	 * Bootstrap the checkout integration.
	 */
	public static function init(): void {
		add_shortcode( 'arena_event_tiers', [ __CLASS__, 'render_tiers_shortcode' ] );
		add_action( 'rest_api_init', [ __CLASS__, 'register_routes' ] );
		add_action( 'wp_enqueue_scripts', [ __CLASS__, 'enqueue_scripts' ] );
	}

	// ─────────────────────────────────────────────────────────────────────────
	// REST routes
	// ─────────────────────────────────────────────────────────────────────────

	/**
	 * Register WP REST API routes for checkout proxying and redirect.
	 */
	public static function register_routes(): void {
		// 1. Proxy checkout start to Arena API.
		register_rest_route(
			self::REST_NAMESPACE,
			'/checkout/start',
			[
				'methods'             => 'POST',
				'callback'            => [ __CLASS__, 'handle_checkout_start' ],
				'permission_callback' => '__return_true', // public endpoint — no WP auth required
			]
		);

		// 2. Redirect to hosted provider checkout URL.
		register_rest_route(
			self::REST_NAMESPACE,
			'/checkout/redirect/(?P<session_id>[a-f0-9\\-]+)',
			[
				'methods'             => 'GET',
				'callback'            => [ __CLASS__, 'handle_checkout_redirect' ],
				'permission_callback' => '__return_true',
			]
		);
	}

	// ─────────────────────────────────────────────────────────────────────────
	// Shortcode — [arena_event_tiers]
	// ─────────────────────────────────────────────────────────────────────────

	/**
	 * Render the [arena_event_tiers] shortcode.
	 *
	 * Attributes:
	 *   event_id (optional) — Arena UUIDv7 to override the current post's meta.
	 *
	 * @param array|string $atts Shortcode attributes.
	 * @return string HTML output.
	 */
	public static function render_tiers_shortcode( $atts ): string {
		$atts = shortcode_atts( [ 'event_id' => '' ], $atts, 'arena_event_tiers' );

		// Resolve arena_event_id from shortcode attribute or current post meta.
		$arena_event_id = (string) $atts['event_id'];
		if ( $arena_event_id === '' ) {
			$post = get_post();
			if ( $post ) {
				$arena_event_id = (string) get_post_meta( $post->ID, '_arena_event_id', true );
			}
		}

		if ( $arena_event_id === '' ) {
			return '<p class="arena-no-event">' . esc_html__( 'No Arena event associated with this page.', 'arena-events' ) . '</p>';
		}

		// Fetch live tier availability from the Arena public feed API.
		$tiers = self::fetch_tier_availability( $arena_event_id );

		// Retrieve stored sessions meta for supplemental data.
		$sessions = [];
		$post     = get_post();
		if ( $post ) {
			$sessions_raw = get_post_meta( $post->ID, '_arena_event_sessions', true );
			if ( $sessions_raw ) {
				$sessions = maybe_unserialize( $sessions_raw );
			}
		}

		return self::render_tiers_html( $arena_event_id, $tiers, $sessions );
	}

	// ─────────────────────────────────────────────────────────────────────────
	// Availability fetch
	// ─────────────────────────────────────────────────────────────────────────

	/**
	 * Fetch live tier availability from the Arena public feed API.
	 *
	 * Calls GET /v1/public/feeds/{feed_token}/sessions and filters sessions
	 * to those belonging to $arena_event_id, then flattens into a tier list.
	 *
	 * @param string $arena_event_id Arena UUIDv7 event identifier.
	 * @return array Flat list of tier arrays (each with a 'session_id' key injected).
	 */
	protected static function fetch_tier_availability( string $arena_event_id ): array {
		$feed_token = Arena_Events_Settings::get_feed_token();
		$api_base   = Arena_Events_Settings::get_api_base_url();

		if ( ! $feed_token ) {
			return [];
		}

		$url = $api_base . '/v1/public/feeds/' . rawurlencode( $feed_token ) . '/sessions';

		$response = wp_remote_get(
			$url,
			[
				'timeout' => 5,
				'headers' => [ 'Accept' => 'application/json' ],
			]
		);

		if ( is_wp_error( $response ) ) {
			return [];
		}

		$body = wp_remote_retrieve_body( $response );
		$data = json_decode( $body, true );

		if ( ! is_array( $data ) ) {
			return [];
		}

		// Flatten tiers from sessions that belong to this event.
		$tiers = [];
		foreach ( $data as $session ) {
			if ( isset( $session['event_id'] ) && $session['event_id'] === $arena_event_id ) {
				if ( isset( $session['tiers'] ) && is_array( $session['tiers'] ) ) {
					foreach ( $session['tiers'] as $tier ) {
						$tier['session_id'] = $session['id'] ?? '';
						$tiers[]            = $tier;
					}
				}
			}
		}

		return $tiers;
	}

	// ─────────────────────────────────────────────────────────────────────────
	// HTML rendering
	// ─────────────────────────────────────────────────────────────────────────

	/**
	 * Render the tier list HTML.
	 *
	 * Each available tier shows a checkout form with qty + holder_email inputs
	 * and a "Buy Ticket" button. Sold-out tiers display a "Sold Out" label.
	 *
	 * @param string $arena_event_id Arena event ID.
	 * @param array  $tiers          Array of tier data from the API.
	 * @param mixed  $sessions       Stored sessions meta (unused — available for extensions).
	 * @return string HTML.
	 */
	protected static function render_tiers_html( string $arena_event_id, array $tiers, $sessions ): string {
		if ( empty( $tiers ) ) {
			return '<div class="arena-tiers arena-no-tiers"><p>' .
				esc_html__( 'No ticket tiers available.', 'arena-events' ) .
				'</p></div>';
		}

		$rest_url = rest_url( self::REST_NAMESPACE . '/checkout/start' );
		$nonce    = wp_create_nonce( 'wp_rest' );

		$html  = '<div class="arena-tiers" data-event-id="' . esc_attr( $arena_event_id ) . '">';
		$html .= '<div class="arena-tiers-list">';

		foreach ( $tiers as $tier ) {
			$tier_id    = esc_attr( $tier['id'] ?? '' );
			$session_id = esc_attr( $tier['session_id'] ?? '' );
			$name       = esc_html( $tier['name'] ?? __( 'General Admission', 'arena-events' ) );
			$price      = isset( $tier['price'] ) ? number_format( (float) $tier['price'] / 100, 2 ) : '0.00';
			$currency   = esc_html( $tier['currency'] ?? 'USD' );
			$available  = (int) ( $tier['capacity_available'] ?? 0 );
			$is_sold_out = $available <= 0;

			$html .= '<div class="arena-tier">';
			$html .= '<div class="arena-tier-info">';
			$html .= '<h3 class="arena-tier-name">' . $name . '</h3>';
			$html .= '<span class="arena-tier-price">' . esc_html( $currency ) . ' ' . esc_html( $price ) . '</span>';

			if ( $is_sold_out ) {
				$html .= '<span class="arena-tier-availability sold-out">' . esc_html__( 'Sold Out', 'arena-events' ) . '</span>';
			} else {
				$html .= '<span class="arena-tier-availability">' .
					sprintf(
						// translators: %d number of tickets remaining.
						esc_html__( '%d remaining', 'arena-events' ),
						$available
					) . '</span>';
			}

			$html .= '</div>'; // arena-tier-info

			if ( ! $is_sold_out ) {
				$max_qty = min( $available, 10 );
				$html .= '<form class="arena-checkout-form"'
					. ' data-rest-url="' . esc_url( $rest_url ) . '"'
					. ' data-nonce="' . esc_attr( $nonce ) . '">';
				$html .= '<input type="hidden" name="tier_id" value="' . $tier_id . '">';
				$html .= '<input type="hidden" name="session_id" value="' . $session_id . '">';
				$html .= '<input type="number" name="qty" value="1" min="1" max="' . esc_attr( (string) $max_qty ) . '" class="arena-qty">';
				$html .= '<input type="email" name="holder_email" placeholder="' . esc_attr__( 'Your email', 'arena-events' ) . '" required class="arena-email">';
				$html .= '<button type="submit" class="arena-checkout-btn">' . esc_html__( 'Buy Ticket', 'arena-events' ) . '</button>';
				$html .= '</form>';
			}

			$html .= '</div>'; // arena-tier
		}

		$html .= '</div>'; // arena-tiers-list
		$html .= '</div>'; // arena-tiers

		return $html;
	}

	// ─────────────────────────────────────────────────────────────────────────
	// REST handler — POST /checkout/start
	// ─────────────────────────────────────────────────────────────────────────

	/**
	 * Proxy a checkout start request to the Arena platform.
	 *
	 * Flow:
	 *   1. Validate required params: tier_id, session_id, holder_email.
	 *   2. Check feed token configured → 503 if missing.
	 *   3. wp_remote_post to /v1/public/feeds/{feed_token}/checkout/start.
	 *   4. On 201: return checkout_session + redirect_url + local_redirect_url.
	 *   5. On error: propagate status code + message.
	 *
	 * @param WP_REST_Request $request Incoming REST request.
	 * @return WP_REST_Response|WP_Error
	 */
	public static function handle_checkout_start( WP_REST_Request $request ) {
		$tier_id      = sanitize_text_field( (string) ( $request->get_param( 'tier_id' ) ?? '' ) );
		$session_id   = sanitize_text_field( (string) ( $request->get_param( 'session_id' ) ?? '' ) );
		$qty          = (int) ( $request->get_param( 'qty' ) ?? 1 );
		$holder_email = sanitize_email( (string) ( $request->get_param( 'holder_email' ) ?? '' ) );
		$promo_code   = $request->get_param( 'promo_code' );

		if ( ! $tier_id || ! $session_id || ! $holder_email ) {
			return new WP_Error(
				'missing_params',
				__( 'tier_id, session_id, and holder_email are required.', 'arena-events' ),
				[ 'status' => 400 ]
			);
		}

		$feed_token = Arena_Events_Settings::get_feed_token();
		$api_base   = Arena_Events_Settings::get_api_base_url();

		if ( ! $feed_token ) {
			return new WP_Error(
				'no_feed_token',
				__( 'Arena feed token not configured.', 'arena-events' ),
				[ 'status' => 503 ]
			);
		}

		// Build request body.
		$body = [
			'tier_id'      => $tier_id,
			'session_id'   => $session_id,
			'qty'          => $qty,
			'holder_email' => $holder_email,
		];

		if ( $promo_code ) {
			$body['promo_code'] = sanitize_text_field( (string) $promo_code );
		}

		$endpoint = $api_base . '/v1/public/feeds/' . rawurlencode( $feed_token ) . '/checkout/start';

		$api_response = wp_remote_post(
			$endpoint,
			[
				'timeout' => 15,
				'headers' => [
					'Content-Type' => 'application/json',
					'Accept'       => 'application/json',
				],
				'body' => wp_json_encode( $body ),
			]
		);

		if ( is_wp_error( $api_response ) ) {
			return new WP_Error(
				'api_error',
				__( 'Could not connect to Arena platform.', 'arena-events' ),
				[ 'status' => 503 ]
			);
		}

		$status_code  = (int) wp_remote_retrieve_response_code( $api_response );
		$body_raw     = wp_remote_retrieve_body( $api_response );
		$data         = json_decode( $body_raw, true );

		if ( $status_code !== 201 ) {
			$msg = is_array( $data ) ? ( $data['error'] ?? $data['message'] ?? __( 'Checkout failed.', 'arena-events' ) ) : __( 'Checkout failed.', 'arena-events' );
			return new WP_Error( 'checkout_failed', $msg, [ 'status' => $status_code ] );
		}

		$checkout_session_id = $data['checkout_session']['id'] ?? '';
		$redirect_url        = $data['redirect_url'] ?? '';

		// Build a local redirect URL via our own REST endpoint.
		$local_redirect_url = rest_url( self::REST_NAMESPACE . '/checkout/redirect/' . $checkout_session_id );

		return new WP_REST_Response(
			[
				'checkout_session'   => $data['checkout_session'] ?? [],
				'redirect_url'       => $redirect_url,
				'local_redirect_url' => $local_redirect_url,
			],
			201
		);
	}

	// ─────────────────────────────────────────────────────────────────────────
	// REST handler — GET /checkout/redirect/{session_id}
	// ─────────────────────────────────────────────────────────────────────────

	/**
	 * Redirect the customer to the hosted provider checkout page.
	 *
	 * Builds the hosted checkout URL as {api_base}/checkout/{session_id}
	 * and calls wp_redirect().
	 *
	 * @param WP_REST_Request $request Incoming REST request.
	 */
	public static function handle_checkout_redirect( WP_REST_Request $request ): void {
		$session_id = sanitize_text_field( (string) ( $request->get_param( 'session_id' ) ?? '' ) );

		if ( ! $session_id ) {
			wp_die(
				esc_html__( 'Invalid checkout session.', 'arena-events' ),
				esc_html__( 'Arena Events', 'arena-events' ),
				[ 'response' => 400 ]
			);
		}

		$feed_token = Arena_Events_Settings::get_feed_token();
		$api_base   = Arena_Events_Settings::get_api_base_url();

		if ( ! $feed_token ) {
			wp_die(
				esc_html__( 'Arena Events: feed token not configured.', 'arena-events' ),
				esc_html__( 'Arena Events', 'arena-events' ),
				[ 'response' => 503 ]
			);
		}

		// Build hosted checkout URL: {api_base}/checkout/{session_id}
		$checkout_url = $api_base . '/checkout/' . rawurlencode( $session_id );

		wp_redirect( $checkout_url, 302 );
		exit;
	}

	// ─────────────────────────────────────────────────────────────────────────
	// Scripts + styles
	// ─────────────────────────────────────────────────────────────────────────

	/**
	 * Enqueue inline JS and CSS for the checkout form on arena_event singular pages.
	 */
	public static function enqueue_scripts(): void {
		if ( ! is_singular( 'arena_event' ) ) {
			return;
		}

		// Inline JS — handles form submission + redirect.
		wp_register_script( 'arena-checkout', false, [], ARENA_EVENTS_VERSION, true );
		wp_enqueue_script( 'arena-checkout' );
		wp_add_inline_script( 'arena-checkout', self::get_checkout_inline_js() );

		// Inline CSS — tier display styles.
		wp_register_style( 'arena-checkout', false, [], ARENA_EVENTS_VERSION );
		wp_enqueue_style( 'arena-checkout' );
		wp_add_inline_style( 'arena-checkout', self::get_checkout_inline_css() );
	}

	/**
	 * Return the inline JavaScript for form submission and redirect.
	 */
	protected static function get_checkout_inline_js(): string {
		return 'document.addEventListener("DOMContentLoaded",function(){' .
			'document.querySelectorAll(".arena-checkout-form").forEach(function(form){' .
				'form.addEventListener("submit",function(e){' .
					'e.preventDefault();' .
					'var restUrl=form.dataset.restUrl,nonce=form.dataset.nonce,data={};' .
					'new FormData(form).forEach(function(v,k){data[k]=k==="qty"?parseInt(v,10):v;});' .
					'var btn=form.querySelector(".arena-checkout-btn");' .
					'btn.disabled=true;btn.textContent="Processing...";' .
					'fetch(restUrl,{method:"POST",headers:{"Content-Type":"application/json","X-WP-Nonce":nonce},body:JSON.stringify(data)})' .
					'.then(function(r){return r.json().then(function(d){return{ok:r.ok,data:d};});})' .
					'.then(function(res){' .
						'if(res.ok&&res.data.redirect_url){window.location.href=res.data.redirect_url;}' .
						'else{var msg=res.data.message||"Checkout failed. Please try again.";' .
						'form.insertAdjacentHTML("beforeend","<p class=\\"arena-error\\">"+msg+"</p>");' .
						'btn.disabled=false;btn.textContent="Buy Ticket";}' .
					'})' .
					'.catch(function(){' .
						'form.insertAdjacentHTML("beforeend","<p class=\\"arena-error\\">Network error. Please try again.</p>");' .
						'btn.disabled=false;btn.textContent="Buy Ticket";' .
					'});' .
				'});' .
			'});' .
		'});';
	}

	/**
	 * Return the inline CSS for tier display.
	 */
	protected static function get_checkout_inline_css(): string {
		return '.arena-tiers{margin:1.5em 0}' .
			'.arena-tier{border:1px solid #e0e0e0;padding:1em;margin:.75em 0;border-radius:4px}' .
			'.arena-tier-name{margin:0 0 .25em}' .
			'.arena-tier-price{font-weight:bold}' .
			'.arena-tier-availability{color:#666;font-size:.9em;margin-left:.5em}' .
			'.arena-tier-availability.sold-out{color:#c00}' .
			'.arena-checkout-form{margin-top:.75em;display:flex;flex-wrap:wrap;gap:.5em;align-items:center}' .
			'.arena-qty{width:60px}' .
			'.arena-email{flex:1;min-width:200px}' .
			'.arena-checkout-btn{cursor:pointer}' .
			'.arena-error{color:#c00;margin:.5em 0}';
	}
}
