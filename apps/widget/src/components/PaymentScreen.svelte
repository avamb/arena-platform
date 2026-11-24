<script lang="ts">
  /**
   * PaymentScreen — the widget pays on its own page (spec 65 §4, stage E4).
   *
   * Stripe Checkout Elements: Express Checkout (Apple Pay, Google Pay, Link)
   * above the Payment Element, the pay button with the amount, the hold clock
   * and the organizer line. Everything Stripe-shaped lives in
   * lib/paymentController.ts; this file only draws its state.
   *
   * The look is Stripe's own (theme 'stripe', white, system font, Stripe blue)
   * and ignores the widget theme on purpose (owner decision). `client_secret`
   * comes in as a prop and goes straight to the controller: it is never
   * rendered, logged or stored.
   */
  import { onMount, onDestroy } from 'svelte';
  import type { CheckoutStatusResponse, PaymentBlock } from '../lib/checkout.js';
  import { formatPrice, getPaymentCommitStrings, payButtonLabel } from '../lib/checkout.js';
  import { countdownSeconds } from '../lib/cart.js';
  import {
    createPaymentController,
    type DeferredPaymentHandle,
    type PaymentController,
    type PaymentUiState,
  } from '../lib/paymentController.js';
  import { declineText, fill, formatHoldTime, getPaymentStrings } from '../lib/paymentI18n.js';
  import type { PaymentSteps } from '../lib/paymentSteps.js';
  import { mountInLightDom, type LightDomMounts } from '../lib/lightDomMount.js';
  import { getCheckoutStatus, postCheckoutHosted } from '../api.js';

  interface Props {
    payment: PaymentBlock;
    checkoutToken: string;
    apiBase?: string;
    locale?: string;
    /** When the form stops taking money (ISO-8601). */
    expiresAt?: string | null;
    /** Order total, minor units. */
    total?: number | null;
    currency?: string;
    organizerName?: string;
    /** The buyer came back from a bank page: check the platform first, show the form only if still unpaid. */
    awaitReturn?: boolean;
    steps: PaymentSteps;
    onPaid: (status: CheckoutStatusResponse) => void;
    onTerminal: (status: CheckoutStatusResponse) => void;
    /** "Start again": the order cannot be paid here any more. */
    onRestart: () => void;
    /** "Back" to the contacts screen; the hold is kept. Absent: no link. */
    onBack?: () => void;
    /** Replaceable in tests and by the host: navigates to the hosted page. */
    redirect?: (url: string) => void;
    /**
     * A prepared checkout (preload-payment="checkout"): the screen loads hidden
     * and waits for `reveal`; read once, on mount.
     */
    deferred?: boolean;
    /** Deferred: the handle the widget reveals the screen through (null when gone). */
    onHandle?: (token: string, handle: DeferredPaymentHandle | null) => void;
    /** Deferred: the hidden form failed; the widget pays the classic way. */
    onDeferredFailure?: (token: string) => void;
  }

  const {
    payment,
    checkoutToken,
    apiBase = '',
    locale = 'en',
    expiresAt = null,
    total = null,
    currency = '',
    organizerName = '',
    awaitReturn = false,
    steps,
    onPaid,
    onTerminal,
    onRestart,
    onBack,
    redirect = (url: string) => {
      window.location.href = url;
    },
    deferred = false,
    onHandle,
    onDeferredFailure,
  }: Props = $props();

  const s = $derived(getPaymentStrings(locale));
  const dir = $derived(locale.trim().toLowerCase().startsWith('he') ? 'rtl' : 'ltr');

  let expressNode = $state<HTMLElement | null>(null);
  let paymentNode = $state<HTMLElement | null>(null);
  let ui = $state<PaymentUiState>({ phase: 'loading', canPay: false, walletsAvailable: false });
  // True only while the screen is checking a payment the buyer made on a bank's
  // page; once the form takes over (or the buyer pays here) it is false.
  let returning = $state(awaitReturn);
  let secondsLeft = $state(Number.POSITIVE_INFINITY);
  const uid = Math.random().toString(36).slice(2, 10);
  const expressSlot = `arena-pay-express-${uid}`;
  const paymentSlot = `arena-pay-element-${uid}`;
  let light: LightDomMounts | null = null;
  let controller: PaymentController | null = null;
  let mountedToken = '';
  let mountedDeferred = false;
  let releaseHandle: (() => void) | null = null;
  let clock: ReturnType<typeof setInterval> | null = null;

  const busy = $derived(
    ui.phase === 'submitting' || ui.phase === 'confirming' || ui.phase === 'falling_back',
  );
  // Stripe accepted the payment: from here only the platform can end this.
  // No clock message, no Back, no "start again".
  const received = $derived(ui.paymentReceived === true);
  const timeUp = $derived(secondsLeft <= 0 && ui.phase !== 'paid' && !busy && !received);
  const formVisible = $derived(
    ui.phase === 'loading' ||
      ui.phase === 'ready' ||
      ui.phase === 'declined' ||
      ui.phase === 'submitting',
  );
  const showWarn = $derived(Number.isFinite(secondsLeft) && secondsLeft > 0 && secondsLeft <= 300);
  const holdTime = $derived(formatHoldTime(expiresAt, locale));
  // Stripe's own total once known, so the summary and the pay button never
  // show two different amounts; ours only until Stripe's first update.
  const amountText = $derived(
    ui.totalText || (total != null && currency ? formatPrice(total, currency) : ''),
  );
  // "Pay 52,50 €" names the amount and the act. The amount is the one STRIPE's
  // session prints: confirm() throws unless the page shows it. Ours is only the
  // answer before Stripe's first update; without either, "Pay now".
  const payLabel = $derived(
    ui.phase === 'submitting'
      ? s.paying
      : ui.totalText
        ? getPaymentCommitStrings(locale).pay_label.replace('{amount}', ui.totalText)
        : ((total != null ? payButtonLabel(total, currency, locale) : undefined) ??
          getPaymentCommitStrings(locale).pay_label_plain),
  );

  function lateSettleMs(): number {
    const toHoldEnd = expiresAt ? Date.parse(expiresAt) - Date.now() : 0;
    return Math.max(60_000, (Number.isFinite(toHoldEnd) ? toHoldEnd : 0) + 5 * 60_000);
  }

  function tick(): void {
    secondsLeft = expiresAt ? countdownSeconds(expiresAt) : Number.POSITIVE_INFINITY;
  }

  function handleState(next: PaymentUiState): void {
    ui = next;
    if (next.phase === 'paid' && next.status) onPaid(next.status);
    else if (next.phase === 'terminal' && next.status) onTerminal(next.status);
  }

  onMount(() => {
    tick();
    clock = setInterval(tick, 1000);
    // Read once: the parent's props may be gone by the time this screen is destroyed.
    mountedToken = checkoutToken;
    mountedDeferred = deferred;
    const handleCb = onHandle;
    const failCb = onDeferredFailure;
    releaseHandle = mountedDeferred && handleCb ? () => handleCb(mountedToken, null) : null;
    controller = createPaymentController({
      block: payment,
      locale,
      steps,
      getStatus: () => getCheckoutStatus(checkoutToken, apiBase, { includePayment: false }),
      postHosted: () => postCheckoutHosted(checkoutToken, apiBase),
      redirect,
      onState: handleState,
      // After Stripe accepts the payment keep asking until the hold is over and a
      // few minutes more (the webhook can be late), never less than a minute.
      lateSettleMaxMs: lateSettleMs(),
      deferred: mountedDeferred,
      onDeferredFailure: () => failCb?.(mountedToken),
    });
    if (mountedDeferred) {
      handleCb?.(mountedToken, {
        reveal: (email: string) => (controller ? controller.reveal(email) : Promise.resolve(false)),
        failed: () => !controller || controller.hasFailed(),
      });
    }
    // Stripe's form must live in the light DOM (never ready inside a ShadowRoot).
    light = mountInLightDom(expressNode, paymentNode, expressSlot, paymentSlot);
    const mounts = { express: light.express, payment: light.payment };
    if (awaitReturn) {
      // Back from the bank's own page: the webhook may already have paid it.
      void controller.awaitSettlement(20_000).then(() => {
        if (ui.phase === 'pending_slow' && controller) {
          returning = false;
          void controller.start(mounts);
        }
      });
    } else {
      void controller.start(mounts);
    }
  });

  onDestroy(() => {
    releaseHandle?.();
    releaseHandle = null;
    if (clock) clearInterval(clock);
    controller?.destroy();
    controller = null;
    light?.cleanup();
    light = null;
  });

  function pay(): void {
    void controller?.submit();
  }
