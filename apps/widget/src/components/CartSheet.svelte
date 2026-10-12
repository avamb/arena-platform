<script lang="ts">
  import { removeCartLine, cartTotal, cartServiceFee, cartItemCount, countdownSeconds, isTwoMinWarning, formatCountdown } from '../lib/cart.js';
  import { getCheckoutI18n, interpolate, payButtonLabel, shouldShowWithdrawalNotice } from '../lib/checkout.js';
  import { sheetAmounts, formatAmount, formatDiscount } from '../lib/promo.js';
  import type { PromoQuote } from '../lib/promo.js';
  import { promoPayBlocked } from '../lib/promoController.js';
  import { getPromoStrings } from '../lib/promoI18n.js';
  import type { CartState } from '../lib/cart.js';
  import BuyerForm from './BuyerForm.svelte';
  import type { BuyerFieldConfig, BuyerFormValues } from '../lib/checkout.js';

  interface Props {
    cart: CartState;
    buyerFields: BuyerFieldConfig[];
    locale?: string;
    /**
     * The channel's service charge ("5.00" = 5 %), from the session's
     * `service_fee_percent`. Shown as its own line above the total so the
     * buyer sees on the sheet what the payment page will ask for.
     */
    serviceFeePercent?: string | null;
    /**
     * The session's `show_withdrawal_notice`: print the "no right of
     * withdrawal" notice above the pay button.
     */
    showWithdrawalNotice?: boolean;
    submitting?: boolean;
    submitError?: string | null;
    /**
     * The promo code applied to this cart, as the buyer typed it; null when
     * none. While it is set the field shows the applied state instead.
     */
    promoCode?: string | null;
    /**
     * The platform's quote for the applied code. Its amounts are shown as they
     * are, but only while `cartKey` still matches the cart it was made for.
     */
    promoQuote?: PromoQuote | null;
    /** Identity of the current cart (`cartQuoteKey`); matches `promoQuote.cartKey` when fresh. */
    cartKey?: string;
    /** A quote request is in flight: the buy button waits for it. */
    promoBusy?: boolean;
    /** Why the last Apply failed, already in the buyer's language. */
    promoError?: string | null;
    /** A neutral status, e.g. "Code removed.". */
    promoNote?: string | null;
    onApplyPromo?: (code: string) => void;
    onRemovePromo?: () => void;
    /**
     * Seat keys currently in conflict (from a 409 response).
     * When non-empty and submitError is set, a "continue without conflicts" CTA
     * is shown so the user can drop conflicting seats and retry (WID-T4).
     */
    conflictKeys?: ReadonlySet<string>;
    /**
     * Called when the user clicks "continue without unavailable seats".
     * The handler in ArenaTickets removes conflicting seats from the selection
     * and clears the conflict state so the user can proceed (WID-T4).
     */
    onContinueWithoutConflicts?: () => void;
    onClose: () => void;
    onRemoveLine: (idx: number) => void;
    onCheckout: (values: BuyerFormValues) => void;
  }
  const {
    cart, buyerFields, locale = 'en', serviceFeePercent = null, showWithdrawalNotice = false, submitting = false, submitError = null,
    promoCode = null, promoQuote = null, cartKey = '', promoBusy = false, promoError = null, promoNote = null,
    onApplyPromo, onRemovePromo,
    conflictKeys, onContinueWithoutConflicts,
    onClose, onRemoveLine, onCheckout,
  }: Props = $props();

  const t = $derived(getCheckoutI18n(locale));
  const p = $derived(getPromoStrings(locale));
  const count = $derived(cartItemCount(cart.lines));
  const total = $derived(cartTotal(cart.lines));
  const localFee = $derived(cartServiceFee(total.amount, serviceFeePercent));
  // With an applied code the money rows are the platform's own quote of this
  // cart (the discount changes the service fee's base, so the sheet must not
  // work it out itself); without one they are the list prices and the same
  // basis-point floor the platform uses.
  const amounts = $derived(sheetAmounts({ subtotal: total.amount, fee: localFee, currency: total.currency }, promoQuote, cartKey));
  const fee = $derived(amounts.fee);
  const payTotal = $derived(amounts.total);
  // The pay button waits for the quote of THIS cart; with no code applied it
  // never waits, so a failed or slow quote cannot hold the purchase.
  const payBlocked = $derived(
    promoPayBlocked({ code: promoCode, quote: promoQuote, busy: promoBusy, error: null, note: null }, cartKey),
  );
  let promoInput = $state('');
  function submitPromo(e: Event): void {
    e.preventDefault();
    if (promoBusy) return;
    onApplyPromo?.(promoInput);
  }
  // The button that takes the buyer to the payment page creates the
  // obligation to pay, so it names the amount and says "pay" ("Pay €50")
  // instead of the neutral "Continue to payment". A free order has nothing to
  // pay, so it keeps the generic label (undefined -> BuyerForm default).
  const payLabel = $derived(payButtonLabel(payTotal, amounts.currency, locale));

  let showBuyerForm = $state(false);
  let secondsLeft = $state(0);
  let tickInterval: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    if (tickInterval) { clearInterval(tickInterval); tickInterval = null; }
    if (cart.expiresAt) {
      secondsLeft = countdownSeconds(cart.expiresAt);
      tickInterval = setInterval(() => {
        secondsLeft = countdownSeconds(cart.expiresAt!);
      }, 1000);
    } else {
      secondsLeft = 0;
    }
    return () => { if (tickInterval) clearInterval(tickInterval); };
  });

  const isWarning = $derived(isTwoMinWarning(secondsLeft));
  const countdownStr = $derived(cart.expiresAt ? formatCountdown(secondsLeft) : null);

  function handleContinue(): void { showBuyerForm = true; }
  function handleBackToCart(): void { showBuyerForm = false; }
