<?php
/**
 * Vino&Co cutover, T0 inventory of what WordPress still owes (spec 62 section 14.2 item 3).
 *
 * READ ONLY. It selects from the WordPress database through WooCommerce's order API and a few
 * prepared SELECTs, and (only with --allpay) asks AllPay's paymentstatus endpoint, which is the
 * same read call allpay-status-sync.php makes every 15 minutes. It writes nothing: no order, no
 * meta, no option, no scheduled action. It prints JSON on stdout. Nothing secret is read into
 * the output; buyers appear as order ids and masked e-mails (a***@domain).
 *
 *   docker cp ops/vino-cutover/wp-inventory.php <wp container>:/tmp/wp-inventory.php
 *   docker exec <wp container> php /tmp/wp-inventory.php                       > inventory-T0.json
 *   docker exec <wp container> php /tmp/wp-inventory.php --days=14 --allpay    > inventory-T0.json
 *   docker exec <wp container> rm /tmp/wp-inventory.php
 *
 * Options
 *   --days=N     window of the "recent payments" section, default 7 (the WP refund-retry horizon)
 *   --allpay     also ask AllPay for the status of the recent payments (adds "allpay_live")
 *   --wp=PATH    WordPress root, default /var/www/html
 *   --summary    print only the counts, no per-order rows
 *
 * Order meta keys (read from the plugins, 2026-11-07):
 *   bil24_external_order_id  the platform order number returned by CREATE_ORDER_EXT
 *   bil24_paid               1 once PAY_ORDER succeeded
 *   bil24_tickets_sent       1 once the ticket e-mail went out
 *   bil24_tickets            JSON list of the tickets the gateway answered
 *   bil24_payment_deadline   unix time of the payment window end
 *   allpay_status            last AllPay status the IPN reported (1 paid, 3/4 refunded)
 *   vino_apw_refusal         PAY_ORDER refused the payment: "expired" or "refused"
 *   vino_apw_outcome         expired_unpaid | refunded | refund_pending | refund_failed | abandoned
 *   vino_apw_refund_attempts / vino_apw_status_attempts   retry counters
 * Action Scheduler hooks: vino_apw_deadline, vino_apw_retry_pay, vino_apw_retry_refund (group
 * vino_apw), btm_process_order (ticket e-mail), lops_tickets_fallback, lops_macs_notify,
 * lops_send_feedback_email.
 */

if ( PHP_SAPI !== 'cli' ) { fwrite( STDERR, "run from the command line only\n" ); exit( 2 ); }

$opt = [ 'days' => 7, 'allpay' => false, 'wp' => '/var/www/html', 'summary' => false ];
foreach ( array_slice( $argv, 1 ) as $a ) {
	if ( preg_match( '/^--days=(\d{1,3})$/', $a, $m ) ) $opt['days'] = max( 1, (int) $m[1] );
	elseif ( $a === '--allpay' ) $opt['allpay'] = true;
	elseif ( $a === '--summary' ) $opt['summary'] = true;
	elseif ( preg_match( '/^--wp=(.+)$/', $a, $m ) ) $opt['wp'] = rtrim( $m[1], '/' );
	else { fwrite( STDERR, "unknown argument: $a\n" ); exit( 2 ); }
}
if ( ! is_file( $opt['wp'] . '/wp-load.php' ) ) { fwrite( STDERR, "no wp-load.php under {$opt['wp']}\n" ); exit( 2 ); }

require $opt['wp'] . '/wp-load.php';
global $wpdb;

function inv_iso( $ts ): ?string { return $ts ? gmdate( 'Y-m-d\TH:i:s\Z', (int) $ts ) : null; }

function inv_mask_email( string $e ): string {
	$e = trim( $e );
	if ( $e === '' || strpos( $e, '@' ) === false ) return '';
	[ $local, $domain ] = explode( '@', $e, 2 );
	return mb_substr( $local, 0, 1 ) . '***@' . $domain;
}

