/**
 * The organization summary (EC-V1, spec 62 §12.2): date presets, the grouping of
 * a card's price × category rows and the totals line. Pure, so the arithmetic
 * is tested without a screen. Money stays in minor units until it is printed.
 */
import { addDays, addMonths } from "@/lib/dates";
import type { OrgSummaryBreakdownItem, OrgSummaryCard } from "@/lib/api/types";

export type RangePreset = "month" | "last" | "quarter" | "all";

export const RANGE_PRESETS: readonly RangePreset[] = ["month", "last", "quarter", "all"];

export interface DayRange {
  /** First day, ISO, or "" for no bound. */
  from: string;
  /** Last day included, ISO, or "" for no bound. */
  to: string;
}

/** The first and last day of a "YYYY-MM" month. */
function monthBounds(month: string): DayRange {
  const first = `${month}-01`;
  return { from: first, to: addDays(`${addMonths(month, 1)}-01`, -1) };
}

/** The range a preset button sets, counted from `today` (ISO). */
export function presetRange(preset: RangePreset, today: string): DayRange {
  const month = today.slice(0, 7);
  switch (preset) {
    case "month":
      return monthBounds(month);
    case "last":
      return monthBounds(addMonths(month, -1));
    case "quarter":
      return { from: addDays(today, -89), to: today };
    case "all":
      return { from: "", to: "" };
  }
}

/** Which preset the range equals, or null for a hand-typed one. */
export function activePreset(range: DayRange, today: string): RangePreset | null {
  return RANGE_PRESETS.find((p) => {
    const r = presetRange(p, today);
    return r.from === range.from && r.to === range.to;
  }) ?? null;
}

/** True when `to` is before `from`: the server would refuse it. */
export function rangeIsBackwards(range: DayRange): boolean {
  return range.from !== "" && range.to !== "" && range.to < range.from;
}

export interface PriceGroup {
  tierId: string;
  tierName: string;
  unitPrice: number;
  tickets: number;
  revenue: number;
  /** The promo-code rows of this price, biggest first; empty when none was used. */
  promos: OrgSummaryBreakdownItem[];
}

/**
 * Folds the card's rows (one per category, price and promo code) into one line
 * per category and price, with the promo-code rows under it. The line adds the
 * promo rows up, so it matches what the buyers paid at that price.
 */
export function groupBreakdown(card: Pick<OrgSummaryCard, "breakdown">): PriceGroup[] {
  const groups = new Map<string, PriceGroup>();
  for (const row of card.breakdown) {
    const key = `${row.tier_id}|${row.unit_price}`;
    let g = groups.get(key);
    if (g === undefined) {
      g = { tierId: row.tier_id, tierName: row.tier_name, unitPrice: row.unit_price, tickets: 0, revenue: 0, promos: [] };
      groups.set(key, g);
    }
    g.tickets += row.tickets;
    g.revenue += row.revenue;
    if (row.promo_code !== null) g.promos.push(row);
  }
  for (const g of groups.values()) g.promos.sort((a, b) => b.tickets - a.tickets || (a.promo_code ?? "").localeCompare(b.promo_code ?? ""));
  return [...groups.values()];
}
