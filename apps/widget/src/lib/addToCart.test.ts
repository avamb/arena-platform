import { describe, it, expect } from 'vitest';
import {
  initialAddToCartState,
  newAddToCartId,
  seatsConsidered,
  trackCartChange,
  withAddToCart,
  MAX_SEATS_CONSIDERED,
} from './addToCart.js';
import type { BuyerContextPayload } from './buyerContext.js';

const yes: BuyerContextPayload = { tracking_consent: true, tracking_consent_text_version: 'v1', fbp: 'fb.1.1.2' };
const no: BuyerContextPayload = { tracking_consent: false, tracking_consent_text_version: 'v1' };
const base = { session_id: 's-1', seats: ['A-1'] };

function ids() {
  let n = 0;
  return () => `cart-${++n}`;
}

describe('addToCartId lifecycle', () => {
  it('is created when the cart first gets items and kept while it has some', () => {
    const make = ids();
    let s = initialAddToCartState();
    s = trackCartChange(s, 0, [], make);
    expect(s.id).toBeNull();
    s = trackCartChange(s, 1, ['A-1'], make);
    expect(s.id).toBe('cart-1');
    s = trackCartChange(s, 2, ['A-1', 'A-2'], make);
    expect(s.id).toBe('cart-1');
  });

  it('is reset when the cart empties, and a new cart gets a new id', () => {
    const make = ids();
    let s = trackCartChange(initialAddToCartState(), 1, ['A-1'], make);
    s = trackCartChange(s, 0, [], make);
    expect(s.id).toBeNull();
    expect(s.seats.size).toBe(0);
    s = trackCartChange(s, 1, ['B-1'], make);
    expect(s.id).toBe('cart-2');
  });

  it('is reset after a checkout starts (the caller starts from the initial state)', () => {
    const make = ids();
    trackCartChange(initialAddToCartState(), 1, ['A-1'], make);
    const after = trackCartChange(initialAddToCartState(), 1, ['A-1'], make);
    expect(after.id).toBe('cart-2');
  });

  it('makes ids the platform accepts', () => {
    const id = newAddToCartId();
    expect(id).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    expect(newAddToCartId()).not.toBe(id);
  });
});

describe('seats_considered', () => {
  it('counts distinct seats toggled, including ones taken off again', () => {
    const make = ids();
    let s = trackCartChange(initialAddToCartState(), 1, ['A-1'], make);
    s = trackCartChange(s, 2, ['A-1', 'A-2'], make);
    s = trackCartChange(s, 1, ['A-2'], make); // A-1 taken off
    s = trackCartChange(s, 2, ['A-2', 'A-1'], make); // and back: still distinct
    expect(seatsConsidered(s, new Map())).toBe(2);
  });

  it('counts GA quantity, not seat keys', () => {
    const s = trackCartChange(initialAddToCartState(), 5, [], ids());
    expect(seatsConsidered(s, new Map([['tier-1', 3], ['tier-2', 2]]))).toBe(5);
  });

  it('is capped at the platform bound', () => {
    const s = trackCartChange(initialAddToCartState(), 1, [], ids());
    expect(seatsConsidered(s, new Map([['t', 5000]]))).toBe(MAX_SEATS_CONSIDERED);
  });
});

describe('withAddToCart', () => {
  const s = trackCartChange(initialAddToCartState(), 1, ['A-1'], ids());

  it('adds buyer_context, add_to_cart_id and seats_considered only with an explicit yes', () => {
    expect(withAddToCart(base, yes, s, new Map())).toEqual({
      ...base,
      buyer_context: yes,
      add_to_cart_id: 'cart-1',
      seats_considered: 1,
    });
  });

  it('leaves the payload alone on a no, without an answer, or without a cart id', () => {
    expect(withAddToCart(base, no, s, new Map())).toBe(base);
    expect(withAddToCart(base, null, s, new Map())).toBe(base);
    expect(withAddToCart(base, yes, initialAddToCartState(), new Map())).toBe(base);
  });

  it('omits seats_considered when nothing was considered', () => {
    const empty = { id: 'x', seats: new Set<string>() };
    expect(withAddToCart(base, yes, empty, new Map())).not.toHaveProperty('seats_considered');
  });
});
