/**
 * Unit tests for apps/widget/src/lib/cart.ts (WID-C)
 *
 * All tests run in Node via vitest — no DOM required.
 */

import { describe, it, expect } from 'vitest';
import {
  emptyCart,
  removeCartLine,
  applyHoldResponse,
  buildSeatedLines,
  buildGaLine,
  cartTotal,
  cartItemCount,
  countdownSeconds,
  isTwoMinWarning,
  formatCountdown,
} from './cart.js';
import type { CartLineItem, CartState } from './cart.js';
import type { CategoryPrice, Tier } from '../types.js';

// ─── Fixture helpers ──────────────────────────────────────────────────────────

function makeLine(overrides: Partial<CartLineItem> = {}): CartLineItem {
  return {
    type: 'seated',
    tierId: 'tier-1',
    tierName: 'Standard',
    quantity: 1,
    priceAmount: 1000,
    currency: 'RUB',
    seatKeys: ['A-1-1'],
    zoneKey: '',
    ...overrides,
  };
}

function makeCart(overrides: Partial<CartState> = {}): CartState {
  return {
    checkoutToken: null,
    expiresAt: null,
    lines: [],
    ...overrides,
  };
}

// ─── emptyCart ────────────────────────────────────────────────────────────────

describe('emptyCart', () => {
  it('returns a cart with no lines', () => {
    const c = emptyCart();
    expect(c.lines).toHaveLength(0);
  });

  it('returns null checkoutToken', () => {
    expect(emptyCart().checkoutToken).toBeNull();
  });

  it('returns null expiresAt', () => {
    expect(emptyCart().expiresAt).toBeNull();
  });

  it('returns a new object each call', () => {
    expect(emptyCart()).not.toBe(emptyCart());
  });
});

// ─── removeCartLine ───────────────────────────────────────────────────────────

describe('removeCartLine', () => {
  it('removes the line at the given index', () => {
    const state = makeCart({
      lines: [makeLine({ tierName: 'Standard' }), makeLine({ tierName: 'VIP' })],
    });
    const next = removeCartLine(state, 0);
    expect(next.lines).toHaveLength(1);
    expect(next.lines[0].tierName).toBe('VIP');
  });

  it('removes the last line', () => {
    const state = makeCart({ lines: [makeLine()] });
    const next = removeCartLine(state, 0);
    expect(next.lines).toHaveLength(0);
  });

  it('ignores out-of-range index (too high)', () => {
    const state = makeCart({ lines: [makeLine()] });
    const next = removeCartLine(state, 5);
    expect(next).toBe(state); // same reference — no change
  });

  it('ignores negative index', () => {
    const state = makeCart({ lines: [makeLine()] });
    const next = removeCartLine(state, -1);
    expect(next).toBe(state);
  });

  it('does not mutate the original state', () => {
    const original = makeCart({ lines: [makeLine(), makeLine()] });
    removeCartLine(original, 0);
    expect(original.lines).toHaveLength(2);
  });

  it('preserves checkoutToken and expiresAt', () => {
    const state = makeCart({
      checkoutToken: 'tok_abc',
      expiresAt: '2026-07-11T12:00:00Z',
      lines: [makeLine()],
    });
    const next = removeCartLine(state, 0);
    expect(next.checkoutToken).toBe('tok_abc');
    expect(next.expiresAt).toBe('2026-07-11T12:00:00Z');
  });
});

// ─── applyHoldResponse ────────────────────────────────────────────────────────

describe('applyHoldResponse', () => {
  it('sets checkoutToken and expiresAt', () => {
    const state = emptyCart();
    const next = applyHoldResponse(state, 'tok_xyz', '2026-07-11T12:30:00Z');
    expect(next.checkoutToken).toBe('tok_xyz');
    expect(next.expiresAt).toBe('2026-07-11T12:30:00Z');
  });

  it('preserves lines', () => {
    const state = makeCart({ lines: [makeLine()] });
    const next = applyHoldResponse(state, 'tok', '2026-07-11T12:00:00Z');
    expect(next.lines).toHaveLength(1);
  });

  it('does not mutate the original state', () => {
    const state = emptyCart();
    applyHoldResponse(state, 'tok', '2026-07-11T12:00:00Z');
    expect(state.checkoutToken).toBeNull();
  });
});

