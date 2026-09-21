<script lang="ts">
  import { cartTotal, cartItemCount, countdownSeconds, isTwoMinWarning, formatCountdown } from '../lib/cart.js';
  import { formatPrice, getCheckoutI18n, pluralizeTickets } from '../lib/checkout.js';
  import type { CartLineItem } from '../lib/cart.js';

  interface Props {
    lines: CartLineItem[];
    expiresAt?: string | null;
    locale?: string;
    onOpen: () => void;
    /**
     * Optional verb for the bar, e.g. "Continue to payment".
     *
     * The bar IS the control that opens the buyer form, but on its own it
     * only states what is in the cart ("1 ticket  €50") — which, directly
     * under a row that already says the same thing, reads as a duplicate
     * line rather than as the next step. A caller whose layout puts the
     * two next to each other passes the verb; every existing embed passes
     * nothing and keeps the bar exactly as it was.
     */
    cta?: string | null;
  }
  const { lines, expiresAt, locale = 'en', onOpen, cta = null }: Props = $props();

  const t = $derived(getCheckoutI18n(locale));

  const count = $derived(cartItemCount(lines));
  const total = $derived(cartTotal(lines));

  // Countdown tick
  let secondsLeft = $state(0);
  let tickInterval: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    if (tickInterval) { clearInterval(tickInterval); tickInterval = null; }
    if (expiresAt) {
      secondsLeft = countdownSeconds(expiresAt);
      tickInterval = setInterval(() => {
        secondsLeft = countdownSeconds(expiresAt!);
      }, 1000);
    } else {
      secondsLeft = 0;
    }
    return () => { if (tickInterval) clearInterval(tickInterval); };
  });

  const isWarning = $derived(isTwoMinWarning(secondsLeft));
  const countdownStr = $derived(expiresAt ? formatCountdown(secondsLeft) : null);
</script>

{#if count > 0}
  <div class="mini-cart" class:warning={isWarning} role="status" aria-label="Cart: {count} items">
    <button class="mini-cart-btn" onclick={onOpen} aria-label="Open cart ({count} items)">
      <span class="mini-cart-count">{count}</span>
      {#if cta}
        <span class="mini-cart-label">
          <span class="mini-cart-cta">{cta}</span>
          <span class="mini-cart-sub">{pluralizeTickets(count, locale, t)}</span>
        </span>
      {:else}
        <span class="mini-cart-label">{pluralizeTickets(count, locale, t)}</span>
      {/if}
      {#if total.currency}
        <span class="mini-cart-total">{formatPrice(total.amount, total.currency)}</span>
      {/if}
      {#if countdownStr}
        <span class="mini-cart-countdown" aria-label="Time remaining: {countdownStr}">⏱ {countdownStr}</span>
      {/if}
    </button>
  </div>
{/if}

<style>
  .mini-cart {
    position: sticky;
    bottom: 0;
    left: 0;
    right: 0;
    z-index: 20;
    /* The sticky bar IS the "go to checkout" control, so it wears the primary
       yellow (see the --_btn-* block on :host in ArenaTickets.svelte) rather
       than the accent. Dark label on both the yellow and the amber warning
       fill — white on #d97706 was only ~3:1 and failed AA. */
    background: var(--_btn-bg, #fcdc54);
    color: var(--_btn-text, #101010);
    box-shadow: 0 -2px 8px rgba(0,0,0,0.18);
  }
  .mini-cart.warning { background: #d97706; }
  .mini-cart-btn:hover { background: color-mix(in srgb, currentColor 8%, transparent); }
  .mini-cart-btn {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.875rem 1rem;
    background: transparent;
    border: none;
    color: inherit;
    font-family: inherit;
    font-size: 1rem;
    font-weight: 600;
    cursor: pointer;
    text-align: left;
  }
  .mini-cart-count {
    /* Tinted with the label colour, not white — invisible on yellow. */
    background: color-mix(in srgb, currentColor 18%, transparent);
    border-radius: 50%;
    min-width: 1.75rem;
    height: 1.75rem;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 0.875rem;
    font-weight: 700;
    flex-shrink: 0;
  }
  .mini-cart-label { flex: 1; }
  /* With a verb the bar carries two lines: the action, then what it acts
     on. The second is deliberately quieter — it restates the row above and
     must not compete with the thing the buyer is meant to press. */
  .mini-cart-cta { display: block; }
  .mini-cart-sub { display: block; font-size: 0.8125rem; font-weight: 500; opacity: 0.75; }
  .mini-cart-total { font-weight: 500; }
  .mini-cart-countdown { font-size: 0.875rem; opacity: 0.9; }
</style>
