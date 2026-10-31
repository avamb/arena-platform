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

/**
 * The name of a country in the language of the screen ("IL" is "Израиль" in Russian),
 * or the code itself when the browser does not know it.
 */
export function regionName(iso2: string | null | undefined, locale: string): string {
  if (iso2 === null || iso2 === undefined || iso2.trim() === "") return "";
  const code = iso2.trim().toUpperCase();
  try {
    return new Intl.DisplayNames([intlLocale(locale)], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

/** "₪ (новый израильский шекель)": the symbol and the name of a currency instead of the bare code. */
export function currencyLabel(currency: string, locale: string): string {
  const code = currency.trim().toUpperCase();
  let symbol = code;
  let name = "";
  try {
    const parts = new Intl.NumberFormat(intlLocale(locale), { style: "currency", currency: code, currencyDisplay: "narrowSymbol" }).formatToParts(0);
    symbol = parts.find((p) => p.type === "currency")?.value ?? code;
  } catch {
    return code;
  }
  try {
    name = new Intl.DisplayNames([intlLocale(locale)], { type: "currency" }).of(code) ?? "";
  } catch {
    name = "";
  }
  if (name === "" || name === code) return symbol;
  return `${symbol} (${name})`;
}

/** Money with the short symbol of the currency ("740,00 ₪"), for text a person pastes elsewhere. */
export function formatMoneySymbol(minor: number, currency: string, locale: string): string {
  const digits = fractionDigits(currency);
  const major = minor / 10 ** digits;
  try {
    return new Intl.NumberFormat(intlLocale(locale), { style: "currency", currency, currencyDisplay: "narrowSymbol" }).format(major);
  } catch {
    return `${major.toFixed(digits)} ${currency}`;
  }
}

/** `label` replaces the city in brackets (a shop names the country of its time, not a city). */
export function formatDateTime(iso: string | null | undefined, tz: string | null | undefined, locale: string, label?: string): string {
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
  const where = label !== undefined && label !== "" ? label : zone !== undefined ? zoneLabel(zone) : "UTC";
  return `${text} (${where})`;
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
