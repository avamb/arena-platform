/**
 * Guards the three defects the first production purchases exposed on the
 * paid-order panel (2026-09-20).
 *
 * The widget's suites assert on pure functions and on the component SOURCE
 * (imported with `?raw`) rather than rendering Svelte — these follow that
 * idiom, plus a real assertion over the i18n table.
 */

import { describe, it, expect } from 'vitest';
import orderStatusSource from './components/OrderStatus.svelte?raw';
import widgetSource from './ArenaTickets.svelte?raw';
import { CHECKOUT_I18N, SUPPORTED_LOCALES } from './lib/checkout.js';

describe('paid order panel', () => {
  // The API returns pdf_url as a root-relative path. Resolved against the
  // PAGE origin it hit the hosted page's own router, which answered "event
  // not found" — so the buyer's download silently went nowhere.
  it('resolves the PDF link against the API, not the page', () => {
    expect(orderStatusSource).toContain('href={pdfHref(ticket.pdf_url)}');
    expect(orderStatusSource).not.toContain('href={ticket.pdf_url}');
    // …and the widget has to hand the component the base to resolve with.
    expect(widgetSource).toContain('apiBase={resolvedApiBase}');
  });

  // A general-admission ticket has no seat, and the label used to fall back
  // to the ticket UUID — an internal identifier no buyer should ever see.
  it('never falls back to the ticket UUID for a seatless ticket', () => {
    expect(orderStatusSource).not.toContain("|| ticket.ticket_id");
    expect(orderStatusSource).toContain('{#if seatLabel(ticket)}');
  });

  // The panel was a dead end: the widget stays mounted on it, so a buyer
  // could not start a second purchase for the same event without reloading.
  it('offers a way back to the picker, and only once the order is paid', () => {
    expect(orderStatusSource).toContain('data-testid="buy-more"');
    expect(orderStatusSource).toContain('{t.buy_more}');
    expect(widgetSource).toContain("onDone={orderStatus.status === 'paid' ? handleDone : undefined}");
    // Leaving the panel must also refresh what is on sale, or the picker
    // would still offer the places this buyer has just bought.
    expect(widgetSource).toContain('function handleDone');
    expect(widgetSource).toMatch(/function handleDone[\s\S]*resolveAndLoadFromFeed/);
  });

  it('localizes that action in every supported locale', () => {
    for (const locale of SUPPORTED_LOCALES) {
      const label = CHECKOUT_I18N[locale]?.buy_more;
      expect(label, `buy_more missing for ${locale}`).toBeTruthy();
      if (locale !== 'en') {
        // An untranslated copy of the English string is the usual way a new
        // key ships half-done.
        expect(label, `buy_more not translated for ${locale}`).not.toBe(CHECKOUT_I18N.en.buy_more);
      }
    }
  });
});
