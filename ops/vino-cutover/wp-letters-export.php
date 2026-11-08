<?php
/**
 * Vino&Co cutover: the delivery status of the ticket e-mails WordPress sent itself.
 *
 * READ ONLY. WordPress sent the ticket letters through its own Brevo account (plugin
 * bil24-ticket-mailer) and lampyris-ops recorded what Brevo reported. That history exists only on
 * the WooCommerce orders, so it is exported once for cmd/arena-wp-letters-import, which files it
 * in the platform's email_letters table. This script writes nothing: no order, no meta, no option.
 * It prints JSON on stdout. The recipient is the billing e-mail in full, because the importer must
 * store it: keep the file private and delete it after the import.
 *
 *   docker cp ops/vino-cutover/wp-letters-export.php <wp container>:/tmp/x.php
 *   docker exec <wp container> php /tmp/x.php > wp-letters.json
 *   docker exec <wp container> rm /tmp/x.php
 *
 * Options
 *   --wp=PATH     WordPress root, default /var/www/html
 *   --page=N      orders per page, default 200
 *
 * Order meta keys read (bil24-ticket-mailer and lampyris-ops, checked 2026-11-08):
 *   bil24_external_order_id     the platform order number (orders.system_id)
 *   _btm_sent_at                site-time stamp written when Brevo accepted the letter (Y-m-d H:i:s)
 *   _btm_brevo_message_id       the Brevo message id, stored raw, e.g. <id@relay>
 *   lops_email_status           latest state {state, ts, detail}; constant LOPS_META_EMAIL_STATUS
 *   lops_email_status_times     {sent, delivered, opened} first-seen unix times
 *   lops_email_delivery_failed  "1" after a bounce, block or error; constant LOPS_META_EMAIL_FAILED
 */

if ( PHP_SAPI !== 'cli' ) { fwrite( STDERR, "run from the command line only\n" ); exit( 2 ); }

$opt = [ 'wp' => '/var/www/html', 'page' => 200 ];
foreach ( array_slice( $argv, 1 ) as $a ) {
	if ( preg_match( '/^--wp=(.+)$/', $a, $m ) ) $opt['wp'] = rtrim( $m[1], '/' );
	elseif ( preg_match( '/^--page=(\d{1,4})$/', $a, $m ) ) $opt['page'] = max( 10, (int) $m[1] );
	else { fwrite( STDERR, "unknown argument: $a\n" ); exit( 2 ); }
}
if ( ! is_file( $opt['wp'] . '/wp-load.php' ) ) { fwrite( STDERR, "no wp-load.php under {$opt['wp']}\n" ); exit( 2 ); }

require $opt['wp'] . '/wp-load.php';

function wle_iso( $ts ): ?string { return $ts ? gmdate( 'Y-m-d\TH:i:s\Z', (int) $ts ) : null; }

/** The mailer stamps current_time('mysql'), i.e. the SITE timezone: convert it to UTC. */
function wle_site_time_to_iso( string $s ): ?string {
	$s = trim( $s );
	if ( $s === '' ) return null;
	try {
		$d = new DateTime( $s, wp_timezone() );
	} catch ( Exception $e ) {
		return null;
	}
	return wle_iso( $d->getTimestamp() );
}

/** One export row for an order, or null when the order never had a letter or a status. */
function wle_row( WC_Order $o ): ?array {
	$sent_at = trim( (string) $o->get_meta( '_btm_sent_at' ) );
	$status  = $o->get_meta( 'lops_email_status' );
	$status  = is_array( $status ) ? $status : [];
	$state   = (string) ( $status['state'] ?? '' );
	if ( $sent_at === '' && $state === '' ) return null;

	$times_raw = $o->get_meta( 'lops_email_status_times' );
	$times     = [];
	foreach ( [ 'sent', 'delivered', 'opened' ] as $k ) {
		if ( is_array( $times_raw ) && ! empty( $times_raw[ $k ] ) ) $times[ $k ] = (int) $times_raw[ $k ];
	}
	$created = $o->get_date_created();
	return [
		'wc_order_id'              => (string) $o->get_id(),
		'platform_order_system_id' => (string) $o->get_meta( 'bil24_external_order_id' ),
		'recipient'                => trim( (string) $o->get_billing_email() ),
		'sent_at'                  => wle_site_time_to_iso( $sent_at ),
		'message_id'               => trim( (string) $o->get_meta( '_btm_brevo_message_id' ) ),
		'status'                   => $state,
		'status_detail'            => (string) ( $status['detail'] ?? '' ),
		'status_ts'                => isset( $status['ts'] ) ? (int) $status['ts'] : 0,
		'times'                    => (object) $times,
		'failed'                   => (string) $o->get_meta( 'lops_email_delivery_failed' ) === '1',
		'order_date'               => $created ? wle_iso( $created->getTimestamp() ) : null,
	];
}

$rows    = [];
$scanned = 0;
$page    = 1;
do {
	$orders = wc_get_orders( [
		'type'    => 'shop_order',
		'status'  => array_keys( wc_get_order_statuses() ),
		'limit'   => $opt['page'],
		'paged'   => $page,
		'orderby' => 'ID',
		'order'   => 'ASC',
		'return'  => 'objects',
	] );
	foreach ( $orders as $o ) {
		if ( ! $o instanceof WC_Order ) continue; // a refund object is not an order
		$scanned++;
		$r = wle_row( $o );
		if ( $r !== null ) $rows[] = $r;
	}
	$n = count( $orders );
	$page++;
	unset( $orders );
} while ( $n >= $opt['page'] );

echo wp_json_encode( [
	'generated_at'   => wle_iso( time() ),
	'source'         => 'wp_brevo',
	'orders_scanned' => $scanned,
	'letters'        => $rows,
], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRETTY_PRINT ), "\n";
