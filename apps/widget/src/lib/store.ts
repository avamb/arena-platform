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
