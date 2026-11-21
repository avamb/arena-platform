<?php
/**
 * Vino&Co cutover: the tickets sold in the Bil24 era (before 2026-09-25), which exist only in
 * WooCommerce order meta, for cmd/arena-legacy-tickets-import.
 *
 * READ ONLY. It reads orders through WooCommerce's order API (HPOS-safe wc_get_orders, paged) and
 * product meta through get_post_meta. It writes nothing: no order, no meta, no option, no
 * scheduled action. It prints JSON on stdout. The buyer's name, e-mail and phone are in full,
 * because the importer must store them: keep the file private and delete it after the import.
 *
 *   docker cp ops/vino-cutover/wp-legacy-tickets-export.php <wp container>:/tmp/x.php
 *   docker exec <wp container> php /tmp/x.php --actions=267465,267466 > legacy-tickets.json
 *   docker exec <wp container> rm /tmp/x.php
 *
 * Options
 *   --actions=A,B  only orders with a ticket of these Bil24 actions (product meta _bil24_action_id),
 *                  and only those tickets; default every action
 *   --wp=PATH      WordPress root, default /var/www/html
 *   --page=N       orders per page, default 200
 *
 * Which orders: status completed or processing, and bil24_external_order_id of 7 digits (a Bil24
 * order of the Bil24 era; the platform's own orders have 10-digit numbers and are not exported).
 *
 * Meta read (checked on order 3461, 2026-11-08):
 *   bil24_external_order_id   the Bil24 order number
 *   bil24_tickets             JSON list, one object per ticket: id, seatId, orderId, seatLocation,
 *                             category, price, discount, charge, totalPrice (MAJOR units), barcode,
 *                             barcodeFormat, actionEvent {id, ...}
 *   lops_bil24_live_ids       JSON list of the ticket ids not refunded; missing -> "live_ids": null
 *   bil24_ticket_id_list      informative
 *   wpml_language             ru / en / he
 *   item meta "Bil24 Action Event ID"   the Bil24 session of the line
 *   product meta _bil24_action_id       the Bil24 action = events.external_bil24_id
 */

if ( PHP_SAPI !== 'cli' ) { fwrite( STDERR, "run from the command line only\n" ); exit( 2 ); }

$opt = [ 'wp' => '/var/www/html', 'page' => 200, 'actions' => [] ];
foreach ( array_slice( $argv, 1 ) as $a ) {
	if ( preg_match( '/^--wp=(.+)$/', $a, $m ) ) $opt['wp'] = rtrim( $m[1], '/' );
	elseif ( preg_match( '/^--page=(\d{1,4})$/', $a, $m ) ) $opt['page'] = max( 10, (int) $m[1] );
	elseif ( preg_match( '/^--actions=([\d,]+)$/', $a, $m ) ) {
		$opt['actions'] = array_values( array_filter( array_map( 'intval', explode( ',', $m[1] ) ) ) );
	}
	else { fwrite( STDERR, "unknown argument: $a\n" ); exit( 2 ); }
}
if ( ! is_file( $opt['wp'] . '/wp-load.php' ) ) { fwrite( STDERR, "no wp-load.php under {$opt['wp']}\n" ); exit( 2 ); }

require $opt['wp'] . '/wp-load.php';

function wlt_iso( $ts ): ?string { return $ts ? gmdate( 'Y-m-d\TH:i:s\Z', (int) $ts ) : null; }

/** A meta value that may be stored as a JSON string or as a PHP array; null when absent. */
function wlt_json_meta( $raw ) {
	if ( is_array( $raw ) ) return $raw;
	$raw = trim( (string) $raw );
	if ( $raw === '' ) return null;
	$v = json_decode( $raw, true );
	return is_array( $v ) ? $v : null;
}

/** The order lines: product, Bil24 action of the product, Bil24 session of the line. */
function wlt_items( WC_Order $o ): array {
	$out = [];
	foreach ( $o->get_items() as $item_id => $item ) {
		if ( ! $item instanceof WC_Order_Item_Product ) continue;
		$product_id = (int) $item->get_product_id();
		$action     = trim( (string) get_post_meta( $product_id, '_bil24_action_id', true ) );
		$out[] = [
			'item_id'               => (string) $item_id,
			'product_id'            => (string) $product_id,
			'name'                  => (string) $item->get_name(),
			'quantity'              => (int) $item->get_quantity(),
			'bil24_action_id'       => $action === '' ? null : (int) $action,
			'bil24_action_event_id' => (int) $item->get_meta( 'Bil24 Action Event ID', true ) ?: null,
		];
	}
	return $out;
}

