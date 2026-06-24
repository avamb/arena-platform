<?php
/**
 * class-webhook.php — WordPress REST API webhook receiver for Arena platform events.
 *
 * Registers a WP REST API endpoint that receives signed outbox events from the
 * Arena ticketing platform and updates local arena_event posts accordingly.
 *
 * Endpoint:    POST /wp-json/arena-events/v1/webhook
 * Auth:        HMAC-SHA256 signature check (X-Arena-Signature: sha256=<hex>)
 * Events:      order_paid, ticket_issued, refund_succeeded
 *
 * Signature verification:
 *   The platform signs every delivery with:
 *     X-Arena-Signature: sha256=<hex(HMAC-SHA256(secret, body))>
 *   This class verifies the signature using the secret stored in
 *   arena_webhook_secret (set via the admin settings page or returned by
 *   the platform's POST /v1/webhooks/subscribers registration endpoint).
 *
 * Local state update strategy:
 *   - order_paid:         Find the arena_event post matching the event's arena_event_id
 *                         and set _arena_order_paid meta; optionally send customer email.
 *   - ticket_issued:      Store ticket meta (_arena_ticket_id, _arena_ticket_code) on
 *                         the post; optionally send customer ticket email.
 *   - refund_succeeded:   Store refund meta (_arena_refund_id) on the post; optionally
 *                         send customer refund notification email.
 *
 * Customer email notifications:
 *   Sent only when arena_email_notifications option is enabled in plugin settings.
 *
 * @package Arena_Events
 */

defined( 'ABSPATH' ) || exit;

/**
 * Arena_Events_Webhook
 *
 * Handles registration of the WP REST webhook endpoint and all incoming
 * Arena platform event payloads.
 */
class Arena_Events_Webhook {

	/** WP REST API namespace. */
	const REST_NAMESPACE = 'arena-events/v1';

	/** WP REST API route. */
	const REST_ROUTE = '/webhook';

	/** Header carrying the HMAC-SHA256 signature from the platform. */
	const SIGNATURE_HEADER = 'X-Arena-Signature';

	/** HMAC signature prefix (matches the platform's ComputeHMAC output). */
	const SIGNATURE_PREFIX = 'sha256=';

	/** Supported incoming event types. */
	const EVENT_ORDER_PAID        = 'order_paid';
	const EVENT_TICKET_ISSUED     = 'ticket_issued';
	const EVENT_REFUND_SUCCEEDED  = 'refund_succeeded';

	/** Post meta keys used to store platform-side identifiers. */
	const META_ORDER_PAID    = '_arena_order_paid';
	const META_TICKET_ID     = '_arena_ticket_id';
	const META_TICKET_CODE   = '_arena_ticket_code';
	const META_REFUND_ID     = '_arena_refund_id';

	/**
	 * Register the WP REST API endpoint via rest_api_init hook.
	 * Called via plugins_loaded → arena_events_init().
	 */
	public static function init(): void {
		add_action( 'rest_api_init', [ __CLASS__, 'register_routes' ] );
	}

	/**
	 * Register the REST route with WordPress.
	 *
	 * Route: POST /wp-json/arena-events/v1/webhook
	 */
	public static function register_routes(): void {
		register_rest_route(
			self::REST_NAMESPACE,
			self::REST_ROUTE,
			[
				'methods'             => WP_REST_Server::CREATABLE,
				'callback'            => [ __CLASS__, 'handle_webhook' ],
				'permission_callback' => '__return_true', // auth is done via HMAC signature
			]
		);
	}

	/**
	 * Main webhook handler.
	 *
	 * Reads the raw request body, verifies the HMAC-SHA256 signature,
	 * parses the JSON payload, and dispatches to the event-specific handler.
	 *
	 * @param WP_REST_Request $request Incoming REST request.
	 * @return WP_REST_Response|WP_Error
	 */
	public static function handle_webhook( WP_REST_Request $request ) {
		// Read raw body for signature verification (must use raw body, not parsed params).
		$raw_body = $request->get_body();

		// Verify HMAC-SHA256 signature.
		$sig_header = $request->get_header( 'x_arena_signature' );
		if ( ! self::verify_signature( $raw_body, $sig_header ) ) {
			error_log( '[Arena Events] Webhook: invalid or missing signature.' );
			return new WP_Error(
				'arena_webhook_signature_invalid',
				'Invalid webhook signature.',
				[ 'status' => 401 ]
			);
		}

		// Parse JSON payload.
		$payload = json_decode( $raw_body, true );
		if ( ! is_array( $payload ) ) {
			error_log( '[Arena Events] Webhook: invalid JSON payload.' );
			return new WP_Error(
				'arena_webhook_bad_payload',
				'Invalid JSON payload.',
				[ 'status' => 400 ]
			);
		}

		$event_type = $payload['event_type'] ?? '';

		error_log( sprintf( '[Arena Events] Webhook: received event_type=%s', $event_type ) );

		switch ( $event_type ) {
			case self::EVENT_ORDER_PAID:
				return self::handle_order_paid( $payload );

			case self::EVENT_TICKET_ISSUED:
				return self::handle_ticket_issued( $payload );

			case self::EVENT_REFUND_SUCCEEDED:
				return self::handle_refund_succeeded( $payload );

			default:
				// Unknown but correctly signed events are acknowledged (no-op).
				error_log( sprintf( '[Arena Events] Webhook: unhandled event_type=%s — acknowledged.', $event_type ) );
				return new WP_REST_Response( [ 'status' => 'acknowledged', 'event_type' => $event_type ], 200 );
		}
	}

