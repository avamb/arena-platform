/**
 * Hermes F-01 (2026-11-01, prod 23b7d49e): after checkout/start refused a code
 * for the buyer's e-mail ("You already used this code. Code removed."), the
 * buyer applied the code again and typed another e-mail. The discount was back
 * (the sheet and Stripe both said 34.34) but the old sentence stayed above the
 * form, because the message was only cleared by the next submit.
 *
 * The message now carries its kind (lib/checkoutError.ts) and a promo refusal
 * goes away as soon as it no longer describes the form. These tests drive the
 * real promo controller with the same wiring ArenaTickets uses, render the
 * real CartSheet with the resulting money, and pin the wiring in the component
 * sources (the components cannot be mounted here: the suite runs Svelte's
 * server build, the idiom of the other widget tests).
 */

import { readFileSync } from 'fs';
import { join } from 'path';
import { fileURLToPath } from 'url';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
import CartSheet from './components/CartSheet.svelte';
import { ApiError } from './api.js';
import type { CheckoutQuoteResponse } from './lib/checkout.js';
import {
  afterCheckoutErrorChange,
  checkoutError,
  isCheckoutErrorStale,
  promoCodeChange,
  type CheckoutError,
  type CheckoutErrorChange,
} from './lib/checkoutError.js';
import { formatAmount, promoErrorMessage } from './lib/promo.js';
import { createPromoController, type PromoState } from './lib/promoController.js';
import { getPromoStrings } from './lib/promoI18n.js';

const P = getPromoStrings('en');
const REFUSAL = `${promoErrorMessage('promo.per_customer_limit', P)} ${P.removed}`;
const CONFLICT = 'Some of the seats you chose were just taken.';

// 2 x 19.90 = 39.80, the code takes 5.46 off: 34.34, the amount Hermes saw.
const QUOTE: CheckoutQuoteResponse = {
  pricing: { subtotal: 3980, discount: 546, platform_fee: 0, total: 3434, currency: 'EUR' },
  promo_applied: true,
};

const GA_LINE = {
  type: 'ga' as const,
  tierId: 'tier-1',
  tierName: 'Standard',
  quantity: 2,
  priceAmount: 1990,
  currency: 'EUR',
  seatKeys: [],
  zoneKey: '',
};

/**
 * The cart sheet's state as ArenaTickets keeps it: the promo controller, the
 * message above the form, and the three moments that can make it stale.
 */
