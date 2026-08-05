/**
 * store.ts — Pure wiring helpers for the WID-R1 purchase-loop integration.
 */
import type { Geometry, CategoryPrice, Tier, FeedSession } from '../types.js';
import type { PublicGAItem } from './checkout.js';
import { buildSeatedLines, buildGaLine, emptyCart, type CartState } from './cart.js';

export type WidgetStage = 'selecting' | 'buyer-form' | 'redirecting' | 'order-status';

const CHECKOUT_TOKEN_KEY = 'arena_checkout_token';

export function saveCheckoutToken(token: string, storage: Storage = sessionStorage): void {
  try { storage.setItem(CHECKOUT_TOKEN_KEY, token); } catch { /* unavailable */ }
}

export function restoreCheckoutToken(storage: Storage = sessionStorage): string | null {
  try { return storage.getItem(CHECKOUT_TOKEN_KEY); } catch { return null; }
}

export function clearCheckoutToken(storage: Storage = sessionStorage): void {
  try { storage.removeItem(CHECKOUT_TOKEN_KEY); } catch { /* ignore */ }
}

export function getCheckoutTokenFromSearch(search: string): string | null {
  const params = new URLSearchParams(search);
  const v = params.get('checkout_token');
  return v && v.trim() ? v.trim() : null;
}

export function totalSelectionCount(
  selectedSeatKeys: ReadonlySet<string>,
  gaQuantities: ReadonlyMap<string, number>,
): number {
  let n = selectedSeatKeys.size;
  for (const q of gaQuantities.values()) n += q;
  return n;
}

export function buildGaItems(gaQuantities: ReadonlyMap<string, number>): PublicGAItem[] {
  const items: PublicGAItem[] = [];
  for (const [tierId, qty] of gaQuantities) {
    if (qty > 0) items.push({ tier_id: tierId, quantity: qty });
  }
  return items;
}

export interface BuildCartParams {
  selectedSeatKeys: ReadonlySet<string>;
  gaQuantities: ReadonlyMap<string, number>;
  session: FeedSession;
  seatCategoryIndex: ReadonlyMap<string, number>;
  categoryByCategoryIndex: ReadonlyMap<number, CategoryPrice>;
  tierById: ReadonlyMap<string, Tier>;
}

export function buildCartFromSelection({
  selectedSeatKeys, gaQuantities, session, seatCategoryIndex, categoryByCategoryIndex, tierById,
}: BuildCartParams): CartState {
  const lines = [];
  if (selectedSeatKeys.size > 0) {
    lines.push(...buildSeatedLines([...selectedSeatKeys], categoryByCategoryIndex, tierById, seatCategoryIndex));
  }
  for (const tier of session.tiers) {
    const qty = gaQuantities.get(tier.id) ?? 0;
    if (qty > 0) {
      lines.push(buildGaLine(tier.id, tier.id, tier.name, qty, tier.price_amount, tier.currency));
    }
  }
  return { ...emptyCart(), lines };
}

export function buildSeatCategoryIndex(geometry: Geometry): Map<string, number> {
  const m = new Map<string, number>();
  for (const section of geometry.sections) {
    for (const row of section.rows) {
      for (const seat of row.seats) {
        m.set(seat.key, seat.category_index);
      }
    }
  }
  return m;
}

export function buildCategoryByIndex(categoryPrices: CategoryPrice[]): Map<number, CategoryPrice> {
  const m = new Map<number, CategoryPrice>();
  for (const cp of categoryPrices) m.set(cp.index, cp);
  return m;
}

export function buildTierById(tiers: Tier[]): Map<string, Tier> {
  const m = new Map<string, Tier>();
  for (const t of tiers) m.set(t.id, t);
  return m;
}

/**
 * Identify GA tiers: session tiers that are NOT referenced in category_prices.
 * These should render as always-visible GA tier cards under the seat map.
 */
export function identifyGaTiers(sessionTiers: Tier[], categoryPrices: CategoryPrice[]): Tier[] {
  const seatedTierIds = new Set(categoryPrices.map((cp) => cp.tier_id).filter(Boolean));
  return sessionTiers.filter((t) => !seatedTierIds.has(t.id));
}

/**
 * A GA area that renders as a clickable polygon on the seat map (AB-40D).
 *
 * The widget spec (08_architecture/16_ticket_widget_ux_and_technology_ru.md
 * §11) mandates "одна поверхность, ноль переключателей" — GA areas with a
 * hit-test polygon are rendered directly on the hall map alongside the
 * seats, and clicking one opens the same inline quantity picker used by the
 * always-visible GA tier cards for GA-only plans.
 */
export interface GaArea {
  categoryIndex: number;
  name: string;
  color: string;
  /** Declared bulk capacity from geometry (upper bound of the quantity picker). */
  capacity: number;
  /** Canvas-space polygon vertices. Always non-empty for entries returned here. */
  polygon: import('../types.js').GeometryPoint[];
  /** Resolved tier — the id, name, price, currency needed to build a cart line. */
  tierId: string;
  tierName: string;
  priceAmount: number;
  currency: string;
}

/**
 * Extract GA categories that render as clickable polygons on the seat map.
 *
 * Skips GA categories without a polygon: those are the hand-entered
 * GA-only path (AB-40 C1) and render as an always-visible tier card
 * beneath the map instead. Also skips GA categories that have no resolved
 * tier binding (no price to charge for the reservation).
 */
export function identifyGaAreas(
  geometry: Geometry,
  categoryPrices: CategoryPrice[],
): GaArea[] {
  const cpByIndex = new Map<number, CategoryPrice>();
  for (const cp of categoryPrices) cpByIndex.set(cp.index, cp);
  const areas: GaArea[] = [];
  for (const cat of geometry.categories) {
    if (cat.kind !== 'general_admission') continue;
    if (!cat.polygon || cat.polygon.length < 3) continue;
    const cp = cpByIndex.get(cat.index);
    if (!cp || !cp.tier_id) continue;
    areas.push({
      categoryIndex: cat.index,
      name: cat.name,
      color: cp.color || cat.color || '#4f46e5',
      capacity: cat.capacity ?? 0,
      polygon: cat.polygon,
      tierId: cp.tier_id,
      tierName: cp.tier_name ?? cat.name,
      priceAmount: cp.price_amount ?? 0,
      currency: cp.currency ?? '',
    });
  }
  return areas;
}