/** What an order row looks like in the report: ids, dates, money, masked e-mail, our own meta. */
function inv_order_row( WC_Order $o ): array {
	$tickets = json_decode( (string) $o->get_meta( 'bil24_tickets' ), true );
	$deadline = (int) $o->get_meta( 'bil24_payment_deadline' );
	$created = $o->get_date_created();
	$paid = $o->get_date_paid();
	return [
		'id'              => $o->get_id(),
		'status'          => $o->get_status(),
		'created_utc'     => $created ? inv_iso( $created->getTimestamp() ) : null,
		'paid_utc'        => $paid ? inv_iso( $paid->getTimestamp() ) : null,
		'age_hours'       => $created ? (int) round( ( time() - $created->getTimestamp() ) / 3600 ) : null,
		'total'           => (string) $o->get_total(),
		'refunded'        => (string) $o->get_total_refunded(),
		'currency'        => $o->get_currency(),
		'payment_method'  => $o->get_payment_method(),
		'email'           => inv_mask_email( (string) $o->get_billing_email() ),
		'platform_order'  => (string) $o->get_meta( 'bil24_external_order_id' ),
		'pay_order_done'  => (int) $o->get_meta( 'bil24_paid' ) === 1,
		'tickets_in_meta' => is_array( $tickets ) ? count( $tickets ) : 0,
		'tickets_sent'    => (int) $o->get_meta( 'bil24_tickets_sent' ) === 1,
		'allpay_status'   => $o->get_meta( 'allpay_status' ) === '' ? null : (int) $o->get_meta( 'allpay_status' ),
		'payment_deadline_utc' => inv_iso( $deadline ),
		'apw_outcome'     => (string) $o->get_meta( 'vino_apw_outcome' ),
		'apw_refusal'     => (string) $o->get_meta( 'vino_apw_refusal' ),
		'apw_refund_attempts' => (int) $o->get_meta( 'vino_apw_refund_attempts' ),
		'apw_refund_first_attempt_utc' => inv_iso( (int) $o->get_meta( 'vino_apw_refund_first_attempt_at' ) ),
	];
}

$now = time();
$report = [
	'generated_utc'   => inv_iso( $now ),
	'table_prefix'    => $wpdb->prefix,
	'woocommerce'     => defined( 'WC_VERSION' ) ? WC_VERSION : null,
	'window_days'     => $opt['days'],
	'read_only'       => true,
];

/* 1. Orders that are not finished: pending / on-hold / processing */
$open_status = [ 'pending', 'on-hold', 'processing' ];
$open = wc_get_orders( [ 'status' => $open_status, 'limit' => -1, 'orderby' => 'date', 'order' => 'ASC' ] );
$by_status = array_fill_keys( $open_status, 0 );
$open_rows = [];
$processing_without_tickets = 0;
foreach ( $open as $o ) {
	$row = inv_order_row( $o );
	$by_status[ $row['status'] ]++;
	// Paid on the site's side but the ticket e-mail is not out: the stop condition 14.4 cares.
	$row['without_tickets'] = $row['status'] === 'processing' && ! $row['tickets_sent'];
	if ( $row['without_tickets'] ) $processing_without_tickets++;
	$open_rows[] = $row;
}
$report['open_orders'] = [
	'counts'                     => $by_status,
	'total'                      => count( $open_rows ),
	'processing_without_tickets' => $processing_without_tickets,
	'oldest_created_utc'         => $open_rows ? $open_rows[0]['created_utc'] : null,
];
if ( ! $opt['summary'] ) $report['open_orders']['rows'] = $open_rows;

/* 2. PAY_ORDER refusals and the refunds that follow them */
$refusal_ids = wc_get_orders( [
	'limit'      => -1,
	'return'     => 'ids',
	'status'     => array_keys( wc_get_order_statuses() ),
	'meta_query' => [
		'relation' => 'OR',
		[ 'key' => 'vino_apw_refusal', 'compare' => 'EXISTS' ],
		[ 'key' => 'vino_apw_outcome', 'value' => [ 'refund_pending', 'refund_failed' ], 'compare' => 'IN' ],
	],
] );
$refusal_rows = [];
$refusal_counts = [ 'with_refusal_meta' => 0, 'refund_pending' => 0, 'refund_failed' => 0, 'refunded' => 0, 'refusal_without_refund_outcome' => 0 ];
foreach ( $refusal_ids as $id ) {
	$o = wc_get_order( $id );
	if ( ! $o ) continue;
	$row = inv_order_row( $o );
	$refusal_rows[] = $row;
	if ( $row['apw_refusal'] !== '' ) $refusal_counts['with_refusal_meta']++;
	if ( isset( $refusal_counts[ $row['apw_outcome'] ] ) ) $refusal_counts[ $row['apw_outcome'] ]++;
	if ( $row['apw_refusal'] !== '' && ! in_array( $row['apw_outcome'], [ 'refunded', 'refund_pending', 'refund_failed' ], true ) ) $refusal_counts['refusal_without_refund_outcome']++;
}
$report['pay_order_refusals'] = [ 'counts' => $refusal_counts, 'total' => count( $refusal_rows ) ];
if ( ! $opt['summary'] ) $report['pay_order_refusals']['rows'] = $refusal_rows;

