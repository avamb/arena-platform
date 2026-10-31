/**
 * store.ts — Pure wiring helpers for the WID-R1 purchase-loop integration.
 */
import type { Geometry, CategoryPrice, Tier, FeedSession, FeedEvent } from '../types.js';
import type { PublicGAItem } from './checkout.js';
import { buildSeatedLines, buildGaLine, emptyCart, type CartState } from './cart.js';

export type WidgetStage = 'selecting' | 'buyer-form' | 'redirecting' | 'order-status';

const CHECKOUT_TOKEN_KEY = 'arena_checkout_token';

/**
 * Storage key for one widget's checkout token.
 *
 * The key is SCOPED to the event (or, lacking an event id, the session) the
 * widget was mounted for, because `sessionStorage` is shared by the whole
 * origin: `tickets.arenasoldout.com` serves every promoter's every event from
 * one origin, so an unscoped key made a checkout started on one event page
 * resume on a different one — a buyer who finished an order and then opened
 * another event was shown their previous order's "payment succeeded" screen
 * instead of that event's ticket picker.
 *
 * An empty scope keeps the legacy unscoped key, so a single-event embed on a
 * customer's own domain behaves exactly as before.
 */
export function checkoutTokenKey(scope?: string | null): string {
  const s = (scope ?? '').trim();
  return s ? `${CHECKOUT_TOKEN_KEY}:${s}` : CHECKOUT_TOKEN_KEY;
}

export function saveCheckoutToken(
  token: string,
  storage: Storage = sessionStorage,
  scope?: string | null,
): void {
  try { storage.setItem(checkoutTokenKey(scope), token); } catch { /* unavailable */ }
}

export function restoreCheckoutToken(
  storage: Storage = sessionStorage,
  scope?: string | null,
): string | null {
  try { return storage.getItem(checkoutTokenKey(scope)); } catch { return null; }
}

export function clearCheckoutToken(
  storage: Storage = sessionStorage,
  scope?: string | null,
): void {
  try { storage.removeItem(checkoutTokenKey(scope)); } catch { /* ignore */ }
}

/** True for a date the organizer cancelled: it stays listed but cannot be bought. */
export function isSessionCancelled(s: FeedSession | null | undefined): boolean {
  return s?.status === 'cancelled';
}

/** True when the event has dates and every one of them was cancelled. */
export function allSessionsCancelled(ev: FeedEvent | null | undefined): boolean {
  return !!ev && ev.sessions.length > 0 && ev.sessions.every(isSessionCancelled);
}

/**
 * Pick the session the picker opens on when the event loads.
 *
 * Prefers the deep-linked session id (`session-id`, `?session=` of a QR for
 * one date), then the first non-cancelled session by start time. A deep link to
 * a cancelled date is not honoured: a buyer who scanned an old poster lands on
 * the next date that can be bought, not on a date that cannot. Unknown or
 * foreign ids fall through to the same choice. When every date was cancelled
 * there is nothing to open: the result is null and the widget shows the
 * "cancelled" notice instead of a picker (UXH-23).
 */
export function pickInitialSession(ev: FeedEvent, deepLinkId: string): FeedSession | null {
  if (deepLinkId) {
    const found = ev.sessions.find((s) => s.id === deepLinkId);
    if (found && !isSessionCancelled(found)) return found;
  }
  const upcoming = ev.sessions
    .filter((s) => !isSessionCancelled(s))
    .sort((a, b) => a.start_at.localeCompare(b.start_at));
  return upcoming[0] ?? null;
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
 * Seated categories of a session with a seat map: the session tiers that
 * category_prices refer to. They have no quantity card (a seat is taken by
 * choosing it on the plan), so without a card of their own a free seated
 * category is invisible next to the paid cards.
 */
export function seatedTiersOf(sessionTiers: Tier[], categoryPrices: CategoryPrice[]): Tier[] {
  const seatedIds = new Set(categoryPrices.map((cp) => cp.tier_id).filter(Boolean));
  return sessionTiers.filter((t) => seatedIds.has(t.id));
}

/** True when every one of the tiers is free at the moment (effective price 0). */
export function tiersAreFree(tiers: Tier[]): boolean {
  return tiers.length > 0 && tiers.every((t) => (t.current_price ?? t.price_amount) === 0);
}

/**
 * GA tiers of a PLAN-LESS session, or null when the session has a schema.
 *
 * A session without `schema_url` never mounts the seat map, so the
 * schema-loaded callback that normally calls identifyGaTiers never fires.
 * Without a schema there are no seated categories at all: every tier of the
 * session is a general-admission tier and must render as a quantity card.
 * Returning null (not []) for a session WITH a schema tells the caller to
 * leave the schema-driven state alone.
 */
export function planlessGaTiers(
  session: { schema_url?: string | null; tiers?: Tier[] | null } | null | undefined,
): Tier[] | null {
  if (!session || session.schema_url) return null;
  return session.tiers ?? [];
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
