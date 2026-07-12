<script lang="ts">
  import { clampGaQuantity, GA_MAX_QUANTITY } from '../lib/selection.js';
  import { formatPrice } from '../lib/checkout.js';
  import type { Tier } from '../types.js';

  interface Props {
    tier: Tier;
    quantity: number;
    onQuantityChange: (tierId: string, qty: number) => void;
  }
  const { tier, quantity, onQuantityChange }: Props = $props();

  const capacity = $derived(tier.capacity ?? GA_MAX_QUANTITY);
  const canIncrease = $derived(quantity < Math.min(GA_MAX_QUANTITY, capacity));
  const canDecrease = $derived(quantity > 0);

  function decrement(): void {
    const newQty = quantity > 0 ? quantity - 1 : 0;
    onQuantityChange(tier.id, newQty);
  }

  function increment(): void {
    const newQty = clampGaQuantity(quantity + 1, capacity);
    onQuantityChange(tier.id, newQty);
  }
</script>

<div class="ga-card" data-tier-id={tier.id}>
  <div class="ga-card-info">
    <span class="ga-card-name">{tier.name}</span>
    {#if tier.price_amount > 0 && tier.currency}
      <span class="ga-card-price">{formatPrice(tier.price_amount, tier.currency)}</span>
    {:else}
      <span class="ga-card-price">Free</span>
    {/if}
  </div>
  <div class="ga-card-stepper">
    <button
      class="step-btn"
      onclick={decrement}
      disabled={!canDecrease}
      aria-label="Decrease quantity for {tier.name}"
    >−</button>
    <span class="step-qty" aria-live="polite" aria-label="{quantity} tickets for {tier.name}">{quantity}</span>
    <button
      class="step-btn"
      onclick={increment}
      disabled={!canIncrease}
      aria-label="Increase quantity for {tier.name}"
    >+</button>
  </div>
</div>

<style>
  .ga-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.875rem 1rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    background: var(--arena-bg, #fff);
  }
  .ga-card-info { display: flex; flex-direction: column; gap: 0.125rem; flex: 1; min-width: 0; }
  .ga-card-name { font-weight: 600; font-size: 0.9375rem; color: var(--arena-color-primary, #1a1a1a); }
  .ga-card-price { font-size: 0.875rem; color: var(--arena-color-secondary, #6b7280); }
  .ga-card-stepper { display: flex; align-items: center; gap: 0.625rem; flex-shrink: 0; }
  .step-btn {
    width: 2rem;
    height: 2rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: 50%;
    background: transparent;
    font-size: 1.125rem;
    line-height: 1;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--arena-color-primary, #1a1a1a);
    transition: background 0.1s;
    font-family: inherit;
  }
  .step-btn:hover:not(:disabled) { background: color-mix(in srgb, currentColor 10%, transparent); }
  .step-btn:disabled { opacity: 0.35; cursor: not-allowed; }
  .step-qty {
    min-width: 1.5rem;
    text-align: center;
    font-weight: 600;
    font-size: 1rem;
    color: var(--arena-color-primary, #1a1a1a);
  }
</style>
