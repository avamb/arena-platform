/**
 * holdBasics.ts — the small part of the seat hold (spec 67) that the main
 * widget bundle needs before the hold code itself is loaded: the hold's clock,
 * the "payment is going on" call and two types.
 *
 * Everything else (controller, routes, words, drawing) loads lazily through
 * lib/holdRuntime.ts. Keep this module free of imports of lib/holds.ts,
 * lib/holdI18n.ts, lib/holdDraw.ts and lib/holdWidget.ts, or they come back
 * into the main bundle; and keep it small: what only the chunk uses belongs
 * in the chunk (a value shared across chunks costs an export).
 */

import { countdownSeconds } from './cart.js';

export interface HoldPolicy {
  select_seconds: number;
  payment_seconds: number;
  extend_seconds: number;
}

/** One rendered seat: the centre comes from `getBBox()`, never from the plan's cx/cy. */
export interface SeatSpot {
  key: string;
  x: number;
  y: number;
  rowKey: string;
  tierId: string;
  /** Price in minor units, or null when the category has none. */
  price: number | null;
  status: string;
}

/**
 * POST /v1/public/checkout/{checkout_token}/paying (§4.8): "payment is going
 * on", sent right before Stripe confirms. Fire and forget: the payment never
 * waits for it. A network failure is sent once more.
 */
export function postCheckoutPaying(checkoutToken: string, apiBase: string): void {
  const url = `${apiBase}/v1/public/checkout/${encodeURIComponent(checkoutToken)}/paying`;
  const send = (): Promise<unknown> => fetch(url, { method: 'POST', keepalive: true });
  try {
    void send().catch(() => {
      setTimeout(() => {
        try {
          void send().catch(() => {});
        } catch {
          /* nothing to do */
        }
      }, 1_000);
    });
  } catch {
    /* nothing to do */
  }
}

/** Seconds left on the hold's own clock (never below zero). */
export function holdSecondsLeft(expiresAt: string | null, nowMs: number = Date.now()): number {
  return expiresAt ? countdownSeconds(expiresAt, nowMs) : 0;
}
