/**
 * The pure rules of the category screens of a date (task 5-4). They are the
 * rules of the Telegram bot's categories dialog (eventbot/categories.go:
 * `categoryCapsFor`, `activeWindow`, `withWindowPrice`, `windowEdge`,
 * `windowEdgeLabel`, `parseCategoryQuantity`), ported without its Telegram
 * shape. Nothing here calls the API or draws anything, and the clock is
 * always a parameter.
 *
 * What a category allows, in short:
 *  - open and close: one press, only NEW sales stop;
 *  - price: only a fixed-price category. It reaches NEW purchases only. When
 *    the category follows a price schedule that covers now, the window in
 *    force now is the one that changes, otherwise the base price;
 *  - sale window: first day and last day (inclusive), venue time;
 *  - quantity: only a category that owns general-admission places;
 *  - a category in a chain keeps ONLY its price, because opening, closing,
 *    resizing or moving its window would fight the automatic hand-over.
 */
import type { TicketTierItem, TierPriceWindow, TierPriceWindowInput } from "@/lib/api/types";
import { addDays, displayDate } from "@/lib/dates";
import type { MessageKey } from "@/lib/i18n/en";
import { parseAmountMinor } from "@/lib/promo";

/** A typed quantity above this is a typo. */
export const MAX_CATEGORY_QUANTITY = 1_000_000;

// ─── what a category allows ──────────────────────────────────────────────────

export interface CategoryCaps {
  toggle: boolean;
  price: boolean;
  window: boolean;
  qty: boolean;
  /** Part of a chain (hands its places on, or receives them). */
  chain: boolean;
  seated: boolean;
  /** Free or pay-what-you-want: there is no price to type. */
  notFixed: boolean;
}

/** True when the category is in a chain: it points at a next one, has a step limit, or another category points at it. */
export function inChain(t: TicketTierItem, all: readonly TicketTierItem[]): boolean {
  if ((t.next_tier_id ?? null) !== null || (t.sell_limit ?? null) !== null) return true;
  return all.some((o) => o.next_tier_id === t.id);
}

/** What the screen offers on the card of `t` among the categories of its date. */
export function categoryCaps(t: TicketTierItem, all: readonly TicketTierItem[]): CategoryCaps {
  const chain = inChain(t, all);
  const fixed = t.pricing_mode === "fixed";
  const kind = t.kind ?? "";
  return {
    toggle: !chain,
    price: fixed,
    window: !chain,
    qty: kind === "ga" && !chain,
    chain,
    seated: kind === "seated",
    notFixed: !fixed,
  };
}

/** An absent flag is open. */
export function isCategoryOpen(t: TicketTierItem): boolean {
  return t.is_open !== false;
}

/** The price buyers pay now: the scheduled one when a window is in force, else the base price. */
export function categoryPriceNow(t: TicketTierItem): number {
  return t.current_price ?? t.price_amount;
}

/** The places the category cannot give up: sold plus on hold. */
export function quantityFloor(t: TicketTierItem): number {
  return (t.sold ?? 0) + (t.held ?? 0);
}

/** The name of the category a chained category hands its places to, or "". */
export function chainTargetName(t: TicketTierItem, all: readonly TicketTierItem[]): string {
  if ((t.next_tier_id ?? null) === null) return "";
  return all.find((o) => o.id === t.next_tier_id)?.name ?? "";
}

// ─── price ───────────────────────────────────────────────────────────────────

/** The index of the window in force at `now`, or -1. valid_to is exclusive, null is open-ended. */
export function activeWindowIndex(windows: readonly TierPriceWindow[], now: Date): number {
  const t = now.getTime();
  return windows.findIndex((w) => {
    const from = new Date(w.valid_from).getTime();
    const to = w.valid_to === null || w.valid_to === undefined ? Infinity : new Date(w.valid_to).getTime();
    return from <= t && t < to;
  });
}

/** The whole schedule as a PUT body, with window `i` priced at `amount`. */
export function withWindowPrice(windows: readonly TierPriceWindow[], i: number, amount: number): TierPriceWindowInput[] {
  return windows.map((w, k) => ({
    valid_from: w.valid_from,
    valid_to: w.valid_to ?? null,
    price_amount: k === i ? amount : w.price_amount,
  }));
}

export type PriceWrite =
  | { kind: "window"; windows: TierPriceWindowInput[]; from: string }
  | { kind: "base" };

/**
 * Where a new price goes. A category that follows a schedule covering now
 * changes the window in force (patching the base price under an active window
 * would change nothing buyers see); otherwise the base price.
 */
export function planPriceWrite(windows: readonly TierPriceWindow[], now: Date, amount: number): PriceWrite {
  const i = activeWindowIndex(windows, now);
  if (i < 0) return { kind: "base" };
  return { kind: "window", windows: withWindowPrice(windows, i, amount), from: windows[i].valid_from };
}

export type Parsed<T> = { ok: true; value: T } | { ok: false; error: MessageKey };

/** A typed price in major units as minor units of the currency. At least one minor unit. */
export function parseCategoryPrice(raw: string, currency: string): Parsed<number> {
  const p = parseAmountMinor(raw, currency);
  return p.ok ? p : { ok: false, error: "categories.price.error" };
}

