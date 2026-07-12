/**
 * Unit tests for apps/widget/src/lib/store.ts (WID-R1)
 *
 * All tests run in Node via vitest — no DOM required.
 */

import { describe, it, expect, beforeEach } from 'vitest';
import {
  getCheckoutTokenFromSearch,
  saveCheckoutToken,
  restoreCheckoutToken,
  clearCheckoutToken,
  totalSelectionCount,
  buildGaItems,
  buildCartFromSelection,
  buildSeatCategoryIndex,
  buildCategoryByIndex,
  buildTierById,
  identifyGaTiers,
} from './store.js';
import type { Geometry, CategoryPrice, Tier, FeedSession } from '../types.js';

// ─── Mock Storage ─────────────────────────────────────────────────────────────

function makeMockStorage(): Storage {
  const store: Record<string, string> = {};
  return {
    getItem: (key: string) => store[key] ?? null,
    setItem: (key: string, value: string) => { store[key] = value; },
    removeItem: (key: string) => { delete store[key]; },
    clear: () => { for (const k of Object.keys(store)) delete store[k]; },
    key: (i: number) => Object.keys(store)[i] ?? null,
    get length() { return Object.keys(store).length; },
  };
}

// ─── Fixture helpers ──────────────────────────────────────────────────────────

function makeTier(id: string, name: string, priceAmount = 1000, currency = 'USD'): Tier {
  return { id, name, pricing_mode: 'fixed', price_amount: priceAmount, currency, sort_order: 0 };
}

function makeCategoryPrice(index: number, tierId?: string): CategoryPrice {
  return { index, name: `Cat ${index}`, color: '#ff0000', tier_id: tierId };
}

function makeSession(tiers: Tier[] = []): FeedSession {
  return {
    id: 'sess-1',
    start_at: '2026-01-01T10:00:00Z',
    end_at: '2026-01-01T12:00:00Z',
    capacity_total: 100,
    status: 'published',
    buyer_fields: [],
    tiers,
  };
}

function makeGeometry(seats: Array<{ key: string; catIdx: number }>): Geometry {
  return {
    schema_version: 1,
    canvas: { width: 800, height: 600 },
    categories: [],
    sections: [
      {
        key: 'sec-1',
        name: 'Section A',
        rows: [
          {
            key: 'row-1',
            name: 'Row 1',
            seats: seats.map(({ key, catIdx }) => ({
              key,
              number: key,
              x: 0,
              y: 0,
              radius: 8,
              category_index: catIdx,
            })),
          },
        ],
      },
    ],
    standing_zones: [],
    tables: [],
    decor_svg: '',
  };
}

// ─── getCheckoutTokenFromSearch ───────────────────────────────────────────────

describe('getCheckoutTokenFromSearch', () => {
  it('returns null when search is empty', () => {
    expect(getCheckoutTokenFromSearch('')).toBeNull();
  });

  it('returns null when checkout_token param is absent', () => {
    expect(getCheckoutTokenFromSearch('?foo=bar')).toBeNull();
  });

  it('returns null when checkout_token is empty string', () => {
    expect(getCheckoutTokenFromSearch('?checkout_token=')).toBeNull();
  });

  it('returns null when checkout_token is only whitespace', () => {
    expect(getCheckoutTokenFromSearch('?checkout_token=   ')).toBeNull();
  });

  it('returns the token when present', () => {
    expect(getCheckoutTokenFromSearch('?checkout_token=abc123')).toBe('abc123');
  });

  it('trims whitespace from the token value', () => {
    expect(getCheckoutTokenFromSearch('?checkout_token=  tok  ')).toBe('tok');
  });

  it('returns correct token when other params are present', () => {
    expect(getCheckoutTokenFromSearch('?foo=bar&checkout_token=xyz&baz=qux')).toBe('xyz');
  });

  it('handles token-only search string without leading ?', () => {
    expect(getCheckoutTokenFromSearch('checkout_token=mytoken')).toBe('mytoken');
  });
});

// ─── saveCheckoutToken / restoreCheckoutToken / clearCheckoutToken ────────────

describe('storage helpers', () => {
  let storage: Storage;

  beforeEach(() => {
    storage = makeMockStorage();
  });

  it('restoreCheckoutToken returns null when nothing is stored', () => {
    expect(restoreCheckoutToken(storage)).toBeNull();
  });

  it('saveCheckoutToken persists the token', () => {
    saveCheckoutToken('tok-1', storage);
    expect(restoreCheckoutToken(storage)).toBe('tok-1');
  });

  it('clearCheckoutToken removes the saved token', () => {
    saveCheckoutToken('tok-2', storage);
    clearCheckoutToken(storage);
    expect(restoreCheckoutToken(storage)).toBeNull();
  });

  it('saveCheckoutToken overwrites a previous token', () => {
    saveCheckoutToken('tok-old', storage);
    saveCheckoutToken('tok-new', storage);
    expect(restoreCheckoutToken(storage)).toBe('tok-new');
  });

  it('clearCheckoutToken is idempotent when nothing stored', () => {
    expect(() => clearCheckoutToken(storage)).not.toThrow();
    expect(restoreCheckoutToken(storage)).toBeNull();
  });
});