/** The Bil24 action of a ticket: the line of its session, else the order's only action. */
function wlt_ticket_action( array $t, array $items ): int {
	$ae      = (int) ( $t['actionEvent']['id'] ?? 0 );
	$actions = [];
	foreach ( $items as $it ) {
		if ( ! $it['bil24_action_id'] ) continue;
		$actions[ $it['bil24_action_id'] ] = true;
		if ( $ae && (int) $it['bil24_action_event_id'] === $ae ) return (int) $it['bil24_action_id'];
	}
	return count( $actions ) === 1 ? (int) array_key_first( $actions ) : 0;
}

$rows    = [];
$scanned = 0;
$page    = 1;
do {
	$orders = wc_get_orders( [
		'type'    => 'shop_order',
		'status'  => [ 'completed', 'processing' ],
		'limit'   => $opt['page'],
		'paged'   => $page,
		'orderby' => 'ID',
		'order'   => 'ASC',
		'return'  => 'objects',
	] );
	foreach ( $orders as $o ) {
		if ( ! $o instanceof WC_Order ) continue; // a refund object is not an order
		$scanned++;
		$bil24 = trim( (string) $o->get_meta( 'bil24_external_order_id' ) );
		if ( ! preg_match( '/^\d{7}$/', $bil24 ) ) continue;

		$items   = wlt_items( $o );
		$tickets = wlt_json_meta( $o->get_meta( 'bil24_tickets' ) ) ?? [];
		if ( $opt['actions'] ) {
			$kept = [];
			foreach ( $tickets as $t ) {
				if ( ! is_array( $t ) ) continue;
				$action = wlt_ticket_action( $t, $items );
				if ( $action === 0 || in_array( $action, $opt['actions'], true ) ) $kept[] = $t;
			}
			$in_filter = false;
			foreach ( $items as $it ) {
				if ( in_array( (int) $it['bil24_action_id'], $opt['actions'], true ) ) $in_filter = true;
			}
			if ( ! $in_filter ) continue;
			$tickets = $kept;
		}
		$live    = wlt_json_meta( $o->get_meta( 'lops_bil24_live_ids' ) );
		$created = $o->get_date_created();
		$paid    = $o->get_date_paid();
		$rows[]  = [
			'wc_order_id'    => (string) $o->get_id(),
			'status'         => $o->get_status(),
			'bil24_order_id' => $bil24,
			'created_at'     => $created ? wlt_iso( $created->getTimestamp() ) : null,
			'paid_at'        => $paid ? wlt_iso( $paid->getTimestamp() ) : null,
			'currency'       => $o->get_currency(),
			'total'          => (string) $o->get_total(),
			'language'       => (string) $o->get_meta( 'wpml_language' ),
			'billing'        => [
				'first_name' => (string) $o->get_billing_first_name(),
				'last_name'  => (string) $o->get_billing_last_name(),
				'email'      => trim( (string) $o->get_billing_email() ),
				'phone'      => trim( (string) $o->get_billing_phone() ),
			],
			'live_ids'       => $live === null ? null : array_values( array_map( 'intval', $live ) ),
			'ticket_id_list' => is_array( $o->get_meta( 'bil24_ticket_id_list' ) )
				? wp_json_encode( $o->get_meta( 'bil24_ticket_id_list' ) )
				: (string) $o->get_meta( 'bil24_ticket_id_list' ),
			'items'          => $items,
			'tickets'        => array_values( $tickets ),
		];
	}
	$n = count( $orders );
	$page++;
	unset( $orders );
} while ( $n >= $opt['page'] );

echo wp_json_encode( [
	'generated_at'   => wlt_iso( time() ),
	'source'         => 'wp_bil24_legacy',
	'orders_scanned' => $scanned,
	'actions'        => $opt['actions'],
	'orders'         => $rows,
], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRETTY_PRINT ), "\n";
