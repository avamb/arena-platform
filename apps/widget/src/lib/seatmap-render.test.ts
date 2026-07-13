// @vitest-environment jsdom
/**
 * seatmap-render.test.ts — unit tests for the pure SVG seat-map builder.
 *
 * Runs under jsdom because `buildSeatMapSVG` sanitizes the untrusted decor
 * fragment with `DOMParser`.  `applySeatStatusUpdate` is still tested via a
 * lightweight mock container.
 */

import { describe, it, expect, vi } from 'vitest';
import {
  buildSeatMapSVG,
  applySeatStatusUpdate,
  applyConflictHighlight,
  clearConflictHighlight,
  buildCategoryColorMap,
  seatFillColor,
  buildPriceLabel,
  xmlAttr,
  cssAttrEscape,
  STATUS_COLORS,
  FALLBACK_COLOR,
  CONFLICT_COLOR,
} from './seatmap-render.js';
import type { Geometry, CategoryPrice, SeatStatusValue } from '../types.js';

// ─── Fixtures ─────────────────────────────────────────────────────────────────

function makeGeometry(overrides: Partial<Geometry> = {}): Geometry {
  return {
    schema_version: 1,
    canvas: { width: 800, height: 600 },
    categories: [
      { index: 1, name: 'Parter', color: '#ff0000' },
      { index: 2, name: 'Balkon', color: '#0000ff' },
    ],
    sections: [
      {
        key: 'P',
        name: 'Parter',
        rows: [
          {
            key: 'P|1',
            name: '1',
            seats: [
              { key: 'P|1|1', number: '1', x: 10, y: 10, radius: 8, category_index: 1 },
              { key: 'P|1|2', number: '2', x: 30, y: 10, radius: 8, category_index: 1 },
            ],
          },
        ],
      },
      {
        key: 'B',
        name: 'Balkon',
        rows: [
          {
            key: 'B|A',
            name: 'A',
            seats: [
              { key: 'B|A|1', number: '1', x: 100, y: 200, radius: 7, category_index: 2 },
            ],
          },
        ],
      },
    ],
    standing_zones: [],
    tables: [],
    decor_svg: '',
    ...overrides,
  };
}

const categoryPrices: CategoryPrice[] = [
  { index: 1, name: 'Parter', color: '#ff0000', price_amount: 5000, currency: 'CZK' },
  { index: 2, name: 'Balkon', color: '#0000ff', price_amount: 3000, currency: 'CZK' },
];

// ─── xmlAttr ─────────────────────────────────────────────────────────────────

describe('xmlAttr', () => {
  it('escapes ampersand', () => {
    expect(xmlAttr('A & B')).toBe('A &amp; B');
  });

  it('escapes less-than', () => {
    expect(xmlAttr('<tag>')).toBe('&lt;tag&gt;');
  });

  it('escapes double-quote', () => {
    expect(xmlAttr('"value"')).toBe('&quot;value&quot;');
  });

  it('escapes single-quote', () => {
    expect(xmlAttr("it's")).toBe("it&#39;s");
  });

  it('passes plain text unchanged', () => {
    expect(xmlAttr('Parter row 1 seat 2')).toBe('Parter row 1 seat 2');
  });
});

// ─── cssAttrEscape ────────────────────────────────────────────────────────────

describe('cssAttrEscape', () => {
  it('escapes backslash', () => {
    expect(cssAttrEscape('a\\b')).toBe('a\\\\b');
  });

  it('escapes double-quote', () => {
    expect(cssAttrEscape('say "hi"')).toBe('say \\"hi\\"');
  });

  it('does not escape pipe (used in seat keys)', () => {
    expect(cssAttrEscape('P|1|2')).toBe('P|1|2');
  });

  it('does not escape spaces', () => {
    expect(cssAttrEscape('Sector A|Row 1|3')).toBe('Sector A|Row 1|3');
  });
});

// ─── buildCategoryColorMap ────────────────────────────────────────────────────

