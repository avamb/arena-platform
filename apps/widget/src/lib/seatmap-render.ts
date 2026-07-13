/**
 * seatmap-render.ts — pure SVG builder for the Arena seat map.
 *
 * WID-B performance contract:
 *   • `buildSeatMapSVG()` is a pure, side-effect-free string builder.
 *     It runs synchronously and returns a complete SVG string for 1 500 seats
 *     well under 100 ms on current hardware.  (The only DOM API it touches is
 *     `DOMParser`, used read-only to sanitize the untrusted decor fragment;
 *     without a DOM the decor layer is omitted — fail closed.)
 *   • `applySeatStatusUpdate()` does keyed DOM mutations: it locates each
 *     changed seat by `data-seat-key` attribute and updates only the `fill`
 *     and `data-status` attributes.  This avoids a full re-render cycle for
 *     every status-polling tick (typically 2–5 s).
 *
 * Standing zones are rendered as labeled rectangle groups (bounds to be
 * extended in a future wave once the geometry model adds zone coordinates).
 *
 * Decor SVG originates from organizer uploads and is UNTRUSTED: it is passed
 * through the strict allowlist sanitizer (`sanitizeDecorSvg`) before being
 * placed inside `<g id="decor">`.  The decor layer is purely decorative and
 * is marked `aria-hidden` individually; the map root itself stays exposed to
 * assistive technology (`role="group"` + accessible name) so the interactive
 * seat nodes remain reachable.
 */

import type { Geometry, CategoryPrice, SeatStatusValue } from '../types.js';
import { sanitizeDecorSvg } from './svg-sanitize.js';

// ─── Price label helper ───────────────────────────────────────────────────────

/**
 * Build a human-readable price label for an aria-label from a seat category.
 *
 * Priority:
 *   1. `price_hint` + `currency_hint` (e.g. "22.00 EUR")
 *   2. `price_amount / 100` + `currency`  (e.g. "50.00 CZK")
 *   3. Empty string when no price info is available.
 *
 * The returned string uses only ASCII-safe characters so it is safe to embed
 * directly in an XML attribute value (no further escaping required beyond the
 * standard `xmlAttr()` call applied to the full aria-label).
 */
export function buildPriceLabel(
  categoryIndex: number,
  categoryPrices: CategoryPrice[],
): string {
  const cp = categoryPrices.find((c) => c.index === categoryIndex);
  if (!cp) return '';
  if (cp.price_hint && cp.currency_hint) {
    return `${cp.price_hint} ${cp.currency_hint}`;
  }
  if (cp.price_amount !== undefined && cp.currency) {
    const amount = (cp.price_amount / 100).toFixed(2);
    return `${amount} ${cp.currency}`;
  }
  return '';
}

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

/**
 * Fill color used to highlight seats that are in conflict (from a 409
 * `reservation.seats_conflict` response).  WCAG-AA red — the same value
 * used for inline error text throughout the widget UI.
 */
