/**
 * externalSale.ts — a date sold by a partner box office (spec 63, L3).
 *
 * The public feed gives such a session `sales_mode: "external"`,
 * `sales_state: "external"`, an empty `tiers` list and an `external` block.
 * The widget shows no seat picker and no cart for it: one button leads the
 * buyer to the partner's site.
 */
import type { FeedSession } from '../types.js';

export interface ExternalSaleInfo {
  url: string;
  seller_name: string;
  /** Major units; null when the partner gave no price. */
  price_from: number | null;
  currency: string | null;
}

/** The partner block of a session, or null when the session is sold here. */
export function externalSaleOf(s: Pick<FeedSession, 'sales_state' | 'external'> | null | undefined): ExternalSaleInfo | null {
  if (!s || s.sales_state !== 'external') return null;
  const ext = s.external;
  if (!ext || typeof ext.url !== 'string' || ext.url.trim() === '') return null;
  // Only a web link may be opened from a button.
  if (!/^https?:\/\//i.test(ext.url.trim())) return null;
  return {
    url: ext.url.trim(),
    seller_name: (ext.seller_name ?? '').trim(),
    price_from: typeof ext.price_from === 'number' ? ext.price_from : null,
    currency: ext.currency ? String(ext.currency) : null,
  };
}

export interface ExternalSaleStrings {
  /** Button; {seller} is the partner's name. */
  buy_at: string;
  /** Button when the partner has no name. */
  buy_at_no_name: string;
  /** Price line; {price} already carries the currency. */
  from_price: string;
}

export const EXTERNAL_SALE_I18N: Record<string, ExternalSaleStrings> = {
  en: { buy_at: 'Buy at {seller}', buy_at_no_name: 'Buy tickets', from_price: 'from {price}' },
  ru: { buy_at: 'Купить на {seller}', buy_at_no_name: 'Купить билеты', from_price: 'от {price}' },
  cs: { buy_at: 'Koupit u {seller}', buy_at_no_name: 'Koupit vstupenky', from_price: 'od {price}' },
  he: { buy_at: 'לרכישה ב-{seller}', buy_at_no_name: 'לרכישת כרטיסים', from_price: 'החל מ-{price}' },
  es: { buy_at: 'Comprar en {seller}', buy_at_no_name: 'Comprar entradas', from_price: 'desde {price}' },
  de: { buy_at: 'Kaufen bei {seller}', buy_at_no_name: 'Tickets kaufen', from_price: 'ab {price}' },
  fr: { buy_at: 'Acheter chez {seller}', buy_at_no_name: 'Acheter des billets', from_price: 'à partir de {price}' },
};

export function getExternalSaleStrings(locale: string): ExternalSaleStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return EXTERNAL_SALE_I18N[lang] ?? EXTERNAL_SALE_I18N.en!;
}

export function externalBuyLabel(locale: string, sellerName: string): string {
  const s = getExternalSaleStrings(locale);
  const name = sellerName.trim();
  return name ? s.buy_at.replace('{seller}', name) : s.buy_at_no_name;
}

/** "from 20 EUR", or '' when the partner gave no price. */
export function externalPriceLine(locale: string, info: ExternalSaleInfo): string {
  if (info.price_from === null) return '';
  const amount = Number.isInteger(info.price_from) ? String(info.price_from) : info.price_from.toFixed(2);
  const price = info.currency ? `${amount} ${info.currency.toUpperCase()}` : amount;
  return getExternalSaleStrings(locale).from_price.replace('{price}', price);
}