// ─── buildGaLine ─────────────────────────────────────────────────────────────

describe('buildGaLine', () => {
  it('builds a GA line with the correct shape', () => {
    const line = buildGaLine('zone-A', 'tier-ga', 'GA Standing', 3, 500, 'USD');
    expect(line.type).toBe('ga');
    expect(line.zoneKey).toBe('zone-A');
    expect(line.tierId).toBe('tier-ga');
    expect(line.tierName).toBe('GA Standing');
    expect(line.quantity).toBe(3);
    expect(line.priceAmount).toBe(500);
    expect(line.currency).toBe('USD');
    expect(line.seatKeys).toEqual([]);
  });
});

// ─── buildSeatedLines ─────────────────────────────────────────────────────────

describe('buildSeatedLines', () => {
  const cp: CategoryPrice = {
    index: 0,
    name: 'Cat A',
    color: '#e11d48',
    tier_id: 'tier-a',
    tier_name: 'Standard',
    pricing_mode: 'fixed',
    price_amount: 1500,
    currency: 'RUB',
  };

  const tier: Tier = {
    id: 'tier-a',
    name: 'Standard',
    pricing_mode: 'fixed',
    price_amount: 1500,
    currency: 'RUB',
    sort_order: 1,
  };

  it('builds one line per tierId group', () => {
    const catMap = new Map([[0, cp]]);
    const tierMap = new Map([['tier-a', tier]]);
    const seatCatMap = new Map([['S1', 0], ['S2', 0]]);
    const lines = buildSeatedLines(['S1', 'S2'], catMap, tierMap, seatCatMap);
    expect(lines).toHaveLength(1);
    expect(lines[0].quantity).toBe(2);
    expect(lines[0].tierId).toBe('tier-a');
    expect(lines[0].priceAmount).toBe(1500);
  });

  it('builds two lines when seats belong to different tiers', () => {
    const cp2: CategoryPrice = { ...cp, index: 1, tier_id: 'tier-b' };
    const tier2: Tier = { ...tier, id: 'tier-b', name: 'VIP', price_amount: 5000 };
    const catMap = new Map([[0, cp], [1, cp2]]);
    const tierMap = new Map([['tier-a', tier], ['tier-b', tier2]]);
    const seatCatMap = new Map([['S1', 0], ['S2', 1]]);
    const lines = buildSeatedLines(['S1', 'S2'], catMap, tierMap, seatCatMap);
    expect(lines).toHaveLength(2);
  });

  it('creates a fallback line for unmapped category', () => {
    const catMap = new Map<number, CategoryPrice>();
    const tierMap = new Map<string, Tier>();
    const seatCatMap = new Map([['S1', 99]]);
    const lines = buildSeatedLines(['S1'], catMap, tierMap, seatCatMap);
    expect(lines).toHaveLength(1);
    expect(lines[0].tierName).toBe('Unknown');
    expect(lines[0].priceAmount).toBe(0);
  });

  it('uses tier price over category price hint', () => {
    const cpWithHint: CategoryPrice = { ...cp, price_amount: 9999 }; // hint differs from tier
    const catMap = new Map([[0, cpWithHint]]);
    const tierMap = new Map([['tier-a', tier]]); // tier has 1500
    const seatCatMap = new Map([['S1', 0]]);
    const lines = buildSeatedLines(['S1'], catMap, tierMap, seatCatMap);
    expect(lines[0].priceAmount).toBe(1500); // tier wins
  });

  it('includes seat keys on the line', () => {
    const catMap = new Map([[0, cp]]);
    const tierMap = new Map([['tier-a', tier]]);
    const seatCatMap = new Map([['S1', 0], ['S2', 0]]);
    const lines = buildSeatedLines(['S1', 'S2'], catMap, tierMap, seatCatMap);
    expect(lines[0].seatKeys).toContain('S1');
    expect(lines[0].seatKeys).toContain('S2');
  });
});

// ─── cartTotal ────────────────────────────────────────────────────────────────

