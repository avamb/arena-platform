<script lang="ts">
  import { removeCartLine, cartTotal, cartItemCount, countdownSeconds, isTwoMinWarning, formatCountdown } from '../lib/cart.js';
  import { formatPrice, getCheckoutI18n } from '../lib/checkout.js';
  import type { CartState } from '../lib/cart.js';
  import BuyerForm from './BuyerForm.svelte';
  import type { BuyerFieldConfig, BuyerFormValues } from '../lib/checkout.js';

  interface Props {
    cart: CartState;
    buyerFields: BuyerFieldConfig[];
    locale?: string;
    submitting?: boolean;
    submitError?: string | null;
    onClose: () => void;
    onRemoveLine: (idx: number) => void;
    onCheckout: (values: BuyerFormValues) => void;
  }
  const {
    cart, buyerFields, locale = 'en', submitting = false, submitError = null,
    onClose, onRemoveLine, onCheckout,
  }: Props = $props();

  const t = $derived(getCheckoutI18n(locale));
  const count = $derived(cartItemCount(cart.lines));
  const total = $derived(cartTotal(cart.lines));

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
                  <span class="line-price">{formatPrice(line.priceAmount * line.quantity, line.currency)}</span>
                {/if}
                <button class="remove-btn" onclick={() => onRemoveLine(idx)} aria-label="Remove {line.tierName}">✕</button>
              </div>
            </li>
          {/each}
        </ul>

        {#if total.currency}
          <div class="total-row">
            <span class="total-label">{t.cart_total_label}</span>
            <span class="total-value">{formatPrice(total.amount, total.currency)}</span>
          </div>
        {/if}

        <button class="cta-btn" onclick={handleContinue} disabled={count === 0}>
          {t.submit_label}
        </button>
      {/if}
    </div>

  {:else}
    <!-- Buyer form -->
    <div class="sheet-body">
      {#if submitError}
        <p class="submit-error" role="alert">{submitError}</p>
      {/if}
      <BuyerForm
        {buyerFields}
        {locale}
        {submitting}
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
  .remove-btn:hover { color: #b91c1c; }
  .total-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.75rem 0;
    border-top: 1px solid var(--arena-border-color, #e5e7eb);
    font-weight: 600;
    font-size: 1rem;
  }
  .cta-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0.75rem 1.5rem;
    background: var(--_accent, #4f46e5);
    color: #fff;
    border: none;
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    font-weight: 600;
    cursor: pointer;
    width: 100%;
    transition: opacity 0.15s;
  }
  .cta-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .submit-error {
    font-size: 0.875rem;
    color: #b91c1c;
    background: #fef2f2;
    padding: 0.5rem 0.75rem;
    border-radius: var(--arena-radius, 8px);
    margin: 0;
  }
</style>
