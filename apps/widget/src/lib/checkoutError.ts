/**
 * The message shown above the buyer form after `checkout/start` refused the
 * purchase, together with WHAT refused it.
 *
 * The kind decides when the message goes stale. A promo refusal ("You already
 * used this code. Code removed.") is about the code and the buyer's e-mail:
 * once the buyer applies a code again, takes the code off, or types another
 * e-mail, the sentence no longer describes the form and must go (Hermes F-01,
 * 2026-11-01: the old text stayed next to a correct discounted total). Every
 * other kind (seats taken, consent, cart size, a generic failure) stays until
 * the next submit, as before.
 */

export type CheckoutErrorKind = 'promo' | 'conflict' | 'consent' | 'cart_limit' | 'other';

export interface CheckoutError {
  kind: CheckoutErrorKind;
  text: string;
  /** The e-mail the refused submit carried; only kept for a promo refusal. */
  email?: string;
}

/** Something the buyer did after the refusal. */
export type CheckoutErrorChange =
  | { type: 'promo_applied' }
  | { type: 'promo_removed' }
  | { type: 'email_changed'; email: string };

export function checkoutError(kind: CheckoutErrorKind, text: string, email?: string): CheckoutError {
  return kind === 'promo' && email !== undefined ? { kind, text, email } : { kind, text };
}

function sameEmail(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase();
}

/** True when the change makes the shown message describe a form that no longer exists. */
export function isCheckoutErrorStale(err: CheckoutError | null, change: CheckoutErrorChange): boolean {
  if (!err || err.kind !== 'promo') return false;
  switch (change.type) {
    case 'promo_applied':
    case 'promo_removed':
      return true;
    case 'email_changed':
      return err.email === undefined || !sameEmail(err.email, change.email);
  }
}

/** The message after the change: null when it went stale, otherwise unchanged. */
export function afterCheckoutErrorChange(
  err: CheckoutError | null,
  change: CheckoutErrorChange,
): CheckoutError | null {
  return isCheckoutErrorStale(err, change) ? null : err;
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
