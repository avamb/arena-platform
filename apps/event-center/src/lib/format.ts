/**
 * Money and dates for people.
 *
 * - Money arrives in MINOR units (cents). The number of fraction digits
 *   comes from the currency itself (Intl), never a hard-coded 100.
 * - A date is shown in the VENUE's time zone with the zone named, because
 *   an organizer in another country must see the time the buyer sees.
 * - Plain `en` is formatted as en-GB with a 24-hour clock: the events are in
 *   Europe, and "11:00 AM" confused organizers (AGENTS.md, hosted pages).
 */

export function intlLocale(locale: string): string {
  return locale === "en" ? "en-GB" : locale;
}

/** How many fraction digits the currency has (2 for EUR, 0 for JPY). */
export function currencyFractionDigits(currency: string): number {
  return fractionDigits(currency);
}

function fractionDigits(currency: string): number {
  try {
    return new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    return 2;
  }
}

export function formatMoney(minor: number, currency: string, locale: string): string {
  const digits = fractionDigits(currency);
  const major = minor / 10 ** digits;
  try {
    return new Intl.NumberFormat(intlLocale(locale), { style: "currency", currency }).format(major);
  } catch {
    return `${major.toFixed(digits)} ${currency}`;
  }
}

export function formatNumber(n: number, locale: string): string {
  return new Intl.NumberFormat(intlLocale(locale)).format(n);
}

/** "Europe/Madrid" → "Madrid", "America/Argentina/Buenos_Aires" → "Buenos Aires". */
export function zoneLabel(tz: string | null | undefined): string {
  if (tz === null || tz === undefined || tz.length === 0) return "";
  const last = tz.split("/").pop() ?? tz;
  return last.replace(/_/g, " ");
}

function safeZone(tz: string | null | undefined): string | undefined {
  if (tz === null || tz === undefined || tz.length === 0) return undefined;
  try {
    new Intl.DateTimeFormat("en", { timeZone: tz });
    return tz;
  } catch {
    return undefined;
  }
}

export function formatDateTime(iso: string | null | undefined, tz: string | null | undefined, locale: string): string {
  if (iso === null || iso === undefined) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const zone = safeZone(tz);
  const text = new Intl.DateTimeFormat(intlLocale(locale), {
    weekday: "short",
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: zone ?? "UTC",
  }).format(d);
  const label = zone !== undefined ? zoneLabel(zone) : "UTC";
  return `${text} (${label})`;
}

/** Day, month and time without the year or the zone ("26.10, 19:31"), for compact lines whose zone is named once nearby. */
export function formatShortDateTime(iso: string | null | undefined, tz: string | null | undefined, locale: string): string {
  if (iso === null || iso === undefined) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return new Intl.DateTimeFormat(intlLocale(locale), {
    day: "2-digit",
    month: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: safeZone(tz) ?? "UTC",
  }).format(d);
}

export function formatDate(iso: string | null | undefined, tz: string | null | undefined, locale: string): string {
  if (iso === null || iso === undefined) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return new Intl.DateTimeFormat(intlLocale(locale), {
    day: "numeric",
    month: "short",
    year: "numeric",
    timeZone: safeZone(tz) ?? "UTC",
  }).format(d);
}
