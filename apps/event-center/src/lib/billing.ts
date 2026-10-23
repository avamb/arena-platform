/**
 * B2-7: the pure rules of the billing usage screen. No React here, so every
 * rule can be tested without a DOM.
 *
 * Months are calendar months in UTC, the same as the billing period of the
 * API and `usage_records`. A charge carries the time of the event it rates, so
 * a month holds the sales of that month, and a reversal that arrives later
 * sits next to the sale it reverses.
 */
import type { BillingChargeItem } from "@/lib/api/types";

export const PAGE_SIZE = 200;

/** "YYYY-MM" of a moment, UTC. */
export function monthOf(d: Date): string {
  return d.toISOString().slice(0, 7);
}

const MONTH_RE = /^(\d{4})-(0[1-9]|1[0-2])$/;

export function isMonth(v: string): boolean {
  return MONTH_RE.test(v);
}

/** The month `delta` months from `ym`. */
export function shiftMonth(ym: string, delta: number): string {
  const m = MONTH_RE.exec(ym);
  if (m === null) throw new Error(`not a month: ${ym}`);
  const idx = Number(m[1]) * 12 + (Number(m[2]) - 1) + delta;
  const year = Math.floor(idx / 12);
  const month = (idx % 12) + 1;
  return `${String(year).padStart(4, "0")}-${String(month).padStart(2, "0")}`;
}

/** The query range of a month: from inclusive, to exclusive (the first moment of the next month). */
export function monthRange(ym: string): { from: string; to: string } {
  return { from: `${ym}-01T00:00:00Z`, to: `${shiftMonth(ym, 1)}-01T00:00:00Z` };
}

/** "October 2026" in the language of the viewer. */
export function formatMonth(ym: string, locale: string): string {
  const [y, m] = ym.split("-").map(Number);
  return new Intl.DateTimeFormat(locale === "en" ? "en-GB" : locale, { month: "long", year: "numeric", timeZone: "UTC" }).format(
    new Date(Date.UTC(y, m - 1, 1)),
  );
}

/** The kinds the API reports, with a label key each; anything else is "other". */
const KINDS = ["ticket_sold", "ticket_refunded", "ticket_cancelled", "session_cancelled", "event_published", "ticket_complimentary"] as const;
export type BillingKindKey = `billing.kind.${(typeof KINDS)[number] | "other"}`;

export function kindKey(kind: string): BillingKindKey {
  return (KINDS as readonly string[]).includes(kind) ? (`billing.kind.${kind}` as BillingKindKey) : "billing.kind.other";
}

const RULES = [
  "fixed_per_ticket",
  "percent_of_price_with_cap",
  "low_price_formula",
  "volume_above_baseline",
  "fixed_per_event",
  "subscription",
  "credit_pack",
  "generator_run",
  "org_rate_override",
  "none",
] as const;
export type BillingRuleKey = `billing.rule.${(typeof RULES)[number] | "other"}`;

export function ruleKey(ruleType: string): BillingRuleKey {
  return (RULES as readonly string[]).includes(ruleType) ? (`billing.rule.${ruleType}` as BillingRuleKey) : "billing.rule.other";
}

const REASONS = ["free", "override", "no_rule", "journal_only"] as const;
export type BillingReasonKey = `billing.reason.${(typeof REASONS)[number]}`;

/** The label key of a stated reason, or null when it is empty or unknown. */
export function reasonKey(reason: string | null | undefined): BillingReasonKey | null {
  return reason !== null && reason !== undefined && (REASONS as readonly string[]).includes(reason)
    ? (`billing.reason.${reason}` as BillingReasonKey)
    : null;
}

export function isReversal(c: Pick<BillingChargeItem, "reversal_of">): boolean {
  return c.reversal_of !== null && c.reversal_of !== undefined;
}

/** One row of the "By event" table, counted from the charges already loaded. */
export interface EventLine {
  /** null = charges that belong to no event. */
  eventId: string | null;
  /** One entry per currency, because currencies are never added together. */
  net: { currency: string; netMinor: number }[];
  charges: number;
}

/** Groups the loaded charges by event. Direct charges are counted, reversals only change the net. */
export function groupByEvent(items: readonly BillingChargeItem[]): EventLine[] {
  const byEvent = new Map<string, { eventId: string | null; net: Map<string, number>; charges: number }>();
  for (const c of items) {
    const key = c.event_id ?? "";
    let line = byEvent.get(key);
    if (line === undefined) {
      line = { eventId: c.event_id ?? null, net: new Map(), charges: 0 };
      byEvent.set(key, line);
    }
    line.net.set(c.currency, (line.net.get(c.currency) ?? 0) + c.amount_minor);
    if (!isReversal(c)) line.charges += 1;
  }
  return [...byEvent.values()]
    .map((l) => ({
      eventId: l.eventId,
      charges: l.charges,
      net: [...l.net.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([currency, netMinor]) => ({ currency, netMinor })),
    }))
    .sort((a, b) => b.charges - a.charges || String(a.eventId).localeCompare(String(b.eventId)));
}
