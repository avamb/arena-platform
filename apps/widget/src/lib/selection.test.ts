/**
 * Unit tests for apps/widget/src/lib/selection.ts (WID-C)
 *
 * All tests run in Node via vitest — no DOM required.
 */

import { describe, it, expect } from 'vitest';
import {
  toggleSeatSelection,
  clearSelection,
  bestAvailableSeats,
  detectSingleSeatGaps,
  clampGaQuantity,
  incrementGaQuantity,
  decrementGaQuantity,
  GA_MIN_QUANTITY,
  GA_MAX_QUANTITY,
} from './selection.js';
import type { Geometry, SeatStatusValue } from '../types.js';

// ─── Fixture helpers ──────────────────────────────────────────────────────────

function makeSeat(
  key: string,
  catIdx = 0,
  x = 0,
  y = 0,
  r = 8,
): Geometry['sections'][0]['rows'][0]['seats'][0] {
  return { key, number: key, x, y, radius: r, category_index: catIdx };
}

function makeGeometry(
  rows: Array<{
    sectionKey?: string;
    rowKey: string;
    seats: Array<{ key: string; catIdx?: number }>;
  }>,
): Geometry {
  // Group rows by sectionKey (default 'S1').
  const sectionMap = new Map<string, typeof rows>();
  for (const r of rows) {
    const sk = r.sectionKey ?? 'S1';
    if (!sectionMap.has(sk)) sectionMap.set(sk, []);
    sectionMap.get(sk)!.push(r);
  }

  const sections = Array.from(sectionMap.entries()).map(([sk, rs]) => ({
    key: sk,
    name: sk,
    rows: rs.map(r => ({
      key: r.rowKey,
      name: r.rowKey,
      seats: r.seats.map(s => makeSeat(s.key, s.catIdx ?? 0)),
    })),
  }));

  return {
    schema_version: 1,
    canvas: { width: 800, height: 600 },
    categories: [
      { index: 0, name: 'Cat A', color: '#e11d48' },
      { index: 1, name: 'Cat B', color: '#3b82f6' },
    ],
    sections,
    standing_zones: [],
    tables: [],
    decor_svg: '',
  };
}

// ─── toggleSeatSelection ──────────────────────────────────────────────────────

describe('toggleSeatSelection', () => {
  it('selects an available seat into an empty set', () => {
    const sel = toggleSeatSelection(new Set(), 'A-1-1', 'available');
    expect(sel.has('A-1-1')).toBe(true);
    expect(sel.size).toBe(1);
  });

  it('deselects a seat already in the set', () => {
    const initial = new Set(['A-1-1']);
    const sel = toggleSeatSelection(initial, 'A-1-1', 'available');
    expect(sel.has('A-1-1')).toBe(false);
    expect(sel.size).toBe(0);
  });

  it('does not select a held seat', () => {
    const sel = toggleSeatSelection(new Set(), 'A-1-2', 'held');
    expect(sel.size).toBe(0);
  });

  it('does not select a sold seat', () => {
    const sel = toggleSeatSelection(new Set(), 'A-1-3', 'sold');
    expect(sel.size).toBe(0);
  });

  it('does not select a blocked seat', () => {
    const sel = toggleSeatSelection(new Set(), 'A-1-4', 'blocked');
    expect(sel.size).toBe(0);
  });

  it('allows deselecting a seat even if now held (optimistic revert)', () => {
    const initial = new Set(['A-1-5']);
    const sel = toggleSeatSelection(initial, 'A-1-5', 'held');
    expect(sel.has('A-1-5')).toBe(false);
  });

  it('treats undefined status as available', () => {
    const sel = toggleSeatSelection(new Set(), 'A-1-6', undefined);
    expect(sel.has('A-1-6')).toBe(true);
  });

  it('does not mutate the input set', () => {
    const initial = new Set(['A-1-1']);
    toggleSeatSelection(initial, 'A-1-2', 'available');
    expect(initial.size).toBe(1); // unchanged
  });

  it('adding the same available seat twice is idempotent', () => {
    const first = toggleSeatSelection(new Set(), 'A-1-1', 'available');
    // deselect then reselect
    const second = toggleSeatSelection(first, 'A-1-1', 'available');
    expect(second.size).toBe(0); // toggle off
  });
});

// ─── clearSelection ────────────────────────────────────────────────────────────

