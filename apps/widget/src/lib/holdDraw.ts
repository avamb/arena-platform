/**
 * holdDraw.ts — how the seat map draws the buyer's own hold (spec 67): the
 * own-hold colours, the pulsing "free seats nearby" and the search for them.
 *
 * Part of the lazy hold chunk (lib/holdRuntime.ts). SeatMapView receives this
 * module as a prop once the chunk is loaded; before that there is no hold and
 * nothing to draw. Do not import it statically from the main bundle.
 */

import type { CategoryPrice, SeatStatusValue } from '../types.js';
import type { SeatSpot } from './holdBasics.js';
import { OWN_HOLD_COLOR, cssAttrEscape, seatFillColor } from './seatmap-render.js';

/** How many nearest free seats are pulsed after a seat was taken. */
export const NEAREST_SEATS = 4;
/** How long they pulse. */
export const PULSE_MS = 6_000;

/**
 * Up to `limit` free seats of the same category (tier) and the same price as
 * `target`, the seats of the same row first, then by distance.
 */
export function nearestSeats(target: SeatSpot, all: readonly SeatSpot[], limit: number = NEAREST_SEATS): string[] {
  const dist = (s: SeatSpot): number => Math.hypot(s.x - target.x, s.y - target.y);
  return all
    .filter((s) => s.key !== target.key && s.status === 'available' && s.tierId === target.tierId && s.price === target.price)
    .map((s) => ({ key: s.key, same: s.rowKey === target.rowKey, d: dist(s) }))
    .sort((a, b) => Number(b.same) - Number(a.same) || a.d - b.d || (a.key < b.key ? -1 : 1))
    .slice(0, Math.max(0, limit))
    .map((s) => s.key);
}

/**
 * The nearest free seats of the tapped seat's category and price on the drawn
 * plan. Positions come from the rendered circles (`getBBox`), never from the
 * plan's cx/cy.
 */
export function nearestOnPlan(container: Element, seatKey: string, categoryPrices: readonly CategoryPrice[]): string[] {
  const circles = container.querySelectorAll<SVGGraphicsElement>('circle[data-seat-key]');
  const spots: SeatSpot[] = [];
  let target: SeatSpot | null = null;
  for (const el of circles) {
    const key = el.getAttribute('data-seat-key') ?? '';
    if (!key) continue;
    const cat = parseInt(el.getAttribute('data-cat') ?? '0', 10);
    const cp = categoryPrices.find((c) => c.index === cat);
    let box: DOMRect | SVGRect;
    try {
      box = el.getBBox();
    } catch {
      continue; // not rendered
    }
    const own = el.getAttribute('data-hold');
    const spot: SeatSpot = {
      key,
      x: box.x + box.width / 2,
      y: box.y + box.height / 2,
      rowKey: el.closest('[data-row-key]')?.getAttribute('data-row-key') ?? '',
      tierId: cp?.tier_id ?? `cat:${cat}`,
      price: cp ? (cp.current_price ?? cp.price_amount ?? null) : null,
      // The seat's own state: a seat of the buyer's hold or conflict is not offered.
      status: own ? 'own' : (el.getAttribute('data-status') ?? 'available'),
    };
    if (key === seatKey) target = spot;
    else spots.push(spot);
  }
  if (!target) return [];
  return nearestSeats(target, spots, NEAREST_SEATS);
}

/**
 * Paint the seats of the buyer's own hold.
 *
 *  - `held`    the server confirmed the seat: `OWN_HOLD_COLOR`, `data-hold="own"`;
 *  - `pending` the click is on its way: the seat keeps its colour, half
 *              transparent, `data-hold="pending"`;
 *  - a seat that left both sets goes back to the colour of its status.
 *
 * Keyed DOM mutation like the other `apply*` helpers: no re-render.
 */
export function applyHoldStyles(
  container: Element,
  held: ReadonlySet<string>,
  pending: ReadonlySet<string>,
  previous: ReadonlySet<string>,
  catColorMap: ReadonlyMap<number, string>,
  seatStatuses: Record<string, SeatStatusValue>,
): void {
  const find = (key: string): Element | null =>
    container.querySelector<Element>(`circle[data-seat-key="${cssAttrEscape(key)}"]`);

  for (const key of previous) {
    if (held.has(key) || pending.has(key)) continue;
    const el = find(key);
    if (!el) continue;
    el.removeAttribute('data-hold');
    el.removeAttribute('fill-opacity');
    // The seat is free again for everybody until the next poll says otherwise.
    const status: SeatStatusValue = seatStatuses[key] === 'held' ? 'available' : (seatStatuses[key] ?? 'available');
    const catIdx = parseInt(el.getAttribute('data-cat') ?? '0', 10);
    el.setAttribute('fill', seatFillColor(catIdx, status, catColorMap));
    el.setAttribute('data-status', status);
  }
  for (const key of held) {
    const el = find(key);
    if (!el) continue;
    el.setAttribute('fill', OWN_HOLD_COLOR);
    el.removeAttribute('fill-opacity');
    el.setAttribute('data-hold', 'own');
  }
  for (const key of pending) {
    const el = find(key);
    if (!el) continue;
    el.setAttribute('fill-opacity', '0.5');
    el.setAttribute('data-hold', 'pending');
  }
}

/** Mark (or unmark) seats with the pulsing outline of "free seats nearby". */
export function setPulse(container: Element, keys: Iterable<string>, on: boolean): void {
  for (const key of keys) {
    const el = container.querySelector<Element>(`circle[data-seat-key="${cssAttrEscape(key)}"]`);
    if (!el) continue;
    if (on) el.setAttribute('data-pulse', 'true');
    else el.removeAttribute('data-pulse');
  }
}

/** The pulsing "free seats nearby" of one seat map. */
export interface SeatPulser {
  /**
   * Pulse up to 4 free seats of the tapped seat's category and price for 6 s.
   * Returns how many seats were lit.
   */
  highlight(seatKey: string, categoryPrices: readonly CategoryPrice[]): number;
  /** The map goes away: stop the timer. */
  dispose(): void;
}

/** A pulser for the map in `container()` (read at each step: the map may be redrawn). */
export function createPulser(container: () => Element | undefined): SeatPulser {
  let pulsed: string[] = [];
  let timer: ReturnType<typeof setTimeout> | null = null;

  function clear(): void {
    if (timer) clearTimeout(timer);
    timer = null;
    const c = container();
    if (c && pulsed.length > 0) setPulse(c, pulsed, false);
    pulsed = [];
  }

  return {
    highlight(seatKey, categoryPrices) {
      clear();
      const c = container();
      if (!c) return 0;
      const keys = nearestOnPlan(c, seatKey, categoryPrices);
      if (keys.length === 0) return 0;
      pulsed = keys;
      setPulse(c, keys, true);
      timer = setTimeout(clear, PULSE_MS);
      return keys.length;
    },
    dispose() {
      if (timer) clearTimeout(timer);
    },
  };
}

/** What SeatMapView needs of this module (passed as its `holdDraw` prop). */
export interface HoldDraw {
  applyHoldStyles: typeof applyHoldStyles;
  createPulser: typeof createPulser;
}
