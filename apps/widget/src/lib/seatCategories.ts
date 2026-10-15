/**
 * S1a: per-seat category overlay for the /schema response.
 *
 * After `PATCH .../seats/category` repainted some seats, the server keeps the
 * stored geometry untouched and ships the per-seat truth beside it:
 *
 *  - `seat_category_index` maps `seat_key` to the category index that carries
 *    the seat's OWN tier (only for seats that left their plan category);
 *  - `extra_categories` holds synthetic categories for tiers that anchor no
 *    plan category, in the same shape as `category_prices`.
 *
 * Every consumer in the widget (seat map colours and price labels, cart price
 * lookup, quick-pick by category) reads `seat.category_index` and
 * `category_prices`, so the overlay is applied ONCE, right after the schema is
 * parsed: the geometry's seats get their effective index and the synthetic
 * categories join `category_prices`. Older servers send neither field and the
 * response is returned unchanged.
 */
import type { SchemaResponse } from '../types.js';

/**
 * Apply `seat_category_index` / `extra_categories` onto `schema` in place and
 * return it. Idempotent: the overlay fields are emptied once applied.
 */
export function applySeatCategoryOverlay(schema: SchemaResponse): SchemaResponse {
  const overrides = schema.seat_category_index;
  const extra = schema.extra_categories;
  const hasOverrides = !!overrides && Object.keys(overrides).length > 0;
  const hasExtra = Array.isArray(extra) && extra.length > 0;
  if (!hasOverrides && !hasExtra) return schema;

  if (hasExtra) {
    const known = new Set(schema.category_prices.map((c) => c.index));
    for (const e of extra) {
      if (!known.has(e.index)) schema.category_prices.push(e);
    }
  }
  if (hasOverrides) {
    for (const section of schema.geometry.sections) {
      for (const row of section.rows) {
        for (const seat of row.seats) {
          const idx = overrides[seat.key];
          if (typeof idx === 'number') seat.category_index = idx;
        }
      }
    }
  }
  schema.seat_category_index = {};
  schema.extra_categories = [];
  return schema;
}
