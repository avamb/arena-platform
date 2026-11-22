/**
 * A date sold by a partner box office (spec 63, task L-3, migration 0220).
 *
 * One form for the wizard (a date of a new event) and for the date's own card
 * (a PATCH of an existing session): the person names the box office link, its
 * name and, if they like, the lowest price and its currency. The platform sells
 * nothing for such a date; buyers get a button to the link.
 *
 * The checks here are about the SHAPE only (the server repeats them and adds its
 * own: no IP address, no platform host). Pure: no React, no network.
 *
 * NOT the same as the "Also on sale" partner links of the event page materials
 * (lib/materials.ts): those are extra links under an event the platform sells;
 * this is the box office that sells the date instead of the platform.
 */
import type { MessageKey } from "@/lib/i18n/en";
import { majorOf, parsePriceMinor } from "@/lib/wizard/parse";
import type { SessionItem } from "@/lib/api/types";

/** Who sells the tickets of a date. */
export type SalesMode = "platform" | "external";

export interface ExternalForm {
  url: string;
  /** The box office name buyers see ("Eventim"). */
  seller: string;
  /** The lowest price as typed; "" = none. */
  price: string;
  /** ISO 4217 as typed; "" = the event's currency. */
  currency: string;
}

export type ExternalField = keyof ExternalForm;
export const EXTERNAL_FIELDS: readonly ExternalField[] = ["url", "seller", "price", "currency"];

export const MAX_EXTERNAL_URL = 2048;
export const MAX_EXTERNAL_SELLER = 80;

export function emptyExternal(): ExternalForm {
  return { url: "", seller: "", price: "", currency: "" };
}

export function isBlankExternal(f: ExternalForm): boolean {
  return f.url.trim() === "" && f.seller.trim() === "" && f.price.trim() === "" && f.currency.trim() === "";
}

/** The form of a session read from the API (platform or null = an empty form). */
export function externalFormOf(session: Pick<SessionItem, "external"> | null | undefined): ExternalForm {
  const e = session?.external;
  if (e === null || e === undefined) return emptyExternal();
  return {
    url: e.url,
    seller: e.seller_name,
    price: e.price_from === null ? "" : String(e.price_from),
    currency: e.currency ?? "",
  };
}

/** True when the session is sold by a partner box office. */
export function isExternalSession(session: Pick<SessionItem, "sales_mode"> | null | undefined): boolean {
  return session?.sales_mode === "external";
}

const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/;

export type ExternalProblems = Partial<Record<ExternalField, MessageKey>>;

/**
 * What is wrong with the shape of the form. `fallbackCurrency` is the event's
 * currency, used when a price is typed without a currency of its own.
 */
export function externalProblems(f: ExternalForm, fallbackCurrency: string): ExternalProblems {
  const out: ExternalProblems = {};
  const url = f.url.trim();
  if (url === "") out.url = "external.err.urlRequired";
  else if (url.length > MAX_EXTERNAL_URL) out.url = "external.err.urlShape";
  else {
    let host = "";
    let ok = false;
    try {
      const u = new URL(url);
      ok = u.protocol === "https:";
      host = u.hostname;
    } catch {
      ok = false;
    }
    // https only, a host name (an address of numbers or of brackets is refused by the server too).
    if (!ok || host === "" || IPV4.test(host) || host.startsWith("[") || !host.includes(".")) out.url = "external.err.urlShape";
  }
  const seller = f.seller.trim();
  if (seller === "") out.seller = "external.err.sellerRequired";
  else if ([...seller].length > MAX_EXTERNAL_SELLER) out.seller = "external.err.sellerLong";
  if (f.price.trim() !== "") {
    if (parsePriceMinor(f.price) === null) out.price = "external.err.priceShape";
    else if (!/^[A-Za-z]{3}$/.test((f.currency.trim() || fallbackCurrency).trim())) out.currency = "external.err.currencyShape";
  }
  return out;
}

/** The currency a typed price is in: its own, else the event's. */
export function externalCurrency(f: ExternalForm, fallbackCurrency: string): string {
  return (f.currency.trim() || fallbackCurrency).trim().toUpperCase();
}

/** The lowest price in MAJOR units, or null when none is typed (or it cannot be read). */
export function externalPriceMajor(f: ExternalForm): number | null {
  if (f.price.trim() === "") return null;
  const minor = parsePriceMinor(f.price);
  return minor === null ? null : majorOf(minor);
}

/** The block of the event-bundle (`actionEvent.externalSale`, camelCase). */
export interface BundleExternalSale {
  url: string;
  sellerName: string;
  priceFrom?: number;
  currency?: string;
}

export function bundleExternalSale(f: ExternalForm, fallbackCurrency: string): BundleExternalSale {
  const out: BundleExternalSale = { url: f.url.trim(), sellerName: f.seller.trim() };
  const price = externalPriceMajor(f);
  if (price !== null) {
    out.priceFrom = price;
    out.currency = externalCurrency(f, fallbackCurrency);
  }
  return out;
}

/** The input of POST/PATCH .../sessions (`SessionExternalSaleInput`, snake_case). */
export interface SessionExternalInput {
  url: string;
  seller_name: string;
  price_from?: number | null;
  currency?: string | null;
}

export function sessionExternalInput(f: ExternalForm, fallbackCurrency: string): SessionExternalInput {
  const out: SessionExternalInput = { url: f.url.trim(), seller_name: f.seller.trim() };
  const price = externalPriceMajor(f);
  if (price !== null) {
    out.price_from = price;
    out.currency = externalCurrency(f, fallbackCurrency);
  }
  return out;
}

/** The body of PATCH .../sessions/{id} that changes who sells the date. */
export interface SalesModePatch {
  sales_mode: SalesMode;
  external?: SessionExternalInput | null;
}

export function salesModePatch(mode: SalesMode, f: ExternalForm, fallbackCurrency: string): SalesModePatch {
  return mode === "external"
    ? { sales_mode: "external", external: sessionExternalInput(f, fallbackCurrency) }
    : { sales_mode: "platform", external: null };
}

/** The line a partner date shows where sales figures would stand. */
export const EXTERNAL_LINE_KEY = "external.line" satisfies MessageKey;
