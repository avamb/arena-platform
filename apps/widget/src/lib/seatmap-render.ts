/**
 * seatmap-render.ts — pure SVG builder for the Arena seat map.
 *
 * WID-B performance contract:
 *   • `buildSeatMapSVG()` is a pure function — no DOM, no side-effects.
 *     It runs synchronously and returns a complete SVG string for 1 500 seats
 *     well under 100 ms on current hardware.
 *   • `applySeatStatusUpdate()` does keyed DOM mutations: it locates each
 *     changed seat by `data-seat-key` attribute and updates only the `fill`
 *     and `data-status` attributes.  This avoids a full re-render cycle for
 *     every status-polling tick (typically 2–5 s).
 *
 * Standing zones are rendered as labeled rectangle groups (bounds to be
 * extended in a future wave once the geometry model adds zone coordinates).
 *
 * Decor SVG is spliced verbatim inside `<g id="decor">` — the backend
 * stores it with deterministic, non-name-spaced serialisation.
 */

import type { Geometry, CategoryPrice, SeatStatusValue } from '../types.js';

// ─── Status color palette ─────────────────────────────────────────────────────

/** Fill color overrides for non-available seat statuses. */
export const STATUS_COLORS: Readonly<Record<string, string>> = {
  available: '', // resolved per-seat from category color
  held: '#f59e0b',
  sold: '#6b7280',
  blocked: '#d1d5db',
} as const;

/** Fallback color when a seat's category index is not resolved. */
export const FALLBACK_COLOR = '#d1d5db';

// ─── Helpers ─────────────────────────────────────────────────────────────────

/**
 * Escape a string for safe inclusion in an XML attribute value (double-quoted).
 * Handles `&`, `<`, `>`, `"`, `'`.
 */