/** A price in minor units back as the number a person types ("25" or "25.5"). */
export function priceInputValue(minor: number, currency: string): string {
  let digits = 2;
  try {
    digits = new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    digits = 2;
  }
  const major = minor / 10 ** digits;
  return digits === 0 ? String(major) : major.toFixed(digits).replace(/\.?0+$/, "");
}

// ─── quantity ────────────────────────────────────────────────────────────────

/** A whole number of places from 1 to MAX_CATEGORY_QUANTITY. */
export function parseCategoryQuantity(raw: string): Parsed<number> {
  const s = raw.replace(/[\s  ]/g, "");
  if (!/^\d{1,9}$/.test(s)) return { ok: false, error: "categories.qty.error" };
  const n = Number(s);
  if (n < 1 || n > MAX_CATEGORY_QUANTITY) return { ok: false, error: "categories.qty.error" };
  return { ok: true, value: n };
}

// ─── sale window, in the venue's zone ────────────────────────────────────────

function validZone(tz: string | null | undefined): string {
  if (tz === null || tz === undefined || tz === "") return "UTC";
  try {
    new Intl.DateTimeFormat("en", { timeZone: tz });
    return tz;
  } catch {
    return "UTC";
  }
}

interface Wall {
  y: number;
  m: number;
  d: number;
  h: number;
  mi: number;
  s: number;
}

function wallClock(ms: number, tz: string): Wall {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: tz,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(new Date(ms));
  const n = (type: string) => Number(parts.find((p) => p.type === type)?.value ?? "0");
  return { y: n("year"), m: n("month"), d: n("day"), h: n("hour"), mi: n("minute"), s: n("second") };
}

/** How many ms the zone is ahead of UTC at that instant. */
function offsetMs(ms: number, tz: string): number {
  const w = wallClock(ms, tz);
  return Date.UTC(w.y, w.m - 1, w.d, w.h, w.mi, w.s) - Math.floor(ms / 1000) * 1000;
}

/** The instant of 00:00 on a calendar day in a zone (DST safe). */
export function zonedMidnight(iso: string, timezone: string | null | undefined): Date {
  const tz = validZone(timezone);
  const [y, m, d] = iso.split("-").map(Number);
  const guess = Date.UTC(y, m - 1, d);
  const first = guess - offsetMs(guess, tz);
  const second = guess - offsetMs(first, tz);
  return new Date(second);
}

/**
 * The instant a window edge falls on. The first day of sale starts at its
 * midnight; the last day "inclusive" ends at the midnight AFTER it (the
 * wizard's own convention). RFC 3339, UTC.
 */
export function windowEdgeInstant(iso: string, timezone: string | null | undefined, inclusiveEnd: boolean): string {
  const day = inclusiveEnd ? addDays(iso, 1) : iso;
  return zonedMidnight(day, timezone).toISOString();
}

export interface EdgeView {
  /** The calendar day to show: for an end the LAST day. Null when the edge is not a midnight. */
  day: string | null;
}

/** The calendar day of an instant in a zone, "YYYY-MM-DD". */
export function dayOfInstant(instant: string, timezone: string | null | undefined): string {
  const tz = validZone(timezone);
  const w = wallClock(new Date(instant).getTime(), tz);
  return `${String(w.y).padStart(4, "0")}-${String(w.m).padStart(2, "0")}-${String(w.d).padStart(2, "0")}`;
}

/**
 * Reads a stored edge back the way the organizer typed it: a midnight is a
 * bare day (the last day for an end), anything else has no day and is shown
 * with its clock.
 */
export function edgeView(instant: string, timezone: string | null | undefined, isEnd: boolean): EdgeView {
  const tz = validZone(timezone);
  const w = wallClock(new Date(instant).getTime(), tz);
  if (w.h !== 0 || w.mi !== 0 || w.s !== 0) return { day: null };
  const day = dayOfInstant(instant, tz);
  return { day: isEnd ? addDays(day, -1) : day };
}

/** The edge as the card says it: "25.11.2026" for a midnight, otherwise null (the screen prints the clock). */
export function edgeLabel(instant: string, timezone: string | null | undefined, isEnd: boolean): string | null {
  const v = edgeView(instant, timezone, isEnd);
  return v.day === null ? null : displayDate(v.day);
}

export interface FieldBounds {
  min?: string;
  max?: string;
}

/**
 * The range of the calendar for one window edge. Never before today (the
 * DateField adds that itself). The last day cannot come before the first day
 * when one is set, and no day after the whole date stops selling is offered:
 * the date's own sales end caps every category window.
 */
export function windowBounds(
  edge: "start" | "end",
  tier: TicketTierItem,
  timezone: string | null | undefined,
  salesEndAt: string | null | undefined,
): FieldBounds {
  const out: FieldBounds = {};
  if (salesEndAt !== null && salesEndAt !== undefined && salesEndAt !== "") out.max = dayOfInstant(salesEndAt, timezone);
  if (edge === "end" && tier.sale_window_start !== null && tier.sale_window_start !== undefined) {
    out.min = dayOfInstant(tier.sale_window_start, timezone);
  }
  return out;
}