/* 3. Action Scheduler */
$as = $wpdb->prefix . 'actionscheduler_actions';
$ag = $wpdb->prefix . 'actionscheduler_groups';
$hooks = [ 'vino_apw_deadline', 'vino_apw_retry_pay', 'vino_apw_retry_refund', 'btm_process_order',
           'lops_tickets_fallback', 'lops_macs_notify', 'lops_send_feedback_email' ];
$ph = implode( ',', array_fill( 0, count( $hooks ), '%s' ) );
$rows = $wpdb->get_results( $wpdb->prepare(
	"SELECT hook, status, COUNT(*) AS n, MIN(scheduled_date_gmt) AS first_at, MAX(scheduled_date_gmt) AS last_at,
	        SUM(status = 'pending' AND scheduled_date_gmt < UTC_TIMESTAMP()) AS overdue
	   FROM {$as} WHERE hook IN ($ph) AND status IN ('pending','in-progress','failed')
	  GROUP BY hook, status ORDER BY hook, status", $hooks ), ARRAY_A );
$sched = [];
foreach ( $rows as $r ) {
	$sched[] = [ 'hook' => $r['hook'], 'status' => $r['status'], 'count' => (int) $r['n'], 'overdue' => (int) $r['overdue'],
	             'first_utc' => $r['first_at'], 'last_utc' => $r['last_at'] ];
}
$failed_recent = (int) $wpdb->get_var( $wpdb->prepare(
	"SELECT COUNT(*) FROM {$as} WHERE hook IN ($ph) AND status = 'failed' AND last_attempt_gmt > %s",
	array_merge( $hooks, [ gmdate( 'Y-m-d H:i:s', $now - $opt['days'] * 86400 ) ] ) ) );
$other_pending = (int) $wpdb->get_var( "SELECT COUNT(*) FROM {$as} WHERE status = 'pending'" );
$report['action_scheduler'] = [
	'by_hook_and_status'         => $sched,
	'failed_in_window'           => $failed_recent,
	'all_pending_any_hook'       => $other_pending,
];
if ( ! $opt['summary'] ) {
	$det = $wpdb->get_results( $wpdb->prepare(
		"SELECT a.hook, a.status, a.scheduled_date_gmt, a.args FROM {$as} a
		  WHERE a.hook IN ($ph) AND a.status IN ('pending','in-progress') ORDER BY a.scheduled_date_gmt LIMIT 500", $hooks ), ARRAY_A );
	$items = [];
	foreach ( $det as $d ) {
		$args = json_decode( (string) $d['args'], true );
		$oid = null;
		if ( is_array( $args ) ) $oid = isset( $args['order_id'] ) ? (int) $args['order_id'] : ( isset( $args[0] ) && is_numeric( $args[0] ) ? (int) $args[0] : null );
		$items[] = [ 'hook' => $d['hook'], 'status' => $d['status'], 'scheduled_utc' => $d['scheduled_date_gmt'], 'order_id' => $oid ];
	}
	$report['action_scheduler']['pending_items'] = $items;
}
// WP-Cron events the cutover cares about (the system cron keeps running on WP).
$report['wp_cron_next_utc'] = [
	'allpay_status_sync_run' => inv_iso( wp_next_scheduled( 'allpay_status_sync_run' ) ),
	'bil24_acf_sync_cron'    => inv_iso( wp_next_scheduled( 'bil24_acf_sync_cron' ) ),
];

/* 4. Recent AllPay payments and whether AllPay confirmed them to the site */
$recent = wc_get_orders( [
	'limit' => -1, 'orderby' => 'date', 'order' => 'ASC',
	'payment_method' => 'allpay-payment-gateway',
	'date_created'   => '>' . ( $now - $opt['days'] * 86400 ),
	'status'         => [ 'pending', 'on-hold', 'processing', 'completed', 'refunded', 'cancelled', 'failed' ],
] );
$rc = [ 'orders' => 0, 'paid_by_wc' => 0, 'paid_without_ipn_status' => 0, 'ipn_status_not_paid' => 0,
        'unpaid_open' => 0, 'paid_without_tickets' => 0 ];
$recent_rows = [];
$gateway = null;
if ( $opt['allpay'] && function_exists( 'WC' ) && WC()->payment_gateways() ) {
	$all = WC()->payment_gateways()->payment_gateways();
	$gateway = $all['allpay-payment-gateway'] ?? null;
	if ( ! $gateway ) $report['allpay_live_error'] = 'gateway allpay-payment-gateway not found';
}
$live_counts = [ 'asked' => 0, 'paid_1' => 0, 'refunded_3_4' => 0, 'not_paid_0' => 0, 'unknown' => 0, 'paid_at_allpay_but_wc_unpaid' => 0, 'wc_paid_but_allpay_not_paid' => 0 ];
foreach ( $recent as $o ) {
	$row = inv_order_row( $o );
	$rc['orders']++;
	$wc_paid = $o->is_paid();
	if ( $wc_paid ) $rc['paid_by_wc']++;
	if ( $wc_paid && $row['allpay_status'] === null ) { $rc['paid_without_ipn_status']++; $row['flag_no_ipn_status'] = true; }
	if ( $row['allpay_status'] !== null && $row['allpay_status'] !== 1 && $wc_paid ) $rc['ipn_status_not_paid']++;
	if ( ! $wc_paid && in_array( $row['status'], [ 'pending', 'on-hold' ], true ) ) $rc['unpaid_open']++;
	if ( $wc_paid && ! $row['tickets_sent'] ) $rc['paid_without_tickets']++;
	if ( $gateway && ( $wc_paid || in_array( $row['status'], [ 'pending', 'on-hold' ], true ) ) ) {
		$req = [ 'login' => (string) $gateway->api_login, 'order_id' => str_replace( '#', '', (string) $o->get_order_number() ) ];
		$req['sign'] = $gateway->get_signature( $req );
		$resp = wp_remote_post( 'https://allpay.to/app/?show=paymentstatus&mode=api11', [ 'body' => http_build_query( $req ), 'timeout' => 20 ] );
		$live = null;
		if ( ! is_wp_error( $resp ) ) {
			$b = json_decode( (string) wp_remote_retrieve_body( $resp ), true );
			if ( is_array( $b ) && isset( $b['status'] ) ) $live = (int) $b['status'];
		}
		$live_counts['asked']++;
		if ( $live === 1 ) $live_counts['paid_1']++;
		elseif ( $live === 3 || $live === 4 ) $live_counts['refunded_3_4']++;
		elseif ( $live === 0 ) $live_counts['not_paid_0']++;
		else $live_counts['unknown']++;
		if ( $live === 1 && ! $wc_paid ) $live_counts['paid_at_allpay_but_wc_unpaid']++;
		if ( $live !== null && $live !== 1 && $live !== 3 && $live !== 4 && $wc_paid ) $live_counts['wc_paid_but_allpay_not_paid']++;
		$row['allpay_live'] = $live;
	}
	$recent_rows[] = $row;
}
$report['recent_allpay_payments'] = [ 'counts' => $rc ];
if ( $gateway ) $report['recent_allpay_payments']['allpay_live_counts'] = $live_counts;
if ( ! $opt['summary'] ) $report['recent_allpay_payments']['rows'] = $recent_rows;

/* 5. The stop condition 14.4 in one line each */
$report['stop_condition_14_4'] = [
	'no_open_orders'                 => count( $open_rows ) === 0,
	'no_processing_without_tickets'  => $processing_without_tickets === 0,
	'refund_queue_empty'             => ( $refusal_counts['refund_pending'] + $refusal_counts['refund_failed'] ) === 0
	                                    && ! array_filter( $sched, static fn( $s ) => $s['hook'] === 'vino_apw_retry_refund' ),
	'no_pending_apw_or_ticket_actions' => ! array_filter( $sched, static fn( $s ) => in_array( $s['status'], [ 'pending', 'in-progress' ], true )
	                                    && in_array( $s['hook'], [ 'vino_apw_deadline', 'vino_apw_retry_pay', 'vino_apw_retry_refund', 'btm_process_order', 'lops_tickets_fallback' ], true ) ),
	'payments_confirmed_by_allpay'   => $gateway ? ( $live_counts['wc_paid_but_allpay_not_paid'] === 0 && $live_counts['paid_at_allpay_but_wc_unpaid'] === 0 && $live_counts['unknown'] === 0 ) : 'not checked: run with --allpay',
];

echo json_encode( $report, JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES ), "\n";