</script>

<!-- Sheet overlay -->
<div class="sheet-overlay" role="presentation" onclick={onClose}></div>

<div class="sheet" role="dialog" aria-modal="true" aria-label="Your cart">
  <div class="sheet-header">
    <button class="back-btn" onclick={showBuyerForm ? handleBackToCart : onClose} aria-label={showBuyerForm ? t.cart_back : t.cart_title}>
      {#if showBuyerForm}← {t.cart_back}{:else}✕{/if}
    </button>
    <h2 class="sheet-title">{showBuyerForm ? t.cart_details_title : t.cart_title}</h2>
  </div>

  {#if !showBuyerForm}
    <!-- Cart lines -->
    {#if countdownStr}
      <div class="countdown-bar" class:warning={isWarning} role="timer" aria-live="polite">
        ⏱ {countdownStr} {t.remaining}
        {#if isWarning} — {t.expires_warn}{/if}
      </div>
    {/if}

    <div class="sheet-body">
      {#if cart.lines.length === 0}
        <p class="empty-msg">{t.cart_empty}</p>
      {:else}
        <ul class="line-list" role="list">
          {#each cart.lines as line, idx (idx)}
            <li class="line-item">
              <div class="line-info">
                <span class="line-name">{line.tierName}</span>
                <span class="line-qty">× {line.quantity}</span>
              </div>
              <div class="line-right">
                {#if line.currency}
                  <span class="line-price">{formatAmount(line.priceAmount * line.quantity, line.currency)}</span>
                {/if}
                <button class="remove-btn" onclick={() => onRemoveLine(idx)} aria-label="Remove {line.tierName}" disabled={promoBusy}>✕</button>
              </div>
            </li>
          {/each}
        </ul>

        {#if onApplyPromo && (total.amount > 0 || promoCode)}
          <div class="promo">
            {#if promoCode}
              <div class="promo-applied">
                <span class="promo-applied-text" role="status">{interpolate(p.applied, { code: promoCode.toUpperCase() })}</span>
                <button type="button" class="promo-remove" onclick={() => onRemovePromo?.()} disabled={promoBusy}>{p.remove}</button>
              </div>
            {:else}
              <form class="promo-form" onsubmit={submitPromo}>
                <label for="arena-promo-input" class="promo-label">{p.label}</label>
                <div class="promo-row">
                  <input
                    id="arena-promo-input"
                    type="text"
                    class="promo-input"
                    placeholder={p.placeholder}
                    bind:value={promoInput}
                    autocomplete="off"
                    autocapitalize="characters"
                    spellcheck="false"
                    aria-invalid={!!promoError}
                    aria-describedby={promoError ? 'arena-promo-err' : undefined}
                    disabled={promoBusy}
                  />
                  <button type="submit" class="promo-apply" disabled={promoBusy}>{p.apply}</button>
                </div>
              </form>
            {/if}
            {#if promoError}
              <p id="arena-promo-err" class="promo-error" role="alert">{promoError}</p>
            {:else if promoBusy}
              <p class="promo-note" role="status">{p.checking}</p>
            {:else if promoNote && !promoCode}
              <p class="promo-note" role="status">{promoNote}</p>
            {/if}
          </div>
        {/if}

        {#if amounts.currency}
          {#if amounts.discounted && amounts.discount > 0}
            <div class="fee-row">
              <span class="fee-label">{p.subtotal_label}</span>
              <span class="fee-value">{formatAmount(amounts.subtotal, amounts.currency)}</span>
            </div>
            <div class="fee-row discount-row">
              <span class="fee-label">{p.discount_label}</span>
              <span class="fee-value">{formatDiscount(amounts.discount, amounts.currency)}</span>
            </div>
          {/if}
          {#if fee > 0}
            <div class="fee-row">
              <span class="fee-label">{t.cart_fee_label}</span>
              <span class="fee-value">{formatAmount(fee, amounts.currency)}</span>
            </div>
          {/if}
          <div class="total-row">
            <span class="total-label">{t.cart_total_label}</span>
            <span class="total-value">{formatAmount(payTotal, amounts.currency)}</span>
          </div>
        {/if}

        <button class="cta-btn" onclick={handleContinue} disabled={count === 0 || payBlocked}>
          {t.submit_label}
        </button>
      {/if}
    </div>

  {:else}
    <!-- Buyer form -->
    <div class="sheet-body">
      {#if submitError}
        <p class="submit-error" role="alert">{submitError}</p>
        {#if conflictKeys && conflictKeys.size > 0 && onContinueWithoutConflicts}
          <!-- WID-T4: one-click "continue without unavailable seats" CTA -->
          <button class="continue-without-btn" onclick={onContinueWithoutConflicts}>
            {t.continue_without_conflicts}
          </button>
        {/if}
      {/if}
      <BuyerForm
        {buyerFields}
        {locale}
        {submitting}
        submitText={payLabel}
        withdrawalNotice={shouldShowWithdrawalNotice(showWithdrawalNotice, payTotal)}
        onSubmit={onCheckout}
      />
    </div>
  {/if}
</div>

<style>
  .sheet-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0,0,0,0.45);
    z-index: 30;
  }
  .sheet {
    position: fixed;
    bottom: 0;
    left: 0;
    right: 0;
    z-index: 40;
    background: var(--arena-bg, #fff);
    border-radius: 16px 16px 0 0;
    max-height: 80vh;
    display: flex;
    flex-direction: column;
    box-shadow: 0 -4px 24px rgba(0,0,0,0.2);
    overflow: hidden;
  }
  /* A full-bleed bottom sheet is right on a phone and wrong on a monitor:
     stretched to 1900px the cart becomes one line of text with the price a
     metre away from it, and the buyer form turns into full-width inputs for
     an e-mail address. Past the phone width the same sheet becomes a card of
     readable width, still docked at the bottom so nothing about the flow or
     the entrance changes. */
  @media (min-width: 720px) {
    .sheet {
      left: 50%;
      right: auto;
      transform: translateX(-50%);
      bottom: 24px;
      width: min(560px, calc(100vw - 3rem));
      border-radius: 16px;
      box-shadow: 0 8px 32px rgba(0,0,0,0.24);
    }
  }
  .sheet-header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 1rem 1rem 0;
    flex-shrink: 0;
  }
  .sheet-title {
    flex: 1;
    font-size: 1.125rem;
    font-weight: 600;
    margin: 0;
    color: var(--arena-color-primary, #1a1a1a);
  }
  .back-btn {
    background: none;
    border: none;
    font-size: 1rem;
    cursor: pointer;
    color: var(--arena-color-secondary, #6b7280);
    padding: 0.25rem;
    border-radius: 4px;
    font-family: inherit;
  }
  .countdown-bar {
    padding: 0.5rem 1rem;
    background: #dbeafe;
    color: #1d4ed8;
    font-size: 0.875rem;
    font-weight: 500;
    flex-shrink: 0;
  }
  .countdown-bar.warning {
    background: #fef3c7;
    color: #92400e;
  }
  .sheet-body {
    overflow-y: auto;
    padding: 1rem;
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .empty-msg {
    color: var(--arena-color-secondary, #6b7280);
    text-align: center;
    margin: 2rem 0;
  }
  .line-list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .line-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.75rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
  }
  .line-info { display: flex; align-items: center; gap: 0.5rem; flex: 1; min-width: 0; }
  .line-name { font-weight: 500; font-size: 0.9375rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .line-qty { color: var(--arena-color-secondary, #6b7280); font-size: 0.875rem; flex-shrink: 0; }
  .line-right { display: flex; align-items: center; gap: 0.5rem; flex-shrink: 0; }
  .line-price { font-weight: 500; font-size: 0.9375rem; }
  .remove-btn {
    background: none;
    border: none;
    color: var(--arena-color-secondary, #6b7280);
    cursor: pointer;
    font-size: 0.875rem;
    padding: 0.25rem;
    border-radius: 4px;
    line-height: 1;
  }
  .remove-btn:hover:not(:disabled) { color: #b91c1c; }
  .remove-btn:disabled { opacity: 0.4; cursor: not-allowed; }
  .fee-row {
    display: flex;
    justify-content: space-between;
    padding: 4px 0;
    font-size: 13px;
    color: var(--_text-muted);
  }
  .discount-row .fee-value {
    color: var(--arena-success, #15803d);
    font-weight: 600;
  }
  .promo {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  .promo-form {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .promo-label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--arena-color-primary, #1a1a1a);
  }
  .promo-row {
    display: flex;
    gap: 0.5rem;
  }
  /* Own input tokens, like BuyerForm: a dark host theme must not turn the
     typing white on the white field. */
  .promo-input {
    flex: 1;
    min-width: 0;
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    color: var(--arena-input-color, #1a1a1a);
    background: var(--arena-input-bg, #fff);
    text-transform: uppercase;
  }
  .promo-input::placeholder { text-transform: none; }
  .promo-input:focus-visible {
    border-color: var(--arena-accent, #4f46e5);
    outline: 3px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 2px;
  }
  .promo-apply,
  .promo-remove {
    padding: 0.5rem 1rem;
    background: transparent;
    color: var(--_accent, #4f46e5);
    border: 1.5px solid var(--_accent, #4f46e5);
    border-radius: var(--arena-radius, 8px);
    font-size: 0.9375rem;
    font-family: inherit;
    font-weight: 500;
    cursor: pointer;
    flex-shrink: 0;
  }
  .promo-apply:hover:not(:disabled),
  .promo-remove:hover:not(:disabled) {
    background: color-mix(in srgb, var(--_accent, #4f46e5) 8%, transparent);
  }
  .promo-apply:disabled,
  .promo-remove:disabled { opacity: 0.6; cursor: not-allowed; }
  .promo-applied {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    border-radius: var(--arena-radius, 8px);
    background: #f0fdf4;
    color: #166534;
    font-size: 0.9375rem;
    font-weight: 500;
  }
  .promo-error {
    margin: 0;
    font-size: 0.8125rem;
    color: #b91c1c;
  }
  .promo-note {
    margin: 0;
    font-size: 0.8125rem;
    color: var(--arena-color-secondary, #6b7280);
  }
  .total-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.75rem 0;
    border-top: 1px solid var(--arena-border-color, #e5e7eb);
    font-weight: 600;
    font-size: 1rem;
  }
  /* Checkout CTA — yellow, like every other primary action (see the --_btn-*
     block on :host in ArenaTickets.svelte). */
  .cta-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0.75rem 1.5rem;
    background: var(--_btn-bg, #fcdc54);
    color: var(--_btn-text, #101010);
    border: none;
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    font-weight: 700;
    cursor: pointer;
    width: 100%;
    transition: background-color 0.15s;
  }
  .cta-btn:hover:not(:disabled) { background: var(--_btn-bg-hover, #f5d23a); }
  .cta-btn:disabled {
    background: var(--_btn-disabled-bg, #e5e7eb);
    color: var(--_btn-disabled-text, #6b7280);
    opacity: 1;
    cursor: not-allowed;
  }
  .submit-error {
    font-size: 0.875rem;
    color: #b91c1c;
    background: #fef2f2;
    padding: 0.5rem 0.75rem;
    border-radius: var(--arena-radius, 8px);
    margin: 0;
  }
  /* WID-T4: secondary action for the 409 conflict case */
  .continue-without-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0.625rem 1.25rem;
    background: transparent;
    color: var(--_accent, #4f46e5);
    border: 1.5px solid var(--_accent, #4f46e5);
    border-radius: var(--arena-radius, 8px);
    font-size: 0.9375rem;
    font-family: inherit;
    font-weight: 500;
    cursor: pointer;
    width: 100%;
    transition: background 0.15s, color 0.15s;
  }
  .continue-without-btn:hover {
    background: color-mix(in srgb, var(--_accent, #4f46e5) 8%, transparent);
  }
</style>
