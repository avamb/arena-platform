import { describe, it, expect } from 'vitest';
import { firstPurchasableGaTier, gaPreselection, identifyGaTiers, planlessGaTiers } from './store.js';
import type { Tier } from '../types.js';

function tier(id: string, over: Partial<Tier> = {}): Tier {
  return {
    id,
    name: id,
    pricing_mode: 'fixed',
    price_amount: 1000,
    currency: 'EUR',
    sort_order: 0,
    ...over,
  };
}

describe('GA preselection', () => {
  it('preselects quantity 1 on the first purchasable tier', () => {
    const pre = gaPreselection([tier('a', { available: 5 }), tier('b', { available: 9 })], new Map(), 0);
    expect(pre).toEqual(new Map([['a', 1]]));
  });

  it('skips a sold-out first tier and picks the next', () => {
    const tiers = [tier('a', { available: 0 }), tier('b', { is_open: false }), tier('c', { available: 3 })];
    expect(firstPurchasableGaTier(tiers)?.id).toBe('c');
    expect(gaPreselection(tiers, new Map(), 0)).toEqual(new Map([['c', 1]]));
  });

  it('returns null when nothing is purchasable', () => {
    expect(gaPreselection([tier('a', { available: 0 })], new Map(), 0)).toBeNull();
  });

  it('skips tiers outside their sale window', () => {
    const now = Date.parse('2026-06-01T12:00:00Z');
    const tiers = [
      tier('future', { sale_window_start: '2026-07-01T00:00:00Z' }),
      tier('past', { sale_window_end: '2026-05-01T00:00:00Z' }),
      tier('ok'),
    ];
    expect(firstPurchasableGaTier(tiers, now)?.id).toBe('ok');
  });

  it('does not override an existing selection', () => {
    expect(gaPreselection([tier('a')], new Map([['a', 2]]), 0)).toBeNull();
    expect(gaPreselection([tier('a')], new Map(), 1)).toBeNull();
  });

  it('is not re-added after the buyer resets to 0 (caller applies it once per session)', () => {
    // Simulates the component: a done-marker per session id gates the call.
    let doneFor: string | null = null;
    let qty = new Map<string, number>();
    const tiers = [tier('a', { available: 4 })];
    const run = (sessionId: string) => {
      if (doneFor === sessionId) return;
      doneFor = sessionId;
      const pre = gaPreselection(tiers, qty, 0);
      if (pre) qty = pre;
    };
    run('s1');
    expect(qty.get('a')).toBe(1);
    qty = new Map([['a', 0]]);
    run('s1');
    expect(qty.get('a')).toBe(0);
    qty = new Map();
    run('s1');
    expect(qty.size).toBe(0);
    run('s2');
    expect(qty.get('a')).toBe(1);
  });

  it('does nothing for a seated-only session (no GA tiers)', () => {
    const seatedTier = tier('seat');
    const gaTiers = identifyGaTiers([seatedTier], [{ tier_id: 'seat' } as never]);
    expect(gaTiers).toEqual([]);
    expect(gaPreselection(gaTiers, new Map(), 0)).toBeNull();
    expect(planlessGaTiers({ schema_url: 'x', tiers: [seatedTier] })).toBeNull();
  });
});
