/**
 * Pure helpers of the event's advertising screen (spec 66, M2). No React here.
 *
 * Money is never converted: when the ad account and the orders are in different currencies, both amounts are shown
 * and MER is not shown.
 */
import { formatNumber, intlLocale } from "@/lib/format";

/** True when both currencies are known and they differ. */
export function currenciesDiffer(adCurrency: string | null, orderCurrency: string | null): boolean {
  return adCurrency !== null && adCurrency !== "" && orderCurrency !== null && orderCurrency !== "" && adCurrency !== orderCurrency;
}

/** MER to show: the server's number, but never across two currencies and never without spend. */
export function merToShow(
  report: { mer: number | null; meta: { currency: string | null; spend_minor: number }; ours: { currency: string | null } },
): number | null {
  if (report.mer === null || report.meta.spend_minor <= 0) return null;
  if (currenciesDiffer(report.meta.currency, report.ours.currency)) return null;
  return report.mer;
}

/** "2.35" for MER, "—" for none. */
export function formatMer(mer: number | null, locale: string): string {
  if (mer === null || !Number.isFinite(mer)) return "—";
  return new Intl.NumberFormat(intlLocale(locale), { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(mer);
}

/** The whole percent of paid orders that carry no campaign id, or null when there are no paid orders. */
export function untaggedPercent(u: { paid_orders: number; untagged_orders: number }): number | null {
  if (u.paid_orders <= 0) return null;
  const share = Math.min(Math.max(u.untagged_orders, 0), u.paid_orders) / u.paid_orders;
  return Math.round(share * 100);
}

export function formatCount(n: number, locale: string): string {
  return formatNumber(n, locale);
}

/** Whether the refresh button is still locked: the server named a time and it has not come. */
export function refreshLocked(notBefore: string | null | undefined, nowMs: number): boolean {
  if (notBefore === null || notBefore === undefined) return false;
  const at = Date.parse(notBefore);
  return Number.isFinite(at) && at > nowMs;
}