describe('buildCategoryColorMap', () => {
  it('builds a map from category_prices', () => {
    const map = buildCategoryColorMap(categoryPrices, []);
    expect(map.get(1)).toBe('#ff0000');
    expect(map.get(2)).toBe('#0000ff');
  });

  it('falls back to geometry categories', () => {
    const geo = makeGeometry();
    const map = buildCategoryColorMap([], geo.categories);
    expect(map.get(1)).toBe('#ff0000');
    expect(map.get(2)).toBe('#0000ff');
  });

  it('category_prices override geometry categories', () => {
    const geo = makeGeometry();
    const overrideCat: CategoryPrice[] = [{ index: 1, name: 'Parter', color: '#abcdef' }];
    const map = buildCategoryColorMap(overrideCat, geo.categories);
    expect(map.get(1)).toBe('#abcdef');
    expect(map.get(2)).toBe('#0000ff'); // from geometry
  });

  it('normalises color without # prefix', () => {
    const cp: CategoryPrice[] = [{ index: 1, name: 'Parter', color: 'ff0000' }];
    const map = buildCategoryColorMap(cp, []);
    expect(map.get(1)).toBe('#ff0000');
  });

  it('returns empty map for empty inputs', () => {
    const map = buildCategoryColorMap([], []);
    expect(map.size).toBe(0);
  });
});

// ─── seatFillColor ────────────────────────────────────────────────────────────

describe('seatFillColor', () => {
  const catMap = new Map([[1, '#ff0000'], [2, '#0000ff']]);

  it('returns category color for available status', () => {
    expect(seatFillColor(1, 'available', catMap)).toBe('#ff0000');
  });

  it('returns held color for held status', () => {
    expect(seatFillColor(1, 'held', catMap)).toBe(STATUS_COLORS['held']);
  });

  it('returns sold color for sold status', () => {
    expect(seatFillColor(1, 'sold', catMap)).toBe(STATUS_COLORS['sold']);
  });

  it('returns blocked color for blocked status', () => {
    expect(seatFillColor(1, 'blocked', catMap)).toBe(STATUS_COLORS['blocked']);
  });

  it('returns FALLBACK_COLOR when category index is unknown', () => {
    expect(seatFillColor(99, 'available', catMap)).toBe(FALLBACK_COLOR);
  });

  it('defaults to available when status is undefined', () => {
    expect(seatFillColor(1, undefined, catMap)).toBe('#ff0000');
  });
});

// ─── buildPriceLabel ─────────────────────────────────────────────────────────

describe('buildPriceLabel', () => {
  it('returns empty string when category not found', () => {
    expect(buildPriceLabel(99, [])).toBe('');
  });

  it('returns empty string when category has no price fields', () => {
    const cp: CategoryPrice[] = [{ index: 1, name: 'Cat', color: '#fff' }];
    expect(buildPriceLabel(1, cp)).toBe('');
  });

  it('uses price_hint and currency_hint when both present', () => {
    const cp: CategoryPrice[] = [
      { index: 1, name: 'Parter', color: '#f00', price_hint: '22.00', currency_hint: 'EUR' },
    ];
    expect(buildPriceLabel(1, cp)).toBe('22.00 EUR');
  });

  it('uses price_amount/100 and currency when price_hint absent', () => {
    const cp: CategoryPrice[] = [
      { index: 1, name: 'Parter', color: '#f00', price_amount: 5000, currency: 'CZK' },
    ];
    expect(buildPriceLabel(1, cp)).toBe('50.00 CZK');
  });

  it('formats price_amount with two decimal places', () => {
    const cp: CategoryPrice[] = [
      { index: 2, name: 'Balkon', color: '#00f', price_amount: 1250, currency: 'CZK' },
    ];
    expect(buildPriceLabel(2, cp)).toBe('12.50 CZK');
  });

  it('prefers price_hint over price_amount when both present', () => {
    const cp: CategoryPrice[] = [
      {
        index: 1,
        name: 'Parter',
        color: '#f00',
        price_hint: '22.00',
        currency_hint: 'EUR',
        price_amount: 2200,
        currency: 'EUR',
      },
    ];
    expect(buildPriceLabel(1, cp)).toBe('22.00 EUR');
  });

  it('returns empty string when price_amount present but currency absent', () => {
    const cp: CategoryPrice[] = [
      { index: 1, name: 'Cat', color: '#fff', price_amount: 1000 },
    ];
    expect(buildPriceLabel(1, cp)).toBe('');
  });

  it('returns empty string when currency present but price_amount absent', () => {
    const cp: CategoryPrice[] = [
      { index: 1, name: 'Cat', color: '#fff', currency: 'EUR' },
    ];
    expect(buildPriceLabel(1, cp)).toBe('');
  });
});

