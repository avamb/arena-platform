/**
 * checkout="one-step": the cart sheet shows the cart lines, the money rows and
 * the buyer form with the pay button on ONE screen. Without it the cart screen
 * keeps its "Continue to payment" button and no buyer form.
 */

import { describe, it, expect } from 'vitest';
import { render } from 'svelte/server';
import CartSheet from './components/CartSheet.svelte';
import widgetSource from './ArenaTickets.svelte?raw';
import { getCheckoutI18n } from './lib/checkout.js';

const t = getCheckoutI18n('en');

const cart = {
  checkoutToken: 'tok',
  expiresAt: null,
  lines: [
    { type: 'ga', tierId: 't1', tierName: 'Standing', quantity: 2, priceAmount: 2500, currency: 'EUR', seatKeys: [], zoneKey: 'z' },
  ],
};

function sheetHtml(extra: Record<string, unknown>): string {
  return render(CartSheet, {
    props: { cart, buyerFields: [], onClose: () => {}, onRemoveLine: () => {}, onCheckout: () => {}, ...extra },
  }).body;
}

describe('one-step checkout in the cart sheet', () => {
  it('shows lines, total and the buyer form on one screen without the continue button', () => {
    const html = sheetHtml({ oneStep: true });
    expect(html).toContain('Standing');
    expect(html).toContain('total-row');
    expect(html).toContain('type="email"');
    expect(html).toContain('<button type="submit"');
    expect(html).toContain(t.cart_details_title);
    expect(html).not.toContain('cta-btn');
    expect(html).not.toContain(t.cart_back);
    expect(html.indexOf('total-row')).toBeLessThan(html.indexOf('type="email"'));
  });

  it('keeps the two-screen flow without the attribute', () => {
    const html = sheetHtml({});
    expect(html).toContain('cta-btn');
    expect(html).toContain(t.submit_label);
    expect(html).toContain('total-row');
    expect(html).not.toContain('type="email"');
  });

  it('reads the checkout attribute in the widget', () => {
    expect(widgetSource).toContain("checkout: { type: 'String', attribute: 'checkout' }");
    expect(widgetSource).toContain("checkout.trim().toLowerCase() === 'one-step'");
  });
});
