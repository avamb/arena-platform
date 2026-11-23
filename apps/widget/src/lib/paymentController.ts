/**
 * paymentController.ts — the embedded payment flow as a small state machine
 * (spec 65 §3.2, §3.5, §3.6, §4), with no DOM and no Svelte so it can be
 * driven by tests with a fake Stripe.
 *
 *   loading -> ready -> submitting -> confirming -> paid
 *                  \         \-> declined -> (ready again, same hold)
 *                   \         \-> falling_back (a non-decline error)
 *                   \-> falling_back -> (redirect to the hosted page)
 *
 * The truth about "paid" is the platform's status endpoint (the webhook), never
 * Stripe's `confirm()` answer: a successful confirm only starts the polling.
 * `client_secret` lives in the `block` argument and goes to Stripe only.
 *
 * Two budgets, both counted from `start()`: Stripe.js must LOAD within 5 s
 * (a load failure falls back at once), and the whole path to the Payment
 * Element's `ready` (init, `loadActions()`, ready) has 15 s. Past either the
 * hosted page is used.
 */

import type { CheckoutStatusResponse, PaymentBlock } from './checkout.js';
import { declineGroup } from './paymentI18n.js';
import {
  attemptLabel,
  methodKind,
  type DeclineGroup,
  type PaymentSteps,
} from './paymentSteps.js';
import {
  loadStripeJs,
  openEmbeddedCheckout,
  STRIPE_FORM_READY_TIMEOUT_MS,
  STRIPE_JS_LOAD_TIMEOUT_MS,
  type CheckoutActionsLike,
  type CheckoutLike,
  type ElementLike,
  type OpenOptions,
  type StripeErrorLike,
} from './stripeElements.js';

export type PaymentPhase =
  | 'loading'
  | 'ready'
  | 'submitting'
  | 'confirming'
  | 'declined'
  | 'paid'
  /** The order reached a final non-paid state (expired, failed, cancelled). */
  | 'terminal'
  /** Confirmed, but the platform did not report paid within the poll window. */
  | 'pending_slow'
  | 'falling_back'
  /** Neither the form nor the hosted page can be opened: start again. */
  | 'unavailable';

export interface PaymentUiState {
  phase: PaymentPhase;
  /** Why the last attempt was declined (phase `declined`, and `ready` after it). */
  group?: DeclineGroup;
  /** Stripe says the form is complete and may be confirmed. */
  canPay: boolean;
  /** A wallet button (Apple Pay, Google Pay, Link) can be shown on this device. */
  walletsAvailable: boolean;
  /**
   * Stripe accepted the payment (a confirm succeeded, or Stripe says one is
   * running). The screen says "payment received" and offers no Back and no
   * "start again" from here on, whatever the clock says.
   */
  paymentReceived?: boolean;
  /**
   * The total exactly as Stripe's own session prints it ("€52.50"). `confirm()`
   * throws unless the page shows Stripe's total, so the pay button uses this.
   */
  totalText?: string;
  /** The final status (phases `paid` and `terminal`). */
  status?: CheckoutStatusResponse;
}

export interface PaymentMounts {
  /** Node for the Express Checkout Element (wallets). */
  express: unknown;
  /** Node for the Payment Element (card and the rest). */
  payment: unknown;
}