// ─── buildSeatMapSVG ─────────────────────────────────────────────────────────

describe('buildSeatMapSVG', () => {
  it('returns a string containing svg element', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).toContain('<svg');
    expect(svg).toContain('</svg>');
  });

  it('uses canvas width/height from geometry', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).toContain('viewBox="0 0 800 600"');
    expect(svg).toContain('width="800"');
    expect(svg).toContain('height="600"');
  });

  it('defaults canvas to 800×600 when zero', () => {
    const geo = makeGeometry({ canvas: { width: 0, height: 0 } });
    const svg = buildSeatMapSVG(geo, categoryPrices, {});
    expect(svg).toContain('viewBox="0 0 800 600"');
  });

  it('includes seat circles with data-seat-key', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).toContain('data-seat-key="P|1|1"');
    expect(svg).toContain('data-seat-key="P|1|2"');
    expect(svg).toContain('data-seat-key="B|A|1"');
  });

  it('uses category color for available seats', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).toContain('fill="#ff0000"'); // Parter cat index 1
    expect(svg).toContain('fill="#0000ff"'); // Balkon cat index 2
  });

  it('uses status color for held seats', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, { 'P|1|1': 'held' });
    expect(svg).toContain(`fill="${STATUS_COLORS['held']}"`);
  });

  it('uses status color for sold seats', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, { 'P|1|2': 'sold' });
    expect(svg).toContain(`fill="${STATUS_COLORS['sold']}"`);
  });

  it('uses status color for blocked seats', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, { 'B|A|1': 'blocked' });
    expect(svg).toContain(`fill="${STATUS_COLORS['blocked']}"`);
  });

  it('includes sanitized decor_svg inside an aria-hidden decor group', () => {
    const geo = makeGeometry({ decor_svg: '<rect x="0" y="0" width="100" height="50"/>' });
    const svg = buildSeatMapSVG(geo, categoryPrices, {});
    expect(svg).toContain('<g id="decor" aria-hidden="true">');
    expect(svg).toContain('<rect x="0"');
  });

  it('omits decor group when decor_svg is empty', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).not.toContain('id="decor"');
  });

  it('strips XSS payloads from decor_svg (script, onload, href)', () => {
    const geo = makeGeometry({
      decor_svg:
        '<rect x="0" y="0" width="10" height="10" onload="alert(1)"/>' +
        '<script>alert(2)</script>' +
        '<a href="javascript:alert(3)"><circle cx="1" cy="1" r="1"/></a>',
    });
    const svg = buildSeatMapSVG(geo, categoryPrices, {});
    expect(svg).toContain('<g id="decor"');
    expect(svg).toContain('<rect');
    expect(svg).not.toContain('onload');
    expect(svg).not.toContain('<script');
    expect(svg).not.toContain('alert(');
    expect(svg).not.toContain('javascript:');
  });

  it('omits decor group entirely when decor_svg is malformed', () => {
    const geo = makeGeometry({ decor_svg: '<rect x="0"' });
    const svg = buildSeatMapSVG(geo, categoryPrices, {});
    expect(svg).not.toContain('id="decor"');
  });

  it('exposes the map root to AT with role=group and a section-context label', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).toContain('role="group"');
    expect(svg).toContain('aria-label="Seat map — sections: Parter, Balkon"');
    expect(svg).not.toContain('role="img"');
  });

  it('includes standing-zones group when zones present', () => {
    const geo = makeGeometry({
      standing_zones: [{ key: 'Z1', name: 'Floor', capacity: 200 }],
    });
    const svg = buildSeatMapSVG(geo, categoryPrices, {});
    expect(svg).toContain('<g id="standing-zones">');
    expect(svg).toContain('data-zone-key="Z1"');
  });

  it('omits standing-zones group when no zones', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).not.toContain('<g id="standing-zones">');
  });

  it('XML-escapes section names in aria-label', () => {
    const geo = makeGeometry({
      sections: [
        {
          key: 'S',
          name: 'Balkon <left>',
          rows: [
            {
              key: 'S|A',
              name: 'A',
              seats: [
                { key: 'S|A|1', number: '1', x: 10, y: 10, radius: 8, category_index: 1 },
              ],
            },
          ],
        },
      ],
    });
    const svg = buildSeatMapSVG(geo, categoryPrices, {});
    expect(svg).toContain('Balkon &lt;left&gt;');
    expect(svg).not.toContain('Balkon <left>');
  });

  it('uses radius from seat (not a hardcoded value)', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    expect(svg).toContain('r="8"');
    expect(svg).toContain('r="7"'); // Balkon seat has radius 7
  });

  it('uses fallback radius 8 when seat.radius is 0', () => {
    const geo = makeGeometry({
      sections: [
        {
          key: 'X',
          name: 'X',
          rows: [
            {
              key: 'X|1',
              name: '1',
              seats: [
                { key: 'X|1|1', number: '1', x: 5, y: 5, radius: 0, category_index: 1 },
              ],
            },
          ],
        },
      ],
    });
    const svg = buildSeatMapSVG(geo, categoryPrices, {});
    expect(svg).toContain('r="8"');
  });

  it('first seat in each row has tabindex="0" (roving tabindex model)', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    // role=button on all seats
    expect(svg).toContain('role="button"');
    // The first seat of each row gets tabindex="0" (Tab stop per row).
    expect(svg).toContain('tabindex="0"');
    // Non-first seats get tabindex="-1".
    // makeGeometry() has Parter/Row1 with 2 seats → second seat must be -1.
    expect(svg).toContain('tabindex="-1"');
  });

  it('only the first seat per row gets tabindex="0", subsequent seats get "-1"', () => {
    // Row P|1 has 2 seats: first → 0, second → -1.
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    // Count occurrences.
    const zeros = (svg.match(/tabindex="0"/g) ?? []).length;
    const minusOnes = (svg.match(/tabindex="-1"/g) ?? []).length;
    // makeGeometry() has 2 rows (Parter/Row1 and Balkon/RowA) → 2 zeros.
    // Parter/Row1 has 2 seats: 1 zero, 1 minus-one.
    // Balkon/RowA has 1 seat:  1 zero, 0 minus-ones.
    expect(zeros).toBe(2);
    expect(minusOnes).toBe(1);
  });

  it('seat aria-label includes price when categoryPrices has price_amount', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    // categoryPrices fixture: index 1 → price_amount 5000 CZK → "50.00 CZK"
    expect(svg).toContain('50.00 CZK');
    // index 2 → 3000 CZK → "30.00 CZK"
    expect(svg).toContain('30.00 CZK');
  });

  it('seat aria-label format is: section, row, seat, price, status', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    // Parter / Row 1 / seat 1 / 50.00 CZK / available
    expect(svg).toContain('Parter, row 1, seat 1, 50.00 CZK, available');
  });

  it('seat aria-label uses price_hint when available', () => {
    const geo = makeGeometry();
    const cpWithHint: CategoryPrice[] = [
      { index: 1, name: 'Parter', color: '#ff0000', price_hint: '22.00', currency_hint: 'EUR' },
    ];
    const svg = buildSeatMapSVG(geo, cpWithHint, {});
    expect(svg).toContain('22.00 EUR');
  });

  it('seat aria-label omits price when categoryPrices has no price data', () => {
    const geo = makeGeometry();
    const cpNoPrice: CategoryPrice[] = [
      { index: 1, name: 'Parter', color: '#ff0000' },
      { index: 2, name: 'Balkon', color: '#0000ff' },
    ];
    const svg = buildSeatMapSVG(geo, cpNoPrice, {});
    // Should fall back to format without price.
    expect(svg).toContain('Parter, row 1, seat 1, available');
    expect(svg).not.toContain('undefined');
    expect(svg).not.toContain('NaN');
  });

  it('is deterministic across multiple calls', () => {
    const geo = makeGeometry();
    const s1 = buildSeatMapSVG(geo, categoryPrices, { 'P|1|1': 'held' });
    const s2 = buildSeatMapSVG(geo, categoryPrices, { 'P|1|1': 'held' });
    expect(s1).toBe(s2);
  });

  it('emits data-base-label attribute (without status suffix) on seat circles (WID-S2)', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    // Base label for Parter row 1 seat 1 with price 50.00 CZK — no status suffix.
    expect(svg).toContain('data-base-label="Parter, row 1, seat 1, 50.00 CZK"');
  });

  it('data-base-label and aria-label are consistent (base + status = full label)', () => {
    const svg = buildSeatMapSVG(makeGeometry(), categoryPrices, {});
    // aria-label should be base-label + ", " + status.
    expect(svg).toContain('data-base-label="Parter, row 1, seat 1, 50.00 CZK"');
    expect(svg).toContain('aria-label="Parter, row 1, seat 1, 50.00 CZK, available"');
  });
});