	/**
	 * Verify the HMAC-SHA256 signature from the X-Arena-Signature header.
	 *
	 * Expected format: "sha256=<hex>"
	 * Algorithm:       HMAC-SHA256(webhook_secret, raw_body)
	 *
	 * @param string $raw_body   Raw request body bytes.
	 * @param string $sig_header Value of the X-Arena-Signature header.
	 * @return bool True when the signature matches.
	 */
	public static function verify_signature( string $raw_body, ?string $sig_header ): bool {
		if ( empty( $sig_header ) ) {
			return false;
		}

		$secret = Arena_Events_Settings::get_webhook_secret();
		if ( $secret === '' ) {
			// Secret not configured — reject all incoming webhooks.
			error_log( '[Arena Events] Webhook: arena_webhook_secret is not configured; rejecting request.' );
			return false;
		}

		if ( strpos( $sig_header, self::SIGNATURE_PREFIX ) !== 0 ) {
			return false;
		}

		$received_hex = substr( $sig_header, strlen( self::SIGNATURE_PREFIX ) );
		$expected_hex = hash_hmac( 'sha256', $raw_body, $secret );

		// Constant-time comparison to prevent timing attacks.
		return hash_equals( $expected_hex, $received_hex );
	}

	// ─────────────────────────────────────────────────────────────────────
	// Event handlers
	// ─────────────────────────────────────────────────────────────────────

	/**
	 * Handle the order_paid event.
	 *
	 * Finds the arena_event post corresponding to the event and records
	 * that an order was paid. Optionally sends a customer confirmation email.
	 *
	 * @param array $payload Decoded webhook JSON payload.
	 * @return WP_REST_Response|WP_Error
	 */
	public static function handle_order_paid( array $payload ) {
		$event_data   = $payload['payload'] ?? $payload;
		$arena_event_id = $event_data['arena_event_id'] ?? $event_data['aggregate_id'] ?? '';
		$order_id       = $event_data['order_id'] ?? '';

		if ( $arena_event_id === '' ) {
			return new WP_Error( 'arena_webhook_missing_event_id', 'Missing arena_event_id.', [ 'status' => 422 ] );
		}

		$post_id = Arena_Events_Sync::find_post_by_arena_id( $arena_event_id );

		if ( $post_id ) {
			update_post_meta( $post_id, self::META_ORDER_PAID, $order_id ?: '1' );
			error_log( sprintf( '[Arena Events] Webhook: order_paid — updated post %d (event=%s).', $post_id, $arena_event_id ) );
		} else {
			error_log( sprintf( '[Arena Events] Webhook: order_paid — no local post for arena_event_id=%s.', $arena_event_id ) );
		}

		// Optionally send customer email.
		if ( Arena_Events_Settings::is_email_notifications_enabled() ) {
			$customer_email = $event_data['customer_email'] ?? '';
			if ( $customer_email !== '' ) {
				self::send_notification_email(
					$customer_email,
					__( 'Your order has been confirmed', 'arena-events' ),
					sprintf(
						/* translators: %s = order ID */
						__( 'Thank you for your order. Your payment has been received (order ID: %s).', 'arena-events' ),
						$order_id
					)
				);
			}
		}

		return new WP_REST_Response( [ 'status' => 'ok', 'event_type' => self::EVENT_ORDER_PAID ], 200 );
	}