export interface PaymentControllerOptions {
  block: PaymentBlock;
  locale: string;
  steps: PaymentSteps;
  /** One status poll: GET /v1/public/checkout/{token}. */
  getStatus: () => Promise<CheckoutStatusResponse>;
  /** POST /v1/public/checkout/{token}/hosted. */
  postHosted: () => Promise<{ redirect_url: string }>;
  /** Navigates to the hosted page. */
  redirect: (url: string) => void;
  onState: (state: PaymentUiState) => void;
  /** Replaceable in tests. */
  open?: (o: OpenOptions) => ReturnType<typeof openEmbeddedCheckout>;
  loadStripe?: OpenOptions['loadStripe'];
  /** The budget from start() to the card form being ready (default 15 s). */
  initTimeoutMs?: number;
  /** The budget from start() to Stripe.js having loaded (default 5 s). */
  loadTimeoutMs?: number;
  /** How long to wait for the wallet availability before reporting the form view without it. */
  walletWaitMs?: number;
  pollIntervalMs?: number;
  /** The fast-poll window; the first minute by default. */
  pollMaxMs?: number;
  /** Poll interval after the fast window (5 s by default). */
  slowPollIntervalMs?: number;
  /** Total polling time once Stripe accepted the payment (default 15 min). */
  lateSettleMaxMs?: number;
  /**
   * After a failed confirm whose hosted fallback gave no page: how long to ask
   * the platform before returning the buyer to the same form (default 12 s).
   */
  recoverPollMaxMs?: number;
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
}

export interface PaymentController {
  /** Opens Stripe and mounts the elements. Call once. */
  start(mounts: PaymentMounts): Promise<void>;
  /** The buyer pressed "Pay". */
  submit(): Promise<void>;
  /** Polls the platform until the order settles (return from a bank page). */
  awaitSettlement(maxMs?: number): Promise<void>;
  /** Tears the elements down. */
  destroy(): void;
}

function errorCode(err: unknown): string {
  if (err !== null && typeof err === 'object') {
    const c = (err as { code?: unknown }).code;
    if (typeof c === 'string') return c;
  }
  return '';
}

/**
 * True for an error that is the CARD's answer (a decline, or a field the buyer
 * must fix): the buyer stays on the form and tries again. Everything else is an
 * integration, session or network error and cannot be fixed by another card,
 * so the caller falls back to the hosted page.
 *
 * Stripe types a decline `card_error` and a bad field `validation_error`. An
 * error whose code is a known decline (or 3-D Secure failure) counts whatever
 * its type; an unknown code counts only under one of those two types.
 */
export function isCardDecline(err: StripeErrorLike | null | undefined): boolean {
  if (!err) return false;
  // A known decline code wins whatever the type: a 3-D Secure failure comes
  // typed `invalid_request_error` (payment_intent_authentication_failure) and
  // is still the bank's answer, so the buyer stays on the form.
  if (declineGroup(err) !== 'other') return true;
  const type = (err.type ?? '').toLowerCase();
  return type === 'card_error' || type === 'validation_error';
}

const defaultSleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

function safely(fn: () => void): void {
  try {
    fn();
  } catch {
    /* a wallet or Stripe call must never break the flow */
  }
}

