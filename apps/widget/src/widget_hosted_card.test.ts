/**
 * Guards the two things the hosted promoter page needs from the widget so
 * a date renders as ONE card (2026-09-21).
 *
 * The page draws the box — date, title, venue, the event's own words — and
 * mounts the picker inside it. Before this the widget drew its own frame in
 * there, so a single master class read as two stacked boxes, and the yellow
 * bar under the category row restated that row ("1 ticket  €50") without
 * ever saying what pressing it does.
 *
 * Source-level assertions (`?raw`), the idiom this suite already uses for
 * component markup — see widget_order_status.test.ts.
 */

import { describe, it, expect } from 'vitest';
import widgetSource from './ArenaTickets.svelte?raw';
import miniCartSource from './components/MiniCart.svelte?raw';
import { CHECKOUT_I18N, SUPPORTED_LOCALES } from './lib/checkout.js';

describe('hosted-page card mode', () => {
  it('drops its own frame only when the embedder asks for it', () => {
    // The attribute has to be declared on the custom element, or the page
    // sets something the component never sees.
    expect(widgetSource).toContain("frame: { type: 'String', attribute: 'frame' }");
    expect(widgetSource).toContain("frame.trim().toLowerCase() === 'hidden'");
    expect(widgetSource).toContain('class:arena-tickets-frame--bare={frameHidden}');
    expect(widgetSource).toMatch(/\.arena-tickets-frame--bare\s*\{[^}]*border:\s*none/);
  });

  it('leaves the frame alone for every existing embed', () => {
    // Absent or misspelled must KEEP the frame: the widget is embedded on
    // customers' own sites, where losing the outline is a live visual
    // regression nobody asked for.
    const [, defaults = ''] = widgetSource.match(/const \{([^}]*)\}: Props = \$props\(\)/) ?? [];
    expect(defaults).toContain("frame = ''");
  });

  it('gives the cart bar a verb in that mode, and only there', () => {
    expect(widgetSource).toContain("cta={frameHidden ? t.continue_to_payment : null}");
    // Default null = the bar every other embed already has.
    expect(miniCartSource).toContain('cta = null');
    expect(miniCartSource).toContain('{#if cta}');
  });

  it('has that verb translated in every supported locale', () => {
    for (const locale of SUPPORTED_LOCALES) {
      const label = CHECKOUT_I18N[locale]?.continue_to_payment;
      expect(label, `continue_to_payment missing for ${locale}`).toBeTruthy();
      if (locale !== 'en') {
        expect(
          label,
          `continue_to_payment not translated for ${locale}`,
        ).not.toBe(CHECKOUT_I18N.en.continue_to_payment);
      }
    }
  });
});