export function xmlAttr(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

/**
 * Escape a string for use in a CSS attribute value selector `[attr="..."]`.
 * Inside a double-quoted CSS string, only `\` and `"` need escaping.
 */
export function cssAttrEscape(s: string): string {
  return s.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

/**
 * Resolve the fill color for a seat given its category index and current status.
 *
 * Available seats use the category color from `categoryPrices` (resolved tier)
 * or fall back to `FALLBACK_COLOR` when the category is unknown.
 */
export function seatFillColor(
  categoryIndex: number,
  status: SeatStatusValue | undefined,
  catColorMap: ReadonlyMap<number, string>,
): string {
  const s = status ?? 'available';
  if (s !== 'available') {
    return STATUS_COLORS[s] ?? FALLBACK_COLOR;
  }
  return catColorMap.get(categoryIndex) ?? FALLBACK_COLOR;
}

/**
 * Build a category-index → hex-color lookup map from the `category_prices`
 * array (falls back to geometry categories for entries without a tier).
 */
export function buildCategoryColorMap(
  categoryPrices: CategoryPrice[],
  geometryCategories: Geometry['categories'],
): Map<number, string> {
  const map = new Map<number, string>();
  // Geometry categories are the baseline; tier-resolved colors override them.
  for (const cat of geometryCategories) {
    if (cat.color) {
      map.set(cat.index, normalizeColor(cat.color));
    }
  }
  for (const cp of categoryPrices) {
    if (cp.color) {
      map.set(cp.index, normalizeColor(cp.color));
    }
  }
  return map;
}

/** Normalize a hex color: ensure it has a `#` prefix and lowercase digits. */
function normalizeColor(hex: string): string {
  const s = hex.trim().replace(/^#/, '').toLowerCase();
  return `#${s}`;
}

// ─── SVG builder ─────────────────────────────────────────────────────────────

/**
 * Build a complete SVG markup string from the geometry and current seat statuses.
 *
 * The returned string can be set as `innerHTML` of a container element to
 * produce the interactive seat map.  Subsequent status updates should use
 * `applySeatStatusUpdate()` to avoid a full re-render.
 *
 * @param geometry       Canonical geometry from the /schema endpoint.
 * @param categoryPrices Category-price array from the /schema endpoint.
 * @param seatStatuses   Current seat-key → status snapshot (can be empty on first render).
 */
export function buildSeatMapSVG(
  geometry: Geometry,
  categoryPrices: CategoryPrice[],
  seatStatuses: Record<string, SeatStatusValue>,
): string {
  const { canvas, sections, standing_zones, decor_svg } = geometry;
  const w = canvas.width > 0 ? canvas.width : 800;
  const h = canvas.height > 0 ? canvas.height : 600;

  const catColors = buildCategoryColorMap(categoryPrices, geometry.categories);

  const parts: string[] = [];

  // ── SVG root ──
  parts.push(
    `<svg xmlns="http://www.w3.org/2000/svg"` +
      ` viewBox="0 0 ${w} ${h}"` +
      ` width="${w}" height="${h}"` +
      ` role="img" aria-label="Seat map"` +
      `>`,
  );

  // ── Decor backdrop (verbatim splice) ──
  if (decor_svg) {
    parts.push(`<g id="decor">${decor_svg}</g>`);
  }

  // ── Standing zones as labeled placeholder groups ──
  if (standing_zones.length > 0) {
    parts.push('<g id="standing-zones">');
    for (const zone of standing_zones) {
      parts.push(
        `<g` +
          ` data-zone-key="${xmlAttr(zone.key)}"` +
          ` aria-label="${xmlAttr(zone.name)} (standing zone, capacity ${zone.capacity})"` +
          `>` +
          `<title>${xmlAttr(zone.name)}</title>` +
          `</g>`,
      );
    }
    parts.push('</g>');
  }

  // ── Seats ──
  parts.push('<g id="seats">');
  for (const section of sections) {
    parts.push(
      `<g data-section-key="${xmlAttr(section.key)}" aria-label="${xmlAttr(section.name)}">`,
    );
    for (const row of section.rows) {
      parts.push(
        `<g data-row-key="${xmlAttr(row.key)}" aria-label="Row ${xmlAttr(row.name)}">`,
      );
      for (const seat of row.seats) {
        const status = seatStatuses[seat.key] ?? 'available';
        const fill = seatFillColor(seat.category_index, status, catColors);
        const r = seat.radius > 0 ? seat.radius : 8;
        const ariaLabel = xmlAttr(
          `${section.name}, row ${row.name}, seat ${seat.number}, ${status}`,
        );
        parts.push(
          `<circle` +
            ` data-seat-key="${xmlAttr(seat.key)}"` +
            ` cx="${seat.x}" cy="${seat.y}" r="${r}"` +
            ` fill="${fill}"` +
            ` data-status="${status}"` +
            ` data-cat="${seat.category_index}"` +
            ` role="button"` +
            ` tabindex="0"` +
            ` aria-label="${ariaLabel}"` +
            `/>`,
        );
      }
      parts.push('</g>');
    }
    parts.push('</g>');
  }
  parts.push('</g>');

  parts.push('</svg>');
  return parts.join('');
}

// ─── Keyed DOM status update ──────────────────────────────────────────────────

/**
 * Apply a seat-status delta to an already-rendered SVG DOM container.
 *
 * Finds each changed seat circle by its `data-seat-key` attribute and updates
 * only `fill`, `data-status`, and `aria-label` — no re-render, no layout
 * reflow.  This satisfies the WID-B "statuses recolor without re-render"
 * requirement for 1 500-seat maps.
 *
 * @param container     DOM element wrapping the rendered SVG (e.g. a <div>).
 * @param delta         Map of seat_key → new status.
 * @param catColorMap   Category-index → hex-color lookup for "available" fills.
 */
export function applySeatStatusUpdate(
  container: Element,
  delta: Record<string, SeatStatusValue>,
  catColorMap: ReadonlyMap<number, string>,
): void {
  for (const [key, status] of Object.entries(delta)) {
    const selector = `circle[data-seat-key="${cssAttrEscape(key)}"]`;
    const el = container.querySelector<SVGCircleElement>(selector);
    if (!el) continue;

    const catIdx = parseInt(el.getAttribute('data-cat') ?? '0', 10);
    const fill = seatFillColor(catIdx, status, catColorMap);

    el.setAttribute('fill', fill);
    el.setAttribute('data-status', status);

    // Update the aria-label status suffix so screen readers announce the change.
    const prev = el.getAttribute('aria-label') ?? '';
    el.setAttribute('aria-label', prev.replace(/,\s+\w+$/, `, ${status}`));
  }
}
