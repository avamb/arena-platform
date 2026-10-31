<script lang="ts">
  import { formatPrice } from '../lib/checkout.js';
  import { priceLabelBesideName } from '../lib/priceLabel.js';
  import type { Tier } from '../types.js';

  /**
   * The card of the seated category (or categories) of a session with a seat
   * map. It looks like a GA category card but has no stepper: such a place is
   * taken by choosing a seat on the plan, and without a card of its own a free
   * seated category is invisible next to the paid ones.
   */
  interface Props {
    tiers: Tier[];
    /** One line saying what to do, in the buyer's language. */
    note: string;
    /** Price label for a free category, in the buyer's language. */
    freeLabel: string;
  }
  const { tiers, note, freeLabel }: Props = $props();

  // With several seated categories (price zones) the card stays generic.
  const single = $derived(tiers.length === 1 ? tiers[0] : null);
  const name = $derived(single ? single.name : '');
  const price = $derived(single ? (single.current_price ?? single.price_amount) : 0);
  const priceText = $derived(
    single
      ? priceLabelBesideName(single.name, price > 0 && single.currency ? formatPrice(price, single.currency) : freeLabel)
      : '',
  );
</script>

<div class="ga-card seated-card" data-testid="seated-tier-card">
  <div class="ga-card-info">
    {#if name}
      <span class="ga-card-name">{name}</span>
    {/if}
    {#if priceText}
      <span class="ga-card-price">{priceText}</span>
    {/if}
    <span class="seated-card-note">{note}</span>
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
  .seated-card-note { font-size: 0.875rem; font-weight: 600; color: var(--arena-accent, #4f46e5); }
</style>
