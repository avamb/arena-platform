import { describe, it, expect } from 'vitest';
import { applySeatCategoryOverlay } from './seatCategories.js';
import { buildSeatMapSVG, buildPriceLabel } from './seatmap-render.js';
import type { SchemaResponse, CategoryPrice } from '../types.js';

function makeSchema(): SchemaResponse {
  const seat = (n: string, cat: number) => ({
    key: `s|1|${n}`,
    number: n,
    x: 10,
    y: 10,
    radius: 5,
    category_index: cat,
  });
  return {
    session_id: 's',
    event_id: 'e',
    admission_mode: 'assigned_seats',
    seating_plan_version_id: 'v',
    seat_status_version: 1,
    geometry_checksum: 'c',
    capacity_seated: 3,
    capacity_standing: 0,
    geometry: {
      schema_version: 1,
      canvas: { width: 400, height: 200 },
      categories: [
        { index: 1, name: 'VIP', color: '#ff0000' },
        { index: 2, name: 'Standard', color: '#00aa00' },
      ],
      sections: [
        {
          key: 's',
          name: 'S',
          rows: [{ key: '1', name: '1', seats: [seat('a1', 1), seat('a2', 1), seat('b1', 2)] }],
        },
      ],
      tables: [],
      decor_svg: '',
    },
    category_prices: [
      { index: 1, name: 'VIP', color: '#ff0000', tier_id: 't-vip', price_amount: 5000, current_price: 5000, currency: 'EUR' },
      { index: 2, name: 'Standard', color: '#00aa00', tier_id: 't-std', price_amount: 2000, current_price: 2000, currency: 'EUR' },
    ],
  } as unknown as SchemaResponse;
}

describe('applySeatCategoryOverlay (S1a)', () => {
  it('leaves a response without overlay fields untouched', () => {
    const schema = makeSchema();
    const before = JSON.stringify(schema);
    applySeatCategoryOverlay(schema);
    expect(JSON.stringify(schema)).toBe(before);
  });

  it('moves a repainted seat to the category of its own tier', () => {
    const schema = makeSchema();
    schema.seat_category_index = { 's|1|a1': 2 };
    schema.extra_categories = [];
    applySeatCategoryOverlay(schema);
    const seats = schema.geometry.sections[0].rows[0].seats;
    expect(seats.map((s) => s.category_index)).toEqual([2, 1, 2]);
  });

  it('adds a synthetic category carrying its own tier and price', () => {
    const schema = makeSchema();
    const extra: CategoryPrice = {
      index: 3,
      name: 'Balcony',
      color: '#8e24aa',
      tier_id: 't-bal',
      price_amount: 1200,
      current_price: 1200,
      currency: 'EUR',
    };
    schema.seat_category_index = { 's|1|a2': 3 };
    schema.extra_categories = [extra];
    applySeatCategoryOverlay(schema);
    expect(schema.category_prices.map((c) => c.index)).toEqual([1, 2, 3]);
    const seats = schema.geometry.sections[0].rows[0].seats;
    expect(seats[1].category_index).toBe(3);
    // The map shows the price of the seat's own tier.
    expect(buildPriceLabel(seats[1].category_index, schema.category_prices)).toBe('12.00 EUR');
    const svg = buildSeatMapSVG(schema.geometry, schema.category_prices, {});
    expect(svg).toContain('data-cat="3"');
  });

  it('is idempotent', () => {
    const schema = makeSchema();
    schema.seat_category_index = { 's|1|a1': 2 };
    schema.extra_categories = [];
    applySeatCategoryOverlay(schema);
    applySeatCategoryOverlay(schema);
    expect(schema.geometry.sections[0].rows[0].seats[0].category_index).toBe(2);
    expect(schema.category_prices).toHaveLength(2);
  });
});