describe('clearSelection', () => {
  it('returns an empty set', () => {
    const s = clearSelection();
    expect(s.size).toBe(0);
  });

  it('returns a new Set every call', () => {
    const a = clearSelection();
    const b = clearSelection();
    expect(a).not.toBe(b);
  });
});

// ─── bestAvailableSeats ───────────────────────────────────────────────────────

describe('bestAvailableSeats', () => {
  it('returns empty array when count is 0', () => {
    const g = makeGeometry([{ rowKey: 'R1', seats: [{ key: 'A' }, { key: 'B' }] }]);
    expect(bestAvailableSeats(g, {}, 0, 0)).toEqual([]);
  });

  it('picks 2 adjacent seats from a simple row', () => {
    const g = makeGeometry([
      { rowKey: 'R1', seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }] },
    ]);
    const result = bestAvailableSeats(g, {}, 0, 2);
    expect(result).toEqual(['A', 'B']);
  });

  it('picks leftmost run when multiple runs exist', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [
          { key: 'A' },
          { key: 'B' }, // sold below
          { key: 'C' },
          { key: 'D' },
          { key: 'E' },
        ],
      },
    ]);
    const statuses: Record<string, SeatStatusValue> = { B: 'sold' };
    const result = bestAvailableSeats(g, statuses, 0, 2);
    expect(result).toEqual(['C', 'D']);
  });

  it('skips held / sold / blocked seats when finding a run', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [
          { key: 'A' },
          { key: 'B' },
          { key: 'C' },
        ],
      },
    ]);
    const statuses: Record<string, SeatStatusValue> = { B: 'held' };
    // Only C is available after the break; a run of 2 is not possible
    const result = bestAvailableSeats(g, statuses, 0, 2);
    expect(result).toEqual([]);
  });

  it('prefers the row with more available seats (row-rank heuristic)', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: '1A' }, { key: '1B' }], // 2 available
      },
      {
        rowKey: 'R2',
        seats: [{ key: '2A' }, { key: '2B' }, { key: '2C' }, { key: '2D' }], // 4 available
      },
    ]);
    const result = bestAvailableSeats(g, {}, 0, 2);
    // Row 2 has more available seats — should be picked
    expect(result).toEqual(['2A', '2B']);
  });

  it('respects category index filter', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [
          { key: 'A', catIdx: 0 },
          { key: 'B', catIdx: 1 },
          { key: 'C', catIdx: 1 },
          { key: 'D', catIdx: 0 },
        ],
      },
    ]);
    // Asking for cat 1: B and C are adjacent
    const result = bestAvailableSeats(g, {}, 1, 2);
    expect(result).toEqual(['B', 'C']);
  });

  it('returns empty when no row has enough adjacent seats', () => {
    const g = makeGeometry([
      { rowKey: 'R1', seats: [{ key: 'A' }] },
    ]);
    const result = bestAvailableSeats(g, {}, 0, 3);
    expect(result).toEqual([]);
  });

  it('returns exactly N seats even when the run is longer', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }, { key: 'D' }, { key: 'E' }],
      },
    ]);
    const result = bestAvailableSeats(g, {}, 0, 3);
    expect(result).toHaveLength(3);
    expect(result).toEqual(['A', 'B', 'C']);
  });

  it('handles multiple sections', () => {
    const g = makeGeometry([
      { sectionKey: 'Sec1', rowKey: 'R1', seats: [{ key: 'S1A' }, { key: 'S1B' }] },
      { sectionKey: 'Sec2', rowKey: 'R1', seats: [{ key: 'S2A' }, { key: 'S2B' }, { key: 'S2C' }] },
    ]);
    const result = bestAvailableSeats(g, {}, 0, 2);
    // Both rows qualify; Sec2/R1 has 3 available → wins
    expect(result).toEqual(['S2A', 'S2B']);
  });

  it('returns single seat when count is 1', () => {
    const g = makeGeometry([
      { rowKey: 'R1', seats: [{ key: 'A' }, { key: 'B' }] },
    ]);
    const result = bestAvailableSeats(g, {}, 0, 1);
    expect(result).toHaveLength(1);
    expect(result[0]).toBe('A');
  });
});

// ─── detectSingleSeatGaps ─────────────────────────────────────────────────────