export const CONFLICT_COLOR = '#b91c1c';

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

  // Accessible name for the map root: include section context so screen-reader
  // users know what the group contains before diving into individual seats.
  const sectionNames = sections.map((s) => s.name).filter((n) => n.trim() !== '');
  const shownSections = sectionNames.slice(0, 5).join(', ');
  const mapLabel =
    sectionNames.length > 0
      ? `Seat map — sections: ${shownSections}${sectionNames.length > 5 ? ', …' : ''}`
      : 'Seat map';

  // ── SVG root (exposed to AT: interactive seats live inside) ──
  parts.push(
    `<svg xmlns="http://www.w3.org/2000/svg"` +
      ` viewBox="0 0 ${w} ${h}"` +
      ` width="${w}" height="${h}"` +
      ` role="group" aria-label="${xmlAttr(mapLabel)}"` +
      `>`,
  );

  // ── Decor backdrop (untrusted organizer upload — sanitized, decorative) ──
  if (decor_svg) {
    const safeDecor = sanitizeDecorSvg(decor_svg);
    if (safeDecor) {
      parts.push(`<g id="decor" aria-hidden="true">${safeDecor}</g>`);
    }
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
  // Roving-tabindex model (WID-R4): the FIRST seat in each row gets
  // tabindex="0" (the row's initial Tab stop).  All other seats in the row
  // get tabindex="-1" so they are reachable only via ArrowLeft/Right keys.
  // ArrowUp/Down move between rows; Tab/Shift+Tab jump between rows.
  // This keeps the seat map navigable without flooding the global Tab order
  // with hundreds of individual seat stops.
  parts.push('<g id="seats">');
  for (const section of sections) {
    parts.push(
      `<g data-section-key="${xmlAttr(section.key)}" aria-label="${xmlAttr(section.name)}">`,
    );
    for (const row of section.rows) {
      parts.push(
        `<g data-row-key="${xmlAttr(row.key)}" aria-label="Row ${xmlAttr(row.name)}">`,
      );
      row.seats.forEach((seat, seatIndexInRow) => {
        const status = seatStatuses[seat.key] ?? 'available';
        const fill = seatFillColor(seat.category_index, status, catColors);
        const r = seat.radius > 0 ? seat.radius : 8;
        // Roving tabindex: only the first seat in each row is a Tab stop.
        const tabIdx = seatIndexInRow === 0 ? '0' : '-1';
        // Build aria-label: section, row, seat, price (if available), status.
        const priceLabel = buildPriceLabel(seat.category_index, categoryPrices);
        const rawLabel = priceLabel
          ? `${section.name}, row ${row.name}, seat ${seat.number}, ${priceLabel}, ${status}`
          : `${section.name}, row ${row.name}, seat ${seat.number}, ${status}`;
        const ariaLabel = xmlAttr(rawLabel);
        parts.push(
          `<circle` +
            ` data-seat-key="${xmlAttr(seat.key)}"` +
            ` cx="${seat.x}" cy="${seat.y}" r="${r}"` +
            ` fill="${fill}"` +
            ` data-status="${status}"` +
            ` data-cat="${seat.category_index}"` +
            ` role="button"` +
            ` tabindex="${tabIdx}"` +
            ` aria-label="${ariaLabel}"` +
            `/>`,
        );
      });
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
    el.setAttribute('aria-label', prev.replace(/,\s+[\w -]+$/, `, ${status}`));
  }
}

// ─── Conflict highlight ───────────────────────────────────────────────────────

/**
 * Highlight seats that are in conflict after a 409 `reservation.seats_conflict`
 * response from `checkout/start` or `recover`.
 *
 * Marks each conflicting seat circle with:
 *  - `fill`          → `CONFLICT_COLOR` (#b91c1c, WCAG-AA error red)
 *  - `data-status`   → `"conflict"` (widget-only overlay value)
 *  - `aria-label`    → trailing status replaced with "conflict — not available"
 *
 * This is a keyed DOM mutation that does not trigger a full SVG re-render.
 *
 * @param container    DOM element wrapping the rendered SVG.
 * @param conflictKeys Set of seat_key strings that are in conflict.
 */
export function applyConflictHighlight(
  container: Element,
  conflictKeys: ReadonlySet<string>,
): void {
  for (const key of conflictKeys) {
    const selector = `circle[data-seat-key="${cssAttrEscape(key)}"]`;
    const el = container.querySelector<SVGCircleElement>(selector);
    if (!el) continue;

    el.setAttribute('fill', CONFLICT_COLOR);
    el.setAttribute('data-status', 'conflict');

    // Update aria-label so assistive technology announces the conflict state.
    const prev = el.getAttribute('aria-label') ?? '';
    const updated = prev.replace(/,\s+[\w -]+$/, ', conflict — not available');
    el.setAttribute(
      'aria-label',
      updated !== prev ? updated : `${prev}, conflict — not available`,
    );
  }
}

// ─── Selection highlight ──────────────────────────────────────────────────────

/**
 * Apply or remove the selection highlight stroke on seat circles.
 * Diffs previous vs current selection to only mutate changed elements.
 */
export function applySelectionHighlights(
  container: Element,
  current: ReadonlySet<string>,
  previous: ReadonlySet<string>,
  accentColor = '#4f46e5',
): void {
  // Remove highlights from deselected seats
  for (const key of previous) {
    if (!current.has(key)) {
      const el = container.querySelector<Element>(`circle[data-seat-key="${cssAttrEscape(key)}"]`);
      if (el) {
        el.removeAttribute('stroke');
        el.removeAttribute('stroke-width');
        el.setAttribute('data-selected', 'false');
        // restore aria-label status suffix
        const prev = el.getAttribute('aria-label') ?? '';
        el.setAttribute('aria-label', prev.replace(/,\s+selected$/, ''));
      }
    }
  }
  // Add highlights to newly selected seats
  for (const key of current) {
    if (!previous.has(key)) {
      const el = container.querySelector<Element>(`circle[data-seat-key="${cssAttrEscape(key)}"]`);
      if (el) {
        el.setAttribute('stroke', accentColor);
        el.setAttribute('stroke-width', '2.5');
        el.setAttribute('data-selected', 'true');
        const prev = el.getAttribute('aria-label') ?? '';
        if (!prev.endsWith(', selected')) {
          el.setAttribute('aria-label', `${prev}, selected`);
        }
      }
    }
  }
}

/**
 * Clear a previously-applied conflict highlight by restoring each seat's
 * real status from the live `seatStatuses` map.
 *
 * Call this when the user dismisses the conflict notice or removes conflicting
 * lines from the cart so the seats return to their normal visual state.
 *
 * @param container    DOM element wrapping the rendered SVG.
 * @param conflictKeys Set of seat_key strings whose highlight should be cleared.
 * @param catColorMap  Category-index → hex-color for "available" fills.
 * @param seatStatuses Current seat_key → SeatStatusValue snapshot.
 */
export function clearConflictHighlight(
  container: Element,
  conflictKeys: ReadonlySet<string>,
  catColorMap: ReadonlyMap<number, string>,
  seatStatuses: Record<string, SeatStatusValue>,
): void {
  const delta: Record<string, SeatStatusValue> = {};
  for (const key of conflictKeys) {
    delta[key] = seatStatuses[key] ?? 'available';
  }
  if (Object.keys(delta).length > 0) {
    applySeatStatusUpdate(container, delta, catColorMap);
  }
}
