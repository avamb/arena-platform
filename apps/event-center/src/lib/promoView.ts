/**
 * How a promo code is written on screen (the bot's promo_view.go): what it
 * gives, where it works, its limits, its dates and its usage. Money goes
 * through formatMoney (minor units). The sentences are message keys, so
 * every language orders its own words.
 *
 * Dates of a code are calendar days: a code ends at 23:59:59 UTC of its last
 * day, so the UTC date is the one the organizer chose, whatever the zone.
 */
import type { PromoCodeItem } from "@/lib/api/types";
import { formatDate, formatDateTime, formatMoney } from "@/lib/format";
import type { Locale, Params } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { isClubCode, promoCurrencyOf } from "@/lib/promo";
import { promoZone } from "@/lib/promoScope";

type T = (key: MessageKey, params?: Params) => string;

export function promoDiscountText(item: PromoCodeItem, t: T, locale: Locale): string {
  if (item.discount_type === "percent") return t("promo.discount.pct", { value: item.discount_value });
  return t("promo.discount.fixed", { amount: formatMoney(item.discount_value, item.currency ?? "", locale) });
}

export function promoSessionsText(item: PromoCodeItem, t: T): string {
  return isClubCode(item)
    ? t("promo.sessions.all")
    : t("promo.sessions.n", { count: item.applies_to_session_ids.length });
}

export function promoLimitsText(item: PromoCodeItem, t: T): string {
  const parts: string[] = [];
  if (item.max_uses !== null) parts.push(t("promo.limit.total", { count: item.max_uses }));
  if (item.max_uses_per_customer !== null) parts.push(t("promo.limit.per", { count: item.max_uses_per_customer }));
  return parts.length === 0 ? t("promo.limit.none") : parts.join(", ");
}

/** A code made by the wizard or the bot ends at 23:59:59 UTC of the day the organizer chose. */
function endsAtUtcDayEnd(iso: string): boolean {
  return iso.slice(11, 19) === "23:59:59";
}

/**
 * The code's dates. A day the organizer chose is a UTC calendar day. Any other
 * instant (for example "extended to the end of sales") is shown in the zone
 * of the venues the code is bound to, with the time, so it reads like the
 * sales end next to it.
 */
function validDate(iso: string, item: PromoCodeItem, locale: Locale): string {
  if (item.sessions.length === 0 || endsAtUtcDayEnd(iso)) return formatDate(iso, "UTC", locale);
  return formatDateTime(iso, promoZone(item), locale);
}

export function promoValidText(item: PromoCodeItem, t: T, locale: Locale): string {
  const parts: string[] = [];
  if (item.valid_from !== null) parts.push(t("promo.valid.from", { date: validDate(item.valid_from, item, locale) }));
  if (item.valid_until !== null) parts.push(t("promo.valid.until", { date: validDate(item.valid_until, item, locale) }));
  return parts.length === 0 ? t("promo.valid.none") : parts.join(" ");
}

/**
 * The number of uses, the discount they took and the last one. Several
 * currencies cannot be added up: the line then says to read the list of
 * uses instead of printing a meaningless sum.
 */
export function promoUsageText(item: PromoCodeItem, t: T, locale: Locale): string {
  if (item.uses === 0) return t("promo.usage.none");
  const when = item.last_used_at !== null ? formatDate(item.last_used_at, "UTC", locale) : "";
  const cur = promoCurrencyOf(item);
  if (cur === "") return t("promo.usage.mixed", { count: item.uses, date: when });
  return t("promo.usage.one", { count: item.uses, discount: formatMoney(item.discount_total, cur, locale), date: when });
}