describe('cartTotal', () => {
  it('returns 0 amount and empty currency for empty lines', () => {
    expect(cartTotal([])).toEqual({ amount: 0, currency: '' });
  });

  it('computes single-line total', () => {
    const line = makeLine({ priceAmount: 2000, quantity: 1 });
    expect(cartTotal([line])).toEqual({ amount: 2000, currency: 'RUB' });
  });

  it('multiplies priceAmount by quantity', () => {
    const line = makeLine({ priceAmount: 500, quantity: 4 });
    expect(cartTotal([line])).toEqual({ amount: 2000, currency: 'RUB' });
  });

  it('sums across multiple lines', () => {
    const lines = [
      makeLine({ priceAmount: 1000, quantity: 2 }),
      makeLine({ priceAmount: 500, quantity: 3 }),
    ];
    expect(cartTotal(lines)).toEqual({ amount: 3500, currency: 'RUB' });
  });

  it('uses the currency of the first line', () => {
    const lines = [
      makeLine({ priceAmount: 100, currency: 'EUR' }),
      makeLine({ priceAmount: 200, currency: 'USD' }),
    ];
    expect(cartTotal(lines).currency).toBe('EUR');
  });
});

// ─── cartItemCount ────────────────────────────────────────────────────────────

describe('cartItemCount', () => {
  it('returns 0 for empty lines', () => {
    expect(cartItemCount([])).toBe(0);
  });

  it('sums quantity across all lines', () => {
    const lines = [makeLine({ quantity: 2 }), makeLine({ quantity: 3 })];
    expect(cartItemCount(lines)).toBe(5);
  });

  it('returns quantity for single line', () => {
    expect(cartItemCount([makeLine({ quantity: 4 })])).toBe(4);
  });
});

// ─── countdownSeconds ─────────────────────────────────────────────────────────

describe('countdownSeconds', () => {
  it('returns whole seconds remaining', () => {
    const now = Date.now();
    const expiresAt = new Date(now + 65_000).toISOString();
    const secs = countdownSeconds(expiresAt, now);
    expect(secs).toBe(65);
  });

  it('returns 0 when already expired', () => {
    const past = new Date(Date.now() - 5000).toISOString();
    expect(countdownSeconds(past)).toBe(0);
  });

  it('returns 0 for invalid date string', () => {
    expect(countdownSeconds('not-a-date', Date.now())).toBe(0);
  });

  it('floors fractional seconds', () => {
    const now = 1_000_000_000;
    const expiresAt = new Date(now + 61_900).toISOString();
    expect(countdownSeconds(expiresAt, now)).toBe(61);
  });

  it('accepts a nowMs parameter for deterministic tests', () => {
    const now = 0;
    const expiresAt = new Date(120_000).toISOString();
    expect(countdownSeconds(expiresAt, now)).toBe(120);
  });
});

// ─── isTwoMinWarning ─────────────────────────────────────────────────────────

describe('isTwoMinWarning', () => {
  it('returns true at exactly 120 seconds', () => {
    expect(isTwoMinWarning(120)).toBe(true);
  });

  it('returns true at 1 second', () => {
    expect(isTwoMinWarning(1)).toBe(true);
  });

  it('returns false at 121 seconds', () => {
    expect(isTwoMinWarning(121)).toBe(false);
  });

  it('returns false at 0 seconds (expired)', () => {
    expect(isTwoMinWarning(0)).toBe(false);
  });

  it('returns false at large remaining time', () => {
    expect(isTwoMinWarning(600)).toBe(false);
  });
});

// ─── formatCountdown ─────────────────────────────────────────────────────────

describe('formatCountdown', () => {
  it('formats 0 as 0:00', () => {
    expect(formatCountdown(0)).toBe('0:00');
  });

  it('formats 65 as 1:05', () => {
    expect(formatCountdown(65)).toBe('1:05');
  });

  it('formats 120 as 2:00', () => {
    expect(formatCountdown(120)).toBe('2:00');
  });

  it('formats 599 as 9:59', () => {
    expect(formatCountdown(599)).toBe('9:59');
  });

  it('pads seconds to 2 digits', () => {
    expect(formatCountdown(61)).toBe('1:01');
  });

  it('handles negative values by clamping to 0', () => {
    expect(formatCountdown(-10)).toBe('0:00');
  });

  it('floors fractional seconds', () => {
    expect(formatCountdown(61.9)).toBe('1:01');
  });
});