	/**
	 * Handle the ticket_issued event.
	 *
	 * Stores ticket identifiers in post meta and optionally sends the
	 * customer a ticket delivery email.
	 *
	 * @param array $payload Decoded webhook JSON payload.
	 * @return WP_REST_Response|WP_Error
	 */
	public static function handle_ticket_issued( array $payload ) {
		$event_data     = $payload['payload'] ?? $payload;
		$arena_event_id = $event_data['arena_event_id'] ?? $event_data['aggregate_id'] ?? '';
		$ticket_id      = $event_data['ticket_id'] ?? '';
		$ticket_code    = $event_data['ticket_code'] ?? '';

		if ( $arena_event_id === '' ) {
			return new WP_Error( 'arena_webhook_missing_event_id', 'Missing arena_event_id.', [ 'status' => 422 ] );
		}

		$post_id = Arena_Events_Sync::find_post_by_arena_id( $arena_event_id );

		if ( $post_id ) {
			if ( $ticket_id !== '' ) {
				// Store as a list — multiple tickets can be issued per event.
				$existing = (array) get_post_meta( $post_id, self::META_TICKET_ID, true );
				if ( ! in_array( $ticket_id, $existing, true ) ) {
					$existing[] = $ticket_id;
					update_post_meta( $post_id, self::META_TICKET_ID, $existing );
				}
			}
			if ( $ticket_code !== '' ) {
				$existing_codes = (array) get_post_meta( $post_id, self::META_TICKET_CODE, true );
				if ( ! in_array( $ticket_code, $existing_codes, true ) ) {
					$existing_codes[] = $ticket_code;
					update_post_meta( $post_id, self::META_TICKET_CODE, $existing_codes );
				}
			}
			error_log( sprintf( '[Arena Events] Webhook: ticket_issued — updated post %d (ticket=%s).', $post_id, $ticket_id ) );
		} else {
			error_log( sprintf( '[Arena Events] Webhook: ticket_issued — no local post for arena_event_id=%s.', $arena_event_id ) );
		}

		// Optionally send customer email.
		if ( Arena_Events_Settings::is_email_notifications_enabled() ) {
			$customer_email = $event_data['customer_email'] ?? '';
			if ( $customer_email !== '' ) {
				self::send_notification_email(
					$customer_email,
					__( 'Your ticket is ready', 'arena-events' ),
					sprintf(
						/* translators: %s = ticket code */
						__( 'Your ticket has been issued. Ticket code: %s', 'arena-events' ),
						$ticket_code
					)
				);
			}
		}

		return new WP_REST_Response( [ 'status' => 'ok', 'event_type' => self::EVENT_TICKET_ISSUED ], 200 );
	}

	/**
	 * Handle the refund_succeeded event.
	 *
	 * Stores refund identifiers in post meta and optionally sends the
	 * customer a refund notification email.
	 *
	 * @param array $payload Decoded webhook JSON payload.
	 * @return WP_REST_Response|WP_Error
	 */
	public static function handle_refund_succeeded( array $payload ) {
		$event_data     = $payload['payload'] ?? $payload;
		$arena_event_id = $event_data['arena_event_id'] ?? $event_data['aggregate_id'] ?? '';
		$refund_id      = $event_data['refund_id'] ?? '';

		if ( $arena_event_id === '' ) {
			return new WP_Error( 'arena_webhook_missing_event_id', 'Missing arena_event_id.', [ 'status' => 422 ] );
		}

		$post_id = Arena_Events_Sync::find_post_by_arena_id( $arena_event_id );

		if ( $post_id ) {
			update_post_meta( $post_id, self::META_REFUND_ID, $refund_id );
			error_log( sprintf( '[Arena Events] Webhook: refund_succeeded — updated post %d (refund=%s).', $post_id, $refund_id ) );
		} else {
			error_log( sprintf( '[Arena Events] Webhook: refund_succeeded — no local post for arena_event_id=%s.', $arena_event_id ) );
		}

		// Optionally send customer email.
		if ( Arena_Events_Settings::is_email_notifications_enabled() ) {
			$customer_email = $event_data['customer_email'] ?? '';
			if ( $customer_email !== '' ) {
				self::send_notification_email(
					$customer_email,
					__( 'Your refund has been processed', 'arena-events' ),
					__( 'Your refund has been successfully processed and will be returned to your original payment method.', 'arena-events' )
				);
			}
		}

		return new WP_REST_Response( [ 'status' => 'ok', 'event_type' => self::EVENT_REFUND_SUCCEEDED ], 200 );
	}

	// ─────────────────────────────────────────────────────────────────────
	// Email helpers
	// ─────────────────────────────────────────────────────────────────────

	/**
	 * Send a customer notification email using wp_mail().
	 *
	 * Only called when arena_email_notifications is enabled.
	 *
	 * @param string $to      Recipient email address.
	 * @param string $subject Email subject.
	 * @param string $message Plain-text email body.
	 * @return bool True when wp_mail() reports success.
	 */
	public static function send_notification_email( string $to, string $subject, string $message ): bool {
		$headers = [ 'Content-Type: text/plain; charset=UTF-8' ];
		$result  = wp_mail( $to, $subject, $message, $headers );

		if ( ! $result ) {
			error_log( sprintf( '[Arena Events] Webhook: failed to send notification email to %s.', $to ) );
		}

		return $result;
	}
}
