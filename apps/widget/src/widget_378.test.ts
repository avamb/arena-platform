/**
 * widget_378.test.ts — structural tests for PR2-22:
 *   Step 1 — SeatMapView session $effect has a generation-token stale-guard.
 *   Step 2 — WP plugin loads widget from versioned tag with SRI infrastructure.
 *   Step 3 — SeatMapView hardcoded strings replaced with i18n catalog keys.
 */

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { resolve } from 'path';
import { fileURLToPath } from 'url';
import {
  CHECKOUT_I18N,
  getCheckoutI18n,
  SUPPORTED_LOCALES,
  type CheckoutI18nStrings,
} from './lib/checkout.js';

// Resolve the WP plugin file via fs so Vite does not need to handle PHP imports.
const __dir = fileURLToPath(new URL('.', import.meta.url));
const wpWidgetSrc = readFileSync(
  resolve(__dir, '../../..', 'apps/wp-plugin/arena-events/includes/class-widget.php'),
  'utf8',
);

// ─── Step 1: SeatMapView generation-token stale-guard ────────────────────────

describe('Step 1: SeatMapView session $effect stale-guard', () => {
  it('SeatMapView.svelte declares schemaGen as a plain let (not $state)', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // Must declare schemaGen as a plain let so writes inside effects do not
    // re-trigger them.
    expect(src).toContain('let schemaGen = 0');
    expect(src).not.toMatch(/\$state\(\s*0\s*\)[\s\S]{0,100}schemaGen/);
  });

  it('SeatMapView.svelte increments schemaGen in the $effect before calling loadSchema', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // Generation is captured via: const gen = ++schemaGen;
    expect(src).toContain('const gen = ++schemaGen');
  });

  it('SeatMapView.svelte passes gen to loadSchema', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // loadSchema must accept gen parameter.
    expect(src).toContain('async function loadSchema(sessionId: string, gen: number)');
    // Called with gen from the effect.
    expect(src).toContain('loadSchema(sessionId, gen)');
  });

  it('SeatMapView.svelte checks schemaGen !== gen before applying schema state', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // After the await, bail if superseded.
    expect(src).toContain('if (schemaGen !== gen) return;');
  });

  it('SeatMapView.svelte only clears schemaLoading for the current generation', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // finally block: if (schemaGen === gen) schemaLoading = false;
    expect(src).toContain('if (schemaGen === gen) schemaLoading = false');
  });

  it('SeatMapView.svelte checks generation in the .then() callback before starting poller', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // .then() callback also checks before calling startPoller.
    expect(src).toMatch(/schemaGen !== gen[\s\S]{1,100}startPoller/);
  });
});

// ─── Step 2: WP plugin versioned tag and SRI ─────────────────────────────────

describe('Step 2: WP plugin CDN pin and SRI', () => {
  it('class-widget.php does NOT reference @master CDN URL', () => {
    expect(wpWidgetSrc).not.toContain('@master');
  });

  it('class-widget.php pins CDN to a versioned tag', () => {
    // Must reference a semantic version tag like @v0.1.0 or @v1.0.0
    expect(wpWidgetSrc).toMatch(/@v\d+\.\d+\.\d+/);
  });

  it('class-widget.php defines WIDGET_GIT_TAG constant', () => {
    expect(wpWidgetSrc).toContain('WIDGET_GIT_TAG');
  });

  it('class-widget.php defines WIDGET_DIST_SRI constant', () => {
    expect(wpWidgetSrc).toContain('WIDGET_DIST_SRI');
  });

  it('class-widget.php hooks add_sri_attributes to script_loader_tag', () => {
    expect(wpWidgetSrc).toContain("'script_loader_tag'");
    expect(wpWidgetSrc).toContain('add_sri_attributes');
  });

  it('class-widget.php add_sri_attributes injects integrity + crossorigin', () => {
    expect(wpWidgetSrc).toContain('integrity=');
    expect(wpWidgetSrc).toContain('crossorigin=');
  });

  it('class-widget.php supports widget_dist_sri settings override', () => {
    expect(wpWidgetSrc).toContain("'widget_dist_sri'");
  });
});

// ─── Step 3: SeatMapView i18n strings ────────────────────────────────────────

