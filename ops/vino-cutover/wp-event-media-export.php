<?php
/**
 * Vino&Co cutover: the event galleries, videos and "after the event" reports kept in WordPress.
 *
 * READ ONLY. On the Vino&Co site the WooCommerce product IS the event (vino-ops-bridge.php), and
 * lampyris-ops keeps per language (ru, en, he) a photo gallery and video links
 * (class-lops-event-media.php) and a report with text, photos and videos
 * (class-lops-event-report.php) in product meta. This prints them as JSON for
 * ops/vino-cutover/wp-event-media-import.mjs. It writes nothing. Image URLs are public upload
 * URLs, nothing secret is in the output.
 *
 *   docker cp ops/vino-cutover/wp-event-media-export.php <wp container>:/tmp/x.php
 *   docker exec <wp container> php /tmp/x.php > wp-event-media.json
 *   docker exec <wp container> rm /tmp/x.php
 *
 * Options
 *   --wp=PATH   WordPress root, default /var/www/html
 *
 * Product meta read. The base language ru has NO suffix on the report keys, the media keys always
 * carry one; a translated product stores nothing, WPML points it at the ru product.
 *   _bil24_action_id            the platform event: events.external_bil24_id
 *   lops_media_gallery_<lang>   image URLs         lops_media_video_urls_<lang>   video URLs
 *   lops_media_custom_<lang>    1 = this language has its own set, 0 = it shows the ru set
 *   lops_report_gallery[_<lang>], lops_report_video_urls[_<lang>], lops_report_text[_<lang>]
 *   lops_report_custom_<lang>   the same flag for the report
 *   lops_partner_links          [{name, url}]
 */

if ( PHP_SAPI !== 'cli' ) { fwrite( STDERR, "run from the command line only\n" ); exit( 2 ); }

$opt = [ 'wp' => '/var/www/html' ];
foreach ( array_slice( $argv, 1 ) as $a ) {
	if ( preg_match( '/^--wp=(.+)$/', $a, $m ) ) $opt['wp'] = rtrim( $m[1], '/' );
	else { fwrite( STDERR, "unknown argument: $a\n" ); exit( 2 ); }
}
if ( ! is_file( $opt['wp'] . '/wp-load.php' ) ) { fwrite( STDERR, "no wp-load.php under {$opt['wp']}\n" ); exit( 2 ); }

require $opt['wp'] . '/wp-load.php';

const WEM_LANGS = [ 'ru', 'en', 'he' ];
const WEM_BASE  = 'ru';

/** Same normalisation as Lops_Event_Media::url_list: JSON, newline text or array; http(s) only; unique. */
function wem_urls( $value ): array {
	if ( is_string( $value ) ) {
		$decoded = json_decode( $value, true );
		$value = is_array( $decoded ) ? $decoded : preg_split( '/\r\n|\r|\n/', $value );
	}
	$out = [];
	foreach ( is_array( $value ) ? $value : [] as $item ) {
		$url = is_array( $item ) ? (string) ( $item['url'] ?? '' ) : (string) $item;
		$url = trim( $url );
		if ( $url === '' || ! preg_match( '#^https?://#i', $url ) ) continue;
		$out[] = $url;
	}
	return array_values( array_unique( $out ) );
}

function wem_meta( int $id, string $key ) { return get_post_meta( $id, $key, true ); }

function wem_truthy( $v ): bool { return in_array( $v, [ true, 1, '1', 'true', 'yes', 'on' ], true ); }

function wem_report_key( string $base, string $lang ): string { return $lang === WEM_BASE ? $base : $base . '_' . $lang; }

function wem_language_set( int $id, string $lang ): array {
	return [
		'media_custom'  => $lang === WEM_BASE ? true : wem_truthy( wem_meta( $id, 'lops_media_custom_' . $lang ) ),
		'gallery'       => wem_urls( wem_meta( $id, 'lops_media_gallery_' . $lang ) ),
		'videos'        => wem_urls( wem_meta( $id, 'lops_media_video_urls_' . $lang ) ),
		'report_custom' => $lang === WEM_BASE ? true : wem_truthy( wem_meta( $id, 'lops_report_custom_' . $lang ) ),
		'report_text'   => trim( (string) wem_meta( $id, wem_report_key( 'lops_report_text', $lang ) ) ),
		'report_photos' => wem_urls( wem_meta( $id, wem_report_key( 'lops_report_gallery', $lang ) ) ),
		'report_videos' => wem_urls( wem_meta( $id, wem_report_key( 'lops_report_video_urls', $lang ) ) ),
	];
}

function wem_partners( int $id ): array {
	$v = wem_meta( $id, 'lops_partner_links' );
	if ( is_string( $v ) ) { $d = json_decode( $v, true ); if ( is_array( $d ) ) $v = $d; }
	$out = [];
	foreach ( is_array( $v ) ? $v : [] as $row ) {
		if ( ! is_array( $row ) ) continue;
		$name = trim( (string) ( $row['name'] ?? '' ) );
		$url  = trim( (string) ( $row['url'] ?? '' ) );
		if ( $name !== '' && preg_match( '#^https?://#i', $url ) ) $out[] = [ 'name' => $name, 'url' => $url ];
	}
	return $out;
}

$events = [];
$page   = 1;
do {
	$ids = get_posts( [
		'post_type'        => 'product',
		'post_status'      => [ 'publish', 'private', 'draft', 'pending', 'future' ],
		'posts_per_page'   => 200,
		'paged'            => $page,
		'orderby'          => 'ID',
		'order'            => 'ASC',
		'fields'           => 'ids',
		'suppress_filters' => true, // keep WPML from hiding the other languages, they are skipped below
		'meta_query'       => [ [ 'key' => '_bil24_action_id', 'compare' => 'EXISTS' ] ],
	] );
	foreach ( $ids as $id ) {
		$id     = (int) $id;
		$action = trim( (string) wem_meta( $id, '_bil24_action_id' ) );
		if ( $action === '' || $action === '0' ) continue;
		// A WPML translation keeps nothing of its own: the sets live on the ru product.
		$base = (int) apply_filters( 'wpml_object_id', $id, 'product', true, WEM_BASE );
		if ( $base > 0 && $base !== $id ) continue;
		$langs = [];
		foreach ( WEM_LANGS as $l ) $langs[ $l ] = wem_language_set( $id, $l );
		$events[] = [
			'product_id'      => $id,
			'bil24_action_id' => $action,
			'title'           => get_the_title( $id ),
			'status'          => get_post_status( $id ),
			'languages'       => (object) $langs,
			'partner_links'   => wem_partners( $id ),
		];
	}
	$n = count( $ids );
	$page++;
} while ( $n >= 200 );

echo wp_json_encode( [
	'generated_at'  => gmdate( 'Y-m-d\TH:i:s\Z' ),
	'base_language' => WEM_BASE,
	'events'        => $events,
], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRETTY_PRINT ), "\n";
