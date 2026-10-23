/**
 * What a promo code is bound to, made readable (promo scope clarity). The API
 * answers `sessions` (event, start, end of sales, venue zone) and `warnings`
 * with every code; these are the pure rules the screens draw from them: the
 * zone to show dates in, the "extend to the end of sales" target and the
 * copy-a-code seed, which never carries sessions or a last day over.
 */
import type { PromoCodeItem } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import type { Locale, Params } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";

type T = (key: MessageKey, params?: Params) => string;
export type PromoScopeSession = PromoCodeItem["sessions"][number];
export type PromoWarning = PromoCodeItem["warnings"][number];

const CANCELLED = "cancelled";

/** A session buyers can still buy: not cancelled and not started. */
export function isLiveSession(s: PromoScopeSession, now: Date): boolean {
  return s.status !== CANCELLED && new Date(s.start_at).getTime() > now.getTime();
}

/** The zone a code's dates are shown in: the venues' zone when they agree, else UTC. */
export function promoZone(item: Pick<PromoCodeItem, "sessions">): string {
  const zones = new Set(item.sessions.map((s) => s.timezone));
  return zones.size === 1 ? [...zones][0] : "UTC";
}

/** "Event. Starts Sat 3 Nov 2026 19:00 (Madrid). Sales end Sat 3 Nov 2026 18:00 (Madrid)." */
export function sessionLine(s: PromoScopeSession, t: T, locale: Locale): string {
  return t("promo.scope.line", {
    event: s.event_name,
    start: formatDateTime(s.start_at, s.timezone, locale),
    end: formatDateTime(s.sales_end_at, s.timezone, locale),
  });
}

/** The instant "extend to the end of sales" sets the last day to, or null when nothing is live. */
export function extendTarget(item: Pick<PromoCodeItem, "sessions">, now: Date): string | null {
  let best: string | null = null;
  for (const s of item.sessions) {
    if (!isLiveSession(s, now)) continue;
    if (best === null || new Date(s.sales_end_at).getTime() > new Date(best).getTime()) best = s.sales_end_at;
  }
  return best;
}

/** True when the code has a last day and it falls before the end of sales of a live session. */
export function canExtend(item: Pick<PromoCodeItem, "sessions" | "valid_until">, now: Date): boolean {
  const target = extendTarget(item, now);
  if (target === null || item.valid_until === null) return false;
  return new Date(item.valid_until).getTime() < new Date(target).getTime();
}

/** The distinct event names of a code, in the order of its sessions. */
export function eventNames(item: Pick<PromoCodeItem, "sessions">): string[] {
  return [...new Set(item.sessions.map((s) => s.event_name))];
}

/** The message key of a warning code; an unknown code reads as a generic one. */
export function warningKey(code: string): MessageKey {
  switch (code) {
    case "promo.warning.valid_until_before_sales_end":
      return "promo.warn.untilBeforeSales";
    case "promo.warning.valid_from_after_sales_start":
      return "promo.warn.fromAfterSales";
    case "promo.warning.session_cancelled":
      return "promo.warn.cancelled";
    case "promo.warning.session_past":
      return "promo.warn.past";
    default:
      return "promo.warn.other";
  }
}

export interface CopySeed {
  type: "percent" | "fixed_amount";
  /** Percent as typed ("15") or major units ("5.50"); the form reads it like any typed value. */
  value: string;
  currency: string;
  total: string;
  per: string;
}

/**
 * What a copy starts from: the discount and the limits. The sessions and the
 * last day are NOT carried over: a copy that silently kept them is how one
 * code ended up bound to the wrong event. The person must choose both again.
 */
export function copySeed(item: PromoCodeItem, digits: number): CopySeed {
  const fixed = item.discount_type === "fixed_amount";
  const value = fixed ? (item.discount_value / 10 ** digits).toFixed(digits) : String(item.discount_value);
  return {
    type: fixed ? "fixed_amount" : "percent",
    value,
    currency: fixed ? (item.currency ?? "") : "",
    total: item.max_uses === null ? "" : String(item.max_uses),
    per: item.max_uses_per_customer === null ? "" : String(item.max_uses_per_customer),
  };
}

export function parseCopyFrom(raw: Record<string, unknown>): { copyFrom?: string } {
  return typeof raw.copyFrom === "string" && /^[0-9a-fA-F-]{36}$/.test(raw.copyFrom) ? { copyFrom: raw.copyFrom } : {};
}