export function createPaymentController(opts: PaymentControllerOptions): PaymentController {
  const open = opts.open ?? openEmbeddedCheckout;
  const sleep = opts.sleep ?? defaultSleep;
  const now = opts.now ?? Date.now;
  const pollInterval = opts.pollIntervalMs ?? 2000;
  const pollMax = opts.pollMaxMs ?? 60_000;
  const slowPollInterval = opts.slowPollIntervalMs ?? 5000;
  // How long to keep asking after Stripe accepted the payment (the hold end plus
  // a few minutes, computed by the caller).
  const lateMax = opts.lateSettleMaxMs ?? 15 * 60_000;
  const budget = opts.initTimeoutMs ?? STRIPE_FORM_READY_TIMEOUT_MS;
  const loadBudget = opts.loadTimeoutMs ?? STRIPE_JS_LOAD_TIMEOUT_MS;
  const rawLoadStripe = opts.loadStripe ?? loadStripeJs;
  const walletWait = opts.walletWaitMs ?? 1500;
  const recoverMax = opts.recoverPollMaxMs ?? 12_000;

  let state: PaymentUiState = { phase: 'loading', canPay: false, walletsAvailable: false };
  let checkout: CheckoutLike | null = null;
  let actions: CheckoutActionsLike | null = null;
  const elements: ElementLike[] = [];
  let attempts = 0;
  let destroyed = false;
  let busy = false;
  let paymentComplete = false;
  let sessionCanConfirm = false;
  let deadlineTimer: ReturnType<typeof setTimeout> | null = null;
  let loadTimer: ReturnType<typeof setTimeout> | null = null;
  let walletTimer: ReturnType<typeof setTimeout> | null = null;
  let timedOut = false;
  let paymentReady = false;
  let walletsKnown = false;
  let formViewSent = false;

  function set(patch: Partial<PaymentUiState>): void {
    if (destroyed) return;
    state = { ...state, ...patch };
    opts.onState(state);
  }

  function clearDeadline(): void {
    if (deadlineTimer) clearTimeout(deadlineTimer);
    deadlineTimer = null;
    clearLoadTimer();
  }

  function clearLoadTimer(): void {
    if (loadTimer) clearTimeout(loadTimer);
    loadTimer = null;
  }

  function recomputeCanPay(): void {
    // Stripe's canConfirm is the authority; the element's `complete` covers
    // the moment before the session reports it.
    set({ canPay: sessionCanConfirm || paymentComplete });
  }

  /**
   * `checkout_form_view`, sent once: when the card form is ready AND the wallet
   * availability is known. If the wallet event never comes, it is sent after a
   * short wait WITHOUT the wallets field rather than with a guessed value.
   */
  function sendFormView(withWallets: boolean): void {
    if (formViewSent || destroyed) return;
    formViewSent = true;
    if (walletTimer) clearTimeout(walletTimer);
    opts.steps.emit(
      'checkout_form_view',
      withWallets ? { wallets_available: state.walletsAvailable } : {},
    );
  }

  function maybeSendFormView(): void {
    if (!paymentReady || formViewSent) return;
    if (walletsKnown) {
      sendFormView(true);
      return;
    }
    if (!walletTimer) walletTimer = setTimeout(() => sendFormView(false), walletWait);
  }

  async function fallBackToHosted(): Promise<void> {
    if (destroyed) return;
    clearDeadline();
    set({ phase: 'falling_back', canPay: false });
    try {
      const { redirect_url } = await opts.postHosted();
      if (redirect_url) {
        opts.redirect(redirect_url);
        return;
      }
      if (attempts > 0) {
        await recoverAfterConfirm();
        return;
      }
      set({ phase: 'unavailable' });
    } catch (err) {
      const code = errorCode(err);
      if (code === 'checkout.payment_in_progress') {
        // Stripe refused to expire the embedded session because it is paid or
        // processing: the money is going through. Wait for the platform; no
        // error, no new checkout.
        await settle(lateMax, true);
        return;
      }
      if (attempts > 0) {
        // A confirm already ran on this session, so the money may have moved
        // (the backend's 30-min floor check answers 409 before it asks Stripe
        // whether the session is paid). Never "start again" here: that opens
        // a new checkout and the buyer pays twice.
        await recoverAfterConfirm();
        return;
      }
      // 409 hosted_fallback_unavailable / not_payable (or the service being
      // down). Before telling the buyer to start again, ask the platform once:
      // the order may have been paid in the meantime.
      await settleOnceOrGiveUp();
    }
  }

  /**
   * A confirm was attempted and the hosted page is not available. Ask the
   * platform for a short while; if the order is still pending, put the buyer
   * back on the SAME embedded session (it was not expired, and Stripe never
   * pays one Checkout Session twice), as a decline of group 'other'.
   */
  async function recoverAfterConfirm(): Promise<void> {
    set({ phase: 'confirming', canPay: false });
    const deadline = now() + recoverMax;
    for (;;) {
      if (destroyed) return;
      try {
        const status = await opts.getStatus();
        if (destroyed) return;
        if (status.status === 'paid') {
          opts.steps.emit('payment_succeeded');
          set({ phase: 'paid', status });
          return;
        }
        if (status.status !== 'pending') {
          set({ phase: 'terminal', status });
          return;
        }
      } catch {
        /* a failed poll says nothing: keep asking until the deadline */
      }
      if (now() >= deadline) break;
      await sleep(pollInterval);
    }
    if (destroyed) return;
    set({ phase: 'declined', group: 'other', canPay: sessionCanConfirm || paymentComplete });
  }

  async function settleOnceOrGiveUp(): Promise<void> {
    try {
      const status = await opts.getStatus();
      if (destroyed) return;
      if (status.status === 'paid') {
        opts.steps.emit('payment_succeeded');
        set({ phase: 'paid', status });
        return;
      }
      if (status.status !== 'pending') {
        set({ phase: 'terminal', status });
        return;
      }
    } catch {
      /* fall through: the order cannot be paid here */
    }
    set({ phase: 'unavailable' });
  }

  /**
   * Polls the platform until the order is paid or reaches a final state.
   * Fast (pollInterval) for the first `pollMax`, then slower (slowPollInterval)
   * until `maxMs` is used up.
   *
   * @param received  The payment was accepted by Stripe (a confirm succeeded, or
   *                  Stripe says one is running): the screen then says so and
   *                  never offers "start again" or Back.
   */
  async function settle(maxMs: number = pollMax, received = false): Promise<void> {
    set(received ? { phase: 'confirming', canPay: false, paymentReceived: true } : { phase: 'confirming', canPay: false });
    const startedAt = now();
    const deadline = startedAt + maxMs;
    for (;;) {
      if (destroyed) return;
      try {
        const status = await opts.getStatus();
        if (destroyed) return;
        if (status.status === 'paid') {
          opts.steps.emit('payment_succeeded');
          set({ phase: 'paid', status });
          return;
        }
        if (status.status !== 'pending') {
          set({ phase: 'terminal', status });
          return;
        }
      } catch {
        /* a failed poll is not a failed payment: try again until the deadline */
      }
      if (now() >= deadline) {
        set({ phase: 'pending_slow' });
        return;
      }
      await sleep(now() - startedAt >= pollMax ? slowPollInterval : pollInterval);
    }
  }

  function declined(err: StripeErrorLike): void {
    const group = declineGroup(err);
    opts.steps.emit('payment_declined', { group, attempt: attemptLabel(attempts) });
    // The session stays open: the buyer may try another card on the same hold.
    set({ phase: 'declined', group, canPay: sessionCanConfirm || paymentComplete });
  }

  async function confirm(extra: Record<string, unknown> = {}): Promise<void> {
    if (!actions || busy || destroyed) return;
    busy = true;
    attempts += 1;
    set({ phase: 'submitting', canPay: false, group: undefined });
    // A wallet payment: if it fails, the wallet sheet must be told.
    const wallet = extra['expressCheckoutConfirmEvent'] as
      | { paymentFailed?: (o?: { reason?: string }) => void }
      | undefined;
    const failWallet = (): void => safely(() => wallet?.paymentFailed?.({ reason: 'fail' }));
    let result;
    try {
      // `if_required`: without it Stripe always leaves the page, even after a
      // successful card payment (spec 65 §3.2).
      result = await actions.confirm({ redirect: 'if_required', ...extra });
    } catch {
      // A throw is an integration or network problem, not a decline. The money
      // may still have moved: the hosted fallback answers payment_in_progress
      // then, and the screen waits for the platform instead of failing.
      busy = false;
      failWallet();
      await fallBackToHosted();
      return;
    }
    busy = false;
    if (result.type === 'error') {
      failWallet();
      if (isCardDecline(result.error)) declined(result.error ?? {});
      else await fallBackToHosted();
      return;
    }
    // Stripe accepted the payment. From here the buyer can never start over:
    // the platform's webhook is the only thing that can end this, and it may
    // take longer than the first minute.
    await settle(lateMax, true);
  }

  function canConfirmNow(): boolean {
    return !!actions && !busy && !destroyed && (state.phase === 'ready' || state.phase === 'declined');
  }

  return {
    async start(mounts) {
      set({ phase: 'loading' });
      const expire = (): void => {
        if (state.phase === 'loading' && !destroyed && !timedOut) {
          timedOut = true;
          void fallBackToHosted();
        }
      };
      // Deadline for everything up to the card form being ready.
      deadlineTimer = setTimeout(expire, budget);
      // Stripe.js itself must have loaded much sooner (spec 65 §3).
      let scriptLoaded = false;
      loadTimer = setTimeout(() => {
        if (!scriptLoaded) expire();
      }, loadBudget);

      let opened;
      try {
        // No budget of its own: the deadlines above are the only ones.
        opened = await open({
          publishableKey: opts.block.publishable_key,
          clientSecret: opts.block.client_secret,
          locale: opts.locale,
          loadStripe: async () => {
            const factory = await rawLoadStripe();
            scriptLoaded = true;
            clearLoadTimer();
            return factory;
          },
          timeoutMs: Number.POSITIVE_INFINITY,
        });
        scriptLoaded = true;
        clearLoadTimer();
      } catch {
        if (!timedOut) await fallBackToHosted();
        return;
      }
      if (destroyed || timedOut) {
        // Too late: the hosted page was already chosen (or the screen is gone).
        safely(() => opened.checkout.destroy?.());
        return;
      }
      checkout = opened.checkout;
      actions = opened.actions;

      try {
        checkout.on('change', (session) => {
          const canConfirm = session?.canConfirm === true;
          sessionCanConfirm = canConfirm;
          const text = session?.total?.total?.amount;
          if (typeof text === 'string' && text) set({ totalText: text });
          recomputeCanPay();
        });

        // Wallets above the card form (spec 65 §4).
        const express = checkout.createExpressCheckoutElement();
        elements.push(express);
        express.on(
          'availablepaymentmethodschange',
          (e: { availablePaymentMethods?: Record<string, boolean> | null }) => {
            const m = e?.availablePaymentMethods;
            walletsKnown = true;
            set({ walletsAvailable: !!m && Object.values(m).some(Boolean) });
            maybeSendFormView();
          },
        );
        express.on('click', (e: { expressPaymentType?: string; resolve?: () => void }) => {
          // The wallet sheet opens only after resolve(), and Stripe allows one
          // second for it: do it first, before anything else can throw.
          safely(() => e?.resolve?.());
          opts.steps.emit('payment_method_selected', { method: methodKind(e?.expressPaymentType) });
          opts.steps.emit('pay_clicked');
        });
        express.on('confirm', (e: { paymentFailed?: (o?: { reason?: string }) => void }) => {
          if (!canConfirmNow()) {
            // Never leave the wallet sheet hanging: tell it the payment failed.
            safely(() => e?.paymentFailed?.({ reason: 'fail' }));
            return;
          }
          void confirm({ expressCheckoutConfirmEvent: e });
        });
        express.mount(mounts.express);

        const payment = checkout.createPaymentElement();
        elements.push(payment);
        payment.on('ready', () => {
          paymentReady = true;
          clearDeadline();
          if (state.phase === 'loading') set({ phase: 'ready' });
          maybeSendFormView();
        });
        payment.on('change', (e: { complete?: boolean; value?: { type?: string } }) => {
          paymentComplete = e?.complete === true;
          // Spec 65 §6.1: the Payment Element reporting complete=true (the bus
          // sends it once per checkout).
          if (paymentComplete) opts.steps.emit('payment_details_complete');
          if (e?.value?.type) {
            opts.steps.emit('payment_method_selected', { method: methodKind(e.value.type) });
          }
          recomputeCanPay();
        });
        payment.on('loaderror', () => {
          if (state.phase === 'loading' || state.phase === 'ready') void fallBackToHosted();
        });
        payment.mount(mounts.payment);
      } catch {
        await fallBackToHosted();
      }
    },

    async submit() {
      if (!canConfirmNow()) return;
      opts.steps.emit('pay_clicked');
      await confirm();
    },

    async awaitSettlement(maxMs) {
      await settle(maxMs);
    },

    destroy() {
      destroyed = true;
      clearDeadline();
      if (walletTimer) clearTimeout(walletTimer);
      for (const el of elements) safely(() => el.destroy?.());
      safely(() => checkout?.destroy?.());
      checkout = null;
      actions = null;
    },
  };
}
