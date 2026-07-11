/**
 * cart.ts — Cart and hold-timer logic for the Arena Tickets widget (WID-C).
 *
 * All functions are pure and side-effect-free — no DOM, no fetch, no timers.
 * The UI layer is responsible for polling `countdownSeconds()` on a fixed
 * interval (e.g. every second).
 *
 * Responsibilities:
 *  • Cart state modelling (seated + GA line items).
 *  • Countdown rendering from the hold `expires_at` ISO timestamp.
 *  • T-2min warning flag for the UI alert state.
 *  • Line removal and total calculation.
 *  • Platform-price total (prices from the tier catalogue, not buyer-entered).
 */

import type { CategoryPrice, Tier } from '../types.js';

// ─── Types ────────────────────────────────────────────────────────────────────

/** A single line in the cart. */
export interface CartLineItem {
  /** 'seated' for reserved seats; 'ga' for general-admission zone quantity. */
  type: 'seated' | 'ga';
  tierId: string;
  tierName: string;
  /** Number of seats (seated) or tickets (GA) on this line. */
  quantity: number;
  /** Per-unit platform price in the smallest currency unit (e.g. kopecks / cents). */
  priceAmount: number;
  currency: string;
  /** Seat keys for seated lines. Empty array for GA lines. */
  seatKeys: string[];
  /** GA zone key for GA lines. Empty string for seated lines. */
  zoneKey: string;
}

/** The overall cart state including hold metadata. */
export interface CartState {
  /** Backend-issued checkout token; null before the hold POST completes. */
  checkoutToken: string | null;
  /**
   * ISO-8601 UTC timestamp at which the hold expires.
   * Null until a hold is successfully created.
   */
  expiresAt: string | null;
  lines: CartLineItem[];
}

// ─── Factory helpers ──────────────────────────────────────────────────────────

/** Return a fresh empty cart state. */
export function emptyCart(): CartState {
  return { checkoutToken: null, expiresAt: null, lines: [] };
}

/**
 * Build seated CartLineItems from a list of seat keys and the schema's
 * `category_prices` + `tiers` arrays.
 *
 * Grouping: one line per (tierId, currency) pair so that seats belonging to
 * different price categories end up on separate lines.
 *
 * Seats whose category_index does not map to any CategoryPrice entry are
 * grouped into a fallback line with tierId='', tierName='Unknown', price=0.
 */
export function buildSeatedLines(
  seatKeys: string[],
  categoryByCategoryIndex: ReadonlyMap<number, CategoryPrice>,
  tierById: ReadonlyMap<string, Tier>,
  seatCategoryIndex: ReadonlyMap<string, number>,
): CartLineItem[] {
  // Group seat keys by tierId.
  const groups = new Map<string, { tierName: string; priceAmount: number; currency: string; keys: string[] }>();

  for (const key of seatKeys) {
    const catIdx = seatCategoryIndex.get(key) ?? -1;
    const cp = categoryByCategoryIndex.get(catIdx);
    const tierId = cp?.tier_id ?? '';
    const tier = tierId ? tierById.get(tierId) : undefined;
    const tierName = tier?.name ?? cp?.tier_name ?? 'Unknown';
    const priceAmount = tier?.price_amount ?? 0;
    const currency = tier?.currency ?? cp?.currency_hint ?? '';

    const existing = groups.get(tierId);
    if (existing) {
      existing.keys.push(key);
    } else {
      groups.set(tierId, { tierName, priceAmount, currency, keys: [key] });
    }
  }

  const lines: CartLineItem[] = [];
  for (const [tierId, g] of groups) {
    lines.push({
      type: 'seated',
      tierId,
      tierName: g.tierName,
      quantity: g.keys.length,
      priceAmount: g.priceAmount,
      currency: g.currency,
      seatKeys: g.keys,
      zoneKey: '',
    });
  }
  return lines;
}

/**
 * Build a single GA CartLineItem.
 */
export function buildGaLine(
  zoneKey: string,
  tierId: string,
  tierName: string,
  quantity: number,
  priceAmount: number,
  currency: string,
): CartLineItem {
  return {
    type: 'ga',
    tierId,
    tierName,
    quantity,
    priceAmount,
    currency,
    seatKeys: [],
    zoneKey,
  };
}

// ─── State mutations (pure) ───────────────────────────────────────────────────

/**
 * Remove a line item by index.  Returns a new CartState.
 * Out-of-range indices are silently ignored.
 */
export function removeCartLine(state: CartState, idx: number): CartState {
  if (idx < 0 || idx >= state.lines.length) return state;
  const lines = [...state.lines.slice(0, idx), ...state.lines.slice(idx + 1)];
  return { ...state, lines };
}

/**
 * Attach the backend hold response (checkout token + expiry) to the cart.
 */
export function applyHoldResponse(
  state: CartState,
  checkoutToken: string,
  expiresAt: string,
): CartState {
  return { ...state, checkoutToken, expiresAt };
}

// ─── Aggregates ───────────────────────────────────────────────────────────────

/**
 * Compute the total platform price across all lines.
 *
 * If lines span multiple currencies (unusual in practice), only the first
 * currency is returned and amounts in other currencies are still summed — the
 * UI should display a warning in that case.  An empty cart returns
 * `{ amount: 0, currency: '' }`.
 */
export function cartTotal(lines: CartLineItem[]): { amount: number; currency: string } {
  if (lines.length === 0) return { amount: 0, currency: '' };
  let amount = 0;
  const currency = lines[0].currency;
  for (const line of lines) {
    amount += line.priceAmount * line.quantity;
  }
  return { amount, currency };
}

/**
 * Total number of individual tickets across all cart lines.
 */
export function cartItemCount(lines: CartLineItem[]): number {
  return lines.reduce((sum, l) => sum + l.quantity, 0);
}

// ─── Countdown ────────────────────────────────────────────────────────────────

/**
 * Compute the number of whole seconds remaining until `expiresAt`.
 *
 * @param expiresAt  ISO-8601 UTC string from the hold API (e.g. `expires_at`).
 * @param nowMs      Current epoch ms; defaults to `Date.now()` when omitted.
 * @returns          Remaining seconds, clamped to 0 (never negative).
 */
export function countdownSeconds(expiresAt: string, nowMs?: number): number {
  const expiry = new Date(expiresAt).getTime();
  const now = nowMs ?? Date.now();
  if (isNaN(expiry)) return 0;
  return Math.max(0, Math.floor((expiry - now) / 1000));
}

/**
 * Return `true` when the hold is within the T-2min warning window.
 *
 * Triggers at ≤ 120 seconds remaining so the UI can switch to an amber
 * "Your seats expire soon" banner.  Returns `false` when already expired
 * (`secondsLeft === 0`).
 */
export function isTwoMinWarning(secondsLeft: number): boolean {
  return secondsLeft > 0 && secondsLeft <= 120;
}

/**
 * Format a countdown as `M:SS` (e.g. `9:05`, `0:42`).
 */
export function formatCountdown(secondsLeft: number): string {
  const s = Math.max(0, Math.floor(secondsLeft));
  const m = Math.floor(s / 60);
  const rem = s % 60;
  return `${m}:${rem.toString().padStart(2, '0')}`;
}