describe('detectSingleSeatGaps', () => {
  it('returns empty array when no gaps exist', () => {
    const g = makeGeometry([
      { rowKey: 'R1', seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }] },
    ]);
    expect(detectSingleSeatGaps(g, {}, new Set())).toEqual([]);
  });

  it('detects a single gap between two selected seats', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }],
      },
    ]);
    // A and C are selected → B is an isolated available seat
    const selected = new Set(['A', 'C']);
    const gaps = detectSingleSeatGaps(g, {}, selected);
    expect(gaps).toContain('B');
    expect(gaps).toHaveLength(1);
  });

  it('detects a single gap at the left edge (neighbour is occupied, row starts)', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }],
      },
    ]);
    // B is selected, A is at left edge → A is isolated
    const selected = new Set(['B']);
    const statuses: Record<string, SeatStatusValue> = {};
    const gaps = detectSingleSeatGaps(g, statuses, selected);
    expect(gaps).toContain('A');
  });

  it('detects a single gap at the right edge', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }],
      },
    ]);
    const selected = new Set(['B']);
    const gaps = detectSingleSeatGaps(g, {}, selected);
    expect(gaps).toContain('C');
  });

  it('does not flag a seat that has an available neighbour', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }, { key: 'D' }],
      },
    ]);
    const selected = new Set<string>();
    const gaps = detectSingleSeatGaps(g, {}, selected);
    // No occupied neighbours — no gaps
    expect(gaps).toHaveLength(0);
  });

  it('detects gap between a sold seat and selected seat', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }],
      },
    ]);
    const statuses: Record<string, SeatStatusValue> = { A: 'sold' };
    const selected = new Set(['C']);
    const gaps = detectSingleSeatGaps(g, statuses, selected);
    expect(gaps).toContain('B');
  });

  it('does not flag selected seats as gaps', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }],
      },
    ]);
    // All selected — nothing is a gap
    const selected = new Set(['A', 'B', 'C']);
    const gaps = detectSingleSeatGaps(g, {}, selected);
    expect(gaps).toHaveLength(0);
  });

  it('does not flag held seats as gaps (they are not available)', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: 'A' }, { key: 'B' }, { key: 'C' }],
      },
    ]);
    const statuses: Record<string, SeatStatusValue> = { A: 'sold', B: 'held', C: 'sold' };
    const gaps = detectSingleSeatGaps(g, statuses, new Set());
    expect(gaps).toHaveLength(0); // B is held, not available
  });

  it('handles multiple rows independently', () => {
    const g = makeGeometry([
      {
        rowKey: 'R1',
        seats: [{ key: '1A' }, { key: '1B' }, { key: '1C' }],
      },
      {
        rowKey: 'R2',
        seats: [{ key: '2A' }, { key: '2B' }, { key: '2C' }],
      },
    ]);
    const selected = new Set(['1A', '1C', '2A', '2C']);
    const gaps = detectSingleSeatGaps(g, {}, selected);
    expect(gaps).toContain('1B');
    expect(gaps).toContain('2B');
    expect(gaps).toHaveLength(2);
  });
});

// ─── GA quantity helpers ──────────────────────────────────────────────────────

describe('clampGaQuantity', () => {
  it('returns GA_MIN_QUANTITY for 0', () => {
    expect(clampGaQuantity(0, 100)).toBe(GA_MIN_QUANTITY);
  });

  it('returns GA_MIN_QUANTITY for negative', () => {
    expect(clampGaQuantity(-5, 100)).toBe(GA_MIN_QUANTITY);
  });

  it('clamps to GA_MAX_QUANTITY', () => {
    expect(clampGaQuantity(999, 1000)).toBe(GA_MAX_QUANTITY);
  });

  it('clamps to zone capacity when lower than GA_MAX_QUANTITY', () => {
    expect(clampGaQuantity(15, 5)).toBe(5);
  });

  it('passes through valid mid-range value', () => {
    expect(clampGaQuantity(5, 100)).toBe(5);
  });
});

describe('incrementGaQuantity', () => {
  it('increments by 1', () => {
    expect(incrementGaQuantity(3, 100)).toBe(4);
  });

  it('does not exceed GA_MAX_QUANTITY', () => {
    expect(incrementGaQuantity(GA_MAX_QUANTITY, 100)).toBe(GA_MAX_QUANTITY);
  });

  it('does not exceed zone capacity', () => {
    expect(incrementGaQuantity(5, 5)).toBe(5);
  });
});

describe('decrementGaQuantity', () => {
  it('decrements by 1', () => {
    expect(decrementGaQuantity(5, 100)).toBe(4);
  });

  it('does not go below GA_MIN_QUANTITY', () => {
    expect(decrementGaQuantity(GA_MIN_QUANTITY, 100)).toBe(GA_MIN_QUANTITY);
  });
});
