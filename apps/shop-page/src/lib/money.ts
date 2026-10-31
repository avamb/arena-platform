/**
 * money.ts - amounts arrive from the API in MINOR units (agorot, cents) and are
 * shown the way the live Shopify storefront shows them: "1,250.00 NIS"
 * (amount with a comma thousands separator, two decimals, then the currency
 * code; the shekel's ISO code ILS is printed as NIS).
 *
 * All arithmetic in this app runs in minor units; this is the only place a
 * minor-unit integer becomes text.
 */

const CODE_ALIAS: Record<string, string> = { ILS: 'NIS' };

/** Zero-decimal currencies (the ISO 4217 minor unit is 0). Everything else has 2. */
const ZERO_DECIMAL = new Set(['JPY', 'KRW', 'VND', 'CLP', 'ISK', 'HUF']);

export function currencyLabel(currency: string): string {
  const code = (currency || '').toUpperCase();
  return CODE_ALIAS[code] ?? code;
}

function groupThousands(intPart: string): string {
  return intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/** "290.00" - the amount alone, as Shopify's {{amount}}. */
export function formatAmount(minor: number, currency = 'ILS'): string {
  const code = (currency || '').toUpperCase();
  const negative = minor < 0;
  const abs = Math.abs(Math.round(minor));
  let text: string;
  if (ZERO_DECIMAL.has(code)) {
    text = groupThousands(String(abs));
  } else {
    const whole = Math.floor(abs / 100);
    const cents = abs % 100;
    text = groupThousands(String(whole)) + '.' + String(cents).padStart(2, '0');
  }
  return (negative ? '-' : '') + text;
}

/** "290.00 NIS" - Shopify's money_with_currency. */
export function formatMoney(minor: number, currency = 'ILS'): string {
  return formatAmount(minor, currency) + ' ' + currencyLabel(currency);
}
