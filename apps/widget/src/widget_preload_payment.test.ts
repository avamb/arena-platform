/**
 * preload-payment="stripe" (2026-11-24): the host page says its channel pays
 * through embedded Stripe, and the widget loads Stripe.js when the checkout
 * sheet opens instead of after "Pay". Measured on staging: Stripe.js started
 * only after checkout/start answered (~0.7 s into a 5.4 s wait for the card form).
 *
 * Source-level assertions (`?raw`) for the wiring, as in widget_hosted_card.test.ts.
 */

import { describe, it, expect } from 'vitest';
import widgetSource from './ArenaTickets.svelte?raw';

describe('preload-payment attribute', () => {
  it('is declared on the custom element', () => {
    expect(widgetSource).toContain("preloadPayment: { type: 'String', attribute: 'preload-payment' }");
  });

  it('starts Stripe.js when the sheet opens, only for preload-payment="stripe"', () => {
    expect(widgetSource).toMatch(/if \(cartSheetOpen && preloadPayment\.trim\(\)\.toLowerCase\(\) === 'stripe'\) \{\s*loadStripeJs\(\)\.catch\(\(\) => \{\}\);/);
  });

  it('shares the one Stripe.js loader with the payment screen', () => {
    expect(widgetSource).toContain("import { loadStripeJs } from './lib/stripeElements.js';");
  });
});