// ─── totalSelectionCount ──────────────────────────────────────────────────────

describe('totalSelectionCount', () => {
  it('returns 0 when both selection and GA are empty', () => {
    expect(totalSelectionCount(new Set(), new Map())).toBe(0);
  });

  it('counts only seated keys when GA map is empty', () => {
    expect(totalSelectionCount(new Set(['a', 'b', 'c']), new Map())).toBe(3);
  });

  it('counts only GA quantities when seat set is empty', () => {
    const ga = new Map([['tier-1', 3], ['tier-2', 2]]);
    expect(totalSelectionCount(new Set(), ga)).toBe(5);
  });

  it('sums seated + GA quantities correctly', () => {
    const ga = new Map([['tier-1', 2]]);
    expect(totalSelectionCount(new Set(['x', 'y']), ga)).toBe(4);
  });

  it('zero-qty GA entries contribute 0', () => {
    const ga = new Map([['tier-1', 0], ['tier-2', 3]]);
    expect(totalSelectionCount(new Set(['a']), ga)).toBe(4);
  });
});

// ─── buildGaItems ─────────────────────────────────────────────────────────────

describe('buildGaItems', () => {
  it('returns empty array for empty map', () => {
    expect(buildGaItems(new Map())).toEqual([]);
  });

  it('returns a single item', () => {
    expect(buildGaItems(new Map([['t1', 2]]))).toEqual([{ tier_id: 't1', quantity: 2 }]);
  });

  it('returns multiple items', () => {
    const result = buildGaItems(new Map([['t1', 1], ['t2', 3]]));
    expect(result).toHaveLength(2);
    expect(result).toContainEqual({ tier_id: 't1', quantity: 1 });
    expect(result).toContainEqual({ tier_id: 't2', quantity: 3 });
  });

  it('excludes zero-quantity entries', () => {
    const result = buildGaItems(new Map([['t1', 0], ['t2', 2]]));
    expect(result).toEqual([{ tier_id: 't2', quantity: 2 }]);
  });

  it('excludes all entries when all are zero', () => {
    expect(buildGaItems(new Map([['t1', 0]]))).toEqual([]);
  });
});

// ─── buildSeatCategoryIndex ────────────────────────────────────────────────────

describe('buildSeatCategoryIndex', () => {
  it('returns empty map for geometry with no seats', () => {
    const geo = makeGeometry([]);
    const m = buildSeatCategoryIndex(geo);
    expect(m.size).toBe(0);
  });

  it('maps each seat key to its category_index', () => {
    const geo = makeGeometry([
      { key: 'A-1-1', catIdx: 0 },
      { key: 'A-1-2', catIdx: 1 },
      { key: 'A-1-3', catIdx: 0 },
    ]);
    const m = buildSeatCategoryIndex(geo);
    expect(m.get('A-1-1')).toBe(0);
    expect(m.get('A-1-2')).toBe(1);
    expect(m.get('A-1-3')).toBe(0);
  });

  it('handles multiple sections and rows', () => {
    const geo: Geometry = {
      schema_version: 1,
      canvas: { width: 800, height: 600 },
      categories: [],
      sections: [
        {
          key: 'sec-1',
          name: 'S1',
          rows: [
            { key: 'r1', name: 'R1', seats: [{ key: 'k1', number: '1', x: 0, y: 0, radius: 8, category_index: 2 }] },
          ],
        },
        {
          key: 'sec-2',
          name: 'S2',
          rows: [
            { key: 'r2', name: 'R2', seats: [{ key: 'k2', number: '2', x: 0, y: 0, radius: 8, category_index: 3 }] },
          ],
        },
      ],
      standing_zones: [],
      tables: [],
      decor_svg: '',
    };
    const m = buildSeatCategoryIndex(geo);
    expect(m.get('k1')).toBe(2);
    expect(m.get('k2')).toBe(3);
  });
});

// ─── buildCategoryByIndex ─────────────────────────────────────────────────────

describe('buildCategoryByIndex', () => {
  it('returns empty map for empty array', () => {
    expect(buildCategoryByIndex([]).size).toBe(0);
  });

  it('maps each category by its index', () => {
    const cats = [makeCategoryPrice(0, 'tier-A'), makeCategoryPrice(1, 'tier-B')];
    const m = buildCategoryByIndex(cats);
    expect(m.get(0)?.tier_id).toBe('tier-A');
    expect(m.get(1)?.tier_id).toBe('tier-B');
  });

  it('later entries overwrite earlier ones with the same index', () => {
    const cats = [makeCategoryPrice(0, 'tier-A'), makeCategoryPrice(0, 'tier-B')];
    const m = buildCategoryByIndex(cats);
    expect(m.get(0)?.tier_id).toBe('tier-B');
  });
});

