/**
 * Guards the wiring of the on-page payment screen (spec 65 E4). Source-level
 * assertions (`?raw`), the idiom this suite uses for component markup.
 */

import { describe, it, expect } from 'vitest';
import widgetSource from './ArenaTickets.svelte?raw';
import screenSource from './components/PaymentScreen.svelte?raw';
import controllerSource from './lib/paymentController.ts?raw';
import stripeSource from './lib/stripeElements.ts?raw';
import { buildCheckoutPayload } from './lib/checkout.js';
import { ARENA_EVENTS } from './lib/events.js';

describe('checkout/start', () => {
  it('announces that the widget can pay on its own page', () => {
    expect(widgetSource).toContain("payload.capabilities = ['stripe_elements']");
  });

  it('keeps the classic redirect when the answer has no payment block', () => {
    // The hosted branch is still there, after the embedded one.
    expect(widgetSource).toMatch(/const embedded = response\.payment;[\s\S]{1,900}const redirectUrl = \(response\.redirect_url/);
    expect(widgetSource).toContain('window.location.href = redirectUrl');
  });

  it('does not add the capability to the pure payload builder (the widget adds it at send time)', () => {
    const p = buildCheckoutPayload('s', { email: 'a@b.cz', name: '', phone: '' }, ['A1'], [], []);
    expect(p.capabilities).toBeUndefined();
  });
});

describe('the client secret', () => {
  const all = { widgetSource, screenSource, controllerSource, stripeSource };

  it('is never written to a storage', () => {
    for (const [name, src] of Object.entries(all)) {
      // Strip comments: the rule is allowed to be written down.
      const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
      expect(code, `${name} touches a storage`).not.toMatch(/sessionStorage|localStorage|document\.cookie/);
    }
  });

  it('is never logged', () => {
    for (const [name, src] of Object.entries({ screenSource, controllerSource, stripeSource })) {
      expect(src, `${name} logs`).not.toMatch(/console\.(log|info|warn|error|debug)/);
    }
    // The root component has two older feed-loading warnings; none may touch payment.
    for (const line of widgetSource.split('\n').filter((l) => /console\./.test(l))) {
      expect(line).not.toMatch(/payment|secret|client_secret/i);
    }
  });

  it('is dropped when the screen is left', () => {
    expect(widgetSource).toContain('function clearPaymentBlock()');
    // Retry, done and finish all clear it.
    expect(widgetSource.match(/clearPaymentBlock\(\)/g)!.length).toBeGreaterThanOrEqual(4);
  });

  it('is not what the widget stores as the resumable token', () => {
    const remembered = widgetSource.match(/rememberCheckoutToken\([^)]*\)/g) ?? [];
    for (const call of remembered) expect(call).not.toMatch(/client_secret|payment/);
  });
});

describe('Stripe.js', () => {
  it('is loaded from js.stripe.com only', () => {
    const urls = stripeSource.match(/https?:\/\/[^\s'"`)]+/g) ?? [];
    const external = urls.filter((u) => !u.includes('stripe.com/dahlia/stripe.js') && !/docs\.stripe\.com/.test(u));
    expect(external).toEqual([]);
  });

  it('uses the checkout elements SDK, not initCheckout, and confirms without a forced redirect', () => {
    expect(stripeSource).toContain('initCheckoutElementsSdk');
    expect(stripeSource).not.toMatch(/\.initCheckout\(/);
    expect(controllerSource).toContain("redirect: 'if_required'");
    expect(stripeSource).toContain("theme: 'stripe'");
  });

  it('mounts the wallets above the card form', () => {
    const e = controllerSource.indexOf('createExpressCheckoutElement');
    const p = controllerSource.indexOf('createPaymentElement');
    expect(e).toBeGreaterThan(0);
    expect(p).toBeGreaterThan(e);
    const screen = screenSource;
    expect(screen.indexOf('bind:this={expressNode}')).toBeLessThan(screen.indexOf('bind:this={paymentNode}'));
  });
});

describe('the screen', () => {
  it('shows the amount on the pay button, a sticky bar and the organizer line', () => {
    expect(screenSource).toContain('payButtonLabel(');
    expect(screenSource).toContain('position: sticky');
    expect(screenSource).toContain('data-testid="payment-organizer"');
  });

  it('uses the stage "paying" and resumes a pending order from the platform', () => {
    expect(widgetSource).toContain("stage = 'paying'");
    expect(widgetSource).toContain('{ includePayment: true }');
  });

  it('hides Back and the time-up message once Stripe accepted the payment', () => {
    expect(screenSource).toContain('const received = $derived(ui.paymentReceived === true)');
    expect(screenSource).toContain('!busy && !received');
    expect(screenSource).toContain('s.payment_received');
  });

  it('keeps one step bus per checkout token', () => {
    expect(widgetSource).toContain('createPaymentStepsRegistry(');
    expect(widgetSource).toContain('paymentSteps.forToken(checkoutToken)');
  });

  it('drops the secret from the status object and counts down to the payment window', () => {
    expect(widgetSource).toContain('orderStatus.payment = null');
    expect(widgetSource).toContain('orderStatus.payment_expires_at ?? orderStatus.expires_at');
  });

  it('offers Back only when there is a cart to go back to', () => {
    expect(widgetSource).toContain('onBack={cart.lines.length > 0 ? backToContacts : undefined}');
    expect(screenSource).toContain('data-testid="payment-back"');
  });

  it("prints Stripe's own total on the pay button and shows the bank-return text only on return", () => {
    expect(screenSource).toContain('ui.totalText');
    expect(screenSource).toContain('{returning ? s.checking_return : received ? s.payment_received : s.confirming}');
  });

  it('turns steps into the arena:checkout host event and forwards nothing else', () => {
    expect(ARENA_EVENTS.CHECKOUT).toBe('arena:checkout');
    expect(widgetSource).toContain('dispatchWidgetEvent(host, ARENA_EVENTS.CHECKOUT');
    for (const [name, src] of Object.entries({ screenSource, controllerSource })) {
      expect(src, `${name} talks to an analytics system`).not.toMatch(/fbq|gtag|dataLayer|sendBeacon/);
    }
  });
});
