/**
 * paymentSteps.ts — the ONE place the payment screen reports what the buyer
 * does (spec 65 §6.1, stage E6a).
 *
 * The payment screen never knows who listens. It calls `emit(step, payload)`;
 * this module hands the step to whoever subscribed. Today the only subscriber
 * is the widget's own `arena:checkout` CustomEvent for host-page developers,
 * and nothing is forwarded to GA, Meta or our funnel yet (E6b/E6c add
 * destinations behind this same call). Steps carry no buyer data: no e-mail,
 * no card detail, no client secret.
 *
 * Closed lists on purpose: the funnel accepts only these values (§6.2), so an
 * unknown method or reason never leaves the browser as free text.
 */

/**
 * The widget-side steps of spec 65 §6.1, under the spec's own names.
 * `checkout_start`, `paid` and `abandoned` are the server's. `payment_3ds` is
 * part of the vocabulary but is not emitted yet: with `redirect: 'if_required'`
 * Stripe's own 3-D Secure frame opens inside `confirm()` and gives the page no
 * event to observe. `payment_succeeded` is the screen's own "paid" mark, sent
 * when the platform's status says so.
 */
export type PaymentStep =
  | 'checkout_form_view'
  | 'payment_method_selected'
  | 'payment_details_complete'
  | 'pay_clicked'
  | 'payment_3ds'
  | 'payment_declined'
  | 'payment_succeeded';

/** Payment methods the funnel tells apart; everything else is `other`. */
export type PaymentMethodKind = 'card' | 'apple_pay' | 'google_pay' | 'link' | 'other';

/** The decline groups of §6.1; the group, never the raw Stripe code, is reported. */
export type DeclineGroup = 'no_funds' | 'bank_declined' | 'input_error' | 'auth_failed' | 'other';

/** Attempt number as the closed list `1`, `2`, `3+` (§6.2). */
export type AttemptLabel = '1' | '2' | '3+';

export interface PaymentStepPayload {
  /** `payment_method_selected`: which method. */
  method?: PaymentMethodKind;
  /** `payment_declined`: why, as a group. */
  group?: DeclineGroup;
  /** `payment_declined`: which try on this order. */
  attempt?: AttemptLabel;
  /** `checkout_form_view`: whether a wallet button is available on this device. Absent when not known. */
  wallets_available?: boolean;
}

export interface PaymentStepEvent extends PaymentStepPayload {
  step: PaymentStep;
}

export type PaymentStepListener = (event: PaymentStepEvent) => void;

export interface PaymentSteps {
  /** Reports a step. Never throws: a broken listener cannot break a payment. */
  emit(step: PaymentStep, payload?: PaymentStepPayload): void;
  /** Subscribes; returns the unsubscribe function. */
  subscribe(listener: PaymentStepListener): () => void;
}

/** `1`, `2`, `3+` for the n-th try (n >= 1). */
export function attemptLabel(n: number): AttemptLabel {
  if (n <= 1) return '1';
  if (n === 2) return '2';
  return '3+';
}

/** Maps Stripe's express payment type or payment-element type onto the closed list. */
export function methodKind(raw: string | null | undefined): PaymentMethodKind {
  switch ((raw ?? '').trim().toLowerCase()) {
    case 'card':
      return 'card';
    case 'apple_pay':
    case 'applepay':
      return 'apple_pay';
    case 'google_pay':
    case 'googlepay':
      return 'google_pay';
    case 'link':
      return 'link';
    default:
      return 'other';
  }
}

/** Where a bus keeps the "already sent" marks of its once-only steps. */
export interface SentMarks {
  load(): string[];
  save(marks: string[]): void;
}

/**
 * Creates a step bus for one payment screen. `checkout_form_view`,
 * `payment_details_complete` and `payment_succeeded` fire once per bus (and,
 * with `marks`, once per checkout even across a reload); a repeated method pick
 * of the same method is dropped so a buyer clicking back and forth is not
 * counted twice.
 */
export function createPaymentSteps(marks?: SentMarks): PaymentSteps {
  const listeners = new Set<PaymentStepListener>();
  const once = new Set<string>(marks ? marks.load() : []);
  let lastMethod: PaymentMethodKind | null = null;

  return {
    emit(step, payload = {}) {
      if (step === 'checkout_form_view' || step === 'payment_details_complete' || step === 'payment_succeeded') {
        if (once.has(step)) return;
        once.add(step);
        marks?.save([...once]);
      }
      if (step === 'payment_method_selected') {
        if (payload.method === lastMethod) return;
        lastMethod = payload.method ?? null;
      }
      const event: PaymentStepEvent = { step, ...payload };
      for (const l of [...listeners]) {
        try {
          l(event);
        } catch {
          /* a listener must never break the payment */
        }
      }
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

/**
 * One step bus PER checkout token: a second purchase in the same tab has a
 * new token and so reports its steps again, while the same checkout resumed
 * (a reload, a return from the bank) does not report the form view twice.
 * Only the latest token's bus is kept, so the registry never grows.
 */
const MARKS_KEY_PREFIX = 'arena_checkout_steps:';

function defaultStorage(): Pick<Storage, 'getItem' | 'setItem'> | null {
  try {
    return typeof sessionStorage !== 'undefined' ? sessionStorage : null;
  } catch {
    return null; // storage blocked
  }
}

/**
 * The "sent once" marks of one checkout in the tab's session storage, keyed by
 * checkout token (spec 65 §6.2). Only step NAMES are stored: no e-mail, no
 * secret. Storage that is missing, full or blocked just means "not marked".
 */
function storageMarks(storage: Pick<Storage, 'getItem' | 'setItem'>, token: string): SentMarks {
  const key = MARKS_KEY_PREFIX + token;
  return {
    load() {
      try {
        const raw = storage.getItem(key);
        const parsed: unknown = raw ? JSON.parse(raw) : [];
        return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : [];
      } catch {
        return [];
      }
    },
    save(marks) {
      try {
        storage.setItem(key, JSON.stringify(marks));
      } catch {
        /* storage unavailable: the in-memory mark still holds */
      }
    },
  };
}

export interface PaymentStepsRegistry {
  forToken(token: string): PaymentSteps;
}

export function createPaymentStepsRegistry(
  onEvent: (event: PaymentStepEvent) => void,
  storage: Pick<Storage, 'getItem' | 'setItem'> | null = defaultStorage(),
): PaymentStepsRegistry {
  let currentToken: string | null = null;
  let current: PaymentSteps | null = null;
  return {
    forToken(token) {
      if (current && currentToken === token) return current;
      const bus = createPaymentSteps(storage ? storageMarks(storage, token) : undefined);
      bus.subscribe(onEvent);
      currentToken = token;
      current = bus;
      return bus;
    },
  };
}