// ─── buildTierById ────────────────────────────────────────────────────────────

describe('buildTierById', () => {
  it('returns empty map for empty array', () => {
    expect(buildTierById([]).size).toBe(0);
  });

  it('maps each tier by id', () => {
    const tiers = [makeTier('t1', 'GA'), makeTier('t2', 'VIP')];
    const m = buildTierById(tiers);
    expect(m.get('t1')?.name).toBe('GA');
    expect(m.get('t2')?.name).toBe('VIP');
  });
});

// ─── buildCartFromSelection ────────────────────────────────────────────────────

describe('buildCartFromSelection', () => {
  const baseTier = makeTier('tier-1', 'Standard', 500, 'USD');
  const catPrice: CategoryPrice = { index: 0, name: 'Standard', color: '#00f', tier_id: 'tier-1' };

  function makeParams(overrides: Partial<Parameters<typeof buildCartFromSelection>[0]> = {}) {
    return {
      selectedSeatKeys: new Set<string>(),
      gaQuantities: new Map<string, number>(),
      session: makeSession([baseTier]),
      seatCategoryIndex: new Map<string, number>([['s1', 0]]),
      categoryByCategoryIndex: new Map<number, CategoryPrice>([[0, catPrice]]),
      tierById: new Map<string, typeof baseTier>([['tier-1', baseTier]]),
      ...overrides,
    };
  }

  it('returns empty cart when no selection and no GA', () => {
    const cart = buildCartFromSelection(makeParams());
    expect(cart.lines).toHaveLength(0);
    expect(cart.checkoutToken).toBeNull();
    expect(cart.expiresAt).toBeNull();
  });

  it('builds seated lines from selected keys', () => {
    const cart = buildCartFromSelection(makeParams({
      selectedSeatKeys: new Set(['s1']),
    }));
    expect(cart.lines).toHaveLength(1);
    expect(cart.lines[0]!.type).toBe('seated');
    expect(cart.lines[0]!.tierId).toBe('tier-1');
    expect(cart.lines[0]!.quantity).toBe(1);
  });

  it('builds GA line from gaQuantities', () => {
    const cart = buildCartFromSelection(makeParams({
      gaQuantities: new Map([['tier-1', 3]]),
    }));
    expect(cart.lines).toHaveLength(1);
    expect(cart.lines[0]!.type).toBe('ga');
    expect(cart.lines[0]!.quantity).toBe(3);
  });

  it('builds mixed cart (seated + GA)', () => {
    const gaTier = makeTier('tier-ga', 'GA Tier', 200, 'USD');
    const cart = buildCartFromSelection(makeParams({
      selectedSeatKeys: new Set(['s1']),
      gaQuantities: new Map([['tier-ga', 2]]),
      session: makeSession([baseTier, gaTier]),
    }));
    // Should have both seated and GA lines.
    expect(cart.lines.length).toBeGreaterThanOrEqual(1);
    const types = cart.lines.map((l) => l.type);
    expect(types).toContain('seated');
    expect(types).toContain('ga');
  });

  it('excludes GA tiers with zero quantity', () => {
    const gaTier = makeTier('tier-ga', 'GA Tier', 200, 'USD');
    const cart = buildCartFromSelection(makeParams({
      gaQuantities: new Map([['tier-ga', 0]]),
      session: makeSession([gaTier]),
    }));
    expect(cart.lines).toHaveLength(0);
  });
});

// ─── identifyGaTiers ─────────────────────────────────────────────────────────

describe('identifyGaTiers', () => {
  it('returns all tiers when categoryPrices is empty (pure GA event)', () => {
    const tiers = [makeTier('t1', 'GA'), makeTier('t2', 'VIP GA')];
    expect(identifyGaTiers(tiers, [])).toHaveLength(2);
  });

  it('returns no tiers when all are referenced in categoryPrices', () => {
    const tiers = [makeTier('t1', 'Standard')];
    const cats = [{ ...makeCategoryPrice(0), tier_id: 't1' }];
    expect(identifyGaTiers(tiers, cats)).toHaveLength(0);
  });

  it('returns only non-referenced tiers in hybrid event', () => {
    const seated = makeTier('seated-tier', 'Seated');
    const ga = makeTier('ga-tier', 'GA');
    const cats = [{ ...makeCategoryPrice(0), tier_id: 'seated-tier' }];
    const result = identifyGaTiers([seated, ga], cats);
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe('ga-tier');
  });

  it('handles categoryPrices with no tier_id gracefully', () => {
    const tiers = [makeTier('t1', 'Tier 1')];
    const cats = [{ ...makeCategoryPrice(0), tier_id: undefined }];
    // t1 is not in cats (tier_id undefined), so it's GA.
    expect(identifyGaTiers(tiers, cats)).toHaveLength(1);
  });
});
