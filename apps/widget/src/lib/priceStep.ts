/**
 * priceStep.ts — the line under a category's price that says how the price
 * will change (spec 08_architecture/51_4_2_smart_price_ru.md, section 4).
 *
 * Every number comes from the feed: the date and the next price of a price
 * window, or the free places of a step and the next step's price. Nothing is
 * made up: no "few left", no countdown, and nothing when the feed says
 * nothing. The price the buyer pays is always the current one.
 *
 * The words are their own table (like promoI18n): money wording reads in the
 * buyer's language even where the rest of the widget falls back to English.
 */
import { formatPrice } from './checkout.js';
import type { Tier } from '../types.js';

export interface PriceStepStrings {
  /** The price rises on a date. {price} the next price, {date} the day. */
  rises_on: string;
  /** The price changes on a date and the feed does not say to what. */
  changes_on: string;
  /** {count} tickets are left at this price, then {price}. */
  left_then: string;
}

export const PRICE_STEP_I18N: Record<string, PriceStepStrings> = {
  en: {
    rises_on: 'Price rises to {price} on {date}',
    changes_on: 'Price changes on {date}',
    left_then: '{count} left at this price, then {price}',
  },
  ru: {
    rises_on: 'Цена вырастет до {price} {date}',
    changes_on: 'Цена изменится {date}',
    left_then: 'По этой цене осталось: {count}, потом {price}',
  },
  cs: {
    rises_on: 'Cena se zvýší na {price} dne {date}',
    changes_on: 'Cena se změní dne {date}',
    left_then: 'Za tuto cenu zbývá: {count}, potom {price}',
  },
  he: {
    rises_on: 'המחיר יעלה ל-{price} ב-{date}',
    changes_on: 'המחיר ישתנה ב-{date}',
    left_then: 'נותרו במחיר זה: {count}, אחר כך {price}',
  },
  es: {
    rises_on: 'El precio sube a {price} el {date}',
    changes_on: 'El precio cambia el {date}',
    left_then: 'Quedan a este precio: {count}, después {price}',
  },
  de: {
    rises_on: 'Der Preis steigt am {date} auf {price}',
    changes_on: 'Der Preis ändert sich am {date}',
    left_then: 'Zu diesem Preis noch: {count}, danach {price}',
  },
  fr: {
    rises_on: 'Le prix passe à {price} le {date}',
    changes_on: 'Le prix change le {date}',
    left_then: 'Encore {count} à ce prix, puis {price}',
  },
};

export function getPriceStepStrings(locale: string): PriceStepStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return PRICE_STEP_I18N[lang] ?? PRICE_STEP_I18N.en!;
}

function fill(template: string, values: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (m, k: string) => values[k] ?? m);
}

/** The date of a price change, in the venue's zone when the feed gives one. */
function formatDay(iso: string, locale: string, timeZone?: string | null): string | null {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  const opts: Intl.DateTimeFormatOptions = { day: 'numeric', month: 'short' };
  if (timeZone) opts.timeZone = timeZone;
  try {
    // Plain English reads as en-GB: the events are in Europe ("10 Nov").
    const tag = !locale || locale.toLowerCase() === 'en' ? 'en-GB' : locale;
    return new Intl.DateTimeFormat(tag, opts).format(d);
  } catch {
    return d.toLocaleDateString();
  }
}

/**
 * priceStepLine is the line for one category card, or null when there is
 * nothing true to say. A step by tickets sold wins over a date: a chain step
 * carries no price windows.
 */
export function priceStepLine(tier: Tier, locale: string, timeZone?: string | null): string | null {
  const s = getPriceStepStrings(locale);
  const step = tier.price_step;
  if (step && step.remaining_at_price != null && step.remaining_at_price > 0 && tier.currency) {
    return fill(s.left_then, {
      count: String(step.remaining_at_price),
      price: formatPrice(step.next_price, tier.currency),
    });
  }
  if (!tier.next_price_change_at) return null;
  const date = formatDay(tier.next_price_change_at, locale, timeZone);
  if (!date) return null;
  const now = tier.current_price ?? tier.price_amount;
  if (tier.next_price != null && tier.next_price > now && tier.currency) {
    return fill(s.rises_on, { price: formatPrice(tier.next_price, tier.currency), date });
  }
  return fill(s.changes_on, { date });
}
