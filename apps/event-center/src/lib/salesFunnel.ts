/**
 * The sales funnel of an event (spec 50, stage A7): pure helpers of the screen.
 *
 * The API answers six steps: the first three are anonymous counters of the
 * widget and the pages, the last three are counted from the orders. Every step
 * has its count and its share of the previous step. This file only turns that
 * answer into rows: it never calls the API and never invents a number.
 */
import type { SalesFunnelResponse, SalesFunnelSource, SalesFunnelStep } from "@/lib/api/types";

/** The six steps in funnel order, as the API names them. */
export const FUNNEL_STEPS = ["view", "session_open", "seats_chosen", "checkout_start", "payment_open", "paid"] as const;
export type FunnelStepName = (typeof FUNNEL_STEPS)[number];

export interface FunnelRow {
  readonly step: string;
  /** People who reached the step, null where the step cannot be read (the view of one session). */
  readonly count: number | null;
  /** Probable bots counted apart; only the first three steps have any. */
  readonly suspect: number;
  /** Share of the previous step, 0 to 1, null when there is nothing to compare with. */
  readonly share: number | null;
  /** Percent lost against the previous step, 0 to 100, null when the share is unknown or above 1. */
  readonly lost: number | null;
  /** Width of the bar, 0 to 100, relative to the biggest count of the list. */
  readonly bar: number;
}

/** Percent lost against the previous step. A share above 1 (a guide, not proof) has no loss. */
export function lostPercent(share: number | null): number | null {
  if (share === null || share > 1) return null;
  return Math.round((1 - share) * 100);
}

/** Turns the steps of the API into rows with the loss and the bar width. */
export function funnelRows(steps: readonly SalesFunnelStep[]): FunnelRow[] {
  const max = steps.reduce((m, s) => Math.max(m, s.count ?? 0), 0);
  return steps.map((s) => ({
    step: s.step,
    count: s.count,
    suspect: s.suspect_count,
    share: s.share_of_previous,
    lost: lostPercent(s.share_of_previous),
    bar: s.count === null || max === 0 ? 0 : Math.round((s.count / max) * 100),
  }));
}

/** "37%" in the locale's own digits and sign placement. */
export function formatShare(share: number, locale: string): string {
  return new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 }).format(share);
}

/** True when nothing was counted in any step: no view, no cart, no order. */
export function isEmptyFunnel(f: SalesFunnelResponse): boolean {
  const total = f.steps.reduce((n, s) => n + (s.count ?? 0) + s.suspect_count, 0);
  return total === 0 && f.gateway === null;
}

/** The count of a named step, 0 when the step is absent or unknown. */
export function stepCount(steps: readonly SalesFunnelStep[], name: string): number {
  return steps.find((s) => s.step === name)?.count ?? 0;
}

export interface SourceRow {
  readonly key: string;
  /** Empty when the visit came without utm values. */
  readonly source: string;
  readonly campaign: string;
  readonly counts: Readonly<Record<string, number>>;
  readonly direct: boolean;
}

/** The sources table: the API's order (biggest first), the no-utm pair last. */
export function sourceRows(sources: readonly SalesFunnelSource[]): SourceRow[] {
  const rows = sources.map((s) => ({
    key: `${s.utm_source}|${s.utm_campaign}`,
    source: s.utm_source,
    campaign: s.utm_campaign,
    counts: Object.fromEntries(FUNNEL_STEPS.map((n) => [n, stepCount(s.steps, n)])),
    direct: s.utm_source === "" && s.utm_campaign === "",
  }));
  return [...rows.filter((r) => !r.direct), ...rows.filter((r) => r.direct)];
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** A calendar date as the API reads it, from the browser's local day. */
export function isoDay(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** The window "the last n days, today included" by the browser's clock. */
export function lastDays(n: number, today: Date): { from: string; to: string } {
  const from = new Date(today.getFullYear(), today.getMonth(), today.getDate() - (n - 1));
  return { from: isoDay(from), to: isoDay(today) };
}
