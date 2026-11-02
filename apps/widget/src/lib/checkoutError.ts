/**
 * The message shown above the buyer form after `checkout/start` refused the
 * purchase, together with WHAT refused it.
 *
 * The kind decides when the message goes stale. A promo refusal ("You already
 * used this code. Code removed. The price is without the discount.") has two
 * parts. The REASON is about the code and the buyer's e-mail: once the buyer
 * types another e-mail it no longer describes the form and goes (Hermes F-01,
 * 2026-11-01). The NOTE that the code came off and the price is the list price
 * stays, because the buyer-form view shows no total: without it a buyer who
 * only fixed the e-mail would pay the full price without knowing (review of
 * PR #355). Applying a code again, taking a code off or leaving the cart ends
 * both parts. Every other kind (seats taken, consent, cart size, a generic
 * failure) stays until the next submit, as before.
 */

export type CheckoutErrorKind = 'promo' | 'promo_note' | 'conflict' | 'consent' | 'cart_limit' | 'other';

export interface CheckoutError {
  kind: CheckoutErrorKind;
  text: string;
  /** The e-mail the refused submit carried; only kept for a promo refusal. */
  email?: string;
  /** What is left on screen once the reason went stale; only for a promo refusal. */
  note?: string;
}

/** Something the buyer did after the refusal. */
export type CheckoutErrorChange =
  | { type: 'promo_applied' }
  | { type: 'promo_removed' }
  | { type: 'cart_left' }
  | { type: 'email_changed'; email: string };

/** A message of a kind other than a promo refusal. */
export function checkoutError(kind: Exclude<CheckoutErrorKind, 'promo' | 'promo_note'>, text: string): CheckoutError {
  return { kind, text };
}

/**
 * The refusal of a code by checkout/start: the reason (in the buyer's words)
 * followed by the note that the code came off, for the e-mail it was refused for.
 */
export function promoRefusal(reason: string, note: string, email: string): CheckoutError {
  return { kind: 'promo', text: `${reason} ${note}`, email, note };
}

function sameEmail(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase();
}

/** The message after the change: unchanged, shortened to its note, or null when it went stale. */
export function afterCheckoutErrorChange(
  err: CheckoutError | null,
  change: CheckoutErrorChange,
): CheckoutError | null {
  if (!err || (err.kind !== 'promo' && err.kind !== 'promo_note')) return err;
  switch (change.type) {
    case 'promo_applied':
    case 'promo_removed':
    case 'cart_left':
      return null;
    case 'email_changed':
      if (err.kind === 'promo_note') return err;
      if (err.email !== undefined && sameEmail(err.email, change.email)) return err;
      return err.note ? { kind: 'promo_note', text: err.note } : null;
  }
}

/**
 * The change a promo state update means for the message: a code that became
 * applied (none before, or another one) is `promo_applied`. A code going away
 * is not reported here, because the start refusal itself takes it off
 * (`drop`); the buyer's own Remove is reported by the remove handler.
 */
export function promoCodeChange(prevCode: string | null, nextCode: string | null): CheckoutErrorChange | null {
  return nextCode !== null && nextCode !== prevCode ? { type: 'promo_applied' } : null;
}
