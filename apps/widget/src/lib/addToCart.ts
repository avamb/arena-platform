/**
 * AddToCart for server-side Meta CAPI / GA4.
 *
 * Every cart gets an id the moment it first holds something; the platform
 * queues one AddToCart per id (event id `atc:<id>`), so repeated quotes of the
 * same cart count once. The id is dropped when the cart empties or a checkout
 * starts: the next cart is a new one. Alongside, the widget remembers every
 * distinct seat the buyer selected in this cart (even ones taken off again):
 * `seats_considered` is that count plus the GA tickets now in the cart (a GA
 * line has no seat keys, its quantity counts).
 *
 * Nothing here is sent without the buyer's explicit yes: `withAddToCart`
 * leaves the payload alone unless the buyer context says
 * `tracking_consent: true`.
 */
import type { BuyerContextPayload } from './buyerContext.js';
import type { CheckoutQuotePayload } from './checkout.js';

/** The platform's bound on seats_considered. */
export const MAX_SEATS_CONSIDERED = 1000;

export interface AddToCartState {
  /** This cart's id; null while the cart is empty. */
  id: string | null;
  /** Distinct seat keys selected in this cart, removed ones included. */
  seats: ReadonlySet<string>;
}

export function initialAddToCartState(): AddToCartState {
  return { id: null, seats: new Set() };
}

/** A fresh cart id: a UUID, or 32 random hex characters without one. */
export function newAddToCartId(): string {
  const c = (globalThis as { crypto?: Crypto }).crypto;
  if (c && typeof c.randomUUID === 'function') return c.randomUUID();
  let out = '';
  for (let i = 0; i < 32; i++) out += Math.floor(Math.random() * 16).toString(16);
  return out;
}

/**
 * The state after the cart changed. An empty cart forgets the id and the
 * seats; a cart with something in it keeps its id (or gets one) and adds the
 * seats selected now.
 */
export function trackCartChange(
  state: AddToCartState,
  cartCount: number,
  selectedSeatKeys: Iterable<string>,
  makeId: () => string = newAddToCartId,
): AddToCartState {
  if (cartCount <= 0) return state.id === null && state.seats.size === 0 ? state : initialAddToCartState();
  const seats = new Set(state.seats);
  for (const k of selectedSeatKeys) seats.add(k);
  return { id: state.id ?? makeId(), seats };
}

/** Seats considered: distinct seat keys plus the GA tickets in the cart, capped. */
export function seatsConsidered(state: AddToCartState, gaQuantities: ReadonlyMap<string, number>): number {
  let n = state.seats.size;
  for (const q of gaQuantities.values()) if (Number.isFinite(q) && q > 0) n += Math.floor(q);
  return Math.min(n, MAX_SEATS_CONSIDERED);
}

/**
 * The quote payload with the AddToCart fields, only when the buyer context
 * says an explicit yes and the cart has an id; otherwise the payload as it was.
 */
export function withAddToCart(
  payload: CheckoutQuotePayload,
  buyerContext: BuyerContextPayload | null,
  state: AddToCartState,
  gaQuantities: ReadonlyMap<string, number>,
  cartEvent?: 'add_to_cart' | 'initiate_checkout',
): CheckoutQuotePayload {
  if (buyerContext?.tracking_consent !== true || !state.id) return payload;
  const out: CheckoutQuotePayload = { ...payload, buyer_context: buyerContext, add_to_cart_id: state.id };
  const seats = seatsConsidered(state, gaQuantities);
  if (seats > 0) out.seats_considered = seats;
  if (cartEvent === 'initiate_checkout') out.cart_event = 'initiate_checkout';
  return out;
}

/** The selection must stay unchanged this long before AddToCart is sent. */
export const ADD_TO_CART_DELAY_MS = 1500;

/**
 * AddToCart starts only after an explicit buyer action in this cart: `touch`
 * (seat click, quantity change) marks the cart; `cartChanged` schedules only a
 * touched, non-empty cart. A preset, default or restored cart never starts it.
 * An empty cart drops the mark, so the next cart needs its own action.
 */
export function createAddToCartTrigger(
  fire: () => void,
  delayMs: number = ADD_TO_CART_DELAY_MS,
): { touch: () => void; cartChanged: (count: number) => void; cancel: () => void } {
  const scheduler = createAddToCartScheduler(fire, delayMs);
  let touched = false;
  return {
    cancel: scheduler.cancel,
    touch: () => {
      touched = true;
    },
    cartChanged: (count: number) => {
      if (count <= 0) {
        touched = false;
        scheduler.cancel();
      } else if (touched) {
        scheduler.schedule();
      }
    },
  };
}

/** A restartable one-shot timer: `schedule` replaces a pending run, `cancel` drops it. */
export function createAddToCartScheduler(
  fire: () => void,
  delayMs: number = ADD_TO_CART_DELAY_MS,
): { schedule: () => void; cancel: () => void } {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const cancel = (): void => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
  };
  return {
    cancel,
    schedule: () => {
      cancel();
      timer = setTimeout(() => {
        timer = null;
        fire();
      }, delayMs);
    },
  };
}
