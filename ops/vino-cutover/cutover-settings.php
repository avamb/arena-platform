<?php
/**
 * Vino&Co prod cutover — the site settings that move with the switch to Arena.
 *
 *   wp eval-file cutover-settings.php            # dry run: prints what would change
 *   wp eval-file cutover-settings.php apply      # applies, after backing every option up
 *   wp eval-file cutover-settings.php rollback   # restores the backups written by apply
 *
 * Inside the container: docker exec -i -w /var/www/html <wp> php -r '...' works the same
 * way: `php cutover-settings.php [apply|rollback]` after `require 'wp-load.php'` below.
 *
 * Secrets are NOT here: the gateway token (Bil24 → Tixgear Sync), the webhook signing secret
 * (Settings → Arena webhook) and the organization API key (event center → Arena) are entered
 * by the owner in the admin. This script only writes non-secret values.
 */

if ( ! defined( 'ABSPATH' ) ) {
	require getcwd() . '/wp-load.php';
}

$mode = $args[0] ?? ( $argv[1] ?? 'dry' );
$apply = $mode === 'apply';
$rollback = $mode === 'rollback';

const VINO_CUT_BACKUP = 'vino_cutover_backup_20260924';
const VINO_CUT_ARENA_URL = 'https://api.arenasoldout.com/compat/bil24/json';
const VINO_CUT_ARENA_BASE = 'https://api.arenasoldout.com';
const VINO_CUT_HOME_PAGE = 495;          // RU home: product grid 82139f0 + carousel 47f96b55
const VINO_CUT_GRID_ID = '82139f0';

$org_id = getenv( 'VINO_ARENA_ORG_ID' ) ?: '';   // the new prod org id, known after it is created
$fid    = getenv( 'VINO_ARENA_FID' ) ?: '';      // the channel's gateway fid

function vino_cut_say( string $s ): void { echo $s, "\n"; }

if ( $rollback ) {
	$b = get_option( VINO_CUT_BACKUP );
	if ( ! is_array( $b ) ) { vino_cut_say( 'no backup found, nothing to roll back' ); return; }
	foreach ( $b['options'] as $name => $value ) {
		if ( $value === null ) delete_option( $name ); else update_option( $name, $value, false );
		vino_cut_say( "restored option $name" );
	}
	if ( isset( $b['elementor_495'] ) ) {
		update_post_meta( VINO_CUT_HOME_PAGE, '_elementor_data', wp_slash( $b['elementor_495'] ) );
		vino_cut_say( 'restored _elementor_data of page 495' );
	}
	if ( class_exists( '\Elementor\Plugin' ) ) \Elementor\Plugin::$instance->files_manager->clear_cache();
	return;
}

$backup = [ 'taken_at' => gmdate( 'c' ), 'options' => [] ];

// 1. Ticket backend: Bil24 → Arena gateway (token/fid entered by the owner in the settings page).
$sync = get_option( 'bil24_acf_sync', [] );
$sync = is_array( $sync ) ? $sync : [];
$backup['options']['bil24_acf_sync'] = $sync;
$new_sync = $sync;
$new_sync['base_url_prod'] = VINO_CUT_ARENA_URL;
$new_sync['environment']   = 'prod';
$new_sync['locale']        = 'en';          // as tested on staging: the gateway answers English
if ( $fid !== '' ) $new_sync['fid'] = (int) $fid;
vino_cut_say( sprintf( '[bil24_acf_sync] base_url_prod: %s → %s; environment: %s → prod; fid: %s → %s',
	$sync['base_url_prod'] ?? '∅', VINO_CUT_ARENA_URL, $sync['environment'] ?? '∅', $sync['fid'] ?? '∅', $fid !== '' ? $fid : '(unchanged, owner enters)' ) );

// 2. Event center → Arena tab (API key is entered by the owner).
$la = get_option( 'lops_arena', [] );
$la = is_array( $la ) ? $la : [];
$backup['options']['lops_arena'] = $la ?: null;
$new_la = $la;
$new_la['base_url'] = VINO_CUT_ARENA_BASE;
if ( $org_id !== '' ) $new_la['org_id'] = $org_id;
$new_la['organizer_name'] = 'Vino&Co';
vino_cut_say( sprintf( '[lops_arena] base_url=%s org_id=%s organizer_name=Vino&Co (api_key: %s)',
	VINO_CUT_ARENA_BASE, $org_id !== '' ? $org_id : '(VINO_ARENA_ORG_ID not set)', ! empty( $la['api_key'] ) ? 'already set' : 'owner enters' ) );

// 3. WPML: products/variations/categories/tags fall back to the Russian original, so the
//    Hebrew and English event pages are not empty (2026 events exist only in Russian).
$icl = get_option( 'icl_sitepress_settings', [] );
$backup['options']['icl_sitepress_settings'] = $icl;
$cp = $icl['custom_posts_sync_option'] ?? [];
$tx = $icl['taxonomies_sync_option'] ?? [];
vino_cut_say( sprintf( '[WPML] product %s→2, product_variation %s→2, product_cat %s→2, product_tag %s→2',
	$cp['product'] ?? '∅', $cp['product_variation'] ?? '∅', $tx['product_cat'] ?? '∅', $tx['product_tag'] ?? '∅' ) );
$cp['product'] = 2; $cp['product_variation'] = 2; $tx['product_cat'] = 2; $tx['product_tag'] = 2;

// 4. RU home page: the product grid is hidden on tablets too (the carousel shows there).
$raw  = get_post_meta( VINO_CUT_HOME_PAGE, '_elementor_data', true );
$data = json_decode( is_string( $raw ) ? $raw : wp_json_encode( $raw ), true );
$backup['elementor_495'] = is_string( $raw ) ? $raw : wp_json_encode( $raw );
$hit = 0;
$walk = function ( &$els ) use ( &$walk, &$hit ) {
	foreach ( $els as &$e ) {
		if ( ( $e['id'] ?? '' ) === VINO_CUT_GRID_ID ) { $e['settings']['hide_tablet'] = 'hidden-tablet'; $hit++; }
		if ( ! empty( $e['elements'] ) ) $walk( $e['elements'] );
	}
};
if ( is_array( $data ) ) $walk( $data );
vino_cut_say( "[Elementor] page 495: grid " . VINO_CUT_GRID_ID . " hide_tablet — elements found: $hit (expected 1)" );

if ( ! $apply ) { vino_cut_say( 'dry run — nothing written. Re-run with "apply".' ); return; }

if ( get_option( VINO_CUT_BACKUP ) === false ) add_option( VINO_CUT_BACKUP, $backup, '', 'no' );
else vino_cut_say( 'backup already exists — kept the FIRST one (the pre-cutover state)' );

update_option( 'bil24_acf_sync', $new_sync );
update_option( 'lops_arena', $new_la, false );
global $sitepress;
if ( $sitepress ) {
	$sitepress->set_setting( 'custom_posts_sync_option', $cp );
	$sitepress->set_setting( 'taxonomies_sync_option', $tx );
	$sitepress->save_settings();
}
if ( $hit === 1 ) update_post_meta( VINO_CUT_HOME_PAGE, '_elementor_data', wp_slash( wp_json_encode( $data ) ) );
if ( class_exists( '\Elementor\Plugin' ) ) \Elementor\Plugin::$instance->files_manager->clear_cache();
wp_cache_flush();
vino_cut_say( 'applied. Backup in option ' . VINO_CUT_BACKUP . '; roll back with "rollback".' );
