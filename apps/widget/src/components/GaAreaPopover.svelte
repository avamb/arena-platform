<script lang="ts">
  /**
   * GaAreaPopover — inline quantity picker opened when the buyer taps a
   * general-admission polygon on the seat map (AB-40D).
   *
   * Renders on top of the seat map surface (positioned by the parent) as a
   * small overlay: name, price, remaining-in-area label, stepper +/-, Done.
   * No new state model — writes go through `onQuantityChange(tierId, qty)`
   * exactly like `GaTierCard`, so the buyer's picks land in the same cart
   * with the same tier binding as the always-visible GA card path.
   *
   * The upper bound is `min(GA_MAX_QUANTITY, area.capacity)` — the area's
   * declared capacity is a shared pool, not a set of pseudo-seats; the
   * remaining-in-pool decrement is applied server-side by reservations.
   */
  import { clampGaQuantity, GA_MAX_QUANTITY } from '../lib/selection.js';
  import { formatPrice } from '../lib/checkout.js';
  import type { GaArea } from '../lib/store.js';

  interface Props {
    area: GaArea;
    quantity: number;
    onQuantityChange: (tierId: string, qty: number) => void;
    onClose: () => void;
  }
  const { area, quantity, onQuantityChange, onClose }: Props = $props();

  const upperBound = $derived(Math.min(GA_MAX_QUANTITY, area.capacity > 0 ? area.capacity : GA_MAX_QUANTITY));
  const canIncrease = $derived(quantity < upperBound);
  const canDecrease = $derived(quantity > 0);

  function decrement(): void {
    onQuantityChange(area.tierId, quantity > 0 ? quantity - 1 : 0);
  }

  function increment(): void {
    onQuantityChange(area.tierId, clampGaQuantity(quantity + 1, upperBound));
  }

  function onKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      onClose();
    }
  }
</script>

<!-- The overlay is a positioned dialog: parent controls the anchor via CSS. -->
<div
  class="ga-popover"
  role="dialog"
  aria-modal="false"
  aria-label={`${area.name} tickets`}
  data-ga-popover-tier-id={area.tierId}
  tabindex="-1"
  onkeydown={onKeydown}
>
  <div class="ga-popover-header">
    <span class="ga-popover-name">{area.name}</span>
    <button
      type="button"
      class="ga-popover-close"
      aria-label="Close"
      onclick={onClose}
    >×</button>
  </div>
  <div class="ga-popover-price">
    {#if area.priceAmount > 0 && area.currency}
      {formatPrice(area.priceAmount, area.currency)}
    {:else}
      Free
    {/if}
    {#if area.capacity > 0}
      <span class="ga-popover-capacity"> · up to {upperBound}</span>
    {/if}
  </div>
  <div class="ga-popover-stepper">
    <button
      type="button"
      class="step-btn"
      onclick={decrement}
      disabled={!canDecrease}
      aria-label={`Decrease quantity for ${area.name}`}
    >−</button>
    <span
      class="step-qty"
      aria-live="polite"
      aria-label={`${quantity} tickets for ${area.name}`}
    >{quantity}</span>
    <button
      type="button"
      class="step-btn"
      onclick={increment}
      disabled={!canIncrease}
      aria-label={`Increase quantity for ${area.name}`}
    >+</button>
  </div>
  <button
    type="button"
    class="ga-popover-done"
    onclick={onClose}
  >Done</button>
</div>

<style>
  .ga-popover {
    position: absolute;
    z-index: 10;
    min-width: 220px;
    max-width: 280px;
    padding: 0.75rem 0.875rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    background: var(--arena-bg, #ffffff);
    color: var(--arena-color-primary, #1a1a1a);
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.14);
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    font-family: inherit;
  }
  .ga-popover-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.5rem;
  }
  .ga-popover-name {
    font-weight: 600;
    font-size: 0.9375rem;
  }
  .ga-popover-close {
    background: transparent;
    border: none;
    font-size: 1.25rem;
    line-height: 1;
    cursor: pointer;
    color: inherit;
    padding: 0 0.25rem;
  }
  .ga-popover-price {
    font-size: 0.875rem;
    color: var(--arena-color-secondary, #6b7280);
  }
  .ga-popover-capacity {
    color: var(--arena-color-secondary, #6b7280);
  }
  .ga-popover-stepper {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.75rem;
  }
  .step-btn {
    width: 2rem;
    height: 2rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: 50%;
    background: transparent;
    font-size: 1.125rem;
    line-height: 1;
    cursor: pointer;
    color: inherit;
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: inherit;
  }
  .step-btn:hover:not(:disabled) {
    background: color-mix(in srgb, currentColor 10%, transparent);
  }
  .step-btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }
  .step-qty {
    min-width: 1.5rem;
    text-align: center;
    font-weight: 600;
    font-size: 1rem;
  }
  .ga-popover-done {
    align-self: stretch;
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--arena-accent, #4f46e5);
    border-radius: var(--arena-radius, 8px);
    background: var(--arena-accent, #4f46e5);
    color: #ffffff;
    font-weight: 600;
    cursor: pointer;
    font-family: inherit;
    font-size: 0.875rem;
  }
</style>