function widget() {
  let promo: PromoState = { code: null, quote: null, busy: false, error: null, note: null };
  let message: CheckoutError | null = null;
  const note = (change: CheckoutErrorChange) => {
    message = afterCheckoutErrorChange(message, change);
  };
  const ctl = createPromoController({
    getCartKey: () => 'cart',
    buildPayload: (code) => ({ session_id: 's1', ga_items: [{ tier_id: 'tier-1', quantity: 2 }], promo_code: code }),
    quote: () => Promise.resolve(QUOTE),
    strings: () => P,
    conflictText: () => null,
    errorCode: (err) => (err instanceof ApiError ? err.code : undefined),
    onChange: (next) => {
      const change = promoCodeChange(promo.code, next.code);
      promo = next;
      if (change) note(change);
    },
  });
  return {
    ctl,
    get promo() {
      return promo;
    },
    get message() {
      return message;
    },
    /** checkout/start refused the code for this e-mail (handleCheckout). */
    startRefusedPromo(email: string) {
      message = checkoutError('promo', REFUSAL, email);
      ctl.drop();
    },
    /** checkout/start answered a seat conflict (handleCheckout). */
    startRefusedSeats() {
      message = checkoutError('conflict', CONFLICT);
    },
    typeEmail(email: string) {
      note({ type: 'email_changed', email });
    },
    removeCode() {
      ctl.remove();
      note({ type: 'promo_removed' });
    },
    /** The cart sheet rendered with this state. */
    sheetHtml(): string {
      return render(CartSheet, {
        props: {
          cart: { checkoutToken: null, expiresAt: null, lines: [GA_LINE] },
          buyerFields: [],
          promoAvailable: true,
          promoCode: promo.code,
          promoQuote: promo.quote,
          cartKey: 'cart',
          submitError: message?.text ?? null,
          onClose: () => {},
          onRemoveLine: () => {},
          onCheckout: () => {},
        },
      }).body;
    },
  };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('a promo refusal from checkout/start goes stale', () => {
  it('goes away when the buyer applies the code again, and the sheet shows the discounted total', async () => {
    const w = widget();
    await w.ctl.apply('VINO');
    w.startRefusedPromo('used@example.com');
    expect(w.message?.text).toBe('You already used this code. Code removed.');
    expect(w.promo.code).toBeNull();

    await w.ctl.apply('VINO');
    expect(w.promo.code).toBe('VINO');
    expect(w.promo.quote?.total).toBe(3434);
    expect(w.message).toBeNull();
  });

  it('goes away when the buyer types another e-mail, even before applying the code again', () => {
    const w = widget();
    w.startRefusedPromo('used@example.com');
    w.typeEmail('used@example.co');
    expect(w.message).toBeNull();
  });

  it('stays while the e-mail is the refused one (only case and spaces differ)', () => {
    const w = widget();
    w.startRefusedPromo('used@example.com');
    w.typeEmail('  USED@example.com ');
    expect(w.message?.text).toBe(REFUSAL);
  });

  it('goes away when the buyer takes a code off', async () => {
    const w = widget();
    w.startRefusedPromo('used@example.com');
    // The buyer applies another code and takes it off again: the message was
    // already gone with the apply, and Remove keeps it gone.
    await w.ctl.apply('OTHER');
    w.removeCode();
    expect(w.message).toBeNull();
    // Remove on its own clears a promo refusal too.
    const v = widget();
    v.startRefusedPromo('used@example.com');
    v.removeCode();
    expect(v.message).toBeNull();
  });

  it('leaves the sheet with the discounted total and no message', async () => {
    const w = widget();
    w.startRefusedPromo('used@example.com');
    // The server render shows the cart view of the sheet (the money); the
    // message prop it is given is the same one the buyer-form view prints.
    expect(w.sheetHtml()).toContain(formatAmount(3980, 'EUR'));
    await w.ctl.apply('VINO');
    w.typeEmail('new@example.com');
    expect(w.sheetHtml()).toContain(formatAmount(3434, 'EUR'));
    expect(w.message).toBeNull();
  });
});

describe('other refusals are left alone', () => {
  it('keeps a seat conflict while the buyer applies or takes off a code and edits the e-mail', async () => {
    const w = widget();
    w.startRefusedSeats();
    await w.ctl.apply('VINO');
    w.typeEmail('someone@example.com');
    w.removeCode();
    expect(w.message).toEqual({ kind: 'conflict', text: CONFLICT });
  });

  it('never calls a non-promo message stale', () => {
    const changes: CheckoutErrorChange[] = [
      { type: 'promo_applied' },
      { type: 'promo_removed' },
      { type: 'email_changed', email: 'x@example.com' },
    ];
    for (const kind of ['conflict', 'consent', 'cart_limit', 'other'] as const) {
      for (const change of changes) {
        expect(isCheckoutErrorStale(checkoutError(kind, 'text', 'a@example.com'), change), `${kind} ${change.type}`).toBe(false);
      }
    }
    expect(afterCheckoutErrorChange(null, { type: 'promo_applied' })).toBeNull();
  });

  it('keeps the e-mail only for a promo refusal', () => {
    expect(checkoutError('conflict', 'text', 'a@example.com')).toEqual({ kind: 'conflict', text: 'text' });
    expect(checkoutError('promo', 'text', 'a@example.com')).toEqual({ kind: 'promo', text: 'text', email: 'a@example.com' });
  });

  it('reports only a code that became applied', () => {
    expect(promoCodeChange(null, 'VINO')).toEqual({ type: 'promo_applied' });
    expect(promoCodeChange('VINO', 'OTHER')).toEqual({ type: 'promo_applied' });
    expect(promoCodeChange('VINO', 'VINO')).toBeNull();
    expect(promoCodeChange('VINO', null)).toBeNull();
    expect(promoCodeChange(null, null)).toBeNull();
  });
});

describe('the components are wired to it', () => {
  const dir = fileURLToPath(new URL('.', import.meta.url));
  const read = (p: string) => readFileSync(join(dir, p), 'utf8');
  const app = read('ArenaTickets.svelte');
  const sheet = read('components/CartSheet.svelte');
  const form = read('components/BuyerForm.svelte');

  it('ArenaTickets keeps the kind and reports every moment that can make a promo refusal stale', () => {
    expect(app).toMatch(/let checkoutError = \$state<CheckoutError \| null>/);
    expect(app).toMatch(/makeCheckoutError\(\s*'promo',[\s\S]{0,120}values\.email,?\s*\)/);
    expect(app).toMatch(/makeCheckoutError\('conflict', t\.conflict_notice\)/);
    expect(app).toMatch(/promoCodeChange\(promo\.code, next\.code\)/);
    expect(app).toMatch(/promoCtl\.remove\(\);\s*noteCheckoutChange\(\{ type: 'promo_removed' \}\)/);
    expect(app).toMatch(/onEmailChange=\{\(email\) => noteCheckoutChange\(\{ type: 'email_changed', email \}\)\}/);
    expect(app).toContain('submitError={checkoutError?.text ?? null}');
  });

  it('the cart sheet hands the e-mail edits of the buyer form up', () => {
    expect(sheet).toMatch(/onEmailChange\?: \(email: string\) => void;/);
    expect(sheet).toMatch(/<BuyerForm[\s\S]*?\{onEmailChange\}[\s\S]*?\/>/);
  });

  it('the buyer form reports a typed e-mail and an accepted suggestion', () => {
    const onInput = form.slice(form.indexOf('function onEmailInput'), form.indexOf('function acceptSuggestion'));
    const onAccept = form.slice(form.indexOf('function acceptSuggestion'), form.indexOf('function handleSubmit'));
    expect(onInput).toContain('onEmailChange?.(email);');
    expect(onAccept).toContain('onEmailChange?.(email);');
  });
});