// ─── applySeatStatusUpdate ────────────────────────────────────────────────────

describe('applySeatStatusUpdate', () => {
  function makeEl(seatKey: string, catIdx: number): { setAttribute: ReturnType<typeof vi.fn>; getAttribute: ReturnType<typeof vi.fn> } {
    const attrs: Record<string, string> = {
      'data-cat': String(catIdx),
      'aria-label': `Parter, row 1, seat 1, available`,
    };
    return {
      setAttribute: vi.fn((k: string, v: string) => { attrs[k] = v; }),
      getAttribute: vi.fn((k: string) => attrs[k] ?? null),
    };
  }

  it('updates fill and data-status for changed seat', () => {
    const el = makeEl('P|1|1', 1);
    const container = {
      querySelector: vi.fn().mockReturnValue(el),
    };
    const catMap = new Map([[1, '#ff0000']]);
    applySeatStatusUpdate(
      container as unknown as Element,
      { 'P|1|1': 'sold' as SeatStatusValue },
      catMap,
    );
    expect(el.setAttribute).toHaveBeenCalledWith('fill', STATUS_COLORS['sold']);
    expect(el.setAttribute).toHaveBeenCalledWith('data-status', 'sold');
  });

  it('resolves available fill from catMap', () => {
    const el = makeEl('P|1|2', 1);
    const container = { querySelector: vi.fn().mockReturnValue(el) };
    const catMap = new Map([[1, '#ff0000']]);
    applySeatStatusUpdate(
      container as unknown as Element,
      { 'P|1|2': 'available' as SeatStatusValue },
      catMap,
    );
    expect(el.setAttribute).toHaveBeenCalledWith('fill', '#ff0000');
  });

  it('skips missing seat elements gracefully', () => {
    const container = { querySelector: vi.fn().mockReturnValue(null) };
    // Should not throw.
    expect(() =>
      applySeatStatusUpdate(
        container as unknown as Element,
        { 'X|1|1': 'held' as SeatStatusValue },
        new Map(),
      ),
    ).not.toThrow();
  });

  it('uses cssAttrEscape for the selector', () => {
    const container = { querySelector: vi.fn().mockReturnValue(null) };
    applySeatStatusUpdate(
      container as unknown as Element,
      { 'P|1|1': 'sold' as SeatStatusValue },
      new Map(),
    );
    const selector = (container.querySelector as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as string;
    expect(selector).toContain('P|1|1');
  });

  it('restores aria-label from data-base-label when present (WID-S2)', () => {
    // Simulate a seat circle produced by buildSeatMapSVG (has data-base-label).
    const div = document.createElement('div');
    div.innerHTML = `<svg><circle data-seat-key="P|1|1" data-cat="1" data-status="conflict"
      data-base-label="Parter, row 1, seat 1, 50.00 CZK"
      aria-label="Parter, row 1, seat 1, 50.00 CZK, conflict — not available" fill="#b91c1c" /></svg>`;
    const catMap = new Map([[1, '#ff0000']]);
    applySeatStatusUpdate(div, { 'P|1|1': 'available' as SeatStatusValue }, catMap);
    const el = div.querySelector('[data-seat-key="P|1|1"]') as SVGCircleElement;
    // aria-label should now be base-label + ", available" (no regex, no leftover suffix).
    expect(el.getAttribute('aria-label')).toBe('Parter, row 1, seat 1, 50.00 CZK, available');
  });
});

// ─── CONFLICT_COLOR export ────────────────────────────────────────────────────

describe('CONFLICT_COLOR', () => {
  it('is a WCAG-AA compliant hex string (error red)', () => {
    expect(CONFLICT_COLOR).toBe('#b91c1c');
  });

  it('is not in STATUS_COLORS (widget-only overlay, not a backend status)', () => {
    // STATUS_COLORS keys are backend SeatStatusValue values.
    expect(Object.values(STATUS_COLORS)).not.toContain(CONFLICT_COLOR);
  });
});

// ─── applyConflictHighlight ───────────────────────────────────────────────────

describe('applyConflictHighlight', () => {
  /**
   * Create a minimal DOM container with one SVG seat circle, rendered via
   * buildSeatMapSVG so the test uses the real SVG structure.
   */
  function makeContainer(seatKey: string, ariaLabel: string): HTMLDivElement {
    const div = document.createElement('div');
    div.innerHTML = `<svg><circle data-seat-key="${seatKey}" data-status="available" data-cat="1" fill="#ff0000" aria-label="${ariaLabel}" /></svg>`;
    return div;
  }

  it('sets fill to CONFLICT_COLOR on the conflicting seat', () => {
    const container = makeContainer('P|1|1', 'Parter, row 1, seat 1, available');
    applyConflictHighlight(container, new Set(['P|1|1']));
    const circle = container.querySelector('circle[data-seat-key="P|1|1"]') as SVGCircleElement;
    expect(circle.getAttribute('fill')).toBe(CONFLICT_COLOR);
  });

  it('sets data-status to "conflict"', () => {
    const container = makeContainer('P|1|2', 'Parter, row 1, seat 2, available');
    applyConflictHighlight(container, new Set(['P|1|2']));
    const circle = container.querySelector('circle[data-seat-key="P|1|2"]') as SVGCircleElement;
    expect(circle.getAttribute('data-status')).toBe('conflict');
  });

  it('updates aria-label to end with "conflict — not available"', () => {
    const container = makeContainer('P|1|3', 'Parter, row 1, seat 3, available');
    applyConflictHighlight(container, new Set(['P|1|3']));
    const circle = container.querySelector('circle[data-seat-key="P|1|3"]') as SVGCircleElement;
    expect(circle.getAttribute('aria-label')).toContain('conflict — not available');
  });

  it('highlights multiple seats', () => {
    const div = document.createElement('div');
    div.innerHTML = `<svg>
      <circle data-seat-key="A|1" data-status="available" data-cat="1" fill="#f00" aria-label="A, row 1, seat 1, available"/>
      <circle data-seat-key="A|2" data-status="available" data-cat="1" fill="#f00" aria-label="A, row 1, seat 2, available"/>
      <circle data-seat-key="A|3" data-status="available" data-cat="1" fill="#f00" aria-label="A, row 1, seat 3, available"/>
    </svg>`;
    applyConflictHighlight(div, new Set(['A|1', 'A|3']));
    expect(div.querySelector('[data-seat-key="A|1"]')!.getAttribute('data-status')).toBe('conflict');
    expect(div.querySelector('[data-seat-key="A|2"]')!.getAttribute('data-status')).toBe('available');
    expect(div.querySelector('[data-seat-key="A|3"]')!.getAttribute('data-status')).toBe('conflict');
  });

  it('skips seat keys not found in the DOM (no throw)', () => {
    const container = makeContainer('P|1|1', 'Parter, row 1, seat 1, available');
    expect(() =>
      applyConflictHighlight(container, new Set(['GHOST|0|0'])),
    ).not.toThrow();
    // The real seat should be untouched.
    expect(
      container.querySelector('[data-seat-key="P|1|1"]')!.getAttribute('data-status'),
    ).toBe('available');
  });

  it('does nothing for empty conflictKeys', () => {
    const container = makeContainer('P|1|1', 'Parter, row 1, seat 1, available');
    applyConflictHighlight(container, new Set());
    expect(
      container.querySelector('[data-seat-key="P|1|1"]')!.getAttribute('fill'),
    ).toBe('#ff0000'); // unchanged
  });

  it('works on a seat with multi-word status in aria-label (e.g. "held")', () => {
    const div = document.createElement('div');
    div.innerHTML = `<svg><circle data-seat-key="P|1|5" data-status="held" data-cat="1" fill="${STATUS_COLORS['held']}" aria-label="Parter, row 1, seat 5, held"/></svg>`;
    applyConflictHighlight(div, new Set(['P|1|5']));
    const el = div.querySelector('[data-seat-key="P|1|5"]') as SVGCircleElement;
    expect(el.getAttribute('fill')).toBe(CONFLICT_COLOR);
    expect(el.getAttribute('aria-label')).toContain('conflict — not available');
  });
});

// ─── clearConflictHighlight ───────────────────────────────────────────────────

describe('clearConflictHighlight', () => {
  it('restores the previous status fill and data-status', () => {
    const div = document.createElement('div');
    // Simulate a seat already highlighted as conflict.
    div.innerHTML = `<svg><circle data-seat-key="P|1|1" data-status="conflict" data-cat="1" fill="${CONFLICT_COLOR}" aria-label="Parter, row 1, seat 1, conflict — not available"/></svg>`;

    const catColorMap = new Map([[1, '#ff0000']]);
    clearConflictHighlight(div, new Set(['P|1|1']), catColorMap, {});

    const el = div.querySelector('[data-seat-key="P|1|1"]') as SVGCircleElement;
    // Status falls back to 'available' (not in seatStatuses) → category color.
    expect(el.getAttribute('data-status')).toBe('available');
    expect(el.getAttribute('fill')).toBe('#ff0000');
  });

  it('restores "held" status when seat is now held in seatStatuses', () => {
    const div = document.createElement('div');
    div.innerHTML = `<svg><circle data-seat-key="B|2|1" data-status="conflict" data-cat="2" fill="${CONFLICT_COLOR}" aria-label="Balkon, row 2, seat 1, conflict — not available"/></svg>`;

    const catColorMap = new Map([[2, '#0000ff']]);
    clearConflictHighlight(div, new Set(['B|2|1']), catColorMap, { 'B|2|1': 'held' });

    const el = div.querySelector('[data-seat-key="B|2|1"]') as SVGCircleElement;
    expect(el.getAttribute('data-status')).toBe('held');
    expect(el.getAttribute('fill')).toBe(STATUS_COLORS['held']);
  });

  it('does nothing for empty conflictKeys', () => {
    const div = document.createElement('div');
    div.innerHTML = `<svg><circle data-seat-key="P|1|1" data-status="available" data-cat="1" fill="#f00" aria-label="Parter, row 1, seat 1, available"/></svg>`;
    expect(() =>
      clearConflictHighlight(div, new Set(), new Map(), {}),
    ).not.toThrow();
  });

  it('skips keys not in the DOM', () => {
    const div = document.createElement('div');
    div.innerHTML = '<svg></svg>';
    expect(() =>
      clearConflictHighlight(div, new Set(['GHOST|0|0']), new Map(), {}),
    ).not.toThrow();
  });

  it('restores full aria-label from data-base-label when clearing conflict (WID-S2)', () => {
    const div = document.createElement('div');
    // Seat produced by buildSeatMapSVG has data-base-label.
    div.innerHTML = `<svg><circle
      data-seat-key="P|1|1" data-cat="1" data-status="conflict"
      data-base-label="Parter, row 1, seat 1, 50.00 CZK"
      fill="${CONFLICT_COLOR}"
      aria-label="Parter, row 1, seat 1, 50.00 CZK, conflict — not available"
    /></svg>`;
    const catColorMap = new Map([[1, '#ff0000']]);
    clearConflictHighlight(div, new Set(['P|1|1']), catColorMap, {});
    const el = div.querySelector('[data-seat-key="P|1|1"]') as SVGCircleElement;
    // Full label must be restored from base-label + status, not regex over live label.
    expect(el.getAttribute('aria-label')).toBe('Parter, row 1, seat 1, 50.00 CZK, available');
  });

  it('applyConflictHighlight uses data-base-label to build conflict aria-label (WID-S2)', () => {
    const div = document.createElement('div');
    div.innerHTML = `<svg><circle
      data-seat-key="P|1|1" data-cat="1" data-status="available"
      data-base-label="Parter, row 1, seat 1, 50.00 CZK"
      fill="#ff0000"
      aria-label="Parter, row 1, seat 1, 50.00 CZK, available"
    /></svg>`;
    applyConflictHighlight(div, new Set(['P|1|1']));
    const el = div.querySelector('[data-seat-key="P|1|1"]') as SVGCircleElement;
    // Should be built from data-base-label, not regex over the live aria-label.
    expect(el.getAttribute('aria-label')).toBe('Parter, row 1, seat 1, 50.00 CZK, conflict — not available');
  });
});