describe('Step 3: SeatMapView hardcoded strings in i18n catalog', () => {
  it('CheckoutI18nStrings interface has seatmap_loading', () => {
    // TypeScript compile-time check: the interface must include these keys.
    const t = getCheckoutI18n('en');
    expect(typeof t.seatmap_loading).toBe('string');
    expect(t.seatmap_loading.length).toBeGreaterThan(0);
  });

  it('CheckoutI18nStrings interface has seatmap_error_load', () => {
    const t = getCheckoutI18n('en');
    expect(typeof t.seatmap_error_load).toBe('string');
    expect(t.seatmap_error_load.length).toBeGreaterThan(0);
  });

  it('CheckoutI18nStrings interface has seatmap_aria_label', () => {
    const t = getCheckoutI18n('en');
    expect(typeof t.seatmap_aria_label).toBe('string');
    expect(t.seatmap_aria_label.length).toBeGreaterThan(0);
  });

  it('CheckoutI18nStrings interface has seatmap_controls_label', () => {
    const t = getCheckoutI18n('en');
    expect(typeof t.seatmap_controls_label).toBe('string');
    expect(t.seatmap_controls_label.length).toBeGreaterThan(0);
  });

  it('CheckoutI18nStrings interface has seatmap_fit_title and seatmap_fit_aria', () => {
    const t = getCheckoutI18n('en');
    expect(typeof t.seatmap_fit_title).toBe('string');
    expect(typeof t.seatmap_fit_aria).toBe('string');
    expect(t.seatmap_fit_title.length).toBeGreaterThan(0);
    expect(t.seatmap_fit_aria.length).toBeGreaterThan(0);
  });

  it('CheckoutI18nStrings interface has seatmap_reset_title and seatmap_reset_aria', () => {
    const t = getCheckoutI18n('en');
    expect(typeof t.seatmap_reset_title).toBe('string');
    expect(typeof t.seatmap_reset_aria).toBe('string');
    expect(t.seatmap_reset_title.length).toBeGreaterThan(0);
    expect(t.seatmap_reset_aria.length).toBeGreaterThan(0);
  });

  it('all four supported locales have seatmap translations (no locale omits them)', () => {
    for (const locale of SUPPORTED_LOCALES) {
      const t: CheckoutI18nStrings = CHECKOUT_I18N[locale];
      expect(t.seatmap_loading, `${locale}.seatmap_loading`).toBeTruthy();
      expect(t.seatmap_error_load, `${locale}.seatmap_error_load`).toBeTruthy();
      expect(t.seatmap_aria_label, `${locale}.seatmap_aria_label`).toBeTruthy();
      expect(t.seatmap_controls_label, `${locale}.seatmap_controls_label`).toBeTruthy();
      expect(t.seatmap_fit_title, `${locale}.seatmap_fit_title`).toBeTruthy();
      expect(t.seatmap_fit_aria, `${locale}.seatmap_fit_aria`).toBeTruthy();
      expect(t.seatmap_reset_title, `${locale}.seatmap_reset_title`).toBeTruthy();
      expect(t.seatmap_reset_aria, `${locale}.seatmap_reset_aria`).toBeTruthy();
    }
  });

  it('ru translations differ from en (not just copied)', () => {
    const en = CHECKOUT_I18N['en'];
    const ru = CHECKOUT_I18N['ru'];
    expect(ru.seatmap_loading).not.toBe(en.seatmap_loading);
    expect(ru.seatmap_aria_label).not.toBe(en.seatmap_aria_label);
  });

  it('cs translations differ from en', () => {
    const en = CHECKOUT_I18N['en'];
    const cs = CHECKOUT_I18N['cs'];
    expect(cs.seatmap_loading).not.toBe(en.seatmap_loading);
    expect(cs.seatmap_aria_label).not.toBe(en.seatmap_aria_label);
  });

  it('he translations differ from en', () => {
    const en = CHECKOUT_I18N['en'];
    const he = CHECKOUT_I18N['he'];
    expect(he.seatmap_loading).not.toBe(en.seatmap_loading);
    expect(he.seatmap_aria_label).not.toBe(en.seatmap_aria_label);
  });

  it('SeatMapView.svelte uses t.seatmap_loading instead of hardcoded English', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('t.seatmap_loading');
    expect(src).not.toContain('"Loading seat map…"');
    expect(src).not.toContain("'Loading seat map…'");
  });

  it('SeatMapView.svelte uses t.seatmap_aria_label for the container aria-label', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('aria-label={t.seatmap_aria_label}');
    expect(src).not.toContain('"Interactive seat map"');
  });

  it('SeatMapView.svelte uses t.seatmap_controls_label for the toolbar aria-label', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('aria-label={t.seatmap_controls_label}');
    expect(src).not.toContain('"Seat map controls"');
  });

  it('SeatMapView.svelte uses t.seatmap_fit_title and t.seatmap_fit_aria on fit button', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('title={t.seatmap_fit_title}');
    expect(src).toContain('aria-label={t.seatmap_fit_aria}');
  });

  it('SeatMapView.svelte uses t.seatmap_reset_title and t.seatmap_reset_aria on reset button', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('title={t.seatmap_reset_title}');
    expect(src).toContain('aria-label={t.seatmap_reset_aria}');
  });

  it('SeatMapView.svelte uses t.seatmap_error_load as fallback error message', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('t.seatmap_error_load');
    expect(src).not.toContain("'Failed to load seat map'");
  });

  it('SeatMapView.svelte imports getCheckoutI18n from checkout lib', async () => {
    const src = await import('./components/SeatMapView.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain("from '../lib/checkout.js'");
    expect(src).toContain('getCheckoutI18n');
    // t is derived from locale prop.
    expect(src).toContain('const t = $derived(getCheckoutI18n(locale))');
  });
});
