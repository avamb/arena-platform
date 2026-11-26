/**
 * preload-payment="stripe" (2026-11-24): the host page says its channel pays
 * through embedded Stripe, and the widget loads Stripe.js when the checkout
 * sheet opens instead of after "Pay". Measured on staging: Stripe.js started
 * only after checkout/start answered (~0.7 s into a 5.4 s wait for the card form).
 *
 * preload-payment="checkout" (same day) also prepares the order and the Stripe
 * session when the sheet opens (lib/preparedCheckout.ts, its own tests) and
 * mounts the payment screen HIDDEN at the place it is shown later.
 *
 * Source-level assertions (`?raw`) for the wiring, as in widget_hosted_card.test.ts.
 */

import { describe, it, expect } from 'vitest';
import widgetSource from './ArenaTickets.svelte?raw';
import screenSource from './components/PaymentScreen.svelte?raw';

describe('preload-payment attribute', () => {
  it('is declared on the custom element', () => {
    expect(widgetSource).toContain("preloadPayment: { type: 'String', attribute: 'preload-payment' }");
  });

  it('starts Stripe.js when the sheet opens, for "stripe" and for "checkout"', () => {
    expect(widgetSource).toContain('const preload = $derived(preloadMode(preloadPayment));');
    expect(widgetSource).toContain('loadsStripeEarly(preload, selectedSession?.embedded_payment)');
    expect(widgetSource).toMatch(/if \(cartSheetOpen && stripeEarly\) \{\s*loadStripeJs\(\)\.catch\(\(\) => \{\}\);/);
  });

  it('preconnects once the cart has a line AND the buyer acted, never on page load', () => {
    expect(widgetSource).toMatch(/if \(cartCount > 0 && stripeEarly && buyerActed\) preconnectStripe\(\);/);
    expect(widgetSource).toContain('let buyerActed = $state(false);');
    expect(widgetSource).toContain("el.addEventListener('pointerdown', mark);");
    expect(widgetSource).toContain("el.addEventListener('keydown', mark);");
  });

  it('shares the one Stripe.js loader with the payment screen', () => {
    expect(widgetSource).toContain("import { loadStripeJs, preconnectStripe } from './lib/stripeElements.js';");
  });
});

describe('preload-payment="checkout"', () => {
  it('prepares only for "checkout", an open sheet and a plain cart', () => {
    const want = widgetSource.slice(widgetSource.indexOf('const prepareWanted = $derived('));
    const block = want.slice(0, want.indexOf('});') + 3);
    expect(block).toContain("preload === 'checkout'");
    expect(block).toContain('cartSheetOpen');
    expect(block).toContain("stage === 'selecting'");
    expect(block).toContain('!checkoutToken');
    expect(block).toMatch(/isPlainCart\(\{[\s\S]*promoCode: promo\.code[\s\S]*marketingTicked[\s\S]*total: cartTotal\(cart\.lines\)\.amount/);
  });

  it('never puts a buyer, a promo code, the marketing tick or the consent on the prepare', () => {
    const src = widgetSource.replace(/\r/g, '');
    const start = src.indexOf('function buildPreparePayload()');
    const body = src.slice(start, src.indexOf('\n  }\n', start) + 4);
    expect(body).toContain('prepare: true');
    expect(body).toContain("capabilities: ['stripe_elements']");
    // (`consent: trackingConsent` is the tracking context's answer, not the checkout consent.)
    expect(body).not.toMatch(/holder_email|buyer:|promo_code|marketing_consent|payload\.consent|consentPayload/);
  });

  it('feeds every change of the sheet, the cart, the code or the tick to the manager', () => {
    expect(widgetSource).toMatch(/const want = prepareWanted;\s*const key = cartKey;\s*untrack\(\(\) => preparedCtl\.update\(want, key\)\);/);
  });

  it('cancels on pagehide and when the widget goes away', () => {
    expect(widgetSource).toContain("window.addEventListener('pagehide', onPageHide)");
    expect(widgetSource).toContain('preparedCtl.pageHide()');
    expect(widgetSource).toContain('preparedCtl.destroy()');
    // Spec 67: with a hold the cancel keeps the seats in it (keep_hold); without one it is the old cancel.
    expect(widgetSource).toContain('postCheckoutCancel(token, resolvedApiBase, { keepalive: true, keepHold: holdView.holdToken !== null })');
  });

  it('counts the sheet down to the prepared hold', () => {
    expect(widgetSource).toContain('if (!checkoutToken) holdExpiresAt = p ? p.expiresAt : null;');
  });

  it('tries the prepared checkout first at "Pay" and keeps the classic path after it', () => {
    const pay = widgetSource.indexOf('async function handleCheckout(');
    const prepared = widgetSource.indexOf('handled = await payPrepared(values);', pay);
    const classic = widgetSource.indexOf('const response = await postCheckoutStart(normFeedToken, payload, resolvedApiBase);', pay);
    expect(prepared).toBeGreaterThan(pay);
    expect(classic).toBeGreaterThan(prepared);
  });

  it('on attach: remembers the token, tells the host page and shows the screen in one step', () => {
    const start = widgetSource.indexOf('async function payPrepared(');
    const fn = widgetSource.slice(start, widgetSource.indexOf('async function handleCheckout(', start));
    const order = [
      'preparedCtl.consume(p.token)',
      'rememberCheckoutToken(p.token)',
      'ARENA_EVENTS.PAYMENT_STARTED',
      'paymentBlock = p.block',
      "stage = 'paying'",
    ].map((s) => fn.indexOf(s));
    for (const i of order) expect(i).toBeGreaterThan(0);
    expect([...order].sort((a, b) => a - b)).toEqual(order);
    // A 422 of the attach shows on the sheet like checkout/start's refusals.
    expect(fn).toContain('showCheckoutError(outcome.error, values)');
  });
});

describe('the one payment screen', () => {
  it('is rendered once, outside the stage chain, keyed by the checkout token', () => {
    expect(widgetSource.match(/<PaymentScreen\b/g)).toHaveLength(1);
    expect(widgetSource).toContain('{#key payView.token}');
    // After the stage chain: the chain's last branch closes before it.
    expect(widgetSource.indexOf('{#if payView}')).toBeGreaterThan(widgetSource.lastIndexOf('<!-- No feed-token provided -->'));
  });

  it('is hidden by a class on the same wrapper: off-screen, invisible, aria-hidden, inert, never display:none', () => {
    expect(widgetSource).toContain('class:arena-pay-slot--hidden={!payView.shown}');
    expect(widgetSource).toContain("aria-hidden={payView.shown ? undefined : 'true'}");
    expect(widgetSource).toContain('inert={!payView.shown}');
    const css = widgetSource.slice(widgetSource.indexOf('.arena-pay-slot--hidden {'));
    const rule = css.slice(0, css.indexOf('}'));
    expect(rule).toContain('visibility: hidden');
    expect(rule).toContain('left: -10000px');
    expect(rule).not.toContain('display: none');
  });

  it('is deferred only for a prepared checkout, shown for stage "paying"', () => {
    expect(widgetSource).toMatch(/if \(stage === 'paying' && paymentBlock && checkoutToken\) \{\s*return \{\s*token: checkoutToken,\s*block: paymentBlock,\s*shown: true,\s*deferred: false,/);
    expect(widgetSource).toMatch(/if \(preparedView && stage === 'selecting'\) \{\s*return \{\s*token: preparedView\.token,\s*block: preparedView\.block,\s*shown: false,\s*deferred: true,/);
  });

  it('hands its reveal handle over and reads its token and mode once, on mount', () => {
    expect(widgetSource).toContain('onHandle={preparedCtl.setHandle}');
    expect(widgetSource).toContain('onDeferredFailure={preparedCtl.markFailed}');
    expect(screenSource).toContain('mountedDeferred = deferred;');
    expect(screenSource).toContain('deferred: mountedDeferred,');
    expect(screenSource).toContain('controller.reveal(email)');
  });

  it('tells its controller when it is shown, so the form view waits for the switch to the screen', () => {
    expect(widgetSource).toContain('shown={payView.shown}');
    expect(screenSource).toMatch(/\$effect\(\(\) => \{\s*if \(shown\) controller\?\.markShown\(\);\s*\}\);/);
    expect(screenSource).toContain('if (shown) controller.markShown();');
  });
});
