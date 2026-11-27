import { describe, it, expect, vi, afterEach } from 'vitest';
import { ADD_TO_CART_DELAY_MS, createAddToCartScheduler, initialAddToCartState, trackCartChange, withAddToCart } from './addToCart.js';
import { createPaymentInfoNotifier } from './paymentInfo.js';
import type { BuyerContextPayload } from './buyerContext.js';
import widgetSource from '../ArenaTickets.svelte?raw';

const yes: BuyerContextPayload = { tracking_consent: true, tracking_consent_text_version: 'v1' };
const base = { session_id: 's-1', seats: ['A-1'] };

afterEach(() => vi.useRealTimers());

describe('AddToCart debounce (spec 68)', () => {
  it('fires 1.5 s after the last change, once, and a change restarts the wait', () => {
    vi.useFakeTimers();
    const fire = vi.fn();
    const s = createAddToCartScheduler(fire);
    expect(ADD_TO_CART_DELAY_MS).toBe(1500);
    s.schedule();
    vi.advanceTimersByTime(1000);
    s.schedule();
    vi.advanceTimersByTime(1000);
    expect(fire).not.toHaveBeenCalled();
    vi.advanceTimersByTime(500);
    expect(fire).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(5000);
    expect(fire).toHaveBeenCalledTimes(1);
  });

  it('cancel drops a pending run', () => {
    vi.useFakeTimers();
    const fire = vi.fn();
    const s = createAddToCartScheduler(fire);
    s.schedule();
    s.cancel();
    vi.advanceTimersByTime(3000);
    expect(fire).not.toHaveBeenCalled();
  });
});

describe('InitiateCheckout quote (spec 68)', () => {
  const st = trackCartChange(initialAddToCartState(), 1, ['A-1'], () => 'cart-1');
  it('carries cart_event only for initiate_checkout', () => {
    expect(withAddToCart(base, yes, st, new Map(), 'initiate_checkout')).toMatchObject({
      add_to_cart_id: 'cart-1',
      cart_event: 'initiate_checkout',
    });
    expect(withAddToCart(base, yes, st, new Map(), 'add_to_cart')).not.toHaveProperty('cart_event');
    expect(withAddToCart(base, yes, st, new Map())).not.toHaveProperty('cart_event');
  });

  it('the widget sends InitiateCheckout from the continue button once, and promo/marketing quotes stay plain', () => {
    expect(widgetSource).toMatch(/function openCartSheet\(\): void \{\s*cartSheetOpen = true;\s*sendInitiateCheckout\(\);/);
    expect(widgetSource).toContain('initiateCheckoutSentFor === addToCart.id');
    const plain = widgetSource.match(/quote: \(payload, signal\) => (\w+)\(payload, signal\)/g) ?? [];
    expect(plain.length).toBe(2);
    for (const p of plain) expect(p).toContain('postPlainQuote');
  });
});

describe('AddPaymentInfo (spec 68)', () => {
  it('posts once per token on payment_details_complete, with consent only', async () => {
    const send = vi.fn().mockResolvedValue(undefined);
    let granted = true;
    const n = createPaymentInfoNotifier({ granted: () => granted, send });
    n.onStep('payment_method_selected', 'tok');
    expect(send).not.toHaveBeenCalled();
    n.onStep('payment_details_complete', 'tok');
    n.onStep('payment_details_complete', 'tok');
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith('tok');
    n.onStep('payment_details_complete', 'tok2');
    expect(send).toHaveBeenCalledTimes(2);
    granted = false;
    n.onStep('payment_details_complete', 'tok3');
    expect(send).toHaveBeenCalledTimes(2);
  });

  it('swallows a failed request', async () => {
    const send = vi.fn().mockRejectedValue(new Error('boom'));
    const n = createPaymentInfoNotifier({ granted: () => true, send });
    expect(() => n.onStep('payment_details_complete', 't')).not.toThrow();
    await Promise.resolve();
  });
});
