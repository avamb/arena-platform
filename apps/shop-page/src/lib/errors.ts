/**
 * errors.ts - what the buyer is told when the API refuses or cannot be reached.
 * A failed read is only "not found" for a real 404; the network, a 5xx and a
 * 429 are "try again". A checkout answer is translated by its `shop.*` code
 * (lib/i18n.ts, ERR), never by the server's English message.
 */
import { ApiError } from './api';
import { paymentUnavailableMessage } from './cart';
import { ERR, MSG } from './i18n';

/** The shop could not be reached or is overloaded: the same request may well work in a moment. */
export function isTransient(e: unknown): boolean {
  if (!(e instanceof ApiError)) return true;
  return e.status === 0 || e.status === 429 || e.status >= 500;
}

/** A real "there is no such thing" answer. */
export const isNotFound = (e: unknown): boolean => e instanceof ApiError && e.status === 404;

/** The text for a failed checkout request. `contactEmail` is where a buyer may write when payment cannot start. */
export function checkoutErrorMessage(e: unknown, contactEmail?: string | null): string {
  if (!(e instanceof ApiError)) return MSG.unreachableCheckout;
  if (e.code === 'shop.invalid_items') {
    const max = Number((e.details as { max_line_qty?: unknown } | null | undefined)?.max_line_qty);
    if (Number.isFinite(max) && max > 0) return `В одном заказе не больше ${max} шт. одного товара. Уменьшите количество и попробуйте ещё раз.`;
  }
  const known = (ERR as Record<string, string>)[e.code];
  if (known) return known;
  if (e.status === 503) return paymentUnavailableMessage(contactEmail);
  if (e.status === 429) return ERR['shop.rate_limited'];
  if (e.status === 0 || e.status >= 500) return MSG.unreachableCheckout;
  if (e.status === 400 || e.status === 422) return MSG.invalidData;
  return MSG.checkoutFailed;
}