</script>

<div class="pay" data-testid="payment-screen" data-phase={ui.phase}>
  <h2 class="pay-title">{s.title}</h2>

  {#if amountText}
    <div class="pay-summary" data-testid="payment-summary">
      <span>{s.title}</span>
      <strong>{amountText}</strong>
    </div>
  {/if}

  {#if onBack && !busy && !received && ui.phase !== 'paid' && ui.phase !== 'terminal'}
    <button type="button" class="pay-back" data-testid="payment-back" onclick={onBack}>
      {dir === 'rtl' ? '→' : '←'} {s.back}
    </button>
  {/if}

  {#if holdTime && !timeUp && !received && ui.phase !== 'paid'}
    <p class="pay-hold" class:warn={showWarn} role={showWarn ? 'alert' : undefined} data-testid="payment-hold">
      {fill(showWarn ? s.held_until_warn : s.held_until, { time: holdTime })}
    </p>
  {/if}

  {#if timeUp}
    <p class="pay-note error" role="alert" data-testid="payment-time-up">{s.time_up}</p>
    <button type="button" class="pay-secondary" onclick={onRestart}>{s.start_again}</button>
  {:else if ui.phase === 'unavailable'}
    <p class="pay-note error" role="alert" data-testid="payment-unavailable">{s.cannot_open}</p>
    <button type="button" class="pay-secondary" onclick={onRestart}>{s.start_again}</button>
  {:else if ui.phase === 'falling_back'}
    <p class="pay-note" aria-live="polite" aria-busy="true">{s.opening_hosted}</p>
  {:else if ui.phase === 'confirming'}
    <p class="pay-note" aria-live="polite" aria-busy="true" data-testid="payment-confirming">
      <span class="spinner" aria-hidden="true"></span>
      {returning ? s.checking_return : received ? s.payment_received : s.confirming}
    </p>
  {:else if ui.phase === 'pending_slow'}
    <p class="pay-note" role="status" data-testid="payment-slow">{s.still_confirming}</p>
  {/if}

  <!-- The form stays mounted while hidden so Stripe keeps its state across a decline. -->
  <div class="pay-form" hidden={!formVisible || timeUp} inert={timeUp || busy}>
    {#if ui.phase === 'loading'}
      <p class="pay-note" aria-live="polite" aria-busy="true">{s.loading}</p>
    {/if}
    <!-- Stays in the layout even with no wallet: the element draws nothing then, and a
         display:none mount point would keep Stripe from measuring it. -->
    <div class="pay-express" bind:this={expressNode}>
      <svelte:element this={'slot'} name={expressSlot}></svelte:element>
    </div>
    {#if ui.walletsAvailable}
      <p class="pay-sep"><span>{s.or_pay_by_card}</span></p>
    {/if}
    <div class="pay-element" bind:this={paymentNode}>
      <svelte:element this={'slot'} name={paymentSlot}></svelte:element>
    </div>

    {#if ui.group}
      <div class="pay-decline" role="alert" data-testid="payment-decline" data-group={ui.group}>
        <p>{declineText(ui.group, s)}</p>
        {#if holdTime}
          <p class="pay-sub">{fill(s.try_another, { time: holdTime })}</p>
        {/if}
      </div>
    {/if}

    <div class="pay-bar">
      <button
        type="button"
        class="pay-button"
        data-testid="pay-button"
        disabled={!ui.canPay || busy || timeUp}
        aria-busy={ui.phase === 'submitting'}
        onclick={pay}
      >
        {payLabel}
      </button>
    </div>
  </div>

  {#if organizerName && ui.phase !== 'paid'}
    <p class="pay-organizer" data-testid="payment-organizer">{fill(s.organizer, { name: organizerName })}</p>
  {/if}
</div>

<style>
  /* Stripe Checkout look: white, system font, Stripe blue. The widget theme
     does not apply on this screen. */
  .pay {
    box-sizing: border-box;
    background: #ffffff;
    color: #30313d;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
    padding: 20px 16px 16px;
    max-width: 520px;
    margin: 0 auto;
    border-radius: 8px;
  }
  .pay-back {
    margin: 0 0 8px;
    padding: 0;
    border: 0;
    background: none;
    color: #0570de;
    font: inherit;
    font-size: 14px;
    cursor: pointer;
  }
  .pay-title {
    margin: 0 0 12px;
    font-size: 18px;
    font-weight: 600;
  }
  .pay-summary {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    padding: 12px 0;
    border-top: 1px solid #e6e6e6;
    border-bottom: 1px solid #e6e6e6;
    margin-bottom: 12px;
    font-size: 15px;
  }
  .pay-summary strong {
    font-size: 20px;
  }
  .pay-hold {
    margin: 0 0 12px;
    font-size: 13px;
    color: #6b7c93;
  }
  .pay-hold.warn {
    color: #b3261e;
    font-weight: 600;
  }
  .pay-note {
    margin: 8px 0;
    font-size: 14px;
  }
  .pay-note.error {
    color: #b3261e;
  }
  .pay-form[hidden] {
    display: none;
  }
  .pay-sep {
    margin: 12px 0;
    text-align: center;
    font-size: 12px;
    color: #6b7c93;
  }
  .pay-decline {
    margin: 12px 0 0;
    padding: 10px 12px;
    border-radius: 6px;
    background: #fdf1f0;
    color: #b3261e;
    font-size: 14px;
  }
  .pay-decline p {
    margin: 0;
  }
  .pay-decline .pay-sub {
    margin-top: 4px;
    color: #5a5a5a;
    font-size: 13px;
  }
  /* On a phone the button sticks to the bottom so the organizer block below
     cannot hide it. */
  .pay-bar {
    position: sticky;
    bottom: 0;
    padding: 12px 0 4px;
    background: linear-gradient(to bottom, rgba(255, 255, 255, 0), #ffffff 30%);
  }
  .pay-button {
    width: 100%;
    min-height: 48px;
    border: 0;
    border-radius: 6px;
    background: #0570de;
    color: #ffffff;
    font: inherit;
    font-size: 16px;
    font-weight: 600;
    cursor: pointer;
  }
  .pay-button:hover:not(:disabled) {
    background: #0a5dba;
  }
  .pay-button:disabled {
    background: #c9d1da;
    color: #ffffff;
    cursor: not-allowed;
  }
  .pay-secondary {
    min-height: 44px;
    padding: 0 16px;
    border: 1px solid #0570de;
    border-radius: 6px;
    background: #ffffff;
    color: #0570de;
    font: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  .pay-organizer {
    margin: 12px 0 0;
    font-size: 12px;
    color: #6b7c93;
  }
  .spinner {
    display: inline-block;
    width: 14px;
    height: 14px;
    margin-inline-end: 6px;
    border: 2px solid #c9d1da;
    border-top-color: #0570de;
    border-radius: 50%;
    vertical-align: -2px;
    animation: pay-spin 0.8s linear infinite;
  }
  @keyframes pay-spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner {
      animation: none;
    }
  }
</style>
